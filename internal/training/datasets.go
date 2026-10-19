package training

// The training datasets: validating, listing, finding, storing (an upload) and previewing them. Where they are is manifest.go.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// MaxDatasetBytes is the largest dataset file there can be (the upload refuses a bigger one, a manifest naming one is a problem).
const MaxDatasetBytes = 64 << 20

// datasetFilenameRe is what an uploaded file's name must be (the dataset gets its name without .jsonl; see datasetNameRe).
var datasetFilenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.jsonl$`)

// ValidateDataset checks that content is a JSONL training dataset, the format the training reads (see
// docs/training/training_data_format.md): UTF-8, every non-blank line a JSON object with string "instruction" and "output"
// (and "input", if present, a string). It returns the number of records, or an error naming the first bad line.
func ValidateDataset(content []byte) (int, error) {
	if !utf8.Valid(content) {
		return 0, errors.New("the file is not valid UTF-8")
	}
	n := 0
	for i, line := range bytes.Split(content, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec map[string]json.RawMessage
		if err := json.Unmarshal(line, &rec); err != nil {
			return 0, fmt.Errorf("line %d is not a JSON object", i+1)
		}
		for _, k := range []string{"instruction", "output", "input"} {
			raw, ok := rec[k]
			if !ok {
				if k == "input" {
					continue
				}
				return 0, fmt.Errorf("line %d has no %q", i+1, k)
			}
			var s string
			if json.Unmarshal(raw, &s) != nil || bytes.Equal(raw, []byte("null")) {
				return 0, fmt.Errorf("line %d: %q must be a string", i+1, k)
			}
		}
		n++
	}
	if n == 0 {
		return 0, errors.New("the file has no records")
	}
	return n, nil
}

// stageDatasetFile writes content to dir/name atomically (a temp file in dir, renamed), creating dir. It returns a
// function that completes the write (the rename), and one that removes the temp file if the caller does not go on.
func stageDatasetFile(dir, name string, content []byte) (commit func() error, discard func(), err error) {
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

// DatasetInfo is a dataset as the API shows it: its record (trainingdb.TrainingDataset; Filename is the name it has on
// the sidecar, "<name>.jsonl") and what its manifest says.
type DatasetInfo struct {
	trainingdb.TrainingDataset
	Manifest
	// Dir is the dataset's subdirectory in the training datasets directory.
	Dir string `json:"dir"`
}

// ensureRecord returns the record of ds, whose data file has the stats s: the stored one if it describes this version of
// the file, otherwise a new one (status stored, not synced), replacing the record that described an earlier version.
func ensureRecord(ctx context.Context, db *sql.DB, ds LocalDataset, s FileStats) (*trainingdb.TrainingDataset, error) {
	rec, err := trainingdb.GetTrainingDatasetByFilename(ctx, db, ds.Filename())
	if err == nil && rec.SHA256 == s.SHA256 && rec.SizeBytes == s.Size && rec.NumRecords == s.Records {
		return rec, nil
	}
	if err != nil && !errors.Is(err, relational.ErrNotFound) {
		return nil, err
	}
	if _, err := trainingdb.SaveTrainingDataset(ctx, db, trainingdb.TrainingDataset{
		Filename: ds.Filename(), SizeBytes: s.Size, NumRecords: s.Records, SHA256: s.SHA256}); err != nil {
		return nil, err
	}
	return trainingdb.GetTrainingDatasetByFilename(ctx, db, ds.Filename())
}

func infoOf(ds LocalDataset, rec *trainingdb.TrainingDataset) DatasetInfo {
	return DatasetInfo{TrainingDataset: *rec, Manifest: ds.Manifest, Dir: filepath.Base(ds.Dir)}
}

// ListDatasets scans the training datasets directory (DiscoverDatasets) and returns its usable datasets, by name, with their
// records (made or updated here when the file is new or changed), and the problems of the ones that are not usable.
func ListDatasets(ctx context.Context, db *sql.DB, dir string) ([]DatasetInfo, []DatasetProblem, error) {
	found, problems := DiscoverDatasets(dir)
	out := []DatasetInfo{}
	for _, ds := range found {
		s, err := DatasetFileStats(ds.Path)
		if err != nil {
			problems = append(problems, DatasetProblem{Dir: filepath.Base(ds.Dir), Error: err.Error()})
			continue
		}
		rec, err := ensureRecord(ctx, db, ds, s)
		if err != nil {
			log.WithError(err).Error("training: list training datasets failed")
			return nil, nil, internalErr("failed to list the datasets", err)
		}
		out = append(out, infoOf(ds, rec))
	}
	if problems == nil {
		problems = []DatasetProblem{}
	}
	return out, problems, nil
}

// ErrUnknownDataset is returned for a dataset that is not in the training datasets directory.
var ErrUnknownDataset = errors.New("unknown dataset")

// FindDataset returns the dataset called name in the training datasets directory, or ErrUnknownDataset. Other errors are
// safe to show to the user.
func FindDataset(dir, name string) (*LocalDataset, error) {
	if !ValidDatasetName(name) {
		return nil, ErrUnknownDataset
	}
	found, _ := DiscoverDatasets(dir)
	for i := range found {
		if found[i].Name == name {
			return &found[i], nil
		}
	}
	return nil, ErrUnknownDataset
}

// readDatasetFile reads the data file of ds. The errors are safe to show to the user.
func readDatasetFile(ds *LocalDataset, name string) ([]byte, error) {
	content, err := os.ReadFile(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return nil, fmt.Errorf("the data file of dataset %s cannot be read", name)
	}
	if len(content) > MaxDatasetBytes {
		return nil, fmt.Errorf("the data file of dataset %s is over %d MB", name, MaxDatasetBytes>>20)
	}
	return content, nil
}

// LoadTrainingDataset returns the record and the file of the dataset called name, to be sent to the sidecar with a job.
// The record is brought up to date with the file just read. An unknown name gives ErrUnknownDataset. The other errors
// are safe to show to the user.
func LoadTrainingDataset(ctx context.Context, db *sql.DB, dir, name string) (*trainingdb.TrainingDataset, []byte, error) {
	ds, err := FindDataset(dir, name)
	if err != nil {
		return nil, nil, err
	}
	content, err := readDatasetFile(ds, name)
	if err != nil {
		return nil, nil, err
	}
	s, err := ContentStats(content)
	if err != nil {
		return nil, nil, fmt.Errorf("dataset %s: invalid data file: %w", name, err)
	}
	rec, err := ensureRecord(ctx, db, *ds, s)
	if err != nil {
		log.WithError(err).Error("training: record training dataset failed")
		return nil, nil, errors.New("failed to record the dataset")
	}
	return rec, content, nil
}

// uploadedManifestDescription is the description of the manifest an upload writes.
const uploadedManifestDescription = "Uploaded through the training datasets API."

// SaveDataset stores an uploaded dataset: content must be a valid dataset (ValidateDataset) and filename (its directory, if it
// has one, is dropped) a plain *.jsonl name. It becomes the dataset named like the file without ".jsonl": it is saved in the
// subdirectory of that name of dir, next to a manifest that is written if there is none, and recorded in
// gb_training_datasets. Saving a name again replaces the dataset's file. The sidecar is not involved. Errors are *Error:
// KindInvalid for a bad name or file, KindConflict for a dataset of that name that has a manifest of its own, naming another
// data file, or that is in another directory (it is not replaced), KindInternal.
func SaveDataset(ctx context.Context, db *sql.DB, dir, filename string, content []byte) (*DatasetInfo, error) {
	if dir == "" {
		return nil, internalErr("no training dataset directory is configured", nil)
	}
	filename = path.Base(strings.ReplaceAll(filename, `\`, "/")) // some browsers send the full path
	name := strings.TrimSuffix(filename, ".jsonl")
	if !datasetFilenameRe.MatchString(filename) || !ValidDatasetName(name) {
		return nil, invalidErr("the file name must be a plain *.jsonl name (letters, digits, . _ -)")
	}
	stats, err := ContentStats(content)
	if err != nil {
		return nil, invalidErr("invalid dataset: " + err.Error())
	}

	// The dataset's subdirectory: a manifest that is there already must be about this very file.
	dsDir := filepath.Join(dir, name)
	manifestPath := filepath.Join(dsDir, ManifestFilename)
	_, statErr := os.Stat(manifestPath)
	writeManifest := errors.Is(statErr, fs.ErrNotExist)
	if !writeManifest {
		if cur, err := ParseManifest(manifestPath); err != nil || cur.Name != name || cur.Manifest.File != filename {
			return nil, conflictErr("a dataset named " + name + " exists already with its own manifest and data file; upload under another name")
		}
	}
	found, _ := DiscoverDatasets(dir)
	for _, f := range found {
		if f.Name == name && filepath.Base(f.Dir) != name {
			return nil, conflictErr("a dataset named " + name + " exists already in " + filepath.Base(f.Dir))
		}
	}

	// The file is staged first, then recorded, then moved into place: a failure leaves neither a record without its
	// file nor a half-written file.
	ctx = context.WithoutCancel(ctx)
	commit, discard, err := stageDatasetFile(dsDir, filename, content)
	if err != nil {
		log.WithError(err).Error("training: save training dataset file failed")
		return nil, internalErr("failed to save the dataset file", err)
	}
	ds := LocalDataset{Manifest: Manifest{Name: name, Description: uploadedManifestDescription, Format: FormatJSONL, File: filename},
		Dir: dsDir, Path: filepath.Join(dsDir, filename)}
	if _, err := trainingdb.SaveTrainingDataset(ctx, db, trainingdb.TrainingDataset{
		Filename: ds.Filename(), SizeBytes: stats.Size, NumRecords: stats.Records, SHA256: stats.SHA256}); err != nil {
		discard()
		log.WithError(err).Error("training: record training dataset failed")
		return nil, internalErr("failed to record the dataset", err)
	}
	if err := commit(); err != nil {
		discard()
		log.WithError(err).Error("training: save training dataset file failed")
		return nil, internalErr("failed to save the dataset file", err)
	}
	if writeManifest {
		if err := writeUploadManifest(manifestPath, ds.Manifest); err != nil {
			log.WithError(err).Error("training: write training dataset manifest failed")
			return nil, internalErr("failed to save the dataset's manifest", err)
		}
	}
	rec, err := trainingdb.GetTrainingDatasetByFilename(ctx, db, ds.Filename())
	if err != nil {
		log.WithError(err).Error("training: read training dataset failed")
		return nil, internalErr("failed to read the dataset's record", err)
	}
	info := infoOf(ds, rec)
	return &info, nil
}

// writeUploadManifest creates the manifest file of an uploaded dataset (it must not exist).
func writeUploadManifest(path string, m Manifest) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
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

// Limits of the dataset preview: the lines returned, and the bytes read from the file to find them.
const (
	PreviewDefaultLines = 100
	PreviewMaxLines     = 1000
	previewReadBytes    = 4 << 20
)

// Preview is the beginning of a dataset's data file.
type Preview struct {
	Name       string   `json:"name"`
	Lines      []string `json:"lines"`
	NumRecords int      `json:"num_records"`
	// Truncated is true if the file has more lines than returned.
	Truncated bool `json:"truncated"`
}

// PreviewDataset returns the first limit lines of the data file of the dataset called name (the name must be valid, see
// ValidDatasetName), so it can be inspected. Only the beginning of the file is read. Errors are *Error: KindNotFound for an
// unknown dataset or a data file that cannot be read, KindInternal.
func PreviewDataset(dir, name string, limit int) (*Preview, error) {
	ds, err := FindDataset(dir, name)
	if err != nil {
		return nil, notFoundErr("unknown dataset")
	}
	stats, err := DatasetFileStats(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return nil, &Error{KindNotFound, fmt.Sprintf("the data file of dataset %s cannot be read", name), err}
	}
	f, err := os.Open(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: open training dataset file failed")
		return nil, &Error{KindNotFound, fmt.Sprintf("the data file of dataset %s cannot be read", name), err}
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, previewReadBytes+1))
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return nil, internalErr("failed to read the dataset file", err)
	}
	cut := len(buf) > previewReadBytes // the file goes on past what was read
	if cut {
		buf = buf[:previewReadBytes]
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			buf = buf[:i+1] // drop the partial last line
		}
	}
	lines := []string{}
	truncated := cut
	for len(buf) > 0 {
		if len(lines) == limit {
			truncated = true
			break
		}
		var line []byte
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			line, buf = buf[:i], buf[i+1:]
		} else {
			line, buf = buf, nil
		}
		lines = append(lines, strings.ToValidUTF8(strings.TrimSuffix(string(line), "\r"), "\uFFFD"))
	}
	return &Preview{Name: ds.Name, Lines: lines, NumRecords: stats.Records, Truncated: truncated}, nil
}
