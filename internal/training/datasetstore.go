package training

// Where the training datasets (of the LoRA/LLM fine-tuning) are kept. There are two stores:
//
//   - the directories (DirStore, the default): the training datasets directories, one subdirectory with a dataset.toml manifest per
//     dataset (see manifest.go);
//   - S3 (S3Store, datasetstore_s3.go): the same layout under a prefix of a bucket, selected with training_datasets_s3_bucket.
//
// The datasets are shared (not per user). Everything that works with datasets (listing, finding, uploading, previewing, starting a
// job, auditing a model) takes a DatasetStore, so it does not know where they are. The GCNII* models do not use any of this: their
// datasets and classes come from the graph database.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	log "github.com/sirupsen/logrus"
)

// DatasetStore is the place the training datasets are kept. It is safe for concurrent use.
type DatasetStore interface {
	// Location says where the datasets are, without secrets, for logs (the directories, or "s3://bucket/prefix").
	Location() string
	// Discover returns the usable datasets by name, and the problems of the others (see DiscoverDatasets, which is what the directories
	// do). Without any datasets it returns nothing.
	Discover(ctx context.Context) ([]LocalDataset, []DatasetProblem)
	// ProblemDir is the Dir a DatasetProblem about ds has.
	ProblemDir(ds LocalDataset) string
	// Stats returns the stats of the data file of ds (the record of the dataset is made from them); an error says why it is not usable.
	Stats(ctx context.Context,
		ds LocalDataset) (FileStats, error)
	// Read returns the data file of ds: at most MaxDatasetBytes+1 bytes (the caller checks the limit).
	Read(ctx context.Context,
		ds LocalDataset) ([]byte, error)
	// ReadHead returns the first n+1 bytes of the data file of ds, or all of it if it is shorter.
	ReadHead(ctx context.Context,
		ds LocalDataset,
		n int64) ([]byte, error)
	// Stage prepares the saving of an uploaded dataset called name, whose data file is filename (name + ".jsonl"), with content (already
	// validated): it checks that the dataset can be saved (a dataset of that name that has a manifest of its own, naming another data file,
	// or that is somewhere else, is a conflict; errors are *Error) and returns the dataset it makes, commit (which writes it) and discard
	// (which drops what was staged, if the caller does not commit). The caller records the dataset between Stage and commit, so a failure
	// leaves neither a record without its file nor a half-written file.
	Stage(ctx context.Context,
		name,
		filename string,
		content []byte) (ds LocalDataset, commit func() error, discard func(), err error)
}

// DirStore is the store of the training datasets directories (see Dirs.Datasets): searched in order, uploads go to the first.
type DirStore []string

var _ DatasetStore = DirStore(nil)

// Location implements DatasetStore.
func (d DirStore) Location() string { return strings.Join(d, ", ") }

// Discover implements DatasetStore.
func (d DirStore) Discover(_ context.Context) ([]LocalDataset, []DatasetProblem) {
	return DiscoverDatasets(d...)
}

// ProblemDir implements DatasetStore: the name of the subdirectory, or with more than one directory its path.
func (d DirStore) ProblemDir(ds LocalDataset) string {
	if len(d) > 1 {
		return ds.Dir
	}
	return filepath.Base(ds.Dir)
}

// Stats implements DatasetStore.
func (d DirStore) Stats(_ context.Context,
	ds LocalDataset) (FileStats, error) {
	return DatasetFileStats(ds.Path)
}

// Read implements DatasetStore.
func (d DirStore) Read(_ context.Context,
	ds LocalDataset) ([]byte, error) {
	return os.ReadFile(ds.Path)
}

// ReadHead implements DatasetStore.
func (d DirStore) ReadHead(_ context.Context,
	ds LocalDataset,
	n int64) ([]byte, error) {
	f, err := os.Open(ds.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n+1))
}

// Stage implements DatasetStore: the dataset goes in the subdirectory called name of the first directory, next to a manifest that is
// written if there is none.
func (d DirStore) Stage(_ context.Context,
	name,
	filename string,
	content []byte) (LocalDataset, func() error, func(), error) {
	if len(d) == 0 || d[0] == "" {
		return LocalDataset{}, nil, nil, internalErr("no training dataset directory is configured", nil)
	}
	dsDir := filepath.Join(d[0], name)
	manifestPath := filepath.Join(dsDir, ManifestFilename)
	_, statErr := os.Stat(manifestPath)
	writeManifest := errors.Is(statErr, fs.ErrNotExist)
	if !writeManifest {
		if cur, err := ParseManifest(manifestPath); err != nil || cur.Name != name || cur.Manifest.File != filename {
			return LocalDataset{}, nil, nil, conflictErr("a dataset named " + name + " exists already with its own manifest and data file; upload under another name")
		}
	}
	found, _ := DiscoverDatasets(d...)
	for _, f := range found {
		if f.Name == name && filepath.Clean(f.Dir) != filepath.Clean(dsDir) {
			where := filepath.Base(f.Dir)
			if len(d) > 1 {
				where = f.Dir
			}
			return LocalDataset{}, nil, nil, conflictErr("a dataset named " + name + " exists already in " + where)
		}
	}
	stage, discard, err := stageDatasetFile(dsDir,
		filename,
		content)
	if err != nil {
		log.WithError(err).Error("training: save training dataset file failed")
		return LocalDataset{}, nil, nil, internalErr("failed to save the dataset file", err)
	}
	ds := newUploadedDataset(name,
		filename,
		dsDir,
		filepath.Join(dsDir, filename))
	commit := func() error {
		if err := stage(); err != nil {
			return internalErr("failed to save the dataset file", err)
		}
		if !writeManifest {
			return nil
		}
		if err := writeUploadManifest(manifestPath, ds.Manifest); err != nil {
			log.WithError(err).Error("training: write training dataset manifest failed")
			return internalErr("failed to save the dataset's manifest", err)
		}
		return nil
	}
	return ds, commit, discard, nil
}

// uploadedManifestDescription is the description of the manifest an upload writes.
const uploadedManifestDescription = "Uploaded through the training datasets API."

// newUploadedDataset is the dataset an upload makes: the manifest it writes, and where it is.
func newUploadedDataset(name,
	filename,
	dir,
	path string) LocalDataset {
	return LocalDataset{
		Manifest: Manifest{
			Name:        name,
			Description: uploadedManifestDescription,
			Format:      FormatJSONL,
			File:        filename,
		},
		Dir:  dir,
		Path: path,
	}
}

// stageDatasetFile writes content to dir/name atomically (a temp file in dir, renamed), creating dir. It returns a
// function that completes the write (the rename), and one that removes the temp file if the caller does not go on.
func stageDatasetFile(dir,
	name string,
	content []byte) (commit func() error, discard func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, nil, err
	}
	discard = func() { os.Remove(tmp.Name()) }
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		discard()
		return nil, nil, err
	}
	if err := tmp.Close(); err != nil {
		discard()
		return nil, nil, err
	}
	return func() error { return os.Rename(tmp.Name(), filepath.Join(dir, name)) }, discard, nil
}

// writeUploadManifest creates the manifest file of an uploaded dataset (it must not exist).
func writeUploadManifest(path string,
	m Manifest) error {
	f, err := os.OpenFile(path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o644)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(m); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// readDatasetFile reads the data file of ds from the store. The errors are safe to show to the user.
func readDatasetFile(ctx context.Context,
	store DatasetStore,
	ds *LocalDataset) ([]byte, error) {
	content, err := store.Read(ctx, *ds)
	if err != nil {
		log.WithError(err).WithField("dataset", ds.Name).Error("training: read training dataset file failed")
		return nil, fmt.Errorf("the data file of dataset %s cannot be read", ds.Name)
	}
	if len(content) > MaxDatasetBytes {
		return nil, fmt.Errorf("the data file of dataset %s is over %d MB",
			ds.Name,
			MaxDatasetBytes>>20)
	}
	return content, nil
}
