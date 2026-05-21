// Package ipc carries the daemon's request/response protocol over a unix
// socket. Requests are newline-delimited JSON; one response per request.
package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Cmd values accepted by the daemon.
const (
	CmdStatus   = "status"
	CmdRefresh  = "refresh"
	CmdSetLabel = "set-label"
	CmdQuit     = "quit"
)

type Request struct {
	Cmd string `json:"cmd"`
	// Value is a free-form payload. For CmdSetLabel: "5h" or "both".
	Value string `json:"value,omitempty"`
}

// WindowStatus is the per-window utilization breakdown.
type WindowStatus struct {
	Utilization float64    `json:"utilization"` // 0-100
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	// Extrapolated is true when Utilization isn't a fresh API value but a
	// projection (5h) or zeroing (7d) from the last good API call.
	Extrapolated bool `json:"extrapolated,omitempty"`
}

// CostBreakdown captures per-model cost in the current 5h block.
type CostBreakdown struct {
	Model      string  `json:"model"`
	CostUSD    float64 `json:"cost_usd"`
	InputTok   int64   `json:"input_tokens"`
	OutputTok  int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
}

// Snapshot is the full state the daemon returns on every IPC reply.
type Snapshot struct {
	FiveHour         WindowStatus    `json:"five_hour"`
	SevenDay         WindowStatus    `json:"seven_day"`
	BurnTokPerMin    float64         `json:"burn_rate_tokens_per_min"`
	BlockCostUSD     float64         `json:"block_cost_usd"`
	BlockCostByModel []CostBreakdown `json:"block_cost_by_model,omitempty"`
	ForecastNote     string          `json:"forecast,omitempty"`
	LabelMode        string          `json:"label_mode"`
	// HasAPI is true once we've successfully called the OAuth usage API at
	// least once and cached its result. When false, FiveHour and SevenDay
	// percentages are not meaningful — the icon should render an error state.
	HasAPI bool `json:"has_api"`
	// Extrapolated is set when FiveHour.Utilization isn't a fresh API value
	// but a projection from local-JSONL token growth since the last
	// successful API call. The number is still meaningful for the icon, but
	// the menu can choose to indicate uncertainty.
	Extrapolated   bool       `json:"extrapolated,omitempty"`
	APIStale       bool       `json:"api_stale"`
	APILastError   string     `json:"api_last_error,omitempty"`
	APILastSuccess *time.Time `json:"api_last_success,omitempty"`
	LastUpdateAt   *time.Time `json:"last_update_at,omitempty"`
}

type Response struct {
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
}

// SocketPath returns the canonical socket path for the current user.
//
// Preference order:
//  1. $XDG_RUNTIME_DIR/claude-watch.sock         (Linux, properly configured session)
//  2. /run/user/<uid>/claude-watch.sock          (Linux fallback)
//  3. $TMPDIR/claude-watch-<uid>.sock            (macOS — has a per-user $TMPDIR)
//  4. /tmp/claude-watch-<uid>.sock               (last resort)
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "claude-watch.sock")
	}
	uid := strconv.Itoa(os.Getuid())
	if linuxDir := filepath.Join("/run/user", uid); dirExists(linuxDir) {
		return filepath.Join(linuxDir, "claude-watch.sock")
	}
	name := "claude-watch-" + uid + ".sock"
	if dir := os.Getenv("TMPDIR"); dir != "" {
		return filepath.Join(dir, name)
	}
	return filepath.Join("/tmp", name)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// EnsureSocketDir makes sure the parent dir of the socket path exists.
func EnsureSocketDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}
