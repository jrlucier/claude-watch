package usage

// ModelPricing is per-million-token rates in USD for one model.
type ModelPricing struct {
	Input          float64 // USD per million input tokens
	Output         float64 // USD per million output tokens
	CacheRead      float64 // USD per million cache-read tokens
	CacheWrite5m   float64 // USD per million 5-min cache-creation tokens
	CacheWrite1h   float64 // USD per million 1-hour cache-creation tokens
}

// pricing is a hardcoded table of public Anthropic prices, current as of 2026.
// Falls back to the empty value (zero cost) for unknown models — better than
// failing the whole aggregation when Anthropic ships a new model.
var pricing = map[string]ModelPricing{
	// Claude 4.7 (Opus 4.7 latest)
	"claude-opus-4-7":            {Input: 5.0, Output: 25.0, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10.0},
	"claude-opus-4-7-1m":         {Input: 5.0, Output: 25.0, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10.0},
	"claude-sonnet-4-6":          {Input: 3.0, Output: 15.0, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6.0},
	"claude-haiku-4-5":           {Input: 1.0, Output: 5.0, CacheRead: 0.10, CacheWrite5m: 1.25, CacheWrite1h: 2.0},
	"claude-haiku-4-5-20251001":  {Input: 1.0, Output: 5.0, CacheRead: 0.10, CacheWrite5m: 1.25, CacheWrite1h: 2.0},

	// Older 4.x — kept for users on prior model pins
	"claude-opus-4":      {Input: 15.0, Output: 75.0, CacheRead: 1.50, CacheWrite5m: 18.75, CacheWrite1h: 30.0},
	"claude-sonnet-4":    {Input: 3.0, Output: 15.0, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6.0},
	"claude-3-5-sonnet":  {Input: 3.0, Output: 15.0, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6.0},
	"claude-3-5-haiku":   {Input: 0.80, Output: 4.0, CacheRead: 0.08, CacheWrite5m: 1.0, CacheWrite1h: 1.6},
}

// PricingFor returns the rates for an exact model id, or the zero value if
// unknown. Callers can still aggregate token counts; cost just won't add up
// for unrecognized models.
func PricingFor(model string) ModelPricing {
	return pricing[model]
}

// CostUSD returns the dollar cost of one record's token mix at the given rates.
// We can't tell which slice of cache_creation_input_tokens fell into the 5m vs
// 1h bucket without the breakdown object, so the caller passes them in
// separately when available; otherwise we use the 5m rate as a conservative
// default.
func (p ModelPricing) CostUSD(in, out, cacheRead, cacheWrite5m, cacheWrite1h int64) float64 {
	const M = 1_000_000.0
	return float64(in)/M*p.Input +
		float64(out)/M*p.Output +
		float64(cacheRead)/M*p.CacheRead +
		float64(cacheWrite5m)/M*p.CacheWrite5m +
		float64(cacheWrite1h)/M*p.CacheWrite1h
}
