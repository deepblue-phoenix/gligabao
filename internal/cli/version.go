package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao/internal/buildinfo"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version embedded in this binary",
	Args:  cobra.NoArgs,
	// The version needs no config file.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE: func(cmd *cobra.Command, args []string) error {
		lib := buildinfo.Library()
		fmt.Fprintln(cmd.OutOrStdout(), lib.String())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
