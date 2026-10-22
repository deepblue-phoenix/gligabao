package training

import "path/filepath"

// Dirs are the directories the training endpoints keep files in. An empty field means the default (see DefaultDirs).
type Dirs struct {
	// Datasets is the training datasets directory: one subdirectory with a dataset.toml manifest per dataset (uploads are
	// saved there too). Created at the first upload; it may not exist.
	Datasets string
	// Evals is the directory of the evals of trained models, one JSON file per eval. It may not exist: then there are no evals.
	Evals string
	// ModelExport is the directory the files of trained models are fetched to for an export, one subdirectory per training
	// job. Created at the first export; gligabao never deletes it.
	ModelExport string
}

// DefaultDirs returns the default directories for the database file dbPath: "datasets_training", "evals_training" and
// "trained_models" in the file's directory (bin/datasets_training, ... for bin/gligabao.db).
func DefaultDirs(dbPath string) Dirs {
	base := filepath.Dir(dbPath)
	return Dirs{
		Datasets:    filepath.Join(base, "datasets_training"),
		Evals:       filepath.Join(base, "evals_training"),
		ModelExport: filepath.Join(base, "trained_models"),
	}
}

// WithDefaults returns d with every empty field replaced by its DefaultDirs(dbPath) value.
func (d Dirs) WithDefaults(dbPath string) Dirs {
	def := DefaultDirs(dbPath)
	if d.Datasets == "" {
		d.Datasets = def.Datasets
	}
	if d.Evals == "" {
		d.Evals = def.Evals
	}
	if d.ModelExport == "" {
		d.ModelExport = def.ModelExport
	}
	return d
}
