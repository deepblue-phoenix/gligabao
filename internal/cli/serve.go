package cli

import (
	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

var (
	serveDBPath       string
	serveVectorDBPath string
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return gligabao.Serve(cmd.Context(), cfg, gligabao.ServeOptions{
			DBPath:       serveDBPath,
			VectorDBPath: serveVectorDBPath,
		})
	},
}

//---------------------------------------------------------------------------------------------

func init() {
	serveCmd.Flags().StringVar(&serveDBPath, "db", gligabao.DefaultDBPath, "SQLite database file path")
	serveCmd.Flags().StringVar(&serveVectorDBPath, "vector-db", "", "sqlite-vec database file path (default: next to --db)")
	rootCmd.AddCommand(serveCmd)
}
