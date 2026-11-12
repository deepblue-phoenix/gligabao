package config

import "github.com/deepblue-phoenix/gligabao/internal/ai"

// AIPrices converts the configured price overrides (prices) into those of the ai package; omitted cache prices default to the input
// price. Use it with ai.NewPriceTable.
func (c *Config) AIPrices() map[string]ai.Price {
	out := make(map[string]ai.Price, len(c.Prices))
	for model, p := range c.Prices {
		ap := ai.Price{
			Input:         p.Input,
			Output:        p.Output,
			CacheWrite:    p.Input,
			CacheRead:     p.Input,
			LongThreshold: p.LongThreshold,
			LongInput:     p.LongInput,
			LongOutput:    p.LongOutput,
		}
		if p.CacheWrite != nil {
			ap.CacheWrite = *p.CacheWrite
		}
		if p.CacheRead != nil {
			ap.CacheRead = *p.CacheRead
		}
		out[model] = ap
	}
	return out
}
