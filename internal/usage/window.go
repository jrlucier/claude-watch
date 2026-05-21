package usage

import (
	"sort"
	"sync"
	"time"
)

// BlockDuration is the rolling-window length Claude bills against for the
// "5-hour" quota.
const BlockDuration = 5 * time.Hour

// Aggregator keeps the history of records in memory and exposes derived
// statistics (current 5h block, 7-day totals, burn rate). Safe for concurrent
// access via the public methods.
type Aggregator struct {
	mu       sync.Mutex
	records  []Record          // sorted by Timestamp; capped at retentionWindow
	seenIDs  map[string]struct{}
	retention time.Duration
}

// NewAggregator creates an Aggregator with the given retention horizon —
// records older than this are dropped on Ingest to bound memory.
func NewAggregator(retention time.Duration) *Aggregator {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return &Aggregator{seenIDs: map[string]struct{}{}, retention: retention}
}

// Ingest appends new records, deduplicating by Message ID.
func (a *Aggregator) Ingest(recs []Record) {
	if len(recs) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range recs {
		if r.MessageID != "" {
			if _, seen := a.seenIDs[r.MessageID]; seen {
				continue
			}
			a.seenIDs[r.MessageID] = struct{}{}
		}
		a.records = append(a.records, r)
	}
	sort.Slice(a.records, func(i, j int) bool { return a.records[i].Timestamp.Before(a.records[j].Timestamp) })
	a.evictOldLocked(time.Now())
}

func (a *Aggregator) evictOldLocked(now time.Time) {
	cutoff := now.Add(-a.retention)
	keep := 0
	for _, r := range a.records {
		if !r.Timestamp.Before(cutoff) {
			break
		}
		keep++
	}
	if keep == 0 {
		return
	}
	for i := 0; i < keep; i++ {
		if id := a.records[i].MessageID; id != "" {
			delete(a.seenIDs, id)
		}
	}
	a.records = a.records[keep:]
}

// Block is a snapshot of one 5-hour billing window.
type Block struct {
	Start time.Time
	End   time.Time
	Records []Record
}

// CurrentBlock returns the active 5h block (the one containing now) and the
// list of records inside it. Blocks chain from the first record we've seen —
// matches ccusage's segmenting heuristic (block N+1 starts when block N ends).
//
// If there are no records, returns a block anchored at now with zero records.
func (a *Aggregator) CurrentBlock(now time.Time) Block {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.records) == 0 {
		return Block{Start: now, End: now.Add(BlockDuration)}
	}
	// Block start = first record's timestamp + N * BlockDuration such that
	// start <= now < start + BlockDuration.
	first := a.records[0].Timestamp
	n := int64(now.Sub(first) / BlockDuration)
	if n < 0 {
		n = 0
	}
	start := first.Add(time.Duration(n) * BlockDuration)
	end := start.Add(BlockDuration)
	var recs []Record
	for _, r := range a.records {
		if r.Timestamp.Before(start) {
			continue
		}
		if !r.Timestamp.Before(end) {
			break
		}
		recs = append(recs, r)
	}
	return Block{Start: start, End: end, Records: recs}
}

// SevenDayRecords returns every record within the last 7 days of now.
func (a *Aggregator) SevenDayRecords(now time.Time) []Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := now.Add(-7 * 24 * time.Hour)
	idx := sort.Search(len(a.records), func(i int) bool {
		return !a.records[i].Timestamp.Before(cutoff)
	})
	out := make([]Record, len(a.records)-idx)
	copy(out, a.records[idx:])
	return out
}

// PerModelCost groups records by model and returns sorted per-model totals.
type PerModel struct {
	Model           string
	CostUSD         float64
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	CacheWriteTokens int64
}

// PerModelCosts aggregates the given records into a per-model breakdown,
// sorted by descending cost.
func PerModelCosts(recs []Record) []PerModel {
	byModel := map[string]*PerModel{}
	for _, r := range recs {
		m, ok := byModel[r.Model]
		if !ok {
			m = &PerModel{Model: r.Model}
			byModel[r.Model] = m
		}
		m.CostUSD += r.CostUSD()
		m.InputTokens += r.InputTok
		m.OutputTokens += r.OutputTok
		m.CacheReadTokens += r.CacheReadTok
		m.CacheWriteTokens += r.CacheWrite5mTok + r.CacheWrite1hTok
	}
	out := make([]PerModel, 0, len(byModel))
	for _, m := range byModel {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CostUSD > out[j].CostUSD })
	return out
}

// BlockCostUSD totals USD across the records.
func BlockCostUSD(recs []Record) float64 {
	var total float64
	for _, r := range recs {
		total += r.CostUSD()
	}
	return total
}

// BlockTokens totals every token bucket across the records. Used to derive
// an extrapolated utilization % when the OAuth API is rate-limited and we
// need to fall back to local data.
func BlockTokens(recs []Record) int64 {
	var total int64
	for _, r := range recs {
		total += r.InputTok + r.OutputTok + r.CacheReadTok + r.CacheWrite5mTok + r.CacheWrite1hTok
	}
	return total
}
