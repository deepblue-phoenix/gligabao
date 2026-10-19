package training

// The training datasets endpoints; the datasets themselves are in internal/training (datasets.go, manifest.go).

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
)

// UploadDatasetOf serves POST /v1/gb/training/datasets[?db=<name>], a multipart form with the JSONL file in the
// field "file" (and optionally "filename", which overrides the uploaded file's name). The file must be a valid dataset
// (core.ValidateDataset) with a plain *.jsonl name. It becomes the dataset named like the file without ".jsonl": Go saves it
// in the subdirectory of that name of the training datasets directory of one of dbs (the first without db; Database.DatasetDir),
// next to a manifest it writes if there is none, and records it in gb_training_datasets of that database. Uploading a
// name again replaces the dataset's file. A dataset of that name that has a manifest of its own, naming another data file,
// or a dataset of that name in another directory, is not replaced: 409. The sidecar is not involved. It answers 201
// with the dataset (core.DatasetInfo). A bad file answers 400, a file over MaxDatasetBytes 413.
func UploadDatasetOf(dbs []core.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
		if n.DatasetDir == "" {
			httputil.Error(w, http.StatusInternalServerError, "no training dataset directory is configured")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, core.MaxDatasetBytes+1<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				httputil.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the dataset is over %d MB", core.MaxDatasetBytes>>20))
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
		if hdr.Size > core.MaxDatasetBytes {
			httputil.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the dataset is over %d MB", core.MaxDatasetBytes>>20))
			return
		}
		filename := strings.TrimSpace(r.FormValue("filename"))
		if filename == "" {
			filename = hdr.Filename
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(file); err != nil {
			httputil.Error(w, http.StatusBadRequest, "failed to read the file")
			return
		}
		info, err := core.SaveDataset(r.Context(), n, filename, buf.Bytes())
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, info)
	}
}

// ListDatasetsOf serves GET /v1/gb/training/datasets[?db=<name>]: {"datasets": [...], "problems": [...]}, the training
// datasets (core.DatasetInfo) found in the training datasets directory by their manifests, by name, and the subdirectories with a
// manifest that are not usable (core.DatasetProblem). It never asks the sidecar.
func ListDatasetsOf(dbs []core.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
		ds, problems, err := core.ListDatasets(r.Context(), n)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"datasets": ds, "problems": problems})
	}
}

// PreviewDatasetOf serves GET /v1/gb/training/datasets/preview?name=<dataset>[&lines=<n>][&db=<name>]: the first lines
// (default 100, at most 1000) of a dataset's data file, so it can be inspected: {"name", "lines": [...], "num_records",
// "truncated"}. truncated is true if the file has more lines than returned. Only the beginning of the file is read. An
// unknown dataset answers 404.
func PreviewDatasetOf(dbs []core.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pickDB(w, r, dbs)
		if !ok {
			return
		}
		name := q.Get("name")
		if !core.ValidDatasetName(name) {
			httputil.Error(w, http.StatusBadRequest, "invalid dataset name")
			return
		}
		limit := core.PreviewDefaultLines
		if v := q.Get("lines"); v != "" {
			l, err := strconv.Atoi(v)
			if err != nil || l < 1 || l > core.PreviewMaxLines {
				httputil.Error(w, http.StatusBadRequest, fmt.Sprintf("lines must be between 1 and %d", core.PreviewMaxLines))
				return
			}
			limit = l
		}
		p, err := core.PreviewDataset(n, name, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, p)
	}
}
