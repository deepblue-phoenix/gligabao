package cli

import (
	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

// newServe builds the `serve` command. Its flags are the single definition of gligabao's serve flags.
func newServe(c *CLI, o Options) *cobra.Command {
	var (
		dbPath, vectorDir, imageDir string
		evalDB, exportDir           string
		datasetDirs, evalDirs       []string
		ontologyDirs                []string
		eval, training, adminUI     bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Arguments are parsed by now: a runtime error is not a usage error, so do not print the help for it.
			cmd.SilenceUsage = true
			cfg := c.cfg
			if cmd.Flags().Changed("eval") {
				cfg.EvalModeEnable = eval
			}
			if cmd.Flags().Changed("training") {
				cfg.TrainingModeEnable = training
			}
			if cmd.Flags().Changed("admin-ui") {
				cfg.AdminUIenable = adminUI
			}
			opts := gligabao.ServeOptions{
				DBPath:                dbPath,
				VectorDir:             vectorDir,
				ImageDir:              imageDir,
				TrainingDatasetDirs:   datasetDirs,
				TrainingEvalDirs:      evalDirs,
				OntologyDirs:          ontologyDirs,
				TrainedModelExportDir: exportDir,
				EvalDBPath:            evalDB,
			}
			if o.ServeOptions != nil {
				cleanup, err := o.ServeOptions(cmd, cfg, &opts)
				if cleanup != nil {
					defer cleanup()
				}
				if err != nil {
					return err
				}
			}
			return gligabao.Serve(cmd.Context(), cfg, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&dbPath, "db", gligabao.DefaultDBPath, "SQLite database file path")
	f.StringVar(&vectorDir, "vector-dir", "", "chromem vector store directory (default: next to --db)")
	f.StringVar(&imageDir, "image-dir", "", "directory of the image files stored by remember with an image (default: next to --db)")
	f.StringArrayVar(&datasetDirs, "training-dataset-dir", nil, "training datasets directory: one subdirectory with a dataset.toml manifest per dataset; repeatable, searched in order, uploads go to the first (default: config datasets_training_dirs, else a datasets_training directory next to --db)")
	f.StringArrayVar(&evalDirs, "training-eval-dir", nil, "directory of the evals of trained models: one JSON file (a map with the keys name and cases) per eval; repeatable, searched in order (default: config evals_training_dirs, else an evals_training directory next to --db)")
	f.StringArrayVar(&ontologyDirs, "ontology-dir", nil, "directory ontology files (OWL, RDF/XML) are loaded from by name through the ontologies API; repeatable, searched in order (default: config ontology_dirs, else none)")
	f.StringVar(&exportDir, "trained-model-export-dir", "", "directory the files of trained models are fetched to for an export, one subdirectory per training job (default: a trained_models directory next to --db)")
	f.StringVar(&evalDB, "eval-db", "", "eval database: SQLite file with the eval datasets, experiments and runs (./run_evals.sh); shown next to the main database in the admin UI, which reads the Evals block from it")
	f.BoolVar(&eval, "eval", false, "register the eval endpoints that return ATIF trajectories (config: eval)")
	f.BoolVar(&training, "training", false, "register the training endpoints under /v1/gb/training/ (config: training)")
	f.BoolVar(&adminUI, "admin-ui", false, "serve the admin UI at /v1/gb/admin/ (config: admin_ui)")
	return cmd
}
