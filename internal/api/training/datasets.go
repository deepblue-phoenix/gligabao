package training

// Training datasets: the datasets are what the training datasets directory holds (manifest.go: a subdirectory with a
// dataset.toml each). The server reads the manifests every time it lists the datasets or starts a job, so a dataset
// added to the directory by hand is available at once; a JSONL file uploaded through the API (UploadDatasetOf) is saved
// there as a dataset of its own. Each dataset has a record in the gb_training_datasets table (its size, records, digest
// and whether the sidecar has this version of the file), made when the server first sees the file or sees it changed.
// The sidecar has no dataset endpoints: the file travels to it inside the request that starts a training job on it (see
// loadTrainingDataset); the sidecar stores it internally.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// MaxDatasetBytes is the largest dataset file there can be (the upload refuses a bigger one, a manifest naming one is a problem).
const MaxDatasetBytes = 64 << 20

// datasetFilenameRe is what an uploaded file's name must be (the dataset gets its name without .jsonl; see datasetNameRe).
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
func ensureRecord(ctx context.Context, n Database, ds LocalDataset, s fileStats) (*trainingdb.TrainingDataset, error) {
	rec, err := trainingdb.GetTrainingDatasetByFilename(ctx, n.DB, ds.Filename())
	if err == nil && rec.SHA256 == s.sha256 && rec.SizeBytes == s.size && rec.NumRecords == s.records {
		return rec, nil
	}
	if err != nil && !errors.Is(err, relational.ErrNotFound) {
		return nil, err
	}
	if _, err := trainingdb.SaveTrainingDataset(ctx, n.DB, trainingdb.TrainingDataset{
		Filename: ds.Filename(), SizeBytes: s.size, NumRecords: s.records, SHA256: s.sha256}); err != nil {
		return nil, err
	}
	return trainingdb.GetTrainingDatasetByFilename(ctx, n.DB, ds.Filename())
}

func infoOf(ds LocalDataset, rec *trainingdb.TrainingDataset) DatasetInfo {
	return DatasetInfo{TrainingDataset: *rec, Manifest: ds.Manifest, Dir: filepath.Base(ds.Dir)}
}

// listDatasets scans n's training datasets directory (DiscoverDatasets) and returns its usable datasets, by name, with their
// records (made or updated here when the file is new or changed), and the problems of the ones that are not usable.
func listDatasets(ctx context.Context, n Database) ([]DatasetInfo, []DatasetProblem, error) {
	found, problems := DiscoverDatasets(n.DatasetDir)
	out := []DatasetInfo{}
	for _, ds := range found {
		s, err := datasetFileStats(ds.Path)
		if err != nil {
			problems = append(problems, DatasetProblem{Dir: filepath.Base(ds.Dir), Error: err.Error()})
			continue
		}
		rec, err := ensureRecord(ctx, n, ds, s)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, infoOf(ds, rec))
	}
	return out, problems, nil
}

var errUnknownDataset = errors.New("unknown dataset")

// findDataset returns the dataset called name in n's training datasets directory, or errUnknownDataset. Other errors are
// safe to show to the user.
func findDataset(n Database, name string) (*LocalDataset, error) {
	if !validDatasetName(name) {
		return nil, errUnknownDataset
	}
	found, _ := DiscoverDatasets(n.DatasetDir)
	for i := range found {
		if found[i].Name == name {
			return &found[i], nil
		}
	}
	return nil, errUnknownDataset
}

// loadTrainingDataset returns the record and the file of the dataset called name, to be sent to the sidecar with a job.
// The record is brought up to date with the file just read. An unknown name gives errUnknownDataset. The other errors
// are safe to show to the user.
func loadTrainingDataset(ctx context.Context, n Database, name string) (*trainingdb.TrainingDataset, []byte, error) {
	ds, err := findDataset(n, name)
	if err != nil {
		return nil, nil, err
	}
	content, err := os.ReadFile(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return nil, nil, fmt.Errorf("the data file of dataset %s cannot be read", name)
	}
	if len(content) > MaxDatasetBytes {
		return nil, nil, fmt.Errorf("the data file of dataset %s is over %d MB", name, MaxDatasetBytes>>20)
	}
	s, err := contentStats(content)
	if err != nil {
		return nil, nil, fmt.Errorf("dataset %s: invalid data file: %w", name, err)
	}
	rec, err := ensureRecord(ctx, n, *ds, s)
	if err != nil {
		log.WithError(err).Error("training: record training dataset failed")
		return nil, nil, errors.New("failed to record the dataset")
	}
	return rec, content, nil
}

// uploadedManifestDescription is the description of the manifest an upload writes.
const uploadedManifestDescription = "Uploaded through the training datasets API."

// UploadDatasetOf serves POST /v1/gb/training/datasets[?db=<name>], a multipart form with the JSONL file in the
// field "file" (and optionally "filename", which overrides the uploaded file's name). The file must be a valid dataset
// (ValidateDataset) with a plain *.jsonl name. It becomes the dataset named like the file without ".jsonl": Go saves it
// in the subdirectory of that name of the training datasets directory of one of dbs (the first without db; Database.DatasetDir),
// next to a manifest it writes if there is none, and records it in gb_training_datasets of that database. Uploading a
// name again replaces the dataset's file. A dataset of that name that has a manifest of its own, naming another data file,
// or a dataset of that name in another directory, is not replaced: 409. The sidecar is not involved. It answers 201
// with the dataset (DatasetInfo). A bad file answers 400, a file over MaxDatasetBytes 413.
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
		filename := strings.TrimSpace(r.FormValue("filename"))
		if filename == "" {
			filename = hdr.Filename
		}
		filename = path.Base(strings.ReplaceAll(filename, `\`, "/")) // some browsers send the full path
		name := strings.TrimSuffix(filename, ".jsonl")
		if !datasetFilenameRe.MatchString(filename) || !validDatasetName(name) {
			httputil.Error(w, http.StatusBadRequest, "the file name must be a plain *.jsonl name (letters, digits, . _ -)")
			return
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(file); err != nil {
			httputil.Error(w, http.StatusBadRequest, "failed to read the file")
			return
		}
		content := buf.Bytes()
		stats, err := contentStats(content)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid dataset: "+err.Error())
			return
		}

		// The dataset's subdirectory: a manifest that is there already must be about this very file.
		dsDir := filepath.Join(n.DatasetDir, name)
		manifestPath := filepath.Join(dsDir, ManifestFilename)
		_, statErr := os.Stat(manifestPath)
		writeManifest := errors.Is(statErr, fs.ErrNotExist)
		if !writeManifest {
			if cur, err := parseManifest(manifestPath); err != nil || cur.Name != name || cur.Manifest.File != filename {
				httputil.Error(w, http.StatusConflict, "a dataset named "+name+" exists already with its own manifest and data file; upload under another name")
				return
			}
		}
		found, _ := DiscoverDatasets(n.DatasetDir)
		for _, f := range found {
			if f.Name == name && filepath.Base(f.Dir) != name {
				httputil.Error(w, http.StatusConflict, "a dataset named "+name+" exists already in "+filepath.Base(f.Dir))
				return
			}
		}

		// The file is staged first, then recorded, then moved into place: a failure leaves neither a record without its
		// file nor a half-written file.
		ctx := context.WithoutCancel(r.Context())
		commit, discard, err := stageDatasetFile(dsDir, filename, content)
		if err != nil {
			log.WithError(err).Error("training: save training dataset file failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to save the dataset file")
			return
		}
		ds := LocalDataset{Manifest: Manifest{Name: name, Description: uploadedManifestDescription, Format: FormatJSONL, File: filename},
			Dir: dsDir, Path: filepath.Join(dsDir, filename)}
		if _, err := trainingdb.SaveTrainingDataset(ctx, n.DB, trainingdb.TrainingDataset{
			Filename: ds.Filename(), SizeBytes: stats.size, NumRecords: stats.records, SHA256: stats.sha256}); err != nil {
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
		if writeManifest {
			if err := writeUploadManifest(manifestPath, ds.Manifest); err != nil {
				log.WithError(err).Error("training: write training dataset manifest failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to save the dataset's manifest")
				return
			}
		}
		rec, err := trainingdb.GetTrainingDatasetByFilename(ctx, n.DB, ds.Filename())
		if err != nil {
			log.WithError(err).Error("training: read training dataset failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the dataset's record")
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, infoOf(ds, rec))
	}
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

// ListDatasetsOf serves GET /v1/gb/training/datasets[?db=<name>]: {"datasets": [...], "problems": [...]}, the training
// datasets (DatasetInfo) found in the training datasets directory by their manifests, by name, and the subdirectories with a
// manifest that are not usable (DatasetProblem). It never asks the sidecar.
func ListDatasetsOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		ds, problems, err := listDatasets(r.Context(), n)
		if err != nil {
			log.WithError(err).Error("training: list training datasets failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the datasets")
			return
		}
		if problems == nil {
			problems = []DatasetProblem{}
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"datasets": ds, "problems": problems})
	}
}

// Limits of the dataset preview: the lines returned, and the bytes read from the file to find them.
const (
	previewDefaultLines = 100
	previewMaxLines     = 1000
	previewReadBytes    = 4 << 20
)

// PreviewDatasetOf serves GET /v1/gb/training/datasets/preview?name=<dataset>[&lines=<n>][&db=<name>]: the first lines
// (default 100, at most 1000) of a dataset's data file, so it can be inspected: {"name", "lines": [...], "num_records",
// "truncated"}. truncated is true if the file has more lines than returned. Only the beginning of the file is read. An
// unknown dataset answers 404.
func PreviewDatasetOf(dbs []Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		name := q.Get("name")
		if !validDatasetName(name) {
			httputil.Error(w, http.StatusBadRequest, "invalid dataset name")
			return
		}
		limit := previewDefaultLines
		if v := q.Get("lines"); v != "" {
			l, err := strconv.Atoi(v)
			if err != nil || l < 1 || l > previewMaxLines {
				httputil.Error(w, http.StatusBadRequest, fmt.Sprintf("lines must be between 1 and %d", previewMaxLines))
				return
			}
			limit = l
		}
		ds, err := findDataset(n, name)
		if errors.Is(err, errUnknownDataset) {
			httputil.Error(w, http.StatusNotFound, "unknown dataset")
			return
		}
		stats, err := datasetFileStats(ds.Path)
		if err != nil {
			log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
			httputil.Error(w, http.StatusNotFound, fmt.Sprintf("the data file of dataset %s cannot be read", name))
			return
		}
		f, err := os.Open(ds.Path)
		if err != nil {
			log.WithError(err).WithField("dataset", name).Error("training: open training dataset file failed")
			httputil.Error(w, http.StatusNotFound, fmt.Sprintf("the data file of dataset %s cannot be read", name))
			return
		}
		defer f.Close()
		buf, err := io.ReadAll(io.LimitReader(f, previewReadBytes+1))
		if err != nil {
			log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the dataset file")
			return
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
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"name": ds.Name, "lines": lines, "num_records": stats.records, "truncated": truncated})
	}
}
