// Package tray renders the claude-watch system tray icon and menu via
// fyne.io/systray (GNOME StatusNotifierItem on Linux, NSStatusBar on macOS).
//
// fyne.io/systray's menu API is append-only — items can only be hidden, not
// removed — so we pre-allocate a small pool of per-model rows.
package tray

import (
	"fmt"
	"log"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/jrlucier/claude-watch/internal/claudeapi"
	"github.com/jrlucier/claude-watch/internal/ipc"
)

const maxModelRows = 5

// Actions is the callback interface the tray uses to drive the daemon.
type Actions struct {
	Snapshot     func() ipc.Snapshot
	RefreshNow   func()
	SetLabelMode func(mode string)
	Quit         func()
}

// Tray owns the systray lifecycle plus menu items.
type Tray struct {
	actions Actions
	done    chan struct{}

	mu sync.Mutex

	fiveHRow    *systray.MenuItem
	sevenDRow   *systray.MenuItem
	burnRow     *systray.MenuItem
	forecastRow *systray.MenuItem
	costRow     *systray.MenuItem
	modelRows   []*systray.MenuItem
	staleRow    *systray.MenuItem
	extrapRow   *systray.MenuItem

	refreshItem *systray.MenuItem
	settings    *systray.MenuItem
	label5h     *systray.MenuItem
	labelBoth   *systray.MenuItem
	quitItem    *systray.MenuItem
}

// RunWith starts the tray. systray.Run blocks on the main goroutine; the
// optional ready callback fires once the menu is built so the daemon can wire
// up SetOnChange and do an initial Refresh.
func RunWith(actions Actions, ready func(*Tray)) {
	t := &Tray{actions: actions, done: make(chan struct{})}
	systray.Run(func() {
		t.onReady()
		if ready != nil {
			ready(t)
		}
	}, t.onExit)
}

func (t *Tray) onReady() {
	t.applyIcon(0, 0, true, false) // grey "waiting" state until first snapshot
	systray.SetTitle("…")
	systray.SetTooltip("claude-watch — initializing")

	// Stale banner + extrapolation note both sit at the very top of the menu
	// and are hidden whenever the API is healthy. We deliberately omit a
	// separator after them — the fyne.io/systray API can't hide separators,
	// and a dangling line at the top of the menu reads as a layout bug.
	t.staleRow = systray.AddMenuItem("⚠ API stale", "")
	t.staleRow.Disable()
	t.staleRow.Hide()
	t.extrapRow = systray.AddMenuItem("ℹ Estimated from local data", "")
	t.extrapRow.Disable()
	t.extrapRow.Hide()

	t.fiveHRow = systray.AddMenuItem("5h …", "")
	t.fiveHRow.Disable()
	t.sevenDRow = systray.AddMenuItem("7d …", "")
	t.sevenDRow.Disable()
	systray.AddSeparator()

	t.burnRow = systray.AddMenuItem("Pace …", "")
	t.burnRow.Disable()
	t.forecastRow = systray.AddMenuItem("Outlook …", "")
	t.forecastRow.Disable()
	systray.AddSeparator()

	t.costRow = systray.AddMenuItem("This block …", "")
	t.costRow.Disable()
	t.modelRows = make([]*systray.MenuItem, maxModelRows)
	for i := range t.modelRows {
		item := systray.AddMenuItem("", "")
		item.Disable()
		item.Hide()
		t.modelRows[i] = item
	}
	systray.AddSeparator()

	t.refreshItem = systray.AddMenuItem("Refresh", "Poll the API and re-aggregate now")
	t.settings = systray.AddMenuItem("Settings", "")
	t.label5h = t.settings.AddSubMenuItemCheckbox("Show 5h only", "Just the 5-hour percent in the panel", false)
	t.labelBoth = t.settings.AddSubMenuItemCheckbox("Show 5h and 7d", "Both percents in the panel", false)
	t.quitItem = systray.AddMenuItem("Quit", "")

	go t.handleClicks()
}

func (t *Tray) handleClicks() {
	for {
		select {
		case <-t.done:
			return
		case <-t.refreshItem.ClickedCh:
			if t.actions.RefreshNow != nil {
				t.actions.RefreshNow()
			}
		case <-t.label5h.ClickedCh:
			if t.actions.SetLabelMode != nil {
				t.actions.SetLabelMode("5h")
			}
		case <-t.labelBoth.ClickedCh:
			if t.actions.SetLabelMode != nil {
				t.actions.SetLabelMode("both")
			}
		case <-t.quitItem.ClickedCh:
			if t.actions.Quit != nil {
				t.actions.Quit()
			}
			return
		}
	}
}

func (t *Tray) onExit() { close(t.done) }

// Refresh re-renders the tray from the latest snapshot. Safe to call from any
// goroutine — serialized under t.mu.
func (t *Tray) Refresh() {
	if t.actions.Snapshot == nil {
		return
	}
	snap := t.actions.Snapshot()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.applyIcon(snap.FiveHour.Utilization, snap.SevenDay.Utilization, snap.APIStale, snap.HasAPI)
	systray.SetTitle(buildTitle(snap))
	systray.SetTooltip(buildTooltip(snap))

	now := time.Now()
	t.fiveHRow.SetTitle(formatWindowRow("5h", snap.FiveHour, now))
	t.sevenDRow.SetTitle(formatWindowRow("7d", snap.SevenDay, now))
	t.burnRow.SetTitle(fmt.Sprintf("Pace: %s tok/min", humanInt(snap.BurnTokPerMin)))
	if snap.ForecastNote != "" {
		t.forecastRow.SetTitle("Outlook: " + snap.ForecastNote)
	} else {
		t.forecastRow.SetTitle("Outlook: steady")
	}
	t.costRow.SetTitle(fmt.Sprintf("This block: $%.2f", snap.BlockCostUSD))

	for i, slot := range t.modelRows {
		if i < len(snap.BlockCostByModel) {
			m := snap.BlockCostByModel[i]
			slot.SetTitle(fmt.Sprintf("  %s  $%.2f", m.Model, m.CostUSD))
			slot.Show()
		} else {
			slot.Hide()
		}
	}

	if snap.APIStale {
		title := "⚠ API stale"
		if friendly := claudeapi.FriendlyError(snap.APILastError); friendly != "" {
			title += " · " + friendly
		}
		t.staleRow.SetTitle(title)
		t.staleRow.Show()
	} else {
		t.staleRow.Hide()
	}
	if snap.Extrapolated {
		t.extrapRow.Show()
	} else {
		t.extrapRow.Hide()
	}

	switch snap.LabelMode {
	case "both":
		t.labelBoth.Check()
		t.label5h.Uncheck()
	default:
		t.label5h.Check()
		t.labelBoth.Uncheck()
	}
}

// applyIcon renders the tray icon and pushes it via SetIcon.
func (t *Tray) applyIcon(fiveH, sevenD float64, stale, hasAPI bool) {
	icon, err := RenderBars(fiveH, sevenD, stale, hasAPI)
	if err != nil {
		log.Printf("warn: render icon: %v", err)
		return
	}
	systray.SetIcon(icon)
}

// buildTitle is the short label rendered next to the icon in the panel.
func buildTitle(snap ipc.Snapshot) string {
	five := int(snap.FiveHour.Utilization + 0.5)
	seven := int(snap.SevenDay.Utilization + 0.5)
	switch snap.LabelMode {
	case "both":
		return fmt.Sprintf("5h %d%% / 7d %d%%", five, seven)
	default:
		return fmt.Sprintf("%d%%", five)
	}
}

// buildTooltip is the hover-over text on the panel icon.
func buildTooltip(snap ipc.Snapshot) string {
	stale := ""
	if snap.APIStale {
		stale = "  (stale)"
	}
	five := snap.FiveHour.Utilization
	seven := snap.SevenDay.Utilization
	burn := humanInt(snap.BurnTokPerMin)
	return fmt.Sprintf("claude-watch: 5h %.0f%% · 7d %.0f%% · %s tok/min%s",
		five, seven, burn, stale)
}

// formatWindowRow renders one of the two utilization rows in the menu, with a
// unicode-block progress bar for visibility. A "~" sits in front of the
// percentage when the value is extrapolated from local data rather than fresh
// from the API.
func formatWindowRow(name string, w ipc.WindowStatus, now time.Time) string {
	bar := unicodeBar(w.Utilization, 20)
	reset := ""
	if w.ResetsAt != nil {
		d := w.ResetsAt.Sub(now)
		if d > 0 {
			reset = "  (resets in " + shortDuration(d) + ")"
		} else {
			reset = "  (resetting now)"
		}
	}
	prefix := " "
	if w.Extrapolated {
		prefix = "~"
	}
	return fmt.Sprintf("%s  %s  %s%3.0f%%%s", name, bar, prefix, w.Utilization, reset)
}

// unicodeBar makes a `cells`-wide bar using "█" filled / "▒" empty.
func unicodeBar(pct float64, cells int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(cells) + 0.5)
	if filled > cells {
		filled = cells
	}
	out := make([]rune, cells)
	for i := 0; i < cells; i++ {
		if i < filled {
			out[i] = '█'
		} else {
			out[i] = '▒'
		}
	}
	return string(out)
}

// shortDuration renders e.g. "2h 15m", "45m", "4d 7h".
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if h <= 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// humanInt renders 1234.5 as "1.2k" and 1500000 as "1.5M". The tray cares
// about thumb-readability, not precision.
func humanInt(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%d", int(v+0.5))
	}
}
