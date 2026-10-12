package cli

import (
	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

var (
	serveDBPath    string
	serveVectorDir string
	serveEvalDB    string
	serveEval      bool
	serveAdminUI   bool
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("eval") {
			cfg.Eval = serveEval
		}
		if cmd.Flags().Changed("admin-ui") {
			cfg.AdminUI = serveAdminUI
		}
		return gligabao.Serve(cmd.Context(), cfg, gligabao.ServeOptions{
			DBPath:     serveDBPath,
			VectorDir:  serveVectorDir,
			EvalDBPath: serveEvalDB,
		})
	},
}

//---------------------------------------------------------------------------------------------

func init() {
	serveCmd.Flags().StringVar(&serveDBPath, "db", gligabao.DefaultDBPath, "SQLite database file path")
	serveCmd.Flags().StringVar(&serveVectorDir, "vector-dir", "", "chromem vector store directory (default: next to --db)")
	serveCmd.Flags().StringVar(&serveEvalDB, "eval-db", "", "eval database: SQLite file with the eval datasets, experiments and runs (./run_evals.sh); shown next to the main database in the admin UI, which reads the Evals block from it")
	serveCmd.Flags().BoolVar(&serveEval, "eval", false, "register the eval endpoints that return ATIF trajectories (config: eval)")
	serveCmd.Flags().BoolVar(&serveAdminUI, "admin-ui", false, "serve the admin UI at /v1/gb/admin/ (config: admin_ui)")
	rootCmd.AddCommand(serveCmd)
}
