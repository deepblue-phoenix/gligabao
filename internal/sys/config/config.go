// Package config loads the application configuration from a TOML file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/web"
)

// FileName is the default config file name.
const FileName = "gligabao.toml"

// Environment variables the API keys are loaded from.
const (
	EnvAnthropicKey          = "ANTHROPIC_API_KEY"
	EnvOpenAIKey             = "OPENAI_API_KEY"
	EnvJevKey                = "JEV_API_KEY"
	EnvLinkupKey             = "LINKUP_API_KEY"
	EnvJevBaseURL            = "JEV_BASE_URL"
	EnvPineconeKey           = "PINECONE_API_KEY"
	EnvPineconeHost          = "PINECONE_INDEX_HOST"
	EnvPostgresDSN           = "POSTGRES_DSN"
	EnvResearchS3Bucket      = "RESEARCH_S3_BUCKET"
	EnvResearchS3Region      = "RESEARCH_S3_REGION"
	EnvResearchS3NotesPrefix = "RESEARCH_S3_NOTES_PREFIX"
	EnvOntologyS3Bucket      = "ONTOLOGY_S3_BUCKET"
	EnvOntologyS3Region      = "ONTOLOGY_S3_REGION"
	EnvOntologyS3Prefix      = "ONTOLOGY_S3_PREFIX"
	EnvS3VectorsBucket       = "S3_VECTORS_BUCKET"
	EnvS3VectorsRegion       = "S3_VECTORS_REGION"
	EnvS3VectorsPrefix       = "S3_VECTORS_PREFIX"
	EnvImageStore            = "IMAGE_STORE"
	EnvImageS3Bucket         = "IMAGE_S3_BUCKET"
	EnvImageS3Region         = "IMAGE_S3_REGION"
	EnvImageS3Prefix         = "IMAGE_S3_PREFIX"
	EnvImageS3Endpoint       = "IMAGE_S3_ENDPOINT"
	EnvAtifS3Bucket          = "ATIF_S3_BUCKET"
	EnvAtifS3Region          = "ATIF_S3_REGION"
	EnvAtifS3Prefix          = "ATIF_S3_PREFIX"
	EnvAtifS3Endpoint        = "ATIF_S3_ENDPOINT"
	EnvEvalsS3Bucket         = "EVALS_S3_BUCKET"
	EnvEvalsS3Region         = "EVALS_S3_REGION"
	EnvEvalsS3Prefix         = "EVALS_S3_PREFIX"
	EnvEvalsS3Endpoint       = "EVALS_S3_ENDPOINT"
	EnvSidecarURL            = "SIDECAR_URL"
)

// Database backends selectable with the db_backend setting.
const (
	DBBackendSQLite   = "sqlite"
	DBBackendPostgres = "postgres"
)

// Vector stores selectable with the vector_store setting.
const (
	VectorStoreChromem   = "chromem"
	VectorStorePinecone  = "pinecone"
	VectorStoreS3Vectors = "s3vectors"
)

// Image stores selectable with the image_store setting.
const (
	ImageStoreFS = "fs"
	ImageStoreS3 = "s3"
)

// DefaultEmbeddingDimensions is the output size of the default embedding model (text-embedding-3-small).
const DefaultEmbeddingDimensions = 1536

// DefaultPort is used when the config does not set a port.
const DefaultPort = 7878

// DefaultGeoRadiusKm is the default of geo_radius_km: the radius, in kilometers, of the proximity search of query.
const DefaultGeoRadiusKm = 50.0

// The defaults of the evals_* settings (the per-user evals).
const (
	DefaultEvalsS3Prefix             = "evals"
	DefaultEvalsMaxCases             = 100
	DefaultEvalsMaxFileBytes         = 1 << 20
	DefaultEvalsMaxTextBytes         = 256 << 10
	DefaultEvalsMaxRunsPerUser       = 2
	DefaultEvalsMaxRuns              = 8
	DefaultEvalsMaxRunCostUSD        = 5.0
	DefaultEvalsMaxUserCostUSDPerDay = 20.0
	DefaultEvalsDatasetRetentionDays = 14
	DefaultEvalsHeartbeatInterval    = 15 * time.Second
	DefaultEvalsHeartbeatStale       = 2 * time.Minute
)

// removedSettings are the settings that were removed, with what the operator has to do instead. A file that still has one fails to load.
var removedSettings = map[string]string{
	"eval":    `the "eval" setting was removed: evals are always on`,
	"eval_db": `the "eval_db" setting was removed: the evals are in the main database`,
}

//---------------------------------------------------------------------------------------------

// Duration is a time.Duration that the TOML file writes as text, e.g. "15s" or "2m".
type Duration time.Duration

// UnmarshalText parses a duration with time.ParseDuration.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("duration %q: %w", string(text), err)
	}
	*d = Duration(v)
	return nil
}

// Std is the duration as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config holds the application configuration.
//
// API keys are read from the ANTHROPIC_API_KEY, OPENAI_API_KEY, JEV_API_KEY, LINKUP_API_KEY and PINECONE_API_KEY
// environment variables; a key set in the TOML file overrides the environment value.
// The Pinecone index host likewise defaults to PINECONE_INDEX_HOST, and the PostgreSQL connection string to POSTGRES_DSN.
type Config struct {
	Port int `toml:"port"`
	// Host is the address the server listens on; empty (the default) is every interface. The CLI starts its local daemon on
	// 127.0.0.1 (`serve --host 127.0.0.1`).
	Host         string `toml:"host"`
	AnthropicKey string `toml:"anthropic_api_key"`
	OpenAIKey    string `toml:"openai_api_key"`
	// LinkupKey is the Linkup API key (also LINKUP_API_KEY), needed when the search or the fetch provider of the research agents is linkup.
	LinkupKey string `toml:"linkup_api_key"`
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
	// next to the main database), "pinecone" or "s3vectors" (an Amazon S3 vector bucket).
	VectorStore string `toml:"vector_store"`
	// EmbeddingDimensions is the size of the stored embeddings. The chromem store fixes it when its
	// directory is created; a Pinecone index must have been created with the same dimension.
	EmbeddingDimensions int `toml:"embedding_dimensions"`
	// PineconeKey and PineconeIndexHost configure the Pinecone vector DB used to store embeddings.
	// The host is the index's data-plane host from the Pinecone console. Both empty = vector DB disabled.
	PineconeKey       string `toml:"pinecone_api_key"`
	PineconeIndexHost string `toml:"pinecone_index_host"`
	// S3VectorsBucket is the (existing) Amazon S3 vector bucket of vector_store = "s3vectors" (also S3_VECTORS_BUCKET).
	S3VectorsBucket string `toml:"s3_vectors_bucket"`
	// S3VectorsRegion is its AWS region (also S3_VECTORS_REGION); empty = the AWS SDK default (AWS_REGION, shared config).
	// Credentials come from the standard AWS chain (environment, shared files, IAM role), never from this file.
	S3VectorsRegion string `toml:"s3_vectors_region"`
	// S3VectorsPrefix is the "subdirectory" of the bucket gligabao uses (also S3_VECTORS_PREFIX): each dataset is the index
	// "<prefix>.<dataset>", so deployments can share a bucket. Optional.
	S3VectorsPrefix string `toml:"s3_vectors_prefix"`
	// ImageStore selects where the image files are kept: "fs" (default; a directory, `serve --image-dir`, by default next
	// to the main database) or "s3" (an Amazon S3 bucket, see image_s3_bucket). Also IMAGE_STORE.
	ImageStore string `toml:"image_store"`
	// ImageS3Bucket is the (existing) S3 bucket of image_store = "s3" (also IMAGE_S3_BUCKET).
	ImageS3Bucket string `toml:"image_s3_bucket"`
	// ImageS3Region is its AWS region (also IMAGE_S3_REGION); empty = the AWS SDK default (AWS_REGION, shared config).
	// Credentials come from the standard AWS chain (environment, shared files, IAM role), never from this file.
	ImageS3Region string `toml:"image_s3_region"`
	// ImageS3Prefix is the "subdirectory" of the bucket the images are stored under (also IMAGE_S3_PREFIX), so
	// deployments can share a bucket. Optional.
	ImageS3Prefix string `toml:"image_s3_prefix"`
	// ImageS3Endpoint is a custom S3 endpoint URL (MinIO, LocalStack; also IMAGE_S3_ENDPOINT). Empty = AWS.
	ImageS3Endpoint string `toml:"image_s3_endpoint"`
	// ChunkSize is the chunk size, in tokens: remember splits a text of more than this many tokens into chunks (see
	// docs/chunking.md). Default chunk.DefaultSize (1024).
	ChunkSize int `toml:"chunk_size"`
	// ChunkOverlapPercent is how much of a chunk repeats the end of the previous one, as a percentage of ChunkSize
	// (0 = no overlap, at most chunk.MaxOverlapPercent). Default chunk.DefaultOverlapPercent (20).
	ChunkOverlapPercent int `toml:"chunk_overlap_percent"`
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
	// Sargo makes `serve` start the sargo model router that is embedded in the binary (a child process, stopped when the server stops; see
	// rust/sargo/README.md). The binary must have been built with ./run_build.sh --sargo, else the server does not start. sargo gets the
	// Anthropic and OpenAI keys of this config. Default false; `serve --sargo` turns it on.
	Sargo bool `toml:"sargo"`
	// SargoConfig is a sargo config file used instead of the default one embedded in the binary (only with sargo = true). A relative path is
	// relative to the config file. The server address sargo listens on is the one in that file.
	SargoConfig string `toml:"sargo_config"`
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
	// EvalsS3Bucket is the (existing) meta bucket of the user evals (also EVALS_S3_BUCKET): each user's eval files are the objects
	// "<prefix>/<user>/<name>.json" and the corpus files they name, under the same user directory. Empty (default): the files are in
	// the local directory EvalsLocalDir. There is no fallback to another bucket. Credentials come from the standard AWS chain.
	EvalsS3Bucket string `toml:"evals_s3_bucket"`
	// EvalsS3Region is its AWS region (also EVALS_S3_REGION); empty = the AWS SDK default.
	EvalsS3Region string `toml:"evals_s3_region"`
	// EvalsS3Prefix is the "subdirectory" of the bucket of the eval files (also EVALS_S3_PREFIX); default "evals".
	EvalsS3Prefix string `toml:"evals_s3_prefix"`
	// EvalsS3Endpoint is a custom S3 endpoint URL (MinIO, LocalStack; also EVALS_S3_ENDPOINT). Empty = AWS.
	EvalsS3Endpoint string `toml:"evals_s3_endpoint"`
	// EvalsLocalDir is the directory of the eval files when there is no bucket: one subdirectory per user. Default: an evals_user
	// directory next to the SQLite database file. A relative path is relative to the config file.
	EvalsLocalDir string `toml:"evals_local_dir"`
	// EvalsTrajectoryDir is the directory the ATIF trajectories of the eval runs are stored in when there is no ATIF bucket. Default: an
	// atif directory next to the SQLite database file (`serve --trajectory-dir` overrides it). A relative path is relative to the config file.
	EvalsTrajectoryDir string `toml:"evals_trajectory_dir"`
	// EvalsMaxCases, EvalsMaxFileBytes and EvalsMaxTextBytes are the limits of an eval: cases per eval, bytes of its JSON file, bytes of a
	// text or of a corpus file. Defaults: 100, 1 MiB, 256 KiB.
	EvalsMaxCases     int `toml:"evals_max_cases"`
	EvalsMaxFileBytes int `toml:"evals_max_file_bytes"`
	EvalsMaxTextBytes int `toml:"evals_max_text_bytes"`
	// EvalsMaxRunsPerUser and EvalsMaxRuns are the eval runs in progress at once, per user and on this server. Defaults: 2 and 8.
	EvalsMaxRunsPerUser int `toml:"evals_max_runs_per_user"`
	EvalsMaxRuns        int `toml:"evals_max_runs"`
	// EvalsMaxRunCostUSD stops a run whose cases cost more (USD); EvalsMaxUserCostUSDPerDay refuses a run of a user whose runs cost
	// more than that in the last 24 hours. Defaults: 5 and 20.
	EvalsMaxRunCostUSD        float64 `toml:"evals_max_run_cost_usd"`
	EvalsMaxUserCostUSDPerDay float64 `toml:"evals_max_user_cost_usd_per_day"`
	// EvalsDatasetRetentionDays is how many days after a run ended its eval datasets (their graphs) are kept. Default 14.
	EvalsDatasetRetentionDays int `toml:"evals_dataset_retention_days"`
	// EvalsHeartbeatInterval is how often a running eval run records that it is alive; a run without a beat for EvalsHeartbeatStale is
	// stale (its server crashed). Defaults: 15s and 2m.
	EvalsHeartbeatInterval Duration `toml:"evals_heartbeat_interval"`
	EvalsHeartbeatStale    Duration `toml:"evals_heartbeat_stale"`
	// OntologyDirs are the directories ontology documents (YAML or JSON) are loaded from by file name: a request to
	// POST /v1/gb/ontologies or PUT /v1/gb/ontologies/{name} with "file" instead of "content" names a file that is looked up in
	// these directories in order. Nothing is scanned or loaded at startup. Default: none (the "file" form is then rejected).
	// `serve --ontology-dir` (repeatable) overrides it.
	OntologyDirs []string `toml:"ontology_dirs"`
	// AgentDefsDir is the directory the definitions of the research agents (one TOML or JSON file each, see docs/agents/agent-defs.md) are loaded
	// from when the server starts. Default: an agent_defs directory next to the binary. A relative path is relative to the config file.
	// `serve --agent-defs-dir` overrides it.
	AgentDefsDir string `toml:"agent_defs_dir"`
	// ResearchNotesDir is the directory the notes files of the investigative research loop are written to (notes_<user>_<run>_<notes_id>.md,
	// see docs/agents/deep-research-loop.md). Default: a research_notes directory next to the binary (see ResearchNotesDirOrDefault), like
	// agent_defs. A relative path is relative to the config file. Only agents with loop = "investigative" write there.
	ResearchNotesDir string `toml:"research_notes_dir"`
	// MontyPython is the interpreter that runs the bridge of the code sandbox of the investigative agents with [code] enabled (it has
	// pydantic-monty installed; also GLIGABAO_MONTY_PYTHON; default python3). MontyBin is the monty worker binary (also MONTY_BIN; default:
	// next to the interpreter, then PATH). MontyWorkers is the most worker processes (default 2) and MontyRequestTimeoutS the parent-side
	// deadline that kills a stuck worker (default 90). See docs/agents/monty-code-execution.md.
	MontyPython          string `toml:"monty_python"`
	MontyBin             string `toml:"monty_bin"`
	MontyWorkers         int    `toml:"monty_workers"`
	MontyRequestTimeoutS int    `toml:"monty_request_timeout_s"`
	// ResearchS3Bucket, when set, also stores the notes files of the investigative loop in this (existing) Amazon S3 bucket (also
	// RESEARCH_S3_BUCKET), the research bucket common to all the research agents: the local files stay the working copy and every
	// write is mirrored to <prefix>/<user>/notes_<user>_<run>_<notes_id>.md. Empty (default): local files only.
	ResearchS3Bucket string `toml:"research_s3_bucket"`
	// ResearchS3Region is its AWS region (also RESEARCH_S3_REGION); empty = the AWS SDK default (AWS_REGION, shared config).
	ResearchS3Region string `toml:"research_s3_region"`
	// ResearchS3NotesPrefix is the subdirectory of the bucket for the investigative notes (also RESEARCH_S3_NOTES_PREFIX); default "research/investigative_notes".
	ResearchS3NotesPrefix string `toml:"research_s3_notes_prefix"`
	// OntologyS3Bucket is the (existing) bucket the ontology documents are also stored in (also ONTOLOGY_S3_BUCKET). Empty (default): ontologies are
	// kept only in the database. There is no fallback to another bucket.
	OntologyS3Bucket string `toml:"ontology_s3_bucket"`
	// OntologyS3Region is its AWS region (also ONTOLOGY_S3_REGION); empty = the AWS SDK default (AWS_REGION, shared config).
	OntologyS3Region string `toml:"ontology_s3_region"`
	// OntologyS3Prefix is the subdirectory of the bucket for the ontologies (also ONTOLOGY_S3_PREFIX); default "ontologies". Each user has a
	// directory below it.
	OntologyS3Prefix string `toml:"ontology_s3_prefix"`
	// AtifS3Bucket is the (existing) bucket the ATIF traces (trajectories) are stored in (also ATIF_S3_BUCKET), one directory per user. Empty
	// (default): the traces are JSON files in the trajectory directory (see `serve --trajectory-dir`). There is no other switch and no fallback
	// to another bucket. With a bucket the research agents of the server record a trace of every research too.
	AtifS3Bucket string `toml:"atif_s3_bucket"`
	// AtifS3Region is its AWS region (also ATIF_S3_REGION); empty = the AWS SDK default (AWS_REGION, shared config).
	AtifS3Region string `toml:"atif_s3_region"`
	// AtifS3Prefix is the subdirectory of the bucket for the traces (also ATIF_S3_PREFIX); default "traces/atif". Each user has a
	// subdirectory of it, and a trace is "<prefix>/<user>/<creation timestamp>_<trajectory_id>.json".
	AtifS3Prefix string `toml:"atif_s3_prefix"`
	// AtifS3Endpoint is a custom S3 endpoint URL (MinIO, LocalStack; also ATIF_S3_ENDPOINT). Empty = AWS.
	AtifS3Endpoint string `toml:"atif_s3_endpoint"`
	// ResearchMaxCostUSD is the most one research (all its agents together) may spend, in USD, whatever its agent definition and its
	// request say: they can only lower it. 0 (default): no cap of the server.
	ResearchMaxCostUSD float64 `toml:"research_max_cost_usd"`
	// ResearchSearchProvider and ResearchFetchProvider are the providers of the web_search and fetch_url tools of the research agents when
	// a request names none (search_provider, fetch_provider of POST /v1/gb/research). Search: "anthropic" (default). Fetch: "local"
	// (default, a plain HTTP GET).
	ResearchSearchProvider string `toml:"research_search_provider"`
	ResearchFetchProvider  string `toml:"research_fetch_provider"`
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

// DefaultResearchNotesDirName is the directory next to the binary that ResearchNotesDirOrDefault falls back to.
const DefaultResearchNotesDirName = "research_notes"

// ResearchNotesDirOrDefault is ResearchNotesDir, or a research_notes directory next to the binary if it is empty.
func (c *Config) ResearchNotesDirOrDefault() string {
	if c.ResearchNotesDir != "" {
		return c.ResearchNotesDir
	}
	exe, err := os.Executable()
	if err != nil {
		return DefaultResearchNotesDirName
	}
	return filepath.Join(filepath.Dir(exe), DefaultResearchNotesDirName)
}

// Default returns a Config populated with default values. API keys are
// taken from the environment.
func Default() *Config {
	return &Config{
		Port: DefaultPort,

		DBBackend:           DBBackendSQLite,
		VectorStore:         VectorStoreChromem,
		EmbeddingDimensions: DefaultEmbeddingDimensions,
		ChunkSize:           memory_chunk.DefaultSize,
		ChunkOverlapPercent: memory_chunk.DefaultOverlapPercent,
		GeoRadiusKm:         DefaultGeoRadiusKm,
		PostgresDSN:         os.Getenv(EnvPostgresDSN),
		AnthropicKey:        os.Getenv(EnvAnthropicKey),
		OpenAIKey:           os.Getenv(EnvOpenAIKey),
		JevKey:              os.Getenv(EnvJevKey),
		LinkupKey:           os.Getenv(EnvLinkupKey),
		JevBaseURL:          os.Getenv(EnvJevBaseURL),
		SidecarURL:          os.Getenv(EnvSidecarURL),

		PineconeKey:       os.Getenv(EnvPineconeKey),
		PineconeIndexHost: os.Getenv(EnvPineconeHost),
		S3VectorsBucket:   os.Getenv(EnvS3VectorsBucket),
		S3VectorsRegion:   os.Getenv(EnvS3VectorsRegion),
		S3VectorsPrefix:   os.Getenv(EnvS3VectorsPrefix),

		EvalsS3Bucket:             os.Getenv(EnvEvalsS3Bucket),
		EvalsS3Region:             os.Getenv(EnvEvalsS3Region),
		EvalsS3Prefix:             envOr(EnvEvalsS3Prefix, DefaultEvalsS3Prefix),
		EvalsS3Endpoint:           os.Getenv(EnvEvalsS3Endpoint),
		EvalsMaxCases:             DefaultEvalsMaxCases,
		EvalsMaxFileBytes:         DefaultEvalsMaxFileBytes,
		EvalsMaxTextBytes:         DefaultEvalsMaxTextBytes,
		EvalsMaxRunsPerUser:       DefaultEvalsMaxRunsPerUser,
		EvalsMaxRuns:              DefaultEvalsMaxRuns,
		EvalsMaxRunCostUSD:        DefaultEvalsMaxRunCostUSD,
		EvalsMaxUserCostUSDPerDay: DefaultEvalsMaxUserCostUSDPerDay,
		EvalsDatasetRetentionDays: DefaultEvalsDatasetRetentionDays,
		EvalsHeartbeatInterval:    Duration(DefaultEvalsHeartbeatInterval),
		EvalsHeartbeatStale:       Duration(DefaultEvalsHeartbeatStale),

		ImageStore:      envOr(EnvImageStore, ImageStoreFS),
		ImageS3Bucket:   os.Getenv(EnvImageS3Bucket),
		ImageS3Region:   os.Getenv(EnvImageS3Region),
		ImageS3Prefix:   os.Getenv(EnvImageS3Prefix),
		ImageS3Endpoint: os.Getenv(EnvImageS3Endpoint),

		OntologyS3Bucket: os.Getenv(EnvOntologyS3Bucket),
		OntologyS3Region: os.Getenv(EnvOntologyS3Region),
		OntologyS3Prefix: os.Getenv(EnvOntologyS3Prefix),
		AtifS3Bucket:     os.Getenv(EnvAtifS3Bucket),
		AtifS3Region:     os.Getenv(EnvAtifS3Region),
		AtifS3Prefix:     os.Getenv(EnvAtifS3Prefix),
		AtifS3Endpoint:   os.Getenv(EnvAtifS3Endpoint),

		ResearchS3Bucket:      os.Getenv(EnvResearchS3Bucket),
		ResearchS3Region:      os.Getenv(EnvResearchS3Region),
		ResearchS3NotesPrefix: os.Getenv(EnvResearchS3NotesPrefix),
	}
}

// Chunking returns the chunking settings (chunk_size, chunk_overlap_percent).
func (c *Config) Chunking() memory_chunk.Config {
	return memory_chunk.Config{
		Size:           c.ChunkSize,
		OverlapPercent: c.ChunkOverlapPercent,
	}
}

// Addr returns the HTTP listen address derived from the host and the port (e.g. ":7878", or "127.0.0.1:7878" with a host).
func (c *Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
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
			return fmt.Errorf("config: postgres_dsn (or %s) is required when db_backend is %q",
				EnvPostgresDSN,
				DBBackendPostgres)
		}
	default:
		return fmt.Errorf("config: db_backend %q must be %q or %q",
			c.DBBackend,
			DBBackendSQLite,
			DBBackendPostgres)
	}
	switch c.VectorStore {
	case VectorStoreChromem, VectorStorePinecone:
	case VectorStoreS3Vectors:
		if strings.TrimSpace(c.S3VectorsBucket) == "" {
			return fmt.Errorf("config: s3_vectors_bucket (or %s) is required when vector_store is %q",
				EnvS3VectorsBucket,
				VectorStoreS3Vectors)
		}
	default:
		return fmt.Errorf("config: vector_store %q must be %q, %q or %q",
			c.VectorStore,
			VectorStoreChromem,
			VectorStorePinecone,
			VectorStoreS3Vectors)
	}
	switch c.ImageStore {
	case ImageStoreFS:
	case ImageStoreS3:
		if strings.TrimSpace(c.ImageS3Bucket) == "" {
			return fmt.Errorf("config: image_s3_bucket (or %s) is required when image_store is %q",
				EnvImageS3Bucket,
				ImageStoreS3)
		}
	default:
		return fmt.Errorf("config: image_store %q must be %q or %q",
			c.ImageStore,
			ImageStoreFS,
			ImageStoreS3)
	}
	if c.EmbeddingDimensions < 1 {
		return fmt.Errorf("config: embedding_dimensions %d must be positive", c.EmbeddingDimensions)
	}
	if c.GeoRadiusKm != 0 && (math.IsNaN(c.GeoRadiusKm) || c.GeoRadiusKm < 0 || c.GeoRadiusKm > memory_geo.MaxDistanceKm) {
		return fmt.Errorf("config: geo_radius_km %v must be greater than 0 and at most %v",
			c.GeoRadiusKm,
			memory_geo.MaxDistanceKm)
	}
	if c.ResearchMaxCostUSD < 0 || math.IsNaN(c.ResearchMaxCostUSD) || math.IsInf(c.ResearchMaxCostUSD, 0) {
		return fmt.Errorf("config: research_max_cost_usd %v must be 0 (no cap) or a positive number of USD", c.ResearchMaxCostUSD)
	}
	if _, err := web.ParseSearch(c.ResearchSearchProvider, web.Anthropic); err != nil {
		return fmt.Errorf("config: research_search_provider: %w", err)
	}
	if _, err := web.ParseFetch(c.ResearchFetchProvider, web.Local); err != nil {
		return fmt.Errorf("config: research_fetch_provider: %w", err)
	}
	if c.LinkupKey == "" && (c.ResearchSearchProvider == string(web.Linkup) || c.ResearchFetchProvider == string(web.Linkup)) {
		return fmt.Errorf("config: the linkup provider needs a key: set %s or linkup_api_key", EnvLinkupKey)
	}
	if err := c.Chunking().Validate(); err != nil {
		return fmt.Errorf("config: chunk_size / chunk_overlap_percent: %w", err)
	}
	if err := c.validateEvals(); err != nil {
		return err
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

// validateEvals checks the evals_* limits and intervals: each limit must be positive (an omitted one keeps its default), and a run is
// stale only after several missed beats.
func (c *Config) validateEvals() error {
	limits := []struct {
		key   string
		value float64
	}{
		{"evals_max_cases", float64(c.EvalsMaxCases)},
		{"evals_max_file_bytes", float64(c.EvalsMaxFileBytes)},
		{"evals_max_text_bytes", float64(c.EvalsMaxTextBytes)},
		{"evals_max_runs_per_user", float64(c.EvalsMaxRunsPerUser)},
		{"evals_max_runs", float64(c.EvalsMaxRuns)},
		{"evals_max_run_cost_usd", c.EvalsMaxRunCostUSD},
		{"evals_max_user_cost_usd_per_day", c.EvalsMaxUserCostUSDPerDay},
		{"evals_dataset_retention_days", float64(c.EvalsDatasetRetentionDays)},
		{"evals_heartbeat_interval", float64(c.EvalsHeartbeatInterval.Std())},
		{"evals_heartbeat_stale", float64(c.EvalsHeartbeatStale.Std())},
	}
	for _, l := range limits {
		if l.value <= 0 || math.IsNaN(l.value) || math.IsInf(l.value, 0) {
			return fmt.Errorf("config: %s %v must be a positive number", l.key, l.value)
		}
	}
	if c.EvalsHeartbeatStale.Std() <= c.EvalsHeartbeatInterval.Std() {
		return fmt.Errorf("config: evals_heartbeat_stale (%s) must be longer than evals_heartbeat_interval (%s)",
			c.EvalsHeartbeatStale.Std(),
			c.EvalsHeartbeatInterval.Std())
	}
	return nil
}

// removedSetting is the message for the first key of keys that is a removed setting, "" if none is.
func removedSetting(keys []toml.Key) string {
	for _, key := range keys {
		if msg, ok := removedSettings[key.String()]; ok {
			return msg
		}
	}
	return ""
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
		return nil, fmt.Errorf("config: parse %s: %w",
			path,
			err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		if msg := removedSetting(undec); msg != "" {
			return nil, fmt.Errorf("config: %s: %s",
				path,
				msg)
		}
		return nil, fmt.Errorf("config: %s: unknown key %q",
			path,
			undec[0].String())
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w",
			path,
			err)
	}
	cfg.resolveDirs(filepath.Dir(path))
	return cfg, nil
}

// resolveDirs makes the relative directories of the *_dirs lists relative to base, the directory of the config file, so that they
// do not depend on the working directory the program is started from.
func (c *Config) resolveDirs(base string) {
	if c.AgentDefsDir != "" && !filepath.IsAbs(c.AgentDefsDir) {
		c.AgentDefsDir = filepath.Join(base, c.AgentDefsDir)
	}
	if c.ResearchNotesDir != "" && !filepath.IsAbs(c.ResearchNotesDir) {
		c.ResearchNotesDir = filepath.Join(base, c.ResearchNotesDir)
	}
	if c.SargoConfig != "" && !filepath.IsAbs(c.SargoConfig) {
		c.SargoConfig = filepath.Join(base, c.SargoConfig)
	}
	if c.EvalsLocalDir != "" && !filepath.IsAbs(c.EvalsLocalDir) {
		c.EvalsLocalDir = filepath.Join(base, c.EvalsLocalDir)
	}
	if c.EvalsTrajectoryDir != "" && !filepath.IsAbs(c.EvalsTrajectoryDir) {
		c.EvalsTrajectoryDir = filepath.Join(base, c.EvalsTrajectoryDir)
	}
	for _, list := range []*[]string{&c.DatasetsTrainingDirs, &c.EvalsTrainingDirs, &c.EvalsKnowledgeDirs, &c.OntologyDirs} {
		for i, d := range *list {
			if !filepath.IsAbs(d) {
				(*list)[i] = filepath.Join(base, d)
			}
		}
	}
}

// envOr returns the environment variable name, or def if it is empty.
func envOr(name,
	def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
