package cli

import (
	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

var (
	serveDBPath     string
	serveVectorDir  string
	serveImageDir   string
	serveDatasetDir string
	serveEvalDir    string
	serveEvalDB     string
	serveEval       bool
	serveAdminUI    bool
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("eval") {
			cfg.EvalModeEnable = serveEval
		}
		if cmd.Flags().Changed("admin-ui") {
			cfg.AdminUIenable = serveAdminUI
		}
		return gligabao.Serve(cmd.Context(), cfg, gligabao.ServeOptions{
			DBPath:             serveDBPath,
			VectorDir:          serveVectorDir,
			ImageDir:           serveImageDir,
			TrainingDatasetDir: serveDatasetDir,
			TrainingEvalDir:    serveEvalDir,
			EvalDBPath:         serveEvalDB,
		})
	},
}

//---------------------------------------------------------------------------------------------

func init() {
	serveCmd.Flags().StringVar(&serveDBPath, "db", gligabao.DefaultDBPath, "SQLite database file path")
	serveCmd.Flags().StringVar(&serveVectorDir, "vector-dir", "", "chromem vector store directory (default: next to --db)")
	serveCmd.Flags().StringVar(&serveImageDir, "image-dir", "", "directory of the image files stored by remember with an image (default: next to --db)")
	serveCmd.Flags().StringVar(&serveDatasetDir, "training-dataset-dir", "", "training datasets directory: one subdirectory with a dataset.toml manifest per dataset (default: a datasets_training directory next to --db)")
	serveCmd.Flags().StringVar(&serveEvalDir, "training-eval-dir", "", "directory of the evals of trained models: one JSON file (a map with the keys name and cases) per eval (default: an evals_training directory next to --db)")
	serveCmd.Flags().StringVar(&serveEvalDB, "eval-db", "", "eval database: SQLite file with the eval datasets, experiments and runs (./run_evals.sh); shown next to the main database in the admin UI, which reads the Evals block from it")
	serveCmd.Flags().BoolVar(&serveEval, "eval", false, "register the eval endpoints that return ATIF trajectories (config: eval)")
	serveCmd.Flags().BoolVar(&serveAdminUI, "admin-ui", false, "serve the admin UI at /v1/gb/admin/ (config: admin_ui)")
	rootCmd.AddCommand(serveCmd)
}
