package claudeapi

import "time"

// UsageResponse is the JSON returned by GET /api/oauth/usage.
//
// Field shape matches what the Haletran GNOME extension extracts in
// extension.js:295-338: top-level `five_hour` / `seven_day` blocks with
// `utilization` (number, 0-100) and `resets_at` (ISO 8601).
type UsageResponse struct {
	FiveHour Window `json:"five_hour"`
	SevenDay Window `json:"seven_day"`
}

// Window is one quota window's utilization snapshot.
type Window struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}
