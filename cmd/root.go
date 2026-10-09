package cmd

import (
	"errors"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/ivan/gligabao/internal/config"
)

var (
	configPath string
	// cfg is the loaded application configuration, available to all commands.
	cfg *config.Config
)

var rootCmd = &cobra.Command{
	Use:   "gligabao",
	Short: "gligabao CLI",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		c, err := config.Load(configPath)
		if err != nil {
			// A missing file is fine only for the default path.
			if errors.Is(err, fs.ErrNotExist) && !cmd.Flags().Changed("config") {
				c = config.Default()
			} else {
				return err
			}
		}
		cfg = c
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", config.DefaultPath(), "path to TOML config file")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
