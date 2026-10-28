package memory_chunk

import (
	"fmt"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   \n\t", 0},
		{"cat", 1},
		{"hello world", 4},   // 2 + 2
		{"Hello, world!", 6}, // 2 + 1 + 2 + 1
		{"2025-01-31", 6},    // 1 + 1 + 1 + 1 + 1 + 1: "2025" (2) "-" "01" (1) "-" "31" (1)
		{"日本語", 3},
		{"internationalization", 5},
	} {
		if got := EstimateTokens(tc.in); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{}).Validate(); err != nil { // zero value: default size, no overlap
		t.Fatal(err)
	}
	for _, c := range []Config{{Size: 5}, {Size: MaxSize + 1}, {Size: 100, OverlapPercent: -1}, {Size: 100, OverlapPercent: 51}} {
		if c.Validate() == nil {
			t.Errorf("%+v: want error", c)
		}
	}
}

// para returns a paragraph of about n estimated tokens that starts with a label.
func para(label string, n int) string {
	words := []string{label}
	for EstimateTokens(strings.Join(words, " ")) < n {
		words = append(words, "word")
	}
	return strings.Join(words, " ") + "."
}

func TestSplitSmallText(t *testing.T) {
	text := "A short note.\n\nWith two paragraphs."
	cs, err := Split(text, DefaultConfig())
	if err != nil || len(cs) != 1 || cs[0].Text != text || cs[0].OverlapTokens != 0 {
		t.Fatalf("got %+v, %v", cs, err)
	}
	if DefaultConfig().NeedsChunking(text) {
		t.Error("small text needs no chunking")
	}
	if cs, err := Split("  \n\n ", DefaultConfig()); err != nil || cs != nil {
		t.Errorf("blank: %v %v", cs, err)
	}
}

func TestSplitParagraphBoundariesAndOverlap(t *testing.T) {
	// Ten paragraphs of ~30 tokens; chunks of 100 tokens with 40% overlap = 40 tokens.
	var ps []string
	for i := 0; i < 10; i++ {
		ps = append(ps, para(fmt.Sprintf("P%d", i), 30))
	}
	text := strings.Join(ps, "\n\n")
	cfg := Config{Size: 100, OverlapPercent: 40}
	if !cfg.NeedsChunking(text) {
		t.Fatal("expected chunking")
	}
	cs, err := Split(text, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < 4 {
		t.Fatalf("got %d chunks", len(cs))
	}
	for i, c := range cs {
		if c.Index != i || c.Tokens > cfg.Size {
			t.Errorf("chunk %d: index %d tokens %d", i, c.Index, c.Tokens)
		}
		if c.Text != text[c.Start:c.End] {
			t.Errorf("chunk %d is not a slice of the text", i)
		}
		// Cuts fall on paragraph boundaries: every chunk starts with a paragraph label and ends with a full stop.
		if !strings.HasPrefix(c.Text, "P") || !strings.HasSuffix(c.Text, ".") {
			t.Errorf("chunk %d does not sit on paragraph boundaries: %q", i, c.Text)
		}
		if i > 0 {
			if c.OverlapTokens == 0 || c.OverlapTokens > 40 {
				t.Errorf("chunk %d overlap = %d", i, c.OverlapTokens)
			}
			if c.Start >= cs[i-1].End { // overlapping: starts before the previous chunk ends
				t.Errorf("chunk %d does not overlap chunk %d", i, i-1)
			}
			if c.Start <= cs[i-1].Start {
				t.Errorf("chunk %d does not advance", i)
			}
		}
	}
	// Every paragraph appears in some chunk.
	for i := range ps {
		if !strings.Contains(strings.Join(texts(cs), "\x00"), fmt.Sprintf("P%d ", i)) {
			t.Errorf("paragraph %d is lost", i)
		}
	}
	if cs[len(cs)-1].End != len(text) {
		t.Error("the last chunk does not reach the end")
	}
}

func texts(cs []Chunk) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

func TestSplitNoOverlap(t *testing.T) {
	var ps []string
	for i := 0; i < 6; i++ {
		ps = append(ps, para(fmt.Sprintf("P%d", i), 40))
	}
	cs, err := Split(strings.Join(ps, "\n\n"), Config{Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cs {
		if c.OverlapTokens != 0 || (i > 0 && c.Start < cs[i-1].End) {
			t.Errorf("chunk %d overlaps with overlap disabled", i)
		}
	}
}

func TestSplitLongParagraphBySentences(t *testing.T) {
	var ss []string
	for i := 0; i < 40; i++ {
		ss = append(ss, fmt.Sprintf("Sentence number %d is about something else entirely.", i))
	}
	text := strings.Join(ss, " ") // one paragraph, far larger than a chunk
	cs, err := Split(text, Config{Size: 64, OverlapPercent: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < 3 {
		t.Fatalf("got %d chunks", len(cs))
	}
	for i, c := range cs {
		if c.Tokens > 64 {
			t.Errorf("chunk %d has %d tokens", i, c.Tokens)
		}
		if !strings.HasPrefix(c.Text, "Sentence") || !strings.HasSuffix(c.Text, ".") {
			t.Errorf("chunk %d is cut inside a sentence: %q", i, c.Text)
		}
	}
}

func TestSplitLongSentenceByWords(t *testing.T) {
	text := strings.Repeat("lorem ipsum dolor sit amet ", 60) // no punctuation, no paragraphs
	cs, err := Split(text, Config{Size: 50, OverlapPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < 3 {
		t.Fatalf("got %d chunks", len(cs))
	}
	for i, c := range cs {
		if c.Tokens > 50 {
			t.Errorf("chunk %d has %d tokens", i, c.Tokens)
		}
		if i > 0 && c.OverlapTokens == 0 {
			t.Errorf("chunk %d has no overlap", i)
		}
	}
}

func TestSplitHugeWord(t *testing.T) {
	text := strings.Repeat("x", 4000) + "\n\n" + "日本語のテキスト。"
	cs, err := Split(text, Config{Size: 40, OverlapPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Tokens > 40 {
			t.Errorf("chunk %d has %d tokens", c.Index, c.Tokens)
		}
	}
	if !strings.Contains(cs[len(cs)-1].Text, "日本語") {
		t.Error("the last paragraph is lost")
	}
}

// With a big paragraph before the cut, the overlap falls back to its last words.
func TestSplitOverlapFallsBackToWords(t *testing.T) {
	text := para("A", 90) + "\n\n" + para("B", 90)
	cs, err := Split(text, Config{Size: 100, OverlapPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("got %d chunks", len(cs))
	}
	c := cs[1]
	// 90 + 20 > 100: the overlap is dropped to make room for the second paragraph.
	if c.OverlapTokens != 0 || !strings.HasPrefix(c.Text, "B") {
		t.Errorf("overlap %d, text %q", c.OverlapTokens, c.Text[:10])
	}

	text = para("A", 60) + "\n\n" + para("B", 60)
	cs, err = Split(text, Config{Size: 100, OverlapPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	c = cs[1]
	if c.OverlapTokens == 0 || c.OverlapTokens > 20 || strings.HasPrefix(c.Text, "B") || c.Tokens > 100 {
		t.Errorf("want a word tail of A (<=20 tokens) before B, got overlap %d tokens %d: %q", c.OverlapTokens, c.Tokens, c.Text)
	}
}
