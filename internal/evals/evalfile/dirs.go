package evalfile

import (
	"errors"
	"flag"
	"io/fs"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// DefaultConfigPath is where the eval runners read the config file from when -config is not given (relative to the repository
// root, where they are run from: next to the databases they default to).
const DefaultConfigPath = "build/gligabao.toml"

// DirsFlag is a repeatable command-line flag that collects directories (flag.Var).
type DirsFlag []string

func (d *DirsFlag) String() string     { return strings.Join(*d, ",") }
func (d *DirsFlag) Set(v string) error { *d = append(*d, v); return nil }

var _ flag.Value = (*DirsFlag)(nil)

// ResolveDirs returns the directories the evals are read from: the ones given on the command line (flagDirs) if any; else the ones
// the config file at configPath lists (pick selects the list of the config); else def. The config file is optional unless
// configExplicit (-config was given): a missing default one is not an error.
func ResolveDirs(flagDirs []string, configPath string, configExplicit bool, pick func(*config.Config) []string, def string) ([]string, error) {
	if len(flagDirs) > 0 {
		return flagDirs, nil
	}
	cfg, err := config.Load(configPath)
	switch {
	case err == nil:
		if dirs := pick(cfg); len(dirs) > 0 {
			return dirs, nil
		}
	case errors.Is(err, fs.ErrNotExist) && !configExplicit:
	default:
		return nil, err
	}
	return []string{def}, nil
}
