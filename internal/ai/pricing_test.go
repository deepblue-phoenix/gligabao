package ai

import "testing"

func TestPriceOverrides(t *testing.T) {
	pt := NewPriceTable(map[string]Price{
		"Claude-Haiku-4-5": {Input: 2, Output: 10}, // overrides built-in
		"my-model":         {Input: 1, Output: 1},  // added
		"my-model-large":   {Input: 4, Output: 4},  // longest prefix wins
	})
	if c, ok := pt.Cost("claude-haiku-4-5-20251001", Usage{InputTokens: 1e6, OutputTokens: 1e6}); !ok || c != 12 {
		t.Fatalf("override: %v %v", c, ok)
	}
	if c, ok := pt.Cost("my-model-large-1", Usage{InputTokens: 1e6}); !ok || c != 4 {
		t.Fatalf("longest prefix: %v %v", c, ok)
	}
	if c, ok := pt.Cost("my-model-x", Usage{OutputTokens: 1e6}); !ok || c != 1 {
		t.Fatalf("added: %v %v", c, ok)
	}
	if c, ok := pt.Cost("claude-3-haiku", Usage{InputTokens: 1e6}); !ok || c != 0.25 {
		t.Fatalf("default kept: %v %v", c, ok)
	}
}

func TestHaiku55Tiers(t *testing.T) {
	pt := NewPriceTable(nil)
	if c, ok := pt.Cost("claude-haiku-5-5", Usage{InputTokens: 100_000, OutputTokens: 1e6}); !ok || !near(c, 0.01+0.5) {
		t.Fatalf("short tier: %v %v", c, ok)
	}
	if c, ok := pt.Cost("claude-haiku-5-5", Usage{InputTokens: 100_001, OutputTokens: 1e6}); !ok || !near(c, 0.0500005+2.5) {
		t.Fatalf("long tier: %v %v", c, ok)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestSupportedModels(t *testing.T) {
	byName := func(ms []ModelInfo) map[string]ModelInfo {
		out := map[string]ModelInfo{}
		for _, m := range ms {
			out[m.Name] = m
		}
		return out
	}
	got := byName(SupportedModels(nil))
	for name, want := range map[string]ModelInfo{
		"claude-haiku-5-5":       {Name: "claude-haiku-5-5", Provider: ProviderAnthropic, Kind: KindLLM},
		"text-embedding-3-small": {Name: "text-embedding-3-small", Provider: ProviderOpenAI, Kind: KindEmbedding},
	} {
		if got[name] != want {
			t.Errorf("%s = %+v, want %+v", name, got[name], want)
		}
	}
	// Config overrides of the price table are supported models too.
	over := byName(SupportedModels(NewPriceTable(map[string]Price{"My-Model": {Input: 1}})))
	if _, ok := over["my-model"]; !ok || len(over) <= len(got) {
		t.Errorf("override not listed: %v", over)
	}
}
