package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

func TestOpenSQLiteChromem(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Config{SQLitePath: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Relational == nil || d.Graph == nil || d.Vector == nil {
		t.Fatalf("missing client: %+v", d)
	}
	if d.Backend != config.DBBackendSQLite || d.VectorStore != config.VectorStoreChromem ||
		d.EmbeddingDimensions != config.DefaultEmbeddingDimensions {
		t.Fatalf("defaults not applied: %s", d)
	}
	if err := d.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(Config{Backend: "mysql"}); err == nil {
		t.Error("unknown backend accepted")
	}
	if _, err := Open(Config{SQLitePath: filepath.Join(t.TempDir(), "t.db"), VectorStore: "nope"}); err == nil {
		t.Error("unknown vector store accepted")
	}
	var nilDB *DB
	if err := nilDB.Close(); err != nil {
		t.Error(err)
	}
}

// TestOpenStoresSupportedModels checks that Open stores the supported models, by name, with IDs derived from the name.
func TestOpenStoresSupportedModels(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	d, err := Open(Config{SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	models, err := relational.ListModels(ctx, d.Relational)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]relational.Model{}
	for _, m := range models {
		byName[m.Name] = m
	}
	for _, m := range ai.SupportedModels(nil) {
		got, ok := byName[m.Name]
		if !ok || got.ID != relational.ModelID(m.Name) || got.Provider != m.Provider || got.Kind != m.Kind || !got.Supported {
			t.Errorf("model %q: %+v (stored: %v)", m.Name, got, ok)
		}
	}
	// A model seen in a response is stored under its own name; a restart keeps it but it is not "supported".
	if _, err := relational.EnsureModel(ctx, d.Relational, relational.Model{Name: "claude-haiku-4-5-20251001", Provider: ai.ProviderAnthropic}); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(Config{SQLitePath: path, Models: []ai.ModelInfo{{Name: "only-this", Provider: "x", Kind: ai.KindLLM}}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	models, err = relational.ListModels(ctx, d.Relational)
	if err != nil {
		t.Fatal(err)
	}
	supported := []string{}
	for _, m := range models {
		if m.Supported {
			supported = append(supported, m.Name)
		}
		if m.Name == "claude-haiku-4-5-20251001" && m.ID != relational.ModelID(m.Name) {
			t.Errorf("snapshot ID %q", m.ID)
		}
	}
	if len(supported) != 1 || supported[0] != "only-this" || len(models) != len(ai.SupportedModels(nil))+2 {
		t.Errorf("after restart: supported %v, %d models", supported, len(models))
	}
}

func TestModelIDDeterministic(t *testing.T) {
	a := relational.ModelID("Claude-Haiku-5-5 ")
	if a != relational.ModelID("claude-haiku-5-5") || len(a) != 16 || a == relational.ModelID("claude-haiku-4-5") || relational.ModelID(" ") != "" {
		t.Errorf("ModelID: %q", a)
	}
}
