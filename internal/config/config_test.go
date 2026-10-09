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
