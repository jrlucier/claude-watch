// Package tray renders the claude-watch system tray icon and menu via
// fyne.io/systray (GNOME StatusNotifierItem on Linux, NSStatusBar on macOS).
//
// fyne.io/systray's menu API is append-only — items can only be hidden, not
// removed — so we pre-allocate a small pool of per-model rows.
package tray

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/jrlucier/claude-watch/internal/claudeapi"
	"github.com/jrlucier/claude-watch/internal/ipc"
)

// Actions is the callback interface the tray uses to drive the daemon.
type Actions struct {
	Snapshot      func() ipc.Snapshot
	RefreshNow    func()
	SetLabelMode  func(mode string)
	SetTimeFormat func(format string)
	Quit          func()
}

// Tray owns the systray lifecycle plus menu items.
type Tray struct {
	actions Actions
	done    chan struct{}

	mu sync.Mutex

	fiveHRow       *systray.MenuItem
	fiveHResetRow  *systray.MenuItem
	sevenDRow      *systray.MenuItem
	sevenDResetRow *systray.MenuItem
	paceRow        *systray.MenuItem
	staleRow       *systray.MenuItem
	extrapRow      *systray.MenuItem

	refreshItem *systray.MenuItem
	settings    *systray.MenuItem
	label5h     *systray.MenuItem
	labelBoth   *systray.MenuItem
	time12hr    *systray.MenuItem
	time24hr    *systray.MenuItem
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
	t.applyIcon(0, 0, true, false, "5h", false) // grey "waiting" state until first snapshot
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

	// Pace leads the menu: it's the "should I stop or keep going" question
	// that the icon is also trying to answer. Bars are the detail beneath.
	t.paceRow = systray.AddMenuItem("…", "")
	t.paceRow.Disable()
	systray.AddSeparator()

	t.fiveHRow = systray.AddMenuItem("5h …", "")
	t.fiveHRow.Disable()
	t.fiveHResetRow = systray.AddMenuItem("", "")
	t.fiveHResetRow.Disable()
	t.fiveHResetRow.Hide()
	t.sevenDRow = systray.AddMenuItem("7d …", "")
	t.sevenDRow.Disable()
	t.sevenDResetRow = systray.AddMenuItem("", "")
	t.sevenDResetRow.Disable()
	t.sevenDResetRow.Hide()
	systray.AddSeparator()

	t.refreshItem = systray.AddMenuItem("Refresh", "Poll the API and re-aggregate now")
	t.settings = systray.AddMenuItem("Settings", "")
	t.label5h = t.settings.AddSubMenuItemCheckbox("Show 5h only", "Just the 5-hour percent in the panel", false)
	t.labelBoth = t.settings.AddSubMenuItemCheckbox("Show 5h and 7d", "Both percents in the panel", false)
	t.settings.AddSeparator()
	t.time12hr = t.settings.AddSubMenuItemCheckbox("12-hour clock", "Reset times shown as e.g. \"Wed 9am\"", false)
	t.time24hr = t.settings.AddSubMenuItemCheckbox("24-hour clock", "Reset times shown as e.g. \"Wed 09:00\"", false)
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
		case <-t.time12hr.ClickedCh:
			if t.actions.SetTimeFormat != nil {
				t.actions.SetTimeFormat("12h")
			}
		case <-t.time24hr.ClickedCh:
			if t.actions.SetTimeFormat != nil {
				t.actions.SetTimeFormat("24h")
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

	t.applyIcon(snap.FiveHour.Utilization, snap.SevenDay.Utilization, snap.APIStale, snap.HasAPI, snap.LabelMode, snap.PaceState == "hot")
	systray.SetTitle(buildTitle(snap))
	systray.SetTooltip(buildTooltip(snap))

	now := time.Now()
	t.fiveHRow.SetTitle(formatWindowRow("5h", snap.FiveHour))
	if reset := formatResetRow(snap.FiveHour, now, snap.TimeFormat); reset != "" {
		t.fiveHResetRow.SetTitle(reset)
		t.fiveHResetRow.Show()
	} else {
		t.fiveHResetRow.Hide()
	}
	t.sevenDRow.SetTitle(formatWindowRow("7d", snap.SevenDay))
	if reset := formatResetRow(snap.SevenDay, now, snap.TimeFormat); reset != "" {
		t.sevenDResetRow.SetTitle(reset)
		t.sevenDResetRow.Show()
	} else {
		t.sevenDResetRow.Hide()
	}
	t.paceRow.SetTitle(centerRow(paceText(snap, now)))

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

	switch snap.TimeFormat {
	case "24h":
		t.time24hr.Check()
		t.time12hr.Uncheck()
	default:
		t.time12hr.Check()
		t.time24hr.Uncheck()
	}
}

// applyIcon renders the tray icon and pushes it via SetIcon.
func (t *Tray) applyIcon(fiveH, sevenD float64, stale, hasAPI bool, labelMode string, paceHot bool) {
	icon, err := RenderBars(fiveH, sevenD, stale, hasAPI, labelMode, paceHot)
	if err != nil {
		log.Printf("warn: render icon: %v", err)
		return
	}
	systray.SetIcon(icon)
}

// buildTitle is the short label rendered next to the icon in the panel.
// Pace-hot is conveyed by the icon's bar track (pink), so the title text
// itself stays clean.
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
	pace := ""
	switch snap.PaceState {
	case "hot":
		pace = " · hot"
	case "capped":
		pace = " · at cap"
	case "on-pace":
		pace = " · on pace"
	case "idle":
		pace = " · idle"
	}
	five := snap.FiveHour.Utilization
	seven := snap.SevenDay.Utilization
	burn := humanInt(snap.BurnTokPerMin)
	return fmt.Sprintf("claude-watch: 5h %.0f%% · 7d %.0f%% · %s tok/min%s%s",
		five, seven, burn, pace, stale)
}

// paceText renders the merged Pace row: state-led copy followed by burn
// rate and projection where they add information. Vocabulary is shared
// with the tooltip and notification so the surfaces teach each other:
// ▲ hot, ✓ steady, ● capped.
func paceText(snap ipc.Snapshot, now time.Time) string {
	burn := humanInt(snap.BurnTokPerMin) + " tok/min"
	switch snap.PaceState {
	case "capped":
		if snap.FiveHour.ResetsAt != nil {
			if d := snap.FiveHour.ResetsAt.Sub(now); d > 0 {
				return "● capped · resets in " + shortDuration(d)
			}
		}
		return "● capped"
	case "hot":
		// The forecast string is "5h cap in ~Xh Ym"; the "5h" prefix is
		// redundant in a row that's already about the 5h budget.
		eta := strings.TrimPrefix(snap.ForecastNote, "5h ")
		if eta == "" {
			return "▲ hot · " + burn
		}
		return "▲ hot · " + burn + " · " + eta
	case "on-pace":
		return "✓ steady · " + burn
	case "idle":
		return "idle"
	default:
		if snap.ForecastNote != "" {
			return burn + " · " + snap.ForecastNote
		}
		return burn
	}
}

// centerRow pads `text` with leading spaces so it lands near the visual
// center of a bar row (which spans ~bar-prefix + 20-cell bar + percentage).
// Block characters render ~2× as wide as a space in the menu's proportional
// font, putting the visual center around column 24 in space-widths.
func centerRow(text string) string {
	const barVisualCenter = 24
	pad := barVisualCenter - len(text)/2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + text
}

// formatWindowRow renders one of the two utilization rows in the menu, with a
// unicode-block progress bar for visibility. A "~" sits in front of the
// percentage when the value is extrapolated from local data rather than fresh
// from the API. The reset time renders on its own row beneath via
// formatResetRow.
func formatWindowRow(name string, w ipc.WindowStatus) string {
	bar := unicodeBar(w.Utilization, 20)
	prefix := " "
	if w.Extrapolated {
		prefix = "~"
	}
	return fmt.Sprintf("%s  %s  %s%3.0f%%", name, bar, prefix, w.Utilization)
}

// formatResetRow renders the line below a window row, e.g.
// "resets Wed 9am in 4d 2h", padded with leading spaces so the text sits
// roughly under the centre of the 20-cell bar above. Returns "" when there's
// no reset time to show. timeFormat is "12h" (default) or "24h".
func formatResetRow(w ipc.WindowStatus, now time.Time, timeFormat string) string {
	if w.ResetsAt == nil {
		return ""
	}
	var text string
	if d := w.ResetsAt.Sub(now); d > 0 {
		text = "resets " + formatResetTime(w.ResetsAt.Local(), timeFormat) + " in " + shortDuration(d)
	} else {
		text = "resetting now"
	}
	return centerRow(text)
}

// formatResetTime renders a reset time per the user's clock-format
// preference. 12-hour mode: "Wed 9am" or "Wed 9:30am". 24-hour mode:
// "Wed 09:00".
func formatResetTime(t time.Time, format string) string {
	if format == "24h" {
		return t.Format("Mon 15:04")
	}
	if t.Minute() == 0 {
		return t.Format("Mon 3pm")
	}
	return t.Format("Mon 3:04pm")
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
