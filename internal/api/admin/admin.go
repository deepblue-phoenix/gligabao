// Package admin contains the HTTP handlers behind the admin UI. Unlike the user-facing API they are not scoped to
// a user_id: they see every user's datasets. They are only mounted when the admin UI is enabled.
package admin

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	log "github.com/sirupsen/logrus"
)

// Counts is the number of artifacts, entities and edges.
type Counts struct {
	Artifacts int `json:"artifacts"`
	Entities  int `json:"entities"`
	Edges     int `json:"edges"`
}

type datasetView struct {
	relational.Dataset
	Counts
}

type overview struct {
	Datasets []datasetView `json:"datasets"`
	// Totals is the sum over Datasets, so it matches what is listed: deleted datasets (whose rows are kept, e.g. the
	// previous runs of an eval that recreates its dataset) count only when include_deleted is set.
	Totals Counts `json:"totals"`
}

// Named is a database shown in the admin UI under a name (e.g. "standard", "eval").
type Named struct {
	Name string
	DB   *sql.DB
	// TrajectoryDir is where the ATIF trajectories of the experiments recorded in DB are stored ("" = unknown);
	// only used for the eval database.
	TrajectoryDir string
	// ImageDir is the directory of the image files of DB's image artifacts ("" = unknown: nothing is reported
	// about the files on disk).
	ImageDir string
	// Vectors is the vector store holding the embeddings of DB's datasets; nil if there is none (the vectors of a
	// deleted dataset are then not cleaned up).
	Vectors vector.Store
	// OpenVectors, if set, is used instead of Vectors: it opens the vector store afresh each time it is needed. For a
	// database whose vectors are written by another process (the eval runner), a store opened at startup would
	// keep showing what it loaded then (chromem reads its directory only when opened).
	OpenVectors func() (vector.Store, error)
}

// vectors returns the vector store of n (nil if there is none) and a function that releases it.
func (n Named) vectors() (vector.Store, func(), error) {
	if n.OpenVectors == nil {
		return n.Vectors, func() {}, nil
	}
	s, err := n.OpenVectors()
	if err != nil {
		return nil, nil, err
	}
	closer, _ := s.(io.Closer)
	return s, func() {
		if closer != nil {
			closer.Close()
		}
	}, nil
}

// Databases serves GET /admin/api/databases: the names of the databases the UI can show, in display order.
func Databases(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(dbs))
		for _, d := range dbs {
			names = append(names, d.Name)
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"databases": names})
	}
}

// OverviewOf serves GET /admin/api/overview[?db=<name>][&include_deleted=true] for one of dbs; without db it
// serves the first one.
func OverviewOf(dbs []Named) http.HandlerFunc {
	handlers := make(map[string]http.HandlerFunc, len(dbs))
	for _, d := range dbs {
		handlers[d.Name] = Overview(d.DB)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		h, ok := handlers[name]
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		h(w, r)
	}
}

// Overview serves GET /admin/api/overview[?include_deleted=true]: the datasets of all users, each with its
// artifact/entity/edge counts, plus the totals over the listed datasets.
func Overview(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		list, err := relational.ListAllDatasets(ctx, d, r.URL.Query().Get("include_deleted") == "true")
		if err != nil {
			log.WithError(err).Error("admin: list datasets failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list datasets")
			return
		}
		out := overview{Datasets: make([]datasetView, 0, len(list))}
		for _, ds := range list {
			c, err := graph.DatasetCounts(ctx, d, ds.ID)
			if err != nil {
				log.WithError(err).WithField("dataset_id", ds.ID).Error("admin: dataset counts failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to count dataset contents")
				return
			}
			out.Datasets = append(out.Datasets, datasetView{ds, Counts{c.Artifacts, c.Entities, c.Edges}})
			out.Totals.Artifacts += c.Artifacts
			out.Totals.Entities += c.Entities
			out.Totals.Edges += c.Edges
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// GraphOf serves GET /admin/api/graph?db=<name>&user_id=...&dataset=... for one of dbs (the first without db):
// the whole entity graph of a live dataset (see memory.ExportGraph).
func GraphOf(dbs []Named) http.HandlerFunc {
	handlers := make(map[string]http.HandlerFunc, len(dbs))
	for _, d := range dbs {
		handlers[d.Name] = Graph(d.DB)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		h, ok := handlers[name]
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		h(w, r)
	}
}

// Graph serves GET /admin/api/graph?user_id=...&dataset=...: the entities and edges of the dataset.
func Graph(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		g, err := memory.ExportGraph(r.Context(), d, q.Get("user_id"), q.Get("dataset"))
		switch {
		case errors.Is(err, memory_core.ErrInvalidUser):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case err != nil:
			log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": q.Get("dataset")}).Error("admin: export graph failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to export graph")
		default:
			httputil.WriteJSON(w, http.StatusOK, g)
		}
	}
}

// DeleteDatasetOf serves DELETE /admin/api/datasets?db=<name>&user_id=...&dataset=... for one of dbs (the first
// without db): a soft delete of the dataset (see memory.DeleteDataset).
func DeleteDatasetOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		name := q.Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name != name {
				continue
			}
			vectors, release, err := n.vectors()
			if err != nil {
				log.WithError(err).Error("admin: open vector store failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to open vector store")
				return
			}
			defer release()
			err = memory.DeleteDataset(r.Context(), n.DB, vectors, q.Get("user_id"), q.Get("dataset"))
			switch {
			case errors.Is(err, memory_core.ErrInvalidUser):
				httputil.Error(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory.ErrDatasetNotFound):
				httputil.Error(w, http.StatusNotFound, "dataset not found")
			case err != nil:
				log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": q.Get("dataset")}).Error("admin: delete dataset failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to delete dataset")
			default:
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
}

// VectorsOf serves GET /admin/api/vectors?db=<name>&user_id=...&dataset=... for one of dbs (the first without db):
// the vector statistics of a live dataset, per index (see memory.VectorStats).
func VectorsOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		name := q.Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name != name {
				continue
			}
			vectors, release, err := n.vectors()
			if err != nil {
				log.WithError(err).Error("admin: open vector store failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to open vector store")
				return
			}
			defer release()
			st, err := memory.VectorStats(r.Context(), n.DB, vectors, q.Get("user_id"), q.Get("dataset"))
			switch {
			case errors.Is(err, memory_core.ErrInvalidUser):
				httputil.Error(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory.ErrDatasetNotFound):
				httputil.Error(w, http.StatusNotFound, "dataset not found")
			case err != nil:
				log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": q.Get("dataset")}).Error("admin: vector stats failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read vector stats")
			default:
				httputil.WriteJSON(w, http.StatusOK, st)
			}
			return
		}
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
}

//---------------------------------------------------------------------------------------------

// imageDisk describes the image directory next to what the database tracks.
type imageDisk struct {
	Dir    string `json:"dir"`
	Exists bool   `json:"exists"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	// Missing is the number of image artifacts whose file is not in the directory.
	Missing int `json:"missing"`
	// Untracked is the number of files in the directory that no image artifact points to (e.g. left by a crash).
	Untracked int `json:"untracked"`
}

type imagesView struct {
	*graph.ImageStats
	// Disk is nil if the image directory is not known.
	Disk *imageDisk `json:"disk,omitempty"`
}

// ImagesOf serves GET /admin/api/images[?db=<name>] for one of dbs (the first without db): statistics of the image
// store: the image artifacts and extractions in the database, and the files in the image directory.
func ImagesOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name != name {
				continue
			}
			st, err := graph.ImageStatistics(r.Context(), n.DB)
			if err != nil {
				log.WithError(err).Error("admin: image stats failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read image statistics")
				return
			}
			out := imagesView{ImageStats: st}
			if n.ImageDir != "" {
				out.Disk = scanImageDir(n.ImageDir, st.Paths)
			}
			httputil.WriteJSON(w, http.StatusOK, out)
			return
		}
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
}

// scanImageDir walks dir and compares its files with the paths the database tracks (relative to dir, slash-separated).
func scanImageDir(dir string, tracked []string) *imageDisk {
	d := &imageDisk{Dir: dir}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.Missing = len(tracked)
		return d
	}
	d.Exists = true
	onDisk := map[string]bool{}
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil // unreadable entries are skipped
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || strings.HasPrefix(e.Name(), ".tmp-") { // a file being written is not yet one of the store
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		d.Files++
		d.Bytes += fi.Size()
		onDisk[filepath.ToSlash(rel)] = true
		return nil
	})
	seen := map[string]bool{}
	for _, p := range tracked {
		if seen[p] { // the same file can back several artifacts
			continue
		}
		seen[p] = true
		if !onDisk[p] {
			d.Missing++
		}
	}
	for p := range onDisk {
		if !seen[p] {
			d.Untracked++
		}
	}
	return d
}
