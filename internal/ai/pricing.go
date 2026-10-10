package ai

import (
	"sort"
	"strings"
)

//---------------------------------------------------------------------------------------------

// Provider names stored with each call.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// Price is a model's price in USD per 1M tokens.
type Price struct {
	Input      float64
	Output     float64
	CacheWrite float64 // Anthropic prompt-cache writes
	CacheRead  float64 // Anthropic prompt-cache hits

	// Optional long-context tier: when a call's total input tokens (including cache) exceed
	// LongThreshold, the whole call's Input/Output use LongInput/LongOutput. 0 disables the tier.
	LongThreshold int
	LongInput     float64
	LongOutput    float64
}

// DefaultPrices returns the built-in price table, keyed by model-name prefix. Neither API returns the
// cost of a call, so it is computed from token usage with this table. The table can be extended or
// overridden from the config file (see config.Config.Prices).
func DefaultPrices() map[string]Price {
	return map[string]Price{
		// Anthropic
		"claude-opus-4-5":   {Input: 5, Output: 25, CacheWrite: 6.25, CacheRead: 0.5},
		"claude-opus-4-1":   {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.5},
		"claude-opus-4":     {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.5},
		"claude-sonnet-4-5": {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3},
		"claude-sonnet-4":   {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3},
		"claude-3-7-sonnet": {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3},
		"claude-haiku-5-5":  {Input: 0.1, Output: 0.5, CacheWrite: 0.125, CacheRead: 0.01, LongThreshold: 100_000, LongInput: 0.5, LongOutput: 2.5},
		"claude-haiku-4-5":  {Input: 1, Output: 5, CacheWrite: 1.25, CacheRead: 0.1},
		"claude-3-5-haiku":  {Input: 0.8, Output: 4, CacheWrite: 1, CacheRead: 0.08},
		"claude-3-haiku":    {Input: 0.25, Output: 1.25, CacheWrite: 0.3, CacheRead: 0.03},
		// OpenAI embeddings
		"text-embedding-3-small": {Input: 0.02, CacheWrite: 0.02, CacheRead: 0.02},
		"text-embedding-3-large": {Input: 0.13, CacheWrite: 0.13, CacheRead: 0.13},
		"text-embedding-ada-002": {Input: 0.10, CacheWrite: 0.10, CacheRead: 0.10},
	}
}

// PriceTable looks up model prices. A response model such as "claude-haiku-4-5-20251001" matches the
// longest key that is a prefix of it ("claude-haiku-4-5").
type PriceTable struct {
	prices map[string]Price
	keys   []string // longest first
}

// NewPriceTable returns DefaultPrices with overrides merged in (an override replaces the default
// entry of the same key; other keys are added). Keys are matched case-insensitively.
func NewPriceTable(overrides map[string]Price) *PriceTable {
	t := &PriceTable{prices: map[string]Price{}}
	for k, p := range DefaultPrices() {
		t.prices[strings.ToLower(k)] = p
	}
	for k, p := range overrides {
		t.prices[strings.ToLower(strings.TrimSpace(k))] = p
	}
	for k := range t.prices {
		t.keys = append(t.keys, k)
	}
	sort.Slice(t.keys, func(i, j int) bool {
		if len(t.keys[i]) != len(t.keys[j]) {
			return len(t.keys[i]) > len(t.keys[j])
		}
		return t.keys[i] < t.keys[j]
	})
	return t
}

// Usage is the token usage of one call.
type Usage struct {
	InputTokens      int // non-cached input tokens
	OutputTokens     int
	CacheWriteTokens int // Anthropic prompt-cache writes
	CacheReadTokens  int // Anthropic prompt-cache reads
}

// Cost returns the USD cost of usage on model. ok is false if the model has no known price (cost is then 0).
func (t *PriceTable) Cost(model string, u Usage) (cost float64, ok bool) {
	p, ok := t.lookup(model)
	if !ok {
		return 0, false
	}
	in, out := p.Input, p.Output
	if total := u.InputTokens + u.CacheWriteTokens + u.CacheReadTokens; p.LongThreshold > 0 && total > p.LongThreshold {
		in, out = p.LongInput, p.LongOutput
	}
	cost = (float64(u.InputTokens)*in + float64(u.OutputTokens)*out +
		float64(u.CacheWriteTokens)*p.CacheWrite + float64(u.CacheReadTokens)*p.CacheRead) / 1e6
	return cost, true
}

func (t *PriceTable) lookup(model string) (Price, bool) {
	m := strings.ToLower(model)
	for _, k := range t.keys {
		if strings.HasPrefix(m, k) {
			return t.prices[k], true
		}
	}
	return Price{}, false
}
