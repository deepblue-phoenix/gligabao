package training

// Training datasets: a JSONL file uploaded through the Go server is validated, saved as a file in the
// training dataset directory and recorded in the gb_training_datasets table. Datasets are managed by the Go server alone:
// the sidecar has no dataset endpoints. The file travels to the sidecar inside the request that starts a training job on
// it (see loadTrainingDataset); the sidecar stores it internally.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	log "github.com/sirupsen/logrus"
)

// MaxDatasetBytes is the largest dataset file the upload accepts.
const MaxDatasetBytes = 64 << 20

// datasetFilenameRe is what the sidecar accepts as a dataset's file name (sidecar/ai_train/ai_train_core/dataset.py).
var datasetFilenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.jsonl$`)

// ValidateDataset checks that content is a JSONL training dataset, the format the training reads (see
// docs/training/TRAINING_DATA_FORMAT.md): UTF-8, every non-blank line a JSON object with string "instruction" and "output"
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
// function that removes the temp file if the caller does not go on, and one that completes the write (the rename).
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

// UploadDatasetOf serves POST /v1/gb/training/datasets[?db=<name>], a multipart form with the JSONL file in the
// field "file" (and optionally "filename", which overrides the uploaded file's name). The file must be a valid dataset
// (ValidateDataset) with a plain *.jsonl name. Go saves it in the training dataset directory of one of dbs (the
// first without db; Database.DatasetDir) and records it in gb_training_datasets of that database; uploading a name
// again replaces the dataset. The sidecar is not involved. It answers 201 with the record (relational.TrainingDataset). A
// bad file answers 400, a file over MaxDatasetBytes 413.
func UploadDatasetOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		if n.DatasetDir == "" {
			httputil.Error(w, http.StatusInternalServerError, "no training dataset directory is configured")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxDatasetBytes+1<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				httputil.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the dataset is over %d MB", MaxDatasetBytes>>20))
				return
			}
			httputil.Error(w, http.StatusBadRequest, "expected a multipart form with the JSONL file in the field \"file\"")
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, hdr, err := r.FormFile("file")
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, "the form has no \"file\" field")
			return
		}
		defer file.Close()
		if hdr.Size > MaxDatasetBytes {
			httputil.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the dataset is over %d MB", MaxDatasetBytes>>20))
			return
		}
		name := strings.TrimSpace(r.FormValue("filename"))
		if name == "" {
			name = hdr.Filename
		}
		name = path.Base(strings.ReplaceAll(name, `\`, "/")) // some browsers send the full path
		if !datasetFilenameRe.MatchString(name) || strings.Contains(name, "..") {
			httputil.Error(w, http.StatusBadRequest, "the file name must be a plain *.jsonl name (letters, digits, . _ -)")
			return
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(file); err != nil {
			httputil.Error(w, http.StatusBadRequest, "failed to read the file")
			return
		}
		content := buf.Bytes()
		records, err := ValidateDataset(content)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid dataset: "+err.Error())
			return
		}

		// The file is staged first, then recorded, then moved into place: a failure leaves neither a record without its
		// file nor a half-written file.
		ctx := context.WithoutCancel(r.Context())
		commit, discard, err := stageDatasetFile(n.DatasetDir, name, content)
		if err != nil {
			log.WithError(err).Error("training: save training dataset file failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to save the dataset file")
			return
		}
		sum := sha256.Sum256(content)
		if _, err := relational.SaveTrainingDataset(ctx, n.DB, relational.TrainingDataset{
			Filename: name, SizeBytes: int64(len(content)), NumRecords: records, SHA256: hex.EncodeToString(sum[:])}); err != nil {
			discard()
			log.WithError(err).Error("training: record training dataset failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to record the dataset")
			return
		}
		if err := commit(); err != nil {
			discard()
			log.WithError(err).Error("training: save training dataset file failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to save the dataset file")
			return
		}
		ds, err := relational.GetTrainingDatasetByFilename(ctx, n.DB, name)
		if err != nil {
			log.WithError(err).Error("training: read training dataset failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the dataset's record")
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, ds)
	}
}

var errUnknownDataset = errors.New("unknown dataset")

// loadTrainingDataset returns the record and the file of the dataset named filename, to be sent to the sidecar with a
// job. The file is read from the dataset directory and checked against the record's digest. An unknown name gives
// errUnknownDataset. The other errors are safe to show to the user.
func loadTrainingDataset(ctx context.Context, n Database, filename string) (*relational.TrainingDataset, []byte, error) {
	ds, err := relational.GetTrainingDatasetByFilename(ctx, n.DB, filename)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, nil, errUnknownDataset
	} else if err != nil {
		log.WithError(err).Error("training: read training dataset failed")
		return nil, nil, errors.New("failed to read the dataset's record")
	}
	content, err := os.ReadFile(filepath.Join(n.DatasetDir, ds.Filename))
	if err != nil {
		log.WithError(err).WithField("filename", filename).Error("training: read training dataset file failed")
		return nil, nil, fmt.Errorf("the file of dataset %s is missing; upload it again", filename)
	}
	if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != ds.SHA256 {
		return nil, nil, fmt.Errorf("the file of dataset %s does not match its record; upload it again", filename)
	}
	return ds, content, nil
}

// ListDatasetsOf serves GET /v1/gb/training/datasets[?db=<name>]: {"datasets": [...]}, the training datasets
// (relational.TrainingDataset) of the database, the latest upload first. It never asks the sidecar.
func ListDatasetsOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		ds, err := relational.ListTrainingDatasets(r.Context(), n.DB)
		if err != nil {
			log.WithError(err).Error("training: list training dataset records failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the datasets")
			return
		}
		if ds == nil {
			ds = []relational.TrainingDataset{}
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"datasets": ds})
	}
}
