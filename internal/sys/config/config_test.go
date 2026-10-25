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
	if cfg.VectorStore != VectorStoreChromem || cfg.EmbeddingDimensions != DefaultEmbeddingDimensions {
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

func TestLoadPostgres(t *testing.T) {
	t.Setenv(EnvPostgresDSN, "")
	if _, err := Load(write(t, "db_backend = \"postgres\"\n")); err == nil {
		t.Fatal("postgres without a DSN should be rejected")
	}
	cfg, err := Load(write(t, "db_backend = \"postgres\"\npostgres_dsn = \"postgres://u:p@h/db\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBBackend != DBBackendPostgres || cfg.PostgresDSN != "postgres://u:p@h/db" {
		t.Fatalf("got %+v", cfg)
	}
	t.Setenv(EnvPostgresDSN, "postgres://env/db")
	if cfg, err = Load(write(t, "db_backend = \"postgres\"\n")); err != nil || cfg.PostgresDSN != "postgres://env/db" {
		t.Fatalf("env DSN: %+v, %v", cfg, err)
	}
}

func TestLoadEval(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil || cfg.EvalModeEnable {
		t.Fatalf("default: %v %+v", err, cfg)
	}
	if cfg, err = Load(write(t, "eval = true\n")); err != nil || !cfg.EvalModeEnable {
		t.Fatalf("eval = true: %v %+v", err, cfg)
	}
}

func TestLoadTraining(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil || cfg.TrainingModeEnable {
		t.Fatalf("default: %v %+v", err, cfg)
	}
	if cfg, err = Load(write(t, "training = true\n")); err != nil || !cfg.TrainingModeEnable {
		t.Fatalf("training = true: %v %+v", err, cfg)
	}
}

func TestTrainedModelDeployDir(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil || cfg.TrainedModelDeployDir != "" {
		t.Fatalf("default: %v %+v", err, cfg)
	}
	if cfg, err = Load(write(t, `trained_model_deploy_dir = "/data/models"`+"\n")); err != nil || cfg.TrainedModelDeployDir != "/data/models" {
		t.Fatalf("set: %v %+v", err, cfg)
	}
}

func TestChunkingSettings(t *testing.T) {
	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Chunking(); c.Size != 1024 || c.OverlapPercent != 20 {
		t.Errorf("default chunking = %+v", c)
	}
	cfg, err = Load(write(t, "chunk_size = 512\nchunk_overlap_percent = 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Chunking(); c.Size != 512 || c.OverlapPercent != 0 {
		t.Errorf("chunking = %+v", c)
	}
	for _, bad := range []string{"chunk_size = 10", "chunk_size = 9000", "chunk_overlap_percent = 60", "chunk_overlap_percent = -1"} {
		if _, err := Load(write(t, bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestJevConfig(t *testing.T) {
	t.Setenv(EnvJevKey, "")
	t.Setenv(EnvJevBaseURL, "")
	cfg, err := Load(write(t, ""))
	if err != nil || cfg.JevKey != "" {
		t.Fatalf("Jev must be off by default: %+v, %v", cfg, err)
	}
	if cfg, err := Load(write(t, "jev_api_key = \"k\"\n")); err != nil || cfg.JevKey != "k" || cfg.JevBaseURL != "" {
		t.Fatalf("a key alone must be enough: %+v, %v", cfg, err)
	}
	cfg, err = Load(write(t, "jev_api_key = \"k\"\njev_base_url = \"http://jev/v1\"\njev_model = \"m\"\n"))
	if err != nil || cfg.JevKey != "k" || cfg.JevBaseURL != "http://jev/v1" || cfg.JevModel != "m" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	t.Setenv(EnvJevKey, "envk")
	t.Setenv(EnvJevBaseURL, "http://env/v1")
	if cfg := Default(); cfg.JevKey != "envk" || cfg.JevBaseURL != "http://env/v1" {
		t.Errorf("env not read: %+v", cfg)
	}
}

// The directory lists are relative to the config file, not to the working directory; absolute ones are kept.
func TestLoadDirs(t *testing.T) {
	p := write(t, `datasets_training_dirs = ["datasets", "/abs/datasets"]
evals_training_dirs = ["../evals_t"]
evals_knowledge_dirs = ["evals_k"]
ontology_dirs = ["onto"]
`)
	base := filepath.Dir(p)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want %v", name, got, want)
			}
		}
	}
	eq("datasets", cfg.DatasetsTrainingDirs, []string{filepath.Join(base, "datasets"), "/abs/datasets"})
	eq("evals_training", cfg.EvalsTrainingDirs, []string{filepath.Join(filepath.Dir(base), "evals_t")})
	eq("evals_knowledge", cfg.EvalsKnowledgeDirs, []string{filepath.Join(base, "evals_k")})
	eq("ontology", cfg.OntologyDirs, []string{filepath.Join(base, "onto")})

	if _, err := Load(write(t, `ontology_dirs = [""]`)); err == nil {
		t.Error("an empty directory should be rejected")
	}
}
