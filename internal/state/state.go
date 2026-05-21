// Package state is the daemon's central thread-safe state holder.
// It merges the OAuth API snapshot with the local-JSONL aggregate, and
// notifies one optional listener (the tray) on every change.
package state

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jrlucier/claude-watch/internal/claudeapi"
	"github.com/jrlucier/claude-watch/internal/ipc"
	"github.com/jrlucier/claude-watch/internal/usage"
)

// State holds the combined snapshot.
type State struct {
	mu sync.Mutex

	// API side.
	apiLastGood          *claudeapi.UsageResponse
	apiLastSuccess       time.Time
	apiLastError         string
	apiStale             bool
	apiLocalTokensAtCall int64 // local 5h-block tokens captured when the API last succeeded; used to extrapolate util when stale

	// Local-JSONL side.
	burnTokPerMin    float64
	blockCostUSD     float64
	blockTokens      int64
	blockCostByModel []ipc.CostBreakdown
	forecast         string

	// User preferences.
	labelMode string

	// Last refresh stamps either side touched.
	lastUpdate time.Time

	onChange func()
}

// New returns an empty State with the given initial label mode.
func New(labelMode string) *State {
	return &State{labelMode: labelMode}
}

// SetOnChange wires a single listener invoked after every successful update.
// The callback runs synchronously on the goroutine that drove the update —
// keep it cheap or hand work off.
func (s *State) SetOnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// SetLabelMode swaps the panel-label mode and fires onChange.
func (s *State) SetLabelMode(mode string) {
	s.mu.Lock()
	if mode == s.labelMode {
		s.mu.Unlock()
		return
	}
	s.labelMode = mode
	s.lastUpdate = time.Now()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// UpdateAPI records a successful API call. blockTokens is the local 5h-block
// token count *at the moment of the call* — capturing it lets us extrapolate
// utilization later when the API goes silent.
func (s *State) UpdateAPI(u claudeapi.UsageResponse, blockTokens int64) {
	s.mu.Lock()
	s.apiLastGood = &u
	s.apiLastSuccess = time.Now()
	s.apiLastError = ""
	s.apiStale = false
	s.apiLocalTokensAtCall = blockTokens
	s.lastUpdate = time.Now()
	cb := s.onChange
	// Snapshot the persistable bits while we still hold the lock, then do
	// the file I/O outside it so Snapshot() / Update*() readers aren't
	// blocked on disk writes.
	on := persisted{
		LastAPI:              s.apiLastGood,
		LastAPISuccess:       s.apiLastSuccess,
		APILocalTokensAtCall: s.apiLocalTokensAtCall,
	}
	s.mu.Unlock()
	persistToDisk(on)
	if cb != nil {
		cb()
	}
}

// RawAPI returns the last successful API response and a flag indicating
// whether it exists. The returned value is the *unmodified* API value (no
// extrapolation applied) — use this when you need the underlying ground
// truth, e.g. to avoid feedback loops through Snapshot().
func (s *State) RawAPI() (claudeapi.UsageResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apiLastGood == nil {
		return claudeapi.UsageResponse{}, false
	}
	return *s.apiLastGood, true
}

// MarkAPIError records a failed API call without dropping the last good values.
// The tray will show last-known with a stale indicator.
func (s *State) MarkAPIError(err error) {
	s.mu.Lock()
	s.apiLastError = err.Error()
	s.apiStale = true
	s.lastUpdate = time.Now()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// UpdateLocal records derived JSONL metrics.
func (s *State) UpdateLocal(burnTokPerMin, blockCostUSD float64, blockTokens int64, perModel []usage.PerModel, forecast string) {
	s.mu.Lock()
	s.burnTokPerMin = burnTokPerMin
	s.blockCostUSD = blockCostUSD
	s.blockTokens = blockTokens
	s.blockCostByModel = perModel2IPC(perModel)
	s.forecast = forecast
	s.lastUpdate = time.Now()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// persisted is the on-disk shape of the bits of state that survive a daemon
// restart. Local-JSONL data isn't persisted — it's cheap to recompute.
type persisted struct {
	LastAPI              *claudeapi.UsageResponse `json:"last_api,omitempty"`
	LastAPISuccess       time.Time                `json:"last_api_success,omitempty"`
	APILocalTokensAtCall int64                    `json:"api_local_tokens_at_call,omitempty"`
}

// statePath returns the canonical persisted-state path under XDG_STATE_HOME.
func statePath() (string, error) {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		stateDir = filepath.Join(home, ".local/state")
	}
	return filepath.Join(stateDir, "claude-watch", "last_state.json"), nil
}

// LoadFromDisk hydrates the State from the last persisted snapshot. The
// loaded data is marked stale immediately — the first successful API call
// after start will clear that. A missing or corrupt file is non-fatal; we
// just start with empty state.
func (s *State) LoadFromDisk() {
	p, err := statePath()
	if err != nil {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("state: read %s: %v", p, err)
		}
		return
	}
	var on persisted
	if err := json.Unmarshal(b, &on); err != nil {
		log.Printf("state: parse %s: %v", p, err)
		return
	}
	if on.LastAPI == nil {
		return
	}
	s.mu.Lock()
	s.apiLastGood = on.LastAPI
	s.apiLastSuccess = on.LastAPISuccess
	s.apiLocalTokensAtCall = on.APILocalTokensAtCall
	s.apiStale = true // restored, not freshly fetched
	s.mu.Unlock()
}

// persistToDisk writes the snapshot atomically. Called without holding the
// state lock — file I/O shouldn't block readers. Failures are logged but
// never surfaced — the daemon keeps running even if /tmp is full or the
// state dir was deleted out from under us.
func persistToDisk(on persisted) {
	if on.LastAPI == nil {
		return
	}
	p, err := statePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		log.Printf("state: mkdir: %v", err)
		return
	}
	b, err := json.MarshalIndent(on, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".state-*.json")
	if err != nil {
		log.Printf("state: create temp: %v", err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		log.Printf("state: write: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		log.Printf("state: close: %v", err)
		return
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		log.Printf("state: rename: %v", err)
		return
	}
}

func perModel2IPC(in []usage.PerModel) []ipc.CostBreakdown {
	out := make([]ipc.CostBreakdown, 0, len(in))
	for _, m := range in {
		out = append(out, ipc.CostBreakdown{
			Model:      m.Model,
			CostUSD:    m.CostUSD,
			InputTok:   m.InputTokens,
			OutputTok:  m.OutputTokens,
			CacheRead:  m.CacheReadTokens,
			CacheWrite: m.CacheWriteTokens,
		})
	}
	return out
}

// LabelMode returns the current label-mode string ("5h" or "both").
func (s *State) LabelMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.labelMode
}

// Snapshot returns an ipc.Snapshot — the same shape served back over IPC and
// consumed by the tray.
func (s *State) Snapshot() ipc.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := ipc.Snapshot{
		BurnTokPerMin:    s.burnTokPerMin,
		BlockCostUSD:     s.blockCostUSD,
		BlockCostByModel: s.blockCostByModel,
		ForecastNote:     s.forecast,
		LabelMode:        s.labelMode,
		HasAPI:           s.apiLastGood != nil,
		APIStale:         s.apiStale,
		APILastError:     s.apiLastError,
	}
	if !s.apiLastSuccess.IsZero() {
		t := s.apiLastSuccess
		snap.APILastSuccess = &t
	}
	if !s.lastUpdate.IsZero() {
		t := s.lastUpdate
		snap.LastUpdateAt = &t
	}
	if s.apiLastGood != nil {
		now := time.Now()
		fiveH := s.apiLastGood.FiveHour.Utilization
		sevenD := s.apiLastGood.SevenDay.Utilization
		fiveHExtrap := false
		sevenDExtrap := false

		// Extrapolation only kicks in when the API is stale. We derive a
		// per-token rate from the last successful call and apply it to the
		// local-JSONL token delta since then. The local 5h-block can roll
		// over while the API is silent — when that happens the local block
		// tokens drop below the captured baseline, and we treat them as a
		// fresh window starting from zero.
		if s.apiStale && s.apiLocalTokensAtCall > 0 {
			perToken := fiveH / float64(s.apiLocalTokensAtCall)
			var extrap float64
			switch {
			case s.apiLastGood.FiveHour.ResetsAt != nil && now.After(*s.apiLastGood.FiveHour.ResetsAt):
				// API said the block ends at T; we're past T. New block — the
				// % is whatever we've spent against the per-token rate so
				// far in the fresh window.
				extrap = float64(s.blockTokens) * perToken
			case s.blockTokens >= s.apiLocalTokensAtCall:
				// Same block, still growing.
				delta := float64(s.blockTokens - s.apiLocalTokensAtCall)
				extrap = fiveH + delta*perToken
			default:
				// Local block reset locally (chained-block rollover from
				// first-message), API reset time hasn't been crossed —
				// treat tokens as the fresh-window count.
				extrap = float64(s.blockTokens) * perToken
			}
			if extrap < 0 {
				extrap = 0
			}
			if extrap > 100 {
				extrap = 100
			}
			fiveH = extrap
			fiveHExtrap = true
		}

		// 7d doesn't track local tokens (its window is too long for a
		// useful local approximation). The only adjustment we make is
		// zeroing it once the weekly reset has passed, since we know the
		// new week starts at 0 even if we can't say how full it is yet.
		if s.apiStale && s.apiLastGood.SevenDay.ResetsAt != nil &&
			now.After(*s.apiLastGood.SevenDay.ResetsAt) {
			sevenD = 0
			sevenDExtrap = true
		}

		snap.FiveHour = ipc.WindowStatus{
			Utilization:  fiveH,
			ResetsAt:     s.apiLastGood.FiveHour.ResetsAt,
			Extrapolated: fiveHExtrap,
		}
		snap.SevenDay = ipc.WindowStatus{
			Utilization:  sevenD,
			ResetsAt:     s.apiLastGood.SevenDay.ResetsAt,
			Extrapolated: sevenDExtrap,
		}
		snap.Extrapolated = fiveHExtrap || sevenDExtrap
	}
	return snap
}
