// Package gligabao is the public API for embedding gligabao in other Go programs.
//
// Everything else lives under internal/ and cannot be imported from outside this module.
// The standalone CLI (`cmd/gligabao`) uses this package too, so both behave identically.
//
//	cfg, err := gligabao.LoadConfig("gligabao.toml") // or gligabao.DefaultConfig()
//	...
//	err = gligabao.Serve(ctx, cfg, gligabao.ServeOptions{DBPath: "data/gligabao.db"})
package gligabao

import (
	"context"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/server"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// Config is the gligabao configuration (the contents of gligabao.toml).
type Config = config.Config

// Price is a per-model price override, USD per 1M tokens.
type Price = config.Price

// DefaultDBPath is the SQLite database file used when ServeOptions.DBPath is empty.
const DefaultDBPath = db.DefaultSQLitePath

// Hooks is a set of callbacks gligabao invokes (e.g. for system events); see package monitoring.
type Hooks = monitoring.Hooks

// SystemEvent is the key/value data attached to a system event.
type SystemEvent = monitoring.Event

// ServeOptions holds the settings that are not part of the Config file.
type ServeOptions struct {
	// DBPath is the SQLite database file (sqlite backend). Default: DefaultDBPath.
	DBPath string
	// VectorDir is the directory of the chromem vector store. Default: next to DBPath.
	VectorDir string
	// Hooks are installed for the lifetime of Serve; every one is called for each event.
	Hooks []Hooks
}

//---------------------------------------------------------------------------------------------

// DefaultConfig returns the default configuration; API keys are taken from the environment.
func DefaultConfig() *Config { return config.Default() }

// DefaultConfigPath returns the default config location (next to the running binary).
func DefaultConfigPath() string { return config.DefaultPath() }

// LoadConfig parses and validates the TOML config file at path.
// A missing file yields an error wrapping fs.ErrNotExist.
func LoadConfig(path string) (*Config, error) { return config.Load(path) }

//---------------------------------------------------------------------------------------------

// Serve starts the HTTP server and blocks until ctx is cancelled or SIGINT/SIGTERM is
// received, then shuts down gracefully. A nil cfg means DefaultConfig().
func Serve(ctx context.Context, cfg *Config, opts ServeOptions) error {
	if cfg == nil {
		cfg = config.Default()
	}
	if opts.DBPath == "" {
		opts.DBPath = DefaultDBPath
	}
	monitoring.SetHooks(opts.Hooks)
	defer monitoring.SetHooks(nil)
	return server.Run(ctx, server.Options{
		Addr:         cfg.Addr(),
		DB:           db.ConfigFrom(cfg, opts.DBPath, opts.VectorDir),
		AnthropicKey: cfg.AnthropicKey,
		Prices:       aiPrices(cfg),
	})
}

// aiPrices converts the configured prices; omitted cache prices default to the input price.
func aiPrices(c *Config) map[string]ai.Price {
	out := make(map[string]ai.Price, len(c.Prices))
	for model, p := range c.Prices {
		ap := ai.Price{Input: p.Input, Output: p.Output, CacheWrite: p.Input, CacheRead: p.Input,
			LongThreshold: p.LongThreshold, LongInput: p.LongInput, LongOutput: p.LongOutput}
		if p.CacheWrite != nil {
			ap.CacheWrite = *p.CacheWrite
		}
		if p.CacheRead != nil {
			ap.CacheRead = *p.CacheRead
		}
		out[model] = ap
	}
	return out
}
