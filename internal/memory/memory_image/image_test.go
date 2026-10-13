package memory_image

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestResolveImage(t *testing.T) {
	if mt, err := ResolveImage(testPNG(t)); err != nil || mt != "image/png" {
		t.Errorf("png: %q, %v", mt, err)
	}
	for name, data := range map[string][]byte{
		"empty": nil,
		"text":  []byte("not an image"),
		"huge":  append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxImageBytes)...),
	} {
		if _, err := ResolveImage(data); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestParseImageExtraction(t *testing.T) {
	x, err := parseImageExtraction("```json\n" + `{"theme":" Plot ","title":"",
		"text_blocks":[{"kind":"Title","text":" Sales 2024 "},{"kind":"weird","text":"Q1"},{"kind":"axis","text":"  "},{"kind":"legend","text":"EU"}]}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if x.Theme != "plot" || x.Title != "Sales 2024" { // the title falls back to the first title block
		t.Errorf("theme %q, title %q", x.Theme, x.Title)
	}
	want := []ImageTextBlock{{Kind: "title", Text: "Sales 2024"}, {Kind: "other", Text: "Q1"}, {Kind: "legend", Text: "EU"}}
	if len(x.TextBlocks) != len(want) {
		t.Fatalf("blocks: %+v", x.TextBlocks)
	}
	for i, b := range want {
		if x.TextBlocks[i] != b {
			t.Errorf("block %d = %+v, want %+v", i, x.TextBlocks[i], b)
		}
	}
	if x.Text != "Sales 2024\nQ1\nEU" {
		t.Errorf("text = %q", x.Text)
	}

	x, err = parseImageExtraction(`{"theme":"hologram"}`)
	if err != nil || x.Theme != "other" || x.TextBlocks == nil || len(x.TextBlocks) != 0 {
		t.Errorf("unknown theme: %+v, %v", x, err)
	}
	if _, err := parseImageExtraction("no json"); err == nil {
		t.Error("expected an error")
	}
}

type imageLLM struct {
	out  string
	seen *ai.Request
}

func (l imageLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	*l.seen = req
	return &ai.Response{Text: l.out, StopReason: "end_turn", Provider: "p", Model: "m", InputTokens: 7, OutputTokens: 3, CostUSD: 0.5}, nil
}

func TestExtractImage(t *testing.T) {
	var seen ai.Request
	data := testPNG(t)
	x, err := ExtractImage(context.Background(), imageLLM{`{"theme":"graph","title":"T","text_blocks":[{"kind":"label","text":"A"}]}`, &seen}, "image/png", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen.Messages) != 1 || len(seen.Messages[0].Images) != 1 || seen.Messages[0].Images[0].MediaType != "image/png" ||
		!bytes.Equal(seen.Messages[0].Images[0].Data, data) || !strings.Contains(seen.System, "OCR") {
		t.Errorf("request: %+v", seen)
	}
	if x.Theme != "graph" || x.Provider != "p" || x.Model != "m" || x.InputTokens != 7 || x.OutputTokens != 3 || x.CostUSD != 0.5 || x.Raw == "" {
		t.Errorf("extraction: %+v", x)
	}
}
