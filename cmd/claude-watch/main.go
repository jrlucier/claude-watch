// Command claude-watch surfaces Claude Code usage in a system tray indicator
// (Linux GNOME / macOS), backed by a small daemon that polls the Anthropic
// OAuth usage API and the local ~/.claude/projects JSONL transcripts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jrlucier/claude-watch/internal/claudeapi"
	"github.com/jrlucier/claude-watch/internal/config"
	"github.com/jrlucier/claude-watch/internal/daemon"
	"github.com/jrlucier/claude-watch/internal/ipc"
	"github.com/jrlucier/claude-watch/internal/state"
	"github.com/jrlucier/claude-watch/internal/tray"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `claude-watch — Claude Code usage tray indicator.

USAGE:
  claude-watch start [--foreground]   Spawn detached daemon and show the tray icon.
  claude-watch status [--json]        Print current snapshot (pretty by default).
  claude-watch refresh                Force an immediate refresh.
  claude-watch set-label 5h|both      Switch panel-label mode.
  claude-watch set-time-format 12h|24h
                                      Switch reset-time clock format.
  claude-watch quit                   Stop the daemon.
  claude-watch version                Print the build version (--version, -v).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	rest := os.Args[2:]

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "start", "daemon":
		runDaemon(rest)
	case "status":
		runStatus(rest)
	case "refresh":
		runSimple(ipc.CmdRefresh, "")
	case "set-label":
		if len(rest) < 1 {
			fatal("set-label requires an argument: 5h or both")
		}
		v := rest[0]
		if v != "5h" && v != "both" {
			fatal("set-label: value must be \"5h\" or \"both\"")
		}
		runSimple(ipc.CmdSetLabel, v)
	case "set-time-format":
		if len(rest) < 1 {
			fatal("set-time-format requires an argument: 12h or 24h")
		}
		v := rest[0]
		if v != "12h" && v != "24h" {
			fatal("set-time-format: value must be \"12h\" or \"24h\"")
		}
		runSimple(ipc.CmdSetTimeFormat, v)
	case "stop", "quit":
		runStop()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

const daemonizedEnv = "CLAUDE_WATCH_DAEMONIZED"

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	foreground := fs.Bool("foreground", false, "run in foreground instead of detaching")
	_ = fs.Parse(args)

	// If a daemon is already up, "start" is a no-op so the autostart unit can
	// be idempotent.
	if ipc.IsDaemonRunning(ipc.SocketPath()) {
		fmt.Fprintln(os.Stderr, "claude-watch daemon already running")
		return
	}

	if !*foreground && os.Getenv(daemonizedEnv) == "" {
		if err := spawnDetached(args); err != nil {
			fatal("spawn daemon: %v", err)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		// Non-fatal: a bad config still lets the daemon run on defaults.
		fmt.Fprintf(os.Stderr, "warn: %v (using defaults)\n", err)
		cfg = config.Default()
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := state.New(cfg.LabelMode, cfg.TimeFormat)
	st.LoadFromDisk() // restore last-good API snapshot from previous run
	d, err := daemon.New(cfg, st)
	if err != nil {
		fatal("start daemon: %v", err)
	}
	d.SetNotifier(tray.NewDesktopNotifier())
	d.Start(rootCtx)

	sockPath := ipc.SocketPath()
	var srv *ipc.Server
	var quitOnce sync.Once
	quit := func() {
		quitOnce.Do(func() {
			if srv != nil {
				_ = srv.Close()
			}
			cancel()
			os.Exit(0)
		})
	}

	srv, err = ipc.NewServer(sockPath, makeHandler(st, d, quit, cfg))
	if err != nil {
		fatal("ipc: %v", err)
	}
	go func() {
		if err := srv.Serve(rootCtx); err != nil {
			fmt.Fprintf(os.Stderr, "ipc serve: %v\n", err)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		quit()
	}()

	if err := tray.EnsureAppindicator(rootCtx); err != nil {
		fmt.Fprintf(os.Stderr, "tray: %v\n", err)
	}

	actions := tray.Actions{
		Snapshot:      st.Snapshot,
		RefreshNow:    d.RefreshNow,
		SetLabelMode:  func(mode string) { setLabel(st, mode) },
		SetTimeFormat: func(f string) { setTimeFormat(st, f) },
		Quit:          quit,
	}
	tray.RunWith(actions, func(t *tray.Tray) {
		st.SetOnChange(t.Refresh)
		t.Refresh()
	})
}

// setLabel updates the in-memory state and persists the new mode to the
// config file so it survives a restart.
func setLabel(st *state.State, mode string) {
	st.SetLabelMode(mode)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: load config: %v\n", err)
		cfg = config.Default()
	}
	cfg.LabelMode = mode
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warn: save config: %v\n", err)
	}
}

// setTimeFormat updates the in-memory state and persists the new clock
// format ("12h" or "24h") to the config file.
func setTimeFormat(st *state.State, format string) {
	st.SetTimeFormat(format)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: load config: %v\n", err)
		cfg = config.Default()
	}
	cfg.TimeFormat = format
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warn: save config: %v\n", err)
	}
}

// spawnDetached re-execs this binary with the daemonized env flag set so the
// child knows to actually run the daemon body. Logs go to a per-user file
// since the child has no controlling terminal.
func spawnDetached(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	logPath, err := logFilePath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", logPath, err)
	}

	cmdArgs := append([]string{"daemon"}, args...)
	cmd := exec.Command(exe, cmdArgs...)
	cmd.Env = append(os.Environ(), daemonizedEnv+"=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	_ = logFile.Close()

	sockPath := ipc.SocketPath()
	deadline := time.Now().Add(8 * time.Second)
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	for time.Now().Before(deadline) {
		if ipc.IsDaemonRunning(sockPath) {
			fmt.Printf("claude-watch daemon started (pid %d, log: %s)\n", cmd.Process.Pid, logPath)
			_ = cmd.Process.Release()
			return nil
		}
		select {
		case err := <-childDone:
			return fmt.Errorf("daemon exited before socket came up (see %s): %v", logPath, err)
		case <-time.After(150 * time.Millisecond):
		}
	}
	_ = cmd.Process.Release()
	return fmt.Errorf("daemon started (pid %d) but socket %s did not appear within 8s; check %s", cmd.Process.Pid, sockPath, logPath)
}

func logFilePath() (string, error) {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		stateDir = filepath.Join(home, ".local/state")
	}
	dir := filepath.Join(stateDir, "claude-watch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return filepath.Join(dir, "daemon.log"), nil
}

const clientCallTimeout = 10 * time.Second

func ensureDaemonRunning() error {
	if ipc.IsDaemonRunning(ipc.SocketPath()) {
		return nil
	}
	if err := spawnDetached(nil); err != nil {
		return fmt.Errorf("auto-start daemon: %w", err)
	}
	return nil
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON instead of a human-readable summary")
	_ = fs.Parse(args)

	if err := ensureDaemonRunning(); err != nil {
		fatal("Error: %v", err)
	}
	resp, err := ipc.Send(ipc.SocketPath(), ipc.Request{Cmd: ipc.CmdStatus}, clientCallTimeout)
	if err != nil {
		fatal("Error: %v", err)
	}
	if !resp.OK {
		fatal("Fail: %s", resp.Error)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(resp.Snapshot, "", "  ")
		fmt.Println(string(b))
		return
	}
	printStatusPretty(resp.Snapshot)
}

func printStatusPretty(s ipc.Snapshot) {
	stale := ""
	if s.APIStale {
		stale = "  (stale)"
	}
	fmt.Printf("5h  %3.0f%%   7d  %3.0f%%%s\n", s.FiveHour.Utilization, s.SevenDay.Utilization, stale)
	if s.FiveHour.ResetsAt != nil {
		fmt.Printf("    5h resets  (%s)\n", s.FiveHour.ResetsAt.Local().Format("Mon 15:04 MST"))
	}
	if s.SevenDay.ResetsAt != nil {
		fmt.Printf("    7d resets  (%s)\n", s.SevenDay.ResetsAt.Local().Format("Mon 15:04 MST"))
	}
	fmt.Printf("Pace        %.0f tok/min\n", s.BurnTokPerMin)
	if s.ForecastNote != "" {
		fmt.Printf("Outlook     %s\n", s.ForecastNote)
	}
	fmt.Printf("This block  $%.2f\n", s.BlockCostUSD)
	for _, m := range s.BlockCostByModel {
		fmt.Printf("  %-30s $%.2f\n", m.Model, m.CostUSD)
	}
	if s.APIStale && s.APILastError != "" {
		fmt.Printf("\nAPI: %s\n", claudeapi.FriendlyError(s.APILastError))
	}
}

func runSimple(cmd, value string) {
	if err := ensureDaemonRunning(); err != nil {
		fatal("Error: %v", err)
	}
	resp, err := ipc.Send(ipc.SocketPath(), ipc.Request{Cmd: cmd, Value: value}, clientCallTimeout)
	if err != nil {
		fatal("Error: %v", err)
	}
	if !resp.OK {
		fatal("Fail: %s", resp.Error)
	}
	fmt.Println("OK")
}

func runStop() {
	sock := ipc.SocketPath()
	if !ipc.IsDaemonRunning(sock) {
		fmt.Println("no daemon running")
		return
	}
	resp, err := ipc.Send(sock, ipc.Request{Cmd: ipc.CmdQuit}, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "IPC quit failed: %v\n", err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "Fail: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Println("claude-watch daemon stopped")
}

func makeHandler(st *state.State, d *daemon.Daemon, quit func(), cfg config.Config) ipc.Handler {
	return func(ctx context.Context, req ipc.Request) (ipc.Response, error) {
		resp := ipc.Response{OK: true}
		switch req.Cmd {
		case ipc.CmdStatus:
			// snapshot filled below
		case ipc.CmdRefresh:
			d.RefreshNow()
		case ipc.CmdSetLabel:
			if req.Value != "5h" && req.Value != "both" {
				return ipc.Response{OK: false, Error: "set-label: value must be \"5h\" or \"both\""}, nil
			}
			setLabel(st, req.Value)
		case ipc.CmdSetTimeFormat:
			if req.Value != "12h" && req.Value != "24h" {
				return ipc.Response{OK: false, Error: "set-time-format: value must be \"12h\" or \"24h\""}, nil
			}
			setTimeFormat(st, req.Value)
		case ipc.CmdQuit:
			resp.Snapshot = st.Snapshot()
			go func() {
				time.Sleep(100 * time.Millisecond)
				quit()
			}()
			return resp, nil
		default:
			return ipc.Response{OK: false, Error: "unknown cmd: " + req.Cmd}, nil
		}
		resp.Snapshot = st.Snapshot()
		return resp, nil
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
