package training

import "path/filepath"

// Dirs are the directories the training endpoints keep files in. An empty field means the default (see DefaultDirs).
type Dirs struct {
	// Datasets are the training datasets directories, searched in order: one subdirectory with a dataset.toml manifest per
	// dataset. A dataset name found in an earlier directory hides the same name in a later one. Uploads are saved in the first
	// (created at the first upload; it may not exist). The others are only read.
	Datasets []string
	// Evals are the directories of the evals of trained models, searched in order, one JSON file per eval; an eval name found in an
	// earlier directory hides the same name in a later one. They may not exist: then there are no evals in them.
	Evals []string
	// ModelDeploy is the directory the files of trained models are fetched to for a deployment, one subdirectory per training
	// job. Created at the first deploy; gligabao never deletes it.
	ModelDeploy string
}

// DefaultDirs returns the default directories for the database file dbPath: "datasets_training", "evals_training" and
// "trained_models" in the file's directory (bin/datasets_training, ... for bin/gligabao.db).
func DefaultDirs(dbPath string) Dirs {
	base := filepath.Dir(dbPath)
	return Dirs{
		Datasets:    []string{filepath.Join(base, "datasets_training")},
		Evals:       []string{filepath.Join(base, "evals_training")},
		ModelDeploy: filepath.Join(base, "trained_models"),
	}
}

// WithDefaults returns d with every empty field replaced by its DefaultDirs(dbPath) value.
func (d Dirs) WithDefaults(dbPath string) Dirs {
	def := DefaultDirs(dbPath)
	if len(d.Datasets) == 0 {
		d.Datasets = def.Datasets
	}
	if len(d.Evals) == 0 {
		d.Evals = def.Evals
	}
	if d.ModelDeploy == "" {
		d.ModelDeploy = def.ModelDeploy
	}
	return d
}
