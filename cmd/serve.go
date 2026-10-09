package cmd

import (
	"github.com/spf13/cobra"

	"github.com/ivan/gligabao/internal/server"
)

var (
	serveDBPath string
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return server.Run(cmd.Context(), server.Options{
			Addr:   cfg.Addr(),
			DBPath: serveDBPath,
		})
	},
}

//---------------------------------------------------------------------------------------------

func init() {
	serveCmd.Flags().StringVar(&serveDBPath, "db", "bin/gligabao.db", "SQLite database file path")
	rootCmd.AddCommand(serveCmd)
}
