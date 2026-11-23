package training

import (
	"context"
	"path/filepath"

	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
)

// Dirs are the directories the training endpoints keep files in. An empty field means the default (see DefaultDirs).
type Dirs struct {
	// Datasets are the training datasets directories, searched in order: one subdirectory with a dataset.toml manifest per
	// dataset. A dataset name found in an earlier directory hides the same name in a later one. Uploads are saved in the first
	// (created at the first upload; it may not exist). The others are only read.
	Datasets []string
	// DatasetsS3 is the second place the datasets can be: with a bucket (Bucket != ""), the datasets are the objects of the S3 bucket
	// (S3Store, prefix DefaultDatasetsS3Prefix by default) and Datasets is not used; uploads go to the bucket. Without one they are in
	// the directories. There is no mix and no fallback from one to the other.
	DatasetsS3 userfiles.S3Config
	// DatasetStore, when set (OpenDatasets does), is the store of the datasets whatever the two fields above say.
	DatasetStore DatasetStore
	// Evals are the directories of the evals of trained models, searched in order, one JSON file per eval; an eval name found in an
	// earlier directory hides the same name in a later one. They may not exist: then there are no evals in them.
	Evals []string
	// ModelDeploy is the directory the files of trained models are fetched to for a deployment, one subdirectory per training
	// job. Created at the first deploy; gligabao never deletes it.
	ModelDeploy string
}

// DefaultDirs returns the default directories for the database file dbPath: "datasets_training", "evals_training" and
// "trained_models" in the file's directory (build/datasets_training, ... for build/gligabao.db).
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

// OpenDatasets returns d with DatasetStore set: S3 if DatasetsS3 has a bucket, else the directories Datasets. The S3 store fails here
// (and the server does not start) when its client cannot be made.
func (d Dirs) OpenDatasets(ctx context.Context) (Dirs, error) {
	if d.DatasetStore != nil {
		return d, nil
	}
	if d.DatasetsS3.Bucket == "" {
		d.DatasetStore = DirStore(d.Datasets)
		return d, nil
	}
	store, err := NewS3Store(ctx, d.DatasetsS3)
	if err != nil {
		return d, err
	}
	d.DatasetStore = store
	return d, nil
}

// Store is the store of the training datasets: DatasetStore if set, else the directories Datasets.
func (d Dirs) Store() DatasetStore {
	if d.DatasetStore != nil {
		return d.DatasetStore
	}
	return DirStore(d.Datasets)
}
