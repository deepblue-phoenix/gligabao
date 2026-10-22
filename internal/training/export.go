package training

// Exporting a trained model: handing its weights to an exporter, a hook supplied by the program that embeds gligabao
// (ServeOptions.TrainedModelExporters). gligabao fetches the model's files from the sidecar's storage (the Modal models
// volume) to a directory on the local disk and calls the exporter with it; what the exporter does with the files (upload them
// to a third party, copy them, convert them, ...) is its own business. Every export is recorded (gb_trained_model_exports).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// ErrInvalidExporter means a registered exporter is malformed.
var ErrInvalidExporter = errors.New("invalid trained model exporter")

// The limits of an exporter.
const (
	MaxExporterNameLen = 64
	MaxExporters       = 20
	// FetchModelTimeout bounds fetching all the files of a model from the sidecar.
	FetchModelTimeout = 30 * time.Minute
	// MaxExportOptionsBytes is the largest JSON a request may give an exporter as its options.
	MaxExportOptionsBytes = 1 << 16
)

var exporterNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ExportTrainedModelRequest is what an exporter is asked to export.
type ExportTrainedModelRequest struct {
	// ExportID is the ID of this export (gb_trained_model_exports.id): unique, so an exporter can use it to name what it
	// creates.
	ExportID int64
	// Model is the trained model: its base model, size, dataset, training settings and files (with their sha256).
	Model trainingdb.TrainedModel
	// Dir is the absolute path of a local directory with the model's files (Model.Files, at the paths they list), already
	// verified against their sizes and sha256. gligabao never deletes it: the same directory is reused by the next export of
	// the model. The exporter must not modify or delete the files in it.
	Dir string
	// Options are the free-form options of the request that asked for the export (may be nil).
	Options map[string]any
}

// ExportTrainedModelResult is what an exporter reports when it is done.
type ExportTrainedModelResult struct {
	// Ref identifies what the export made, in the exporter's terms (e.g. the name of the model it created elsewhere).
	Ref string
	// Details is anything else worth recording; it must be JSON-encodable (may be nil).
	Details map[string]any
}

// ExportTrainedModelFunc exports one trained model. It runs synchronously: the request that asked for the export waits for
// it. It must honour ctx and may be called concurrently. A returned error (or a panic) marks the export failed.
type ExportTrainedModelFunc func(ctx context.Context, req ExportTrainedModelRequest) (ExportTrainedModelResult, error)

// TrainedModelExporter is a named way of exporting trained models.
type TrainedModelExporter struct {
	// Name identifies the exporter: letters, digits, '_', '-' and '.', starting with a letter or digit, unique. It is what
	// an export request names, and is stored with the export.
	Name string
	// Export does the exporting.
	Export ExportTrainedModelFunc
}

// ResolveExporters validates the exporters registered at startup (ErrInvalidExporter, with what is wrong, if one is
// malformed) and returns them sorted by name.
func ResolveExporters(list []TrainedModelExporter) ([]TrainedModelExporter, error) {
	if len(list) > MaxExporters {
		return nil, fmt.Errorf("%w: at most %d exporters may be registered, got %d", ErrInvalidExporter, MaxExporters, len(list))
	}
	seen := map[string]bool{}
	out := make([]TrainedModelExporter, 0, len(list))
	for i, e := range list {
		switch {
		case e.Name == "" || len(e.Name) > MaxExporterNameLen || !exporterNameRE.MatchString(e.Name):
			return nil, fmt.Errorf("%w: exporter %d: name %q must be 1-%d letters, digits, '_', '-' or '.', starting with a letter or digit", ErrInvalidExporter, i, e.Name, MaxExporterNameLen)
		case seen[e.Name]:
			return nil, fmt.Errorf("%w: duplicate exporter name %q", ErrInvalidExporter, e.Name)
		case e.Export == nil:
			return nil, fmt.Errorf("%w: exporter %q has no Export function", ErrInvalidExporter, e.Name)
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ModelFileFetcher reads the files of a trained model from where the sidecar keeps them; *sidecar.Client is one.
type ModelFileFetcher interface {
	DownloadModelFile(ctx context.Context, modelPath, file string, w io.Writer) (int64, error)
}

// ModelDir is the directory under root the files of trained model tm are fetched to: one directory per job, named after it.
func ModelDir(root string, tm *trainingdb.TrainedModel) (string, error) {
	if !exporterNameRE.MatchString(tm.JobID) || strings.ContainsAny(tm.JobID, `/\`) {
		return "", fmt.Errorf("job id %q is not usable as a directory name", tm.JobID)
	}
	abs, err := filepath.Abs(filepath.Join(root, tm.JobID))
	if err != nil {
		return "", err
	}
	return abs, nil
}

// fetchLocks serialize the fetches into one directory, so two exports of a model do not download it twice at the same time.
var fetchLocks sync.Map // dir -> *sync.Mutex

// FetchModelFiles makes sure dir holds the files of trained model tm, at the paths tm.Files lists, each with the recorded size and
// sha256 (a file that is already there and matches is kept, the others are downloaded through f and verified). A file is
// written under a temporary name and renamed when it is complete, so dir never holds a half-written file under its real name.
func FetchModelFiles(ctx context.Context, f ModelFileFetcher, tm *trainingdb.TrainedModel, dir string) error {
	if len(tm.Files) == 0 {
		return errors.New("the trained model has no recorded files")
	}
	mu, _ := fetchLocks.LoadOrStore(dir, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	for _, file := range tm.Files {
		if !filepath.IsLocal(filepath.FromSlash(file.Path)) || strings.Contains(file.Path, `\`) {
			return fmt.Errorf("file path %q is not a relative path inside the model's directory", file.Path)
		}
		target := filepath.Join(dir, filepath.FromSlash(file.Path))
		if fileMatches(target, file) {
			continue
		}
		if err := fetchFile(ctx, f, tm.Path, file, target); err != nil {
			return fmt.Errorf("fetch %s: %w", file.Path, err)
		}
	}
	return nil
}

// fileMatches reports whether path is a file of the size and sha256 of want (the sha256 is not checked when none is recorded).
func fileMatches(path string, want trainingdb.TrainedModelFile) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() != want.SizeBytes {
		return false
	}
	if want.SHA256 == "" {
		return true
	}
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want.SHA256)
}

func fetchFile(ctx context.Context, f ModelFileFetcher, modelPath string, file trainingdb.TrainedModelFile, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	h := sha256.New()
	n, err := f.DownloadModelFile(ctx, modelPath, file.Path, io.MultiWriter(tmp, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != file.SizeBytes {
		return fmt.Errorf("got %d bytes, the model records %d", n, file.SizeBytes)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); file.SHA256 != "" && !strings.EqualFold(sum, file.SHA256) {
		return fmt.Errorf("sha256 is %s, the model records %s", sum, file.SHA256)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// ExportRequest is what the caller of ExportModel chooses.
type ExportRequest struct {
	// Exporter is the name of the exporter to use.
	Exporter string
	// Options are passed to the exporter as they are.
	Options map[string]any
}

// ExportModel exports trained model tm through the exporter named by in: it fetches the model's files from the sidecar (f) into
// its directory under dir (see ModelDir), calls the exporter with it and waits for it, and records the export
// (gb_trained_model_exports) with its outcome, a failure included. It returns the stored record, whose status is
// "completed". Errors are *Error: KindInvalid for an unknown exporter or bad options, KindUpstream when fetching the files or
// the exporter fails (the export is recorded as failed), KindInternal. A system event training.model_export.completed or
// .failed is emitted at the end.
func ExportModel(ctx context.Context, db *sql.DB, f ModelFileFetcher, exporters []TrainedModelExporter, dir string, tm *trainingdb.TrainedModel, in ExportRequest) (*trainingdb.TrainedModelExport, error) {
	exp, err := findExporter(exporters, in.Exporter)
	if err != nil {
		return nil, err
	}
	optionsJSON := json.RawMessage("{}")
	if in.Options != nil {
		b, err := json.Marshal(in.Options)
		if err != nil || len(b) > MaxExportOptionsBytes {
			return nil, invalidErr(fmt.Sprintf("options must be a JSON object of at most %d bytes", MaxExportOptionsBytes))
		}
		optionsJSON = b
	}
	modelDir, err := ModelDir(dir, tm)
	if err != nil {
		return nil, invalidErr("the model cannot be exported: " + err.Error())
	}

	dbCtx := context.WithoutCancel(ctx) // the writes must outlive the request
	l := log.WithFields(log.Fields{"model_id": tm.ID, "exporter": exp.Name})
	id, err := trainingdb.RecordTrainedModelExport(dbCtx, db, trainingdb.TrainedModelExport{TrainedModelID: tm.ID, Exporter: exp.Name, Options: optionsJSON})
	if err != nil {
		l.WithError(err).Error("training: recording the export failed")
		return nil, internalErr("failed to record the export", err)
	}
	l = l.WithField("export_id", id)
	started := time.Now()
	fail := func(msg string, cause error) error {
		l.WithError(cause).Error("training: model export failed")
		ms := time.Since(started).Milliseconds()
		if err := trainingdb.FinishTrainedModelExport(dbCtx, db, id, trainingdb.TrainedModelExportFailed, "", "", msg, ms); err != nil {
			l.WithError(err).Error("training: updating the export failed")
		}
		monitoring.EmitSystemEvent(dbCtx, "training.model_export.failed", monitoring.Event{
			"model_id": tm.ID, "export_id": id, "exporter": exp.Name, "duration_ms": ms, "error": msg})
		return &Error{KindUpstream, fmt.Sprintf("export %d failed: %s", id, msg), cause}
	}

	fctx, cancel := context.WithTimeout(ctx, FetchModelTimeout)
	err = FetchModelFiles(fctx, f, tm, modelDir)
	cancel()
	if err != nil {
		return nil, fail("fetching the model's files failed: "+err.Error(), err)
	}
	if err := trainingdb.SetTrainedModelExportDir(dbCtx, db, id, modelDir); err != nil {
		l.WithError(err).Error("training: updating the export failed")
	}

	res, err := callExporter(ctx, exp, ExportTrainedModelRequest{ExportID: id, Model: *tm, Dir: modelDir, Options: in.Options})
	if err != nil {
		return nil, fail(err.Error(), err)
	}
	details := "{}"
	if res.Details != nil {
		b, err := json.Marshal(res.Details)
		if err != nil {
			return nil, fail("the exporter's details are not JSON-encodable: "+err.Error(), err)
		}
		details = string(b)
	}
	ms := time.Since(started).Milliseconds()
	if err := trainingdb.FinishTrainedModelExport(dbCtx, db, id, trainingdb.TrainedModelExportCompleted, res.Ref, details, "", ms); err != nil {
		l.WithError(err).Error("training: updating the export failed")
		return nil, internalErr("the export completed but failed to record it", err)
	}
	monitoring.EmitSystemEvent(dbCtx, "training.model_export.completed", monitoring.Event{
		"model_id": tm.ID, "export_id": id, "exporter": exp.Name, "duration_ms": ms})
	l.WithFields(log.Fields{"ref": res.Ref, "duration_ms": ms}).Info("training: trained model exported")
	out, err := trainingdb.GetTrainedModelExport(dbCtx, db, id)
	if err != nil {
		return nil, internalErr("failed to read the export", err)
	}
	return out, nil
}

func findExporter(exporters []TrainedModelExporter, name string) (TrainedModelExporter, error) {
	if len(exporters) == 0 {
		return TrainedModelExporter{}, invalidErr("no trained model exporter is registered")
	}
	names := make([]string, 0, len(exporters))
	for _, e := range exporters {
		if e.Name == name {
			return e, nil
		}
		names = append(names, e.Name)
	}
	if name == "" {
		return TrainedModelExporter{}, invalidErr("exporter is required: one of " + strings.Join(names, ", "))
	}
	return TrainedModelExporter{}, invalidErr(fmt.Sprintf("unknown exporter %q: one of %s", name, strings.Join(names, ", ")))
}

// callExporter runs the exporter; a panic is returned as an error.
func callExporter(ctx context.Context, exp TrainedModelExporter, req ExportTrainedModelRequest) (res ExportTrainedModelResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("exporter %q panicked: %v", exp.Name, r)
		}
	}()
	res, err = exp.Export(ctx, req)
	if err != nil {
		err = fmt.Errorf("exporter %q failed: %w", exp.Name, err)
	}
	return res, err
}

// ListExports reads the exports of trained model modelID, newest first, at most limit: a KindInternal Error if it cannot.
func ListExports(ctx context.Context, db *sql.DB, modelID int64, limit int) ([]trainingdb.TrainedModelExport, error) {
	list, err := trainingdb.ListTrainedModelExports(ctx, db, modelID, limit)
	if err != nil {
		log.WithError(err).Error("training: list trained model exports failed")
		return nil, internalErr("failed to list the exports", err)
	}
	return list, nil
}

var _ ModelFileFetcher = (*sidecar.Client)(nil)
