package usage

import (
	"fmt"
	"time"
)

// burnWindow is the lookback for computing tokens/min. Short enough to react
// to a current spike, long enough to ride out one-message lulls.
const burnWindow = 10 * time.Minute

// BurnRateTokensPerMin returns the recent (~10 min) total-token-per-minute
// burn rate across all the records given. Returns 0 when there's not enough
// history to compute a meaningful rate.
func BurnRateTokensPerMin(recs []Record, now time.Time) float64 {
	cutoff := now.Add(-burnWindow)
	var total int64
	var first time.Time
	for _, r := range recs {
		if r.Timestamp.Before(cutoff) {
			continue
		}
		if first.IsZero() || r.Timestamp.Before(first) {
			first = r.Timestamp
		}
		total += r.InputTok + r.OutputTok + r.CacheReadTok + r.CacheWrite5mTok + r.CacheWrite1hTok
	}
	if total == 0 || first.IsZero() {
		return 0
	}
	span := now.Sub(first).Minutes()
	if span < 0.5 {
		span = 0.5
	}
	return float64(total) / span
}

// Forecast returns a human-readable forecast string. Strategy:
//   - If we have a 5h utilization % and a recent burn rate, project when 100 %
//     would be hit assuming the burn continues linearly.
//   - Cross-reference against resetsAt — if exhaustion is past the reset, say
//     "OK through reset".
//
// All inputs may be zero/nil; an empty string is a valid result.
func Forecast(fiveHourPct float64, resetsAt *time.Time, burnTokPerMin float64, now time.Time) string {
	if fiveHourPct <= 0 || burnTokPerMin <= 0 {
		return ""
	}
	if fiveHourPct >= 100 {
		return "5h limit hit"
	}
	// We can't translate burn rate into utilization % directly (Anthropic's
	// limit is in tokens of some weighted mix we don't know), so we estimate
	// the per-minute rise in utilization from the API itself: how much did the
	// % move per minute since the block started? — but we don't have that
	// history here. Instead, approximate using the heuristic that the % has
	// risen monotonically from 0 since the block began. resetsAt + 5h ago is
	// the block start; (now - start) gives the elapsed minutes; current % over
	// elapsed gives the per-minute rise; divide remaining % by that.
	if resetsAt == nil {
		return ""
	}
	start := resetsAt.Add(-BlockDuration)
	elapsed := now.Sub(start).Minutes()
	if elapsed < 1 {
		return ""
	}
	risePerMin := fiveHourPct / elapsed
	if risePerMin <= 0 {
		return ""
	}
	minToFull := (100 - fiveHourPct) / risePerMin
	exhaustionAt := now.Add(time.Duration(minToFull) * time.Minute)
	if exhaustionAt.After(*resetsAt) {
		return "steady through reset"
	}
	return fmt.Sprintf("5h cap in ~%s", shortDuration(time.Duration(minToFull)*time.Minute))
}

// shortDuration renders e.g. "2h 40m" or "35m".
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if h <= 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
