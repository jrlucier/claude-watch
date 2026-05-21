package tray

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// candidateExtensions is the ordered list of GNOME Shell extensions we try to
// enable to get a StatusNotifierWatcher on the session bus. Ubuntu's fork is
// preferred when present; the upstream "appindicatorsupport" is the fallback.
var candidateExtensions = []string{
	"ubuntu-appindicators@ubuntu.com",
	"appindicatorsupport@rgcjonas.gmail.com",
}

// EnsureAppindicator best-effort enables the appindicator GNOME Shell
// extension so the tray icon can register. Returns nil on success; the error
// is informational — callers should still attempt to render the tray, since
// other tray hosts (KDE, swaybar, etc.) may already be providing
// StatusNotifierWatcher.
func EnsureAppindicator(ctx context.Context) error {
	if statusNotifierWatcherUp(ctx) {
		return nil
	}
	if _, err := exec.LookPath("gnome-extensions"); err != nil {
		return fmt.Errorf("StatusNotifierWatcher not available and `gnome-extensions` not on PATH")
	}

	enabled, err := listEnabledExtensions(ctx)
	if err != nil {
		return err
	}
	for _, id := range candidateExtensions {
		if enabled[id] {
			return nil
		}
	}

	available, err := listAllExtensions(ctx)
	if err != nil {
		return err
	}

	var attempted []string
	for _, id := range candidateExtensions {
		if !available[id] {
			continue
		}
		attempted = append(attempted, id)
		if err := enableExtension(ctx, id); err != nil {
			log.Printf("warn: enable %s: %v", id, err)
			continue
		}
		if waitForWatcher(ctx, 3*time.Second) {
			log.Printf("enabled GNOME extension %s for system-tray support", id)
			return nil
		}
	}
	if len(attempted) == 0 {
		return fmt.Errorf("no known appindicator GNOME extension is installed (looked for %s)", strings.Join(candidateExtensions, ", "))
	}
	return fmt.Errorf("enabled %s but StatusNotifierWatcher did not come up within 3s — tray icon may not appear", strings.Join(attempted, ", "))
}

func statusNotifierWatcherUp(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "dbus-send",
		"--session", "--type=method_call", "--print-reply",
		"--dest=org.kde.StatusNotifierWatcher",
		"/StatusNotifierWatcher",
		"org.freedesktop.DBus.Peer.Ping")
	return cmd.Run() == nil
}

func waitForWatcher(ctx context.Context, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if statusNotifierWatcherUp(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

func listEnabledExtensions(ctx context.Context) (map[string]bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "gnome-extensions", "list", "--enabled").Output()
	if err != nil {
		return nil, fmt.Errorf("gnome-extensions list --enabled: %w", err)
	}
	return linesToSet(string(out)), nil
}

func listAllExtensions(ctx context.Context) (map[string]bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "gnome-extensions", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("gnome-extensions list: %w", err)
	}
	return linesToSet(string(out)), nil
}

func enableExtension(ctx context.Context, id string) error {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "gnome-extensions", "enable", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func linesToSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out[line] = true
	}
	return out
}
