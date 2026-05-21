// Package daemon owns the polling loops and fsnotify watches that drive
// state updates.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/jrlucier/claude-watch/internal/claudeapi"
	"github.com/jrlucier/claude-watch/internal/config"
	"github.com/jrlucier/claude-watch/internal/state"
	"github.com/jrlucier/claude-watch/internal/usage"
)

// Notifier delivers desktop notifications. Wired up by the tray package.
type Notifier interface {
	Notify(title, body string)
}

type noopNotifier struct{}

func (noopNotifier) Notify(string, string) {}

// Daemon ties the API client, the JSONL aggregator, and the state holder
// together with their polling timers.
type Daemon struct {
	cfg       config.Config
	api       *claudeapi.Client
	reader    *usage.Reader
	agg       *usage.Aggregator
	state     *state.State
	notifier  Notifier

	mu                  sync.Mutex
	refreshAPICh        chan struct{}
	refreshJSONLCh      chan struct{}
	notifiedFiveH       map[int]time.Time // threshold % → time it last fired in the current 5h window
	notifiedSevenD      map[int]time.Time
	lastResetFiveH      time.Time
	lastResetSevenD     time.Time
	consecutiveFailures int
}

// New constructs a Daemon. The caller still has to call Start.
func New(cfg config.Config, st *state.State) (*Daemon, error) {
	api, err := claudeapi.NewClient(cfg.ProxyURL)
	if err != nil {
		return nil, err
	}
	reader, err := usage.NewReader("")
	if err != nil {
		return nil, err
	}
	return &Daemon{
		cfg:            cfg,
		api:            api,
		reader:         reader,
		agg:            usage.NewAggregator(7 * 24 * time.Hour),
		state:          st,
		notifier:       noopNotifier{},
		refreshAPICh:   make(chan struct{}, 1),
		refreshJSONLCh: make(chan struct{}, 1),
		notifiedFiveH:  map[int]time.Time{},
		notifiedSevenD: map[int]time.Time{},
	}, nil
}

// SetNotifier wires up a desktop-notification sink.
func (d *Daemon) SetNotifier(n Notifier) {
	if n == nil {
		return
	}
	d.mu.Lock()
	d.notifier = n
	d.mu.Unlock()
}

// Start launches the polling goroutines and fsnotify watchers. Returns
// immediately; cancel ctx to stop.
//
// We run one synchronous JSONL scan before kicking off the loops so the
// first API success can capture a meaningful local-block-tokens baseline.
// Without it, the very first API call lands while the aggregator is empty
// and we'd persist apiLocalTokensAtCall=0 — which then disables future
// extrapolation until the next API success.
func (d *Daemon) Start(ctx context.Context) {
	d.doJSONL()
	go d.apiLoop(ctx)
	go d.jsonlLoop(ctx)
	go d.watchCredentials(ctx)
	go d.watchProjects(ctx)
}

// RefreshNow signals both loops to re-poll on their next iteration.
func (d *Daemon) RefreshNow() {
	d.kick(d.refreshAPICh)
	d.kick(d.refreshJSONLCh)
}

func (d *Daemon) kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// apiLoop polls the OAuth usage API on a timer, refreshes immediately when
// kicked, and applies the result to state. The next-poll interval is dynamic:
// the success path uses the configured cadence; 429s respect Retry-After (or
// back off exponentially); other errors get a gentler backoff. We never poll
// more often than once per MinAPIRefreshSeconds.
func (d *Daemon) apiLoop(ctx context.Context) {
	basePeriod := time.Duration(d.cfg.APIRefreshSeconds) * time.Second
	tick := time.NewTimer(0) // first call: immediate
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-d.refreshAPICh:
			if !tick.Stop() {
				select {
				case <-tick.C:
				default:
				}
			}
		}
		next := d.doAPI(ctx, basePeriod)
		tick.Reset(next)
	}
}

// doAPI runs one API call and returns the duration the caller should wait
// before the next attempt.
func (d *Daemon) doAPI(ctx context.Context, basePeriod time.Duration) time.Duration {
	token, err := claudeapi.ReadToken()
	if err != nil {
		d.state.MarkAPIError(err)
		return d.backoffPeriod(basePeriod)
	}
	u, err := d.api.Fetch(ctx, token)
	if err != nil {
		var herr *claudeapi.HTTPError
		if errors.As(err, &herr) {
			switch {
			case herr.Status == 401:
				// Token may be stale; retry on next tick after a normal poll
				// interval (the credentials-watcher will also kick us if a
				// refresh lands sooner).
				log.Printf("api 401: token may be stale; retry after %s", basePeriod)
				d.state.MarkAPIError(err)
				d.bumpFailure()
				return basePeriod
			case herr.Status == 429:
				wait := herr.RetryAfter
				if wait < basePeriod {
					wait = basePeriod
				}
				if wait > maxBackoff {
					wait = maxBackoff
				}
				log.Printf("api 429: rate-limited; backing off %s", wait)
				d.state.MarkAPIError(err)
				d.bumpFailure()
				return wait
			}
		}
		d.state.MarkAPIError(err)
		d.bumpFailure()
		return d.backoffPeriod(basePeriod)
	}
	d.resetFailure()
	// Capture the current local block-tokens at the moment the API call
	// landed — that's the baseline for extrapolation if the API later goes
	// silent.
	block := d.agg.CurrentBlock(time.Now())
	d.state.UpdateAPI(u, usage.BlockTokens(block.Records))
	d.maybeNotify(u)
	return basePeriod
}

// maxBackoff is the ceiling on the API poll interval. With base = 5 min, the
// schedule is 5 → 10 → 15, then 15 thereafter. Past 15 min an outage isn't
// recovering on its own and the user should rather see stale data refresh on
// a predictable cadence than have us back off into invisibility.
const maxBackoff = 15 * time.Minute

// bumpFailure / resetFailure / backoffPeriod implement a mild exponential
// backoff for non-429 errors.
func (d *Daemon) bumpFailure() {
	d.mu.Lock()
	d.consecutiveFailures++
	d.mu.Unlock()
}
func (d *Daemon) resetFailure() {
	d.mu.Lock()
	d.consecutiveFailures = 0
	d.mu.Unlock()
}
func (d *Daemon) backoffPeriod(base time.Duration) time.Duration {
	d.mu.Lock()
	n := d.consecutiveFailures
	d.mu.Unlock()
	// 1 fail → base; 2 → 2x; 3 → 4x; … capped at maxBackoff.
	// Clamp the shift count to >=0 — a negative shift count panics at
	// runtime, and while we only call this after bumpFailure(), the
	// invariant is fragile so we guard explicitly.
	shift := n - 1
	if shift < 0 {
		shift = 0
	}
	mult := 1 << minInt(shift, 5)
	wait := base * time.Duration(mult)
	if wait > maxBackoff {
		wait = maxBackoff
	}
	return wait
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// jsonlLoop polls the projects tree on a timer, refreshes immediately when
// kicked, and updates the local-derived metrics.
func (d *Daemon) jsonlLoop(ctx context.Context) {
	period := time.Duration(d.cfg.JSONLRefreshSeconds) * time.Second
	tick := time.NewTimer(0)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-d.refreshJSONLCh:
			if !tick.Stop() {
				select {
				case <-tick.C:
				default:
				}
			}
		}
		d.doJSONL()
		tick.Reset(period)
	}
}

func (d *Daemon) doJSONL() {
	recs, err := d.reader.Scan()
	if err != nil {
		log.Printf("jsonl scan: %v", err)
	}
	d.agg.Ingest(recs)
	now := time.Now()
	block := d.agg.CurrentBlock(now)
	perModel := usage.PerModelCosts(block.Records)
	cost := usage.BlockCostUSD(block.Records)
	tokens := usage.BlockTokens(block.Records)
	burn := usage.BurnRateTokensPerMin(block.Records, now)

	// Forecast must use the *raw* API util — feeding the extrapolated value
	// back through Forecast() would compound the projection on top of the
	// projection. Skip the forecast entirely when we're stale; the menu
	// already signals that with the "ℹ Estimated from local data" row.
	forecast := ""
	raw, ok := d.state.RawAPI()
	if ok {
		forecast = usage.Forecast(raw.FiveHour.Utilization, raw.FiveHour.ResetsAt, burn, now)
	}
	d.state.UpdateLocal(burn, cost, tokens, perModel, forecast)
}

// watchCredentials triggers an immediate API re-poll on any change to the
// credentials file. The `claude` CLI writes the token atomically (rename),
// which on Linux surfaces as a Create event on the directory and a Rename on
// the old path — watching the parent dir picks both up reliably.
func (d *Daemon) watchCredentials(ctx context.Context) {
	credPath, err := claudeapi.CredentialsPath()
	if err != nil {
		log.Printf("watch credentials: resolve path: %v", err)
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watch credentials: fsnotify: %v", err)
		return
	}
	defer w.Close()
	if err := w.Add(filepath.Dir(credPath)); err != nil {
		log.Printf("watch credentials: add %s: %v", filepath.Dir(credPath), err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != filepath.Base(credPath) {
				continue
			}
			d.kick(d.refreshAPICh)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("watch credentials: %v", err)
		}
	}
}

// watchProjects coalesces JSONL writes into a single re-aggregation per
// debounce window. Fsnotify can fire many Write events for a single Claude
// Code session as the JSONL is appended line by line.
func (d *Daemon) watchProjects(ctx context.Context) {
	dir, err := usage.ProjectsDir()
	if err != nil {
		log.Printf("watch projects: resolve path: %v", err)
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watch projects: fsnotify: %v", err)
		return
	}
	defer w.Close()
	// Recursive add: walk once at startup, then add each new subdir as we see
	// Create events.
	d.addAllSubdirs(w, dir)
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&fsnotify.Create != 0 {
				// New project dir or new session JSONL → watch any new dir.
				if isDir(ev.Name) {
					_ = w.Add(ev.Name)
				}
			}
			if !pending {
				pending = true
				debounce.Reset(750 * time.Millisecond)
			}
		case <-debounce.C:
			pending = false
			d.kick(d.refreshJSONLCh)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("watch projects: %v", err)
		}
	}
}

// addAllSubdirs walks root once and adds every directory to the watcher.
// Best-effort: a missing root (fresh user with no ~/.claude/projects yet) is
// fine, we just won't catch anything until it exists.
func (d *Daemon) addAllSubdirs(w *fsnotify.Watcher, root string) {
	_ = w.Add(root)
	_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil || de == nil {
			return nil
		}
		if de.IsDir() && path != root {
			_ = w.Add(path)
		}
		return nil
	})
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// Per-threshold notification: fire at most once per (window, threshold) pair.
// The window-reset detection uses resets_at: if it has advanced past the last
// known reset, clear the seen-threshold map for that window.
func (d *Daemon) maybeNotify(u claudeapi.UsageResponse) {
	if len(d.cfg.NotifyThresholds) == 0 {
		return
	}
	d.mu.Lock()
	n := d.notifier
	if n == nil {
		d.mu.Unlock()
		return
	}
	if u.FiveHour.ResetsAt != nil && u.FiveHour.ResetsAt.After(d.lastResetFiveH) {
		d.notifiedFiveH = map[int]time.Time{}
		d.lastResetFiveH = *u.FiveHour.ResetsAt
	}
	if u.SevenDay.ResetsAt != nil && u.SevenDay.ResetsAt.After(d.lastResetSevenD) {
		d.notifiedSevenD = map[int]time.Time{}
		d.lastResetSevenD = *u.SevenDay.ResetsAt
	}
	thresholds := append([]int(nil), d.cfg.NotifyThresholds...)
	sort.Ints(thresholds)
	// Collect the (title, body) pairs to fire while still holding the lock,
	// then notify outside it — desktop notifications can block on DBus.
	type msg struct{ title, body string }
	var pending []msg
	for _, t := range thresholds {
		if u.FiveHour.Utilization >= float64(t) {
			if _, seen := d.notifiedFiveH[t]; !seen {
				d.notifiedFiveH[t] = time.Now()
				pending = append(pending, msg{"Claude usage", formatThresholdMsg("5-hour", u.FiveHour.Utilization, t)})
			}
		}
		if u.SevenDay.Utilization >= float64(t) {
			if _, seen := d.notifiedSevenD[t]; !seen {
				d.notifiedSevenD[t] = time.Now()
				pending = append(pending, msg{"Claude usage", formatThresholdMsg("7-day", u.SevenDay.Utilization, t)})
			}
		}
	}
	d.mu.Unlock()
	for _, m := range pending {
		n.Notify(m.title, m.body)
	}
}

func formatThresholdMsg(window string, util float64, threshold int) string {
	return fmt.Sprintf("%s usage crossed %d%% (now %.1f%%)", window, threshold, util)
}
