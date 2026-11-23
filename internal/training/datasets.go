package training

// The training datasets: validating, listing, finding, storing (an upload) and previewing them. Where they are is manifest.go.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

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
				return 0, fmt.Errorf("line %d has no %q",
					i+1,
					k)
			}
			var s string
			if json.Unmarshal(raw, &s) != nil || bytes.Equal(raw, []byte("null")) {
				return 0, fmt.Errorf("line %d: %q must be a string",
					i+1,
					k)
			}
		}
		n++
	}
	if n == 0 {
		return 0, errors.New("the file has no records")
	}
	return n, nil
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
func ensureRecord(ctx context.Context,
	db *sql.DB,
	ds LocalDataset,
	s FileStats) (*trainingdb.TrainingDataset, error) {
	rec, err := trainingdb.GetTrainingDatasetByFilename(ctx,
		db,
		ds.Filename())
	if err == nil && rec.SHA256 == s.SHA256 && rec.SizeBytes == s.Size && rec.NumRecords == s.Records {
		return rec, nil
	}
	if err != nil && !errors.Is(err, relational.ErrNotFound) {
		return nil, err
	}
	if _, err := trainingdb.SaveTrainingDataset(ctx,
		db,
		trainingdb.TrainingDataset{
			Filename:   ds.Filename(),
			SizeBytes:  s.Size,
			NumRecords: s.Records,
			SHA256:     s.SHA256,
		}); err != nil {
		return nil, err
	}
	return trainingdb.GetTrainingDatasetByFilename(ctx,
		db,
		ds.Filename())
}

func infoOf(ds LocalDataset,
	rec *trainingdb.TrainingDataset) DatasetInfo {
	return DatasetInfo{
		TrainingDataset: *rec,
		Manifest:        ds.Manifest,
		Dir:             filepath.Base(ds.Dir),
	}
}

// ListDatasets scans the training datasets store (DatasetStore.Discover) and returns its usable datasets, by name, with their
// records (made or updated here when the file is new or changed), and the problems of the ones that are not usable.
func ListDatasets(ctx context.Context,
	db *sql.DB,
	store DatasetStore) ([]DatasetInfo, []DatasetProblem, error) {
	found, problems := store.Discover(ctx)
	out := []DatasetInfo{}
	for _, ds := range found {
		s, err := store.Stats(ctx, ds)
		if err != nil {
			problems = append(problems, DatasetProblem{
				Dir:   store.ProblemDir(ds),
				Error: err.Error(),
			})
			continue
		}
		rec, err := ensureRecord(ctx,
			db,
			ds,
			s)
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

// ErrUnknownDataset is returned for a dataset that is not in the training datasets directories.
var ErrUnknownDataset = errors.New("unknown dataset")

// FindDataset returns the dataset called name in the training datasets store (in the directories, the first one that has it), or
// ErrUnknownDataset. Other errors are safe to show to the user.
func FindDataset(ctx context.Context,
	store DatasetStore,
	name string) (*LocalDataset, error) {
	if !ValidDatasetName(name) {
		return nil, ErrUnknownDataset
	}
	found, _ := store.Discover(ctx)
	for i := range found {
		if found[i].Name == name {
			return &found[i], nil
		}
	}
	return nil, ErrUnknownDataset
}

// ReadDataset returns the data file of ds. The errors are safe to show to the user.
func ReadDataset(ctx context.Context,
	store DatasetStore,
	ds *LocalDataset) ([]byte, error) {
	return readDatasetFile(ctx,
		store,
		ds)
}

// LoadTrainingDataset returns the record and the file of the dataset called name, to be sent to the sidecar with a job.
// The record is brought up to date with the file just read. An unknown name gives ErrUnknownDataset. The other errors
// are safe to show to the user.
func LoadTrainingDataset(ctx context.Context,
	db *sql.DB,
	store DatasetStore,
	name string) (*trainingdb.TrainingDataset, []byte, error) {
	ds, err := FindDataset(ctx,
		store,
		name)
	if err != nil {
		return nil, nil, err
	}
	content, err := readDatasetFile(ctx,
		store,
		ds)
	if err != nil {
		return nil, nil, err
	}
	s, err := ContentStats(content)
	if err != nil {
		return nil, nil, fmt.Errorf("dataset %s: invalid data file: %w",
			name,
			err)
	}
	rec, err := ensureRecord(ctx,
		db,
		*ds,
		s)
	if err != nil {
		log.WithError(err).Error("training: record training dataset failed")
		return nil, nil, errors.New("failed to record the dataset")
	}
	return rec, content, nil
}

// SaveDataset stores an uploaded dataset: content must be a valid dataset (ValidateDataset) and filename (its directory, if it
// has one, is dropped) a plain *.jsonl name. It becomes the dataset named like the file without ".jsonl": it is saved in the store
// (DatasetStore.Stage: in the directories, the subdirectory of that name of the first one; in S3, the "directory" of that name),
// next to a manifest that is written if there is none, and recorded in gb_training_datasets. Saving a name again replaces the
// dataset's file. The sidecar is not involved. Errors are *Error: KindInvalid for a bad name or file, KindConflict for a dataset of
// that name that has a manifest of its own, naming another data file, or that is somewhere else (it is not replaced), KindInternal.
func SaveDataset(ctx context.Context,
	db *sql.DB,
	store DatasetStore,
	filename string,
	content []byte) (*DatasetInfo, error) {
	filename = path.Base(strings.ReplaceAll(filename,
		`\`,
		"/")) // some browsers send the full path
	name := strings.TrimSuffix(filename, ".jsonl")
	if !datasetFilenameRe.MatchString(filename) || !ValidDatasetName(name) {
		return nil, invalidErr("the file name must be a plain *.jsonl name (letters, digits, . _ -)")
	}
	stats, err := ContentStats(content)
	if err != nil {
		return nil, invalidErr("invalid dataset: " + err.Error())
	}

	// The file is staged first, then recorded, then written: a failure leaves neither a record without its file nor a
	// half-written file.
	ctx = context.WithoutCancel(ctx)
	ds, commit, discard, err := store.Stage(ctx,
		name,
		filename,
		content)
	if err != nil {
		return nil, err
	}
	if _, err := trainingdb.SaveTrainingDataset(ctx,
		db,
		trainingdb.TrainingDataset{
			Filename:   ds.Filename(),
			SizeBytes:  stats.Size,
			NumRecords: stats.Records,
			SHA256:     stats.SHA256,
		}); err != nil {
		discard()
		log.WithError(err).Error("training: record training dataset failed")
		return nil, internalErr("failed to record the dataset", err)
	}
	if err := commit(); err != nil {
		discard()
		return nil, err
	}
	rec, err := trainingdb.GetTrainingDatasetByFilename(ctx,
		db,
		ds.Filename())
	if err != nil {
		log.WithError(err).Error("training: read training dataset failed")
		return nil, internalErr("failed to read the dataset's record", err)
	}
	info := infoOf(ds, rec)
	return &info, nil
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
func PreviewDataset(ctx context.Context,
	store DatasetStore,
	name string,
	limit int) (*Preview, error) {
	ds, err := FindDataset(ctx,
		store,
		name)
	if err != nil {
		return nil, notFoundErr("unknown dataset")
	}
	stats, err := store.Stats(ctx, *ds)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return nil, &Error{KindNotFound, fmt.Sprintf("the data file of dataset %s cannot be read", name), err}
	}
	buf, err := store.ReadHead(ctx,
		*ds,
		previewReadBytes)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return nil, &Error{KindNotFound, fmt.Sprintf("the data file of dataset %s cannot be read", name), err}
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
	return &Preview{
		Name:       ds.Name,
		Lines:      lines,
		NumRecords: stats.Records,
		Truncated:  truncated,
	}, nil
}
