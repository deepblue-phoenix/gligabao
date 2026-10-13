// Package config loads the application configuration from a TOML file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/deepblue-phoenix/gligabao/internal/chunk"
)

// FileName is the default config file name.
const FileName = "gligabao.toml"

// Environment variables the API keys are loaded from.
const (
	EnvAnthropicKey = "ANTHROPIC_API_KEY"
	EnvOpenAIKey    = "OPENAI_API_KEY"
	EnvJevKey       = "JEV_API_KEY"
	EnvJevBaseURL   = "JEV_BASE_URL"
	EnvPineconeKey  = "PINECONE_API_KEY"
	EnvPineconeHost = "PINECONE_INDEX_HOST"
	EnvPostgresDSN  = "POSTGRES_DSN"
)

// Database backends selectable with the db_backend setting.
const (
	DBBackendSQLite   = "sqlite"
	DBBackendPostgres = "postgres"
)

// Vector stores selectable with the vector_store setting.
const (
	VectorStoreChromem  = "chromem"
	VectorStorePinecone = "pinecone"
)

// DefaultEmbeddingDimensions is the output size of the default embedding model (text-embedding-3-small).
const DefaultEmbeddingDimensions = 1536

// DefaultPort is used when the config does not set a port.
const DefaultPort = 7878

//---------------------------------------------------------------------------------------------

// Config holds the application configuration.
//
// API keys are read from the ANTHROPIC_API_KEY, OPENAI_API_KEY, JEV_API_KEY and PINECONE_API_KEY
// environment variables; a key set in the TOML file overrides the environment value.
// The Pinecone index host likewise defaults to PINECONE_INDEX_HOST, and the PostgreSQL connection string to POSTGRES_DSN.
type Config struct {
	Port         int    `toml:"port"`
	AnthropicKey string `toml:"anthropic_api_key"`
	OpenAIKey    string `toml:"openai_api_key"`
	// JevKey is the TypeSafe API key of Jev, the system 1 model. Jev is only used when it is set.
	JevKey string `toml:"jev_api_key"`
	// JevBaseURL overrides the API root of Jev (default https://api.typesafe.ai; for tests and proxies).
	JevBaseURL string `toml:"jev_base_url"`
	// JevModel is the model sent to Jev. Default "jev-latest".
	JevModel string `toml:"jev_model"`
	// DBBackend selects the main database: "sqlite" (default) or "postgres".
	DBBackend string `toml:"db_backend"`
	// PostgresDSN is the PostgreSQL connection string (a URL like postgres://user:pass@host:5432/dbname).
	// Required when db_backend = "postgres". The sqlite database file is set with `serve --db`.
	PostgresDSN string `toml:"postgres_dsn"`
	// VectorStore selects where embeddings are stored: "chromem" (default; chromem-go, pure Go, a local directory
	// next to the main database) or "pinecone".
	VectorStore string `toml:"vector_store"`
	// EmbeddingDimensions is the size of the stored embeddings. The chromem store fixes it when its
	// directory is created; a Pinecone index must have been created with the same dimension.
	EmbeddingDimensions int `toml:"embedding_dimensions"`
	// PineconeKey and PineconeIndexHost configure the Pinecone vector DB used to store embeddings.
	// The host is the index's data-plane host from the Pinecone console. Both empty = vector DB disabled.
	PineconeKey       string `toml:"pinecone_api_key"`
	PineconeIndexHost string `toml:"pinecone_index_host"`
	// ChunkSize is the chunk size, in tokens: remember splits a text of more than this many tokens into chunks (see
	// docs/chunking.md). Default chunk.DefaultSize (1024).
	ChunkSize int `toml:"chunk_size"`
	// ChunkOverlapPercent is how much of a chunk repeats the end of the previous one, as a percentage of ChunkSize
	// (0 = no overlap, at most chunk.MaxOverlapPercent). Default chunk.DefaultOverlapPercent (20).
	ChunkOverlapPercent int `toml:"chunk_overlap_percent"`
	// Eval registers the eval endpoints (/v1/gb/eval/remember, /v1/gb/eval/recall) that return ATIF trajectories
	// with their results. Default false; `serve --eval` turns it on.
	Eval bool `toml:"eval"`
	// AdminUI registers the admin UI handlers (served at /v1/gb/admin/). Default false; `serve --admin-ui` turns it on.
	// Needs a binary built with the web assets embedded (./run_build.sh, on by default).
	AdminUI bool `toml:"admin_ui"`
	// Prices overrides/extends the built-in per-model price table used to compute LLM call costs.
	// Keys are model-name prefixes (the longest matching prefix wins).
	Prices map[string]Price `toml:"prices"`
}

// Price is a model's price in USD per 1M tokens. Omitted cache prices default to the input price.
type Price struct {
	Input      float64  `toml:"input"`
	Output     float64  `toml:"output"`
	CacheWrite *float64 `toml:"cache_write"`
	CacheRead  *float64 `toml:"cache_read"`
	// Optional long-context tier: when a call's total input tokens exceed LongThreshold, the whole call
	// is billed at LongInput / LongOutput instead of Input / Output. 0 disables the tier.
	LongThreshold int     `toml:"long_threshold"`
	LongInput     float64 `toml:"long_input"`
	LongOutput    float64 `toml:"long_output"`
}

//---------------------------------------------------------------------------------------------

// DefaultPath returns the default config location: next to the running binary.
// If the binary location cannot be determined, FileName (relative to the
// working directory) is returned.
func DefaultPath() string {
	exe, err := os.Executable()
	if err != nil {
		return FileName
	}
	return filepath.Join(filepath.Dir(exe), FileName)
}

// Default returns a Config populated with default values. API keys are
// taken from the environment.
func Default() *Config {
	return &Config{
		Port: DefaultPort,

		DBBackend:           DBBackendSQLite,
		VectorStore:         VectorStoreChromem,
		EmbeddingDimensions: DefaultEmbeddingDimensions,
		ChunkSize:           chunk.DefaultSize,
		ChunkOverlapPercent: chunk.DefaultOverlapPercent,
		PostgresDSN:         os.Getenv(EnvPostgresDSN),
		AnthropicKey:        os.Getenv(EnvAnthropicKey),
		OpenAIKey:           os.Getenv(EnvOpenAIKey),
		JevKey:              os.Getenv(EnvJevKey),
		JevBaseURL:          os.Getenv(EnvJevBaseURL),

		PineconeKey:       os.Getenv(EnvPineconeKey),
		PineconeIndexHost: os.Getenv(EnvPineconeHost),
	}
}

// Chunking returns the chunking settings (chunk_size, chunk_overlap_percent).
func (c *Config) Chunking() chunk.Config {
	return chunk.Config{Size: c.ChunkSize, OverlapPercent: c.ChunkOverlapPercent}
}

// Addr returns the HTTP listen address derived from the port (e.g. ":7878").
func (c *Config) Addr() string {
	return ":" + strconv.Itoa(c.Port)
}

// Validate checks that the config values are usable.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("config: port %d out of range (1-65535)", c.Port)
	}
	switch c.DBBackend {
	case DBBackendSQLite:
	case DBBackendPostgres:
		if strings.TrimSpace(c.PostgresDSN) == "" {
			return fmt.Errorf("config: postgres_dsn (or %s) is required when db_backend is %q", EnvPostgresDSN, DBBackendPostgres)
		}
	default:
		return fmt.Errorf("config: db_backend %q must be %q or %q", c.DBBackend, DBBackendSQLite, DBBackendPostgres)
	}
	switch c.VectorStore {
	case VectorStoreChromem, VectorStorePinecone:
	default:
		return fmt.Errorf("config: vector_store %q must be %q or %q", c.VectorStore, VectorStoreChromem, VectorStorePinecone)
	}
	if c.EmbeddingDimensions < 1 {
		return fmt.Errorf("config: embedding_dimensions %d must be positive", c.EmbeddingDimensions)
	}
	if err := c.Chunking().Validate(); err != nil {
		return fmt.Errorf("config: chunk_size / chunk_overlap_percent: %w", err)
	}
	for model, p := range c.Prices {
		if strings.TrimSpace(model) == "" {
			return errors.New("config: prices: empty model name")
		}
		for _, v := range []float64{p.Input, p.Output, deref(p.CacheWrite), deref(p.CacheRead), p.LongInput, p.LongOutput, float64(p.LongThreshold)} {
			if v < 0 {
				return fmt.Errorf("config: prices.%q: prices must not be negative", model)
			}
		}
	}
	return nil
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Load parses the TOML file at path. Unset fields keep their defaults.
// Unknown keys are rejected. If the file does not exist, a wrapped
// fs.ErrNotExist error is returned.
func Load(path string) (*Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("config: %w", err)
		}
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return nil, fmt.Errorf("config: %s: unknown key %q", path, undec[0].String())
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}
