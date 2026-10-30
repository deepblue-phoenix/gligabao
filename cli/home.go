package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepblue-phoenix/gligabao"
)

// EnvHome overrides the directory of the CLI's state and default data (default: ~/.gligabao).
const EnvHome = "GLIGABAO_HOME"

// homeDirName is the name of the directory, in the home of the user, that holds the state and the default data of the local daemon.
const homeDirName = ".gligabao"

// layout says where the local daemon of the CLI keeps its config and data.
//
// If the working directory has a gligabao config file (gligabao.toml), or --config was given, the CLI works the way `serve` does: that
// config, and the paths of `serve` (relative to the working directory). Otherwise it uses a stable directory, ~/.gligabao
// (EnvHome), so that an agent gets the same memory whatever directory it runs in: ~/.gligabao/gligabao.toml if it exists, else
// the defaults, and the database in ~/.gligabao/gligabao.db.
type layout struct {
	// StateDir is ~/.gligabao: the daemon's log and pid files always live here.
	StateDir string
	// Project is true when the config comes from the working directory or --config.
	Project bool
	// ConfigPath is the config file; "" means the defaults (no file).
	ConfigPath string
	// DBPath is the --db the daemon is started with; "" leaves the default of `serve`.
	DBPath string
}

// resolveLayout decides the layout. configFlag is the value of --config and configChanged says it was given.
func resolveLayout(configFlag string, configChanged bool) (layout, error) {
	l := layout{}
	if env := os.Getenv(EnvHome); env != "" {
		l.StateDir = env
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return l, fmt.Errorf("cannot find the home directory (set %s): %w", EnvHome, err)
		}
		l.StateDir = filepath.Join(home, homeDirName)
	}
	cwdConfig := filepath.Base(gligabao.DefaultConfigPath())
	switch {
	case configChanged:
		l.Project, l.ConfigPath = true, configFlag
	case fileExists(cwdConfig):
		l.Project, l.ConfigPath = true, cwdConfig
	default:
		if p := filepath.Join(l.StateDir, cwdConfig); fileExists(p) {
			l.ConfigPath = p
		}
		l.DBPath = filepath.Join(l.StateDir, "gligabao.db")
	}
	return l, nil
}

func (l layout) load() (*gligabao.Config, error) {
	if l.ConfigPath == "" {
		return gligabao.DefaultConfig(), nil
	}
	return gligabao.LoadConfig(l.ConfigPath)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// tail returns the last n lines of the file at path (for the error when the daemon did not start).
func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
