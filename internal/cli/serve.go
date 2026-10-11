package cli

import (
	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

var (
	serveDBPath    string
	serveVectorDir string
	serveEval      bool
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("eval") {
			cfg.Eval = serveEval
		}
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
	serveCmd.Flags().BoolVar(&serveEval, "eval", false, "register the eval endpoints that return ATIF trajectories (config: eval)")
	rootCmd.AddCommand(serveCmd)
}
