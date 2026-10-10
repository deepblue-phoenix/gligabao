package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	cfg, err := Load(write(t, "port = 9090\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9090 || cfg.Addr() != ":9090" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadDefaultPort(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("got %d", cfg.Port)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, content := range map[string]string{
		"invalid toml": "port = ",
		"wrong type":   `port = "x"`,
		"unknown key":  "porrt = 1",
		"out of range": "port = 70000",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestKeysFromEnv(t *testing.T) {
	t.Setenv(EnvAnthropicKey, "a-env")
	t.Setenv(EnvOpenAIKey, "o-env")
	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicKey != "a-env" || cfg.OpenAIKey != "o-env" {
		t.Fatalf("got %+v", cfg)
	}
	if d := Default(); d.AnthropicKey != "a-env" || d.OpenAIKey != "o-env" {
		t.Fatalf("default: %+v", d)
	}
}

func TestKeysFileOverridesEnv(t *testing.T) {
	t.Setenv(EnvAnthropicKey, "a-env")
	t.Setenv(EnvOpenAIKey, "o-env")
	cfg, err := Load(write(t, "anthropic_api_key = \"a-file\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicKey != "a-file" || cfg.OpenAIKey != "o-env" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestPrices(t *testing.T) {
	cfg, err := Load(write(t, "[prices.\"claude-haiku-4-5\"]\ninput = 1.5\noutput = 7\ncache_read = 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Prices["claude-haiku-4-5"]
	if p.Input != 1.5 || p.Output != 7 || p.CacheWrite != nil || p.CacheRead == nil || *p.CacheRead != 0 {
		t.Fatalf("got %+v", p)
	}
}

func TestPricesErrors(t *testing.T) {
	for name, content := range map[string]string{
		"negative":    "[prices.m]\ninput = -1\n",
		"unknown key": "[prices.m]\ninpt = 1\n",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestPinecone(t *testing.T) {
	t.Setenv(EnvPineconeKey, "p-env")
	t.Setenv(EnvPineconeHost, "")
	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PineconeKey != "p-env" || cfg.PineconeIndexHost != "" {
		t.Fatalf("got %+v", cfg)
	}
	cfg, err = Load(write(t, "pinecone_api_key = \"p-file\"\npinecone_index_host = \"idx.pinecone.io\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PineconeKey != "p-file" || cfg.PineconeIndexHost != "idx.pinecone.io" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestVectorStore(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VectorStore != VectorStoreSQLite || cfg.EmbeddingDimensions != DefaultEmbeddingDimensions {
		t.Fatalf("defaults: %+v", cfg)
	}
	cfg, err = Load(write(t, "vector_store = \"pinecone\"\nembedding_dimensions = 512\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VectorStore != VectorStorePinecone || cfg.EmbeddingDimensions != 512 {
		t.Fatalf("got %+v", cfg)
	}
	for name, content := range map[string]string{
		"unknown store": "vector_store = \"qdrant\"",
		"empty store":   "vector_store = \"\"",
		"zero dims":     "embedding_dimensions = 0",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestDBBackend(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBBackend != DBBackendSQLite {
		t.Fatalf("default: %q", cfg.DBBackend)
	}
	if _, err := Load(write(t, "db_backend = \"sqlite\"")); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"unknown backend": "db_backend = \"mysql\"",
		"empty backend":   "db_backend = \"\"",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
