// Package config loads the application configuration from a TOML file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// FileName is the default config file name.
const FileName = "gligabao.toml"

// DefaultPort is used when the config does not set a port.
const DefaultPort = 8080

// Config holds the application configuration.
type Config struct {
	Port int `toml:"port"`
}

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

// Default returns a Config populated with default values.
func Default() *Config {
	return &Config{Port: DefaultPort}
}

// Addr returns the HTTP listen address derived from the port (e.g. ":8080").
func (c *Config) Addr() string {
	return ":" + strconv.Itoa(c.Port)
}

// Validate checks that the config values are usable.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("config: port %d out of range (1-65535)", c.Port)
	}
	return nil
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
