package gligabao

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	sargoembed "github.com/deepblue-phoenix/gligabao/rust/sargo"
)

func TestSargoEnv(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want []string
	}{
		"none": {Config{}, []string{}},
		"both": {
			Config{AnthropicKey: "a", OpenAIKey: "o"},
			[]string{"ANTHROPIC_API_KEY=a", "OPENAI_API_KEY=o"},
		},
		"one": {Config{OpenAIKey: "o"}, []string{"OPENAI_API_KEY=o"}},
	} {
		if got := sargoEnv(&tc.cfg); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestStartSargoOffDoesNothing(t *testing.T) {
	for _, cfg := range []*Config{nil, {}} {
		client, stop, err := startSargo(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if client != nil || gcniiService(client) != nil {
			t.Fatal("sargo is off: there must be no client and no gcnii service")
		}
		stop()
	}
}

// With sargo on and no embedded binary the server must not start silently without it.
func TestStartSargoOnWithoutBinaryFails(t *testing.T) {
	if sargoembed.Available() {
		t.Skip("this build embeds sargo")
	}
	_, _, err := startSargo(context.Background(), &Config{Sargo: true})
	if !errors.Is(err, sargo.ErrNotEmbedded) {
		t.Fatalf("got %v, want ErrNotEmbedded", err)
	}
}

func TestSargoGCNIIDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gcnii")
	got, err := sargoGCNIIDir(&Config{SargoGCNIIDir: dir})
	if err != nil || got != dir {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = sargoGCNIIDir(&Config{})
	if err != nil || !filepath.IsAbs(got) || filepath.Base(got) != "gcnii_models" {
		t.Fatalf("default: got %q, %v", got, err)
	}
}
