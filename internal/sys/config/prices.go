package config

import "github.com/deepblue-phoenix/gligabao/internal/llm"

// AIPrices converts the configured price overrides (prices) into those of the llm package; omitted cache prices default to the input
// price. Use it with llm.NewPriceTable.
func (c *Config) AIPrices() map[string]llm.Price {
	out := make(map[string]llm.Price, len(c.Prices))
	for model, p := range c.Prices {
		ap := llm.Price{
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
