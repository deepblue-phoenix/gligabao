// Package config loads the application configuration from a TOML file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
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
	EnvSidecarURL   = "SIDECAR_URL"
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

// DefaultGeoRadiusKm is the default of geo_radius_km: the radius, in kilometers, of the proximity search of query.
const DefaultGeoRadiusKm = 50.0

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
	// EvalModeEnable registers the eval endpoints (/v1/gb/eval/remember, /v1/gb/eval/query) that return ATIF trajectories
	// with their results. Default false; `serve --eval` turns it on.
	EvalModeEnable bool `toml:"eval"`
	// TrainingModeEnable registers the training handlers (/v1/gb/training/...: jobs, models, datasets, evals, audits, the
	// progress feed) and starts the training monitor. Default false (deployments that do not train have no use for them);
	// `serve --training` turns it on.
	TrainingModeEnable bool `toml:"training"`
	// AdminUIenable registers the admin UI handlers (served at /v1/gb/admin/). Default false; `serve --admin-ui` turns it on.
	// Needs a binary built with the web assets embedded (./run_build.sh, on by default).
	AdminUIenable bool `toml:"admin_ui"`
	// Geo makes remember find the places among the entities it extracts (a city, a country, a region, a landmark, ...) and store their
	// coordinates in gb_entity_geo, using the public Nominatim geocoder of OpenStreetMap (a program that embeds gligabao can supply
	// its own resolver, ServeOptions.GeoResolver, which turns it on too). Default false: it costs one more LLM call per text and
	// sends the names of places to the geocoder. `serve --geo` turns it on.
	Geo bool `toml:"geo"`
	// GeoRadiusKm is the default radius, in kilometers, within which query looks for the geo entities near the places of a question
	// (the entities that match it and the places it mentions) and gives them to the LLM as context; a request can override it with
	// geo_radius_km. It does not turn that search on: a request has to ask for it (geo_nearby, geo_radius_km). Only used with geo on. Default DefaultGeoRadiusKm (50); at most 20015 (half the circumference of the Earth).
	GeoRadiusKm float64 `toml:"geo_radius_km"`
	// MCP registers the MCP (Model Context Protocol) endpoint, POST /v1/gb/mcp (streamable HTTP), which exposes remember and query as
	// MCP tools. It has no authentication yet. Default false; `serve --mcp` turns it on.
	MCP bool `toml:"mcp"`
	// SidecarURL is the base URL of the Python sidecar's REST API (sidecar/), which runs the training jobs the admin
	// endpoint POST /v1/gb/v1/gb/training/jobs starts. Default: SIDECAR_URL, else http://127.0.0.1:8787.
	SidecarURL string `toml:"sidecar_url"`
	// TrainedModelDeployDir is the directory the files of trained models are fetched to for a deployment, one subdirectory per
	// training job (gligabao never deletes them). Default: a "trained_models" directory next to the SQLite database file.
	// `serve --trained-model-deploy-dir` overrides it.
	TrainedModelDeployDir string `toml:"trained_model_deploy_dir"`
	// DatasetsTrainingDirs are the training datasets directories (each has one subdirectory with a dataset.toml manifest per
	// dataset), searched in order: a dataset name found in an earlier directory hides the same name in a later one, and uploads
	// are saved in the first. Default: a "datasets_training" directory next to the SQLite database file.
	// `serve --training-dataset-dir` (repeatable) overrides it.
	DatasetsTrainingDirs []string `toml:"datasets_training_dirs"`
	// EvalsTrainingDirs are the directories of the evals of trained models (one JSON file per eval), searched in order: an eval
	// name found in an earlier directory hides the same name in a later one. Default: an "evals_training" directory next to the
	// SQLite database file. `serve --training-eval-dir` (repeatable) and the runner's -evals override it.
	EvalsTrainingDirs []string `toml:"evals_training_dirs"`
	// EvalsKnowledgeDirs are the directories of the knowledge evals (one JSON file per eval, plus the corpus files the cases refer
	// to with text_file), searched in order like EvalsTrainingDirs. Read by the knowledge eval runner, not by the server. Default
	// (runner): build/evals_knowledge. The runner's -evals (repeatable) overrides it.
	EvalsKnowledgeDirs []string `toml:"evals_knowledge_dirs"`
	// OntologyDirs are the directories ontology documents (OWL, RDF/XML) are loaded from by file name: a request to
	// POST /v1/gb/ontologies or PUT /v1/gb/ontologies/{name} with "file" instead of "content" names a file that is looked up in
	// these directories in order. Nothing is scanned or loaded at startup. Default: none (the "file" form is then rejected).
	// `serve --ontology-dir` (repeatable) overrides it.
	OntologyDirs []string `toml:"ontology_dirs"`
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
		GeoRadiusKm:         DefaultGeoRadiusKm,
		PostgresDSN:         os.Getenv(EnvPostgresDSN),
		AnthropicKey:        os.Getenv(EnvAnthropicKey),
		OpenAIKey:           os.Getenv(EnvOpenAIKey),
		JevKey:              os.Getenv(EnvJevKey),
		JevBaseURL:          os.Getenv(EnvJevBaseURL),
		SidecarURL:          os.Getenv(EnvSidecarURL),

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
	if c.GeoRadiusKm != 0 && (math.IsNaN(c.GeoRadiusKm) || c.GeoRadiusKm < 0 || c.GeoRadiusKm > memory_geo.MaxDistanceKm) {
		return fmt.Errorf("config: geo_radius_km %v must be greater than 0 and at most %v", c.GeoRadiusKm, memory_geo.MaxDistanceKm)
	}
	if err := c.Chunking().Validate(); err != nil {
		return fmt.Errorf("config: chunk_size / chunk_overlap_percent: %w", err)
	}
	for key, dirs := range map[string][]string{
		"datasets_training_dirs": c.DatasetsTrainingDirs, "evals_training_dirs": c.EvalsTrainingDirs,
		"evals_knowledge_dirs": c.EvalsKnowledgeDirs, "ontology_dirs": c.OntologyDirs,
	} {
		for _, d := range dirs {
			if strings.TrimSpace(d) == "" {
				return fmt.Errorf("config: %s: empty directory", key)
			}
		}
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
	cfg.resolveDirs(filepath.Dir(path))
	return cfg, nil
}

// resolveDirs makes the relative directories of the *_dirs lists relative to base, the directory of the config file, so that they
// do not depend on the working directory the program is started from.
func (c *Config) resolveDirs(base string) {
	for _, list := range []*[]string{&c.DatasetsTrainingDirs, &c.EvalsTrainingDirs, &c.EvalsKnowledgeDirs, &c.OntologyDirs} {
		for i, d := range *list {
			if !filepath.IsAbs(d) {
				(*list)[i] = filepath.Join(base, d)
			}
		}
	}
}
