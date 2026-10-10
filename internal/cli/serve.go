package cli

import (
	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

var (
	serveDBPath    string
	serveVectorDir string
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return gligabao.Serve(cmd.Context(), cfg, gligabao.ServeOptions{
			DBPath:    serveDBPath,
			VectorDir: serveVectorDir,
		})
	},
}

//---------------------------------------------------------------------------------------------

func init() {
	serveCmd.Flags().StringVar(&serveDBPath, "db", gligabao.DefaultDBPath, "SQLite database file path")
	serveCmd.Flags().StringVar(&serveVectorDir, "vector-dir", "", "chromem vector store directory (default: next to --db)")
	rootCmd.AddCommand(serveCmd)
}
