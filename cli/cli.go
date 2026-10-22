// Package cli defines the gligabao command-line interface (serve, version and their flags).
//
// It is public so that a program embedding gligabao gets every flag of the gligabao CLI without redefining them: build the
// command tree with New, then add only what the program has on top (its own flags, commands, ServeOptions). A flag added
// to the gligabao CLI later is then in the embedding program too, with no change there.
package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
	"github.com/deepblue-phoenix/gligabao/internal/sys/buildinfo"
)

// Options customizes the command tree of New. The zero value is the standalone gligabao CLI.
type Options struct {
	// Use is the program's name (default "gligabao").
	Use string
	// Short is the root command's description (default "gligabao CLI").
	Short string
	// VersionString, if set, is what `version` and `--version` print instead of gligabao's own version.
	VersionString func() string
	// PreRun, if set, runs before every command that loads the config (all but `version`), before the config is loaded.
	PreRun func(cmd *cobra.Command) error
	// ServeOptions, if set, is called by `serve` with the loaded config (flags already applied) and the options built from
	// the flags; it may change both (set App, Hooks, TimeseriesProviders, ... or change a default) and an error aborts
	// the command. The program's own flags are read from cmd. A non-nil cleanup func it returns is called when Serve returns
	// (also when it returns an error).
	ServeOptions func(cmd *cobra.Command, cfg *gligabao.Config, opts *gligabao.ServeOptions) (cleanup func(), err error)
}

// CLI is the command tree. Add the program's own flags and commands to Root and Serve.
type CLI struct {
	// Root is the root command (it has the persistent flag --config).
	Root *cobra.Command
	// Serve is the `serve` command, with all of gligabao's flags.
	Serve *cobra.Command
	cfg   *gligabao.Config
}

// Config is the configuration loaded for the running command (nil before one runs, and for `version`).
func (c *CLI) Config() *gligabao.Config { return c.cfg }

// New builds the command tree: the root with --config, `serve` with all of its flags, and `version`.
func New(o Options) *CLI {
	if o.Use == "" {
		o.Use = "gligabao"
	}
	if o.Short == "" {
		o.Short = "gligabao CLI"
	}
	c := &CLI{}
	var configPath string

	root := &cobra.Command{
		Use:   o.Use,
		Short: o.Short,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if o.PreRun != nil {
				if err := o.PreRun(cmd); err != nil {
					return err
				}
			}
			cfg, err := gligabao.LoadConfig(configPath)
			if err != nil {
				// A missing file is fine only for the default path.
				if errors.Is(err, fs.ErrNotExist) && !cmd.Flags().Changed("config") {
					cfg = gligabao.DefaultConfig()
				} else {
					return err
				}
			}
			c.cfg = cfg
			return nil
		},
	}
	versionString := o.VersionString
	if versionString == nil {
		versionString = buildinfo.Library().String
	}
	root.Version = buildinfo.Library().Version
	if o.VersionString != nil {
		root.Version = "-" // cobra only adds --version if Version is set; the template below prints the real one
	}
	root.SetVersionTemplate(versionString() + "\n")
	root.PersistentFlags().StringVar(&configPath, "config", gligabao.DefaultConfigPath(), "path to TOML config file")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version embedded in this binary",
		Args:  cobra.NoArgs,
		// The version needs no config file.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
			return nil
		},
	})

	c.Root = root
	c.Serve = newServe(c, o)
	root.AddCommand(c.Serve)
	return c
}

// Execute runs the root command and exits with status 1 on error.
func (c *CLI) Execute() {
	if err := c.Root.Execute(); err != nil {
		os.Exit(1)
	}
}

// Execute runs the standalone gligabao CLI.
func Execute() { New(Options{}).Execute() }
