// Package ontologies contains the HTTP handlers of the ontologies: loading, updating, reading them and their grounding events.
package ontologies

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology/ontospec"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

const maxBodyBytes = memory_ontology.MaxBytes + 64<<10 // the document is JSON-escaped, leave room for it and the other fields

type loadRequest struct {
	Name        string `json:"name"` // POST only; PUT takes the name from the path
	Description string `json:"description"`
	// Format is "yaml" or "json"; optional: it is detected from the content (or the extension of the file).
	Format string `json:"format"`
	// Content is the ontology as a YAML or JSON document.
	Content string `json:"content"`
	// Spec is the ontology as a JSON object, instead of Content.
	Spec json.RawMessage `json:"spec"`
	// File is the name of a file of the configured ontology directories (ontology_dirs), the first that has it, to load instead
	// of Content. Exactly one of Content, Spec and File is set.
	File string `json:"file"`
}

// problemsResponse is the 400 of an ontology with problems.
type problemsResponse struct {
	Error    string             `json:"error"`
	Problems []ontospec.Problem `json:"problems,omitempty"`
}

// Load serves POST /v1/gb/ontologies: parses the YAML or JSON ontology in "content" (or "spec", or in the file "file" of the ontology
// directories dirs), checks it, and stores it under the given name, as its version 1. The ontology belongs to the user of the request
// (the gb-user-id header). An invalid ontology is a 400 with the list of its problems.
func Load(d *sql.DB,
	dirs []string) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		req, ok := decode(w,
			r,
			dirs)
		if !ok {
			return
		}
		o, err := memory_ontology.Create(r.Context(),
			d,
			httputil.UserID(r),
			req.Name,
			req.Description,
			req.Format,
			[]byte(req.Content))
		if err != nil {
			fail(w,
				"load",
				err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusCreated,
			o)
	}
}

// Update serves PUT /v1/gb/ontologies/{name}. The name comes from the path. The document replaces the ontology stored under that name and
// is its next version (the version is incremented and the old documents are kept); the compiled ontology is swapped for the new one.
func Update(d *sql.DB,
	dirs []string) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		req, ok := decode(w,
			r,
			dirs)
		if !ok {
			return
		}
		o, err := memory_ontology.Update(r.Context(),
			d,
			httputil.UserID(r),
			r.PathValue("name"),
			req.Description,
			req.Format,
			[]byte(req.Content))
		if err != nil {
			fail(w,
				"update",
				err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			o)
	}
}

// List serves GET /v1/gb/ontologies: the user's ontologies, without their specs.
func List(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		list, err := memory_ontology.List(r.Context(),
			d,
			httputil.UserID(r))
		if err != nil {
			fail(w,
				"list",
				err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string]any{"ontologies": list})
	}
}

// Get serves GET /v1/gb/ontologies/{name}: the ontology with its spec.
func Get(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		o, err := memory_ontology.Get(r.Context(),
			d,
			httputil.UserID(r),
			r.PathValue("name"))
		if err != nil {
			fail(w,
				"get",
				err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			o)
	}
}

// Events serves GET /v1/gb/ontologies/{name}/events?artifact_id=&outcome=&limit=: what grounding made of the nodes and edges of the
// remember calls that used the ontology, newest first (see docs/ontologies/ontologies.md).
func Events(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		q := r.URL.Query()
		f := graph.OntologyEventFilter{Outcome: q.Get("outcome")}
		for _, p := range []struct {
			name string
			dst  *int64
		}{{"artifact_id", &f.ArtifactID}} {
			if v := q.Get(p.name); v != "" {
				n, err := strconv.ParseInt(v,
					10,
					64)
				if err != nil || n < 0 {
					httputil.Error(w,
						http.StatusBadRequest,
						p.name+" must be a positive integer")
					return
				}
				*p.dst = n
			}
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > maxEvents {
				httputil.Error(w,
					http.StatusBadRequest,
					"limit must be between 1 and "+strconv.Itoa(maxEvents))
				return
			}
			f.Limit = n
		}
		if f.Outcome != "" && !validOutcome(f.Outcome) {
			httputil.Error(w,
				http.StatusBadRequest,
				"unknown outcome "+strconv.Quote(f.Outcome))
			return
		}
		events, err := memory_ontology.Events(r.Context(),
			d,
			httputil.UserID(r),
			r.PathValue("name"),
			f)
		if err != nil {
			fail(w,
				"list events of",
				err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string]any{"events": events})
	}
}

const maxEvents = 1000

func validOutcome(o string) bool {
	switch o {
	case ontospec.OutcomeMatched, ontospec.OutcomeMatchedViaAlias, ontospec.OutcomeUnmatched, ontospec.OutcomeViolation, ontospec.OutcomeDropped:
		return true
	}
	return false
}

// decode reads and checks the request body, and reads the document of "file" (or the object "spec") into Content; it writes the error
// response itself when it returns false.
func decode(w http.ResponseWriter,
	r *http.Request,
	dirs []string) (loadRequest, bool) {
	var req loadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w,
		r.Body,
		maxBodyBytes)).Decode(&req); err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			"invalid JSON body: "+err.Error())
		return req, false
	}
	if f := strings.ToLower(strings.TrimSpace(req.Format)); f != "" && f != ontospec.FormatYAML && f != "yml" && f != ontospec.FormatJSON {
		httputil.Error(w,
			http.StatusBadRequest,
			`unsupported format: use "yaml" or "json"`)
		return req, false
	}
	hasSpec := len(req.Spec) > 0 && string(req.Spec) != "null"
	set := 0
	for _, b := range []bool{req.Content != "", req.File != "", hasSpec} {
		if b {
			set++
		}
	}
	switch {
	case set > 1:
		httputil.Error(w,
			http.StatusBadRequest,
			`set only one of "content", "spec" and "file"`)
		return req, false
	case set == 0:
		httputil.Error(w,
			http.StatusBadRequest,
			`"content", "spec" or "file" is required`)
		return req, false
	case hasSpec:
		req.Content, req.Format = string(req.Spec), ontospec.FormatJSON
	case req.File != "":
		doc, err := readFile(dirs, req.File)
		if err != nil {
			fail(w,
				"read",
				err)
			return req, false
		}
		req.Content = string(doc)
		if req.Format == "" && strings.EqualFold(filepath.Ext(req.File), ".json") {
			req.Format = ontospec.FormatJSON
		}
	}
	return req, true
}

var (
	errNoDirs       = errors.New("no ontology directories are configured (ontology_dirs), so a file cannot be loaded")
	errBadFileName  = errors.New("file must be a plain file name, without a directory")
	errFileNotFound = errors.New("ontology file not found")
)

// readFile reads the file called name from the first of the ontology directories dirs that has it. The name must be a plain file
// name: it cannot reach outside the directories.
func readFile(dirs []string,
	name string) ([]byte, error) {
	if len(dirs) == 0 {
		return nil, errNoDirs
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, errBadFileName
	}
	for _, dir := range dirs {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		doc, err := readDocument(f)
		f.Close()
		return doc, err
	}
	return nil, errFileNotFound
}

// readDocument reads a regular file, at most memory_ontology.MaxBytes of it; a bigger one is an invalid ontology.
func readDocument(f *os.File) ([]byte, error) {
	if st, err := f.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, errFileNotFound
	}
	doc, err := io.ReadAll(io.LimitReader(f, memory_ontology.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func fail(w http.ResponseWriter,
	op string,
	err error) {
	var invalid *ontospec.InvalidError
	switch {
	case errors.As(err, &invalid):
		httputil.WriteJSON(w,
			http.StatusBadRequest,
			problemsResponse{
				Error:    err.Error(),
				Problems: invalid.Problems,
			})
	case errors.Is(err, memory_ontology.ErrInvalidName), errors.Is(err, memory_core.ErrInvalidUser), errors.Is(err, memory_ontology.ErrInvalid),
		errors.Is(err, errNoDirs), errors.Is(err, errBadFileName):
		httputil.Error(w,
			http.StatusBadRequest,
			err.Error())
	case errors.Is(err, errFileNotFound):
		httputil.Error(w,
			http.StatusNotFound,
			err.Error())
	case errors.Is(err, memory_ontology.ErrExists):
		httputil.Error(w,
			http.StatusConflict,
			"ontology already exists")
	case errors.Is(err, memory_ontology.ErrNotFound):
		httputil.Error(w,
			http.StatusNotFound,
			"ontology not found")
	default:
		log.WithError(err).WithField("op", op).Error("ontology request failed")
		httputil.Error(w,
			http.StatusInternalServerError,
			"failed to "+op+" ontology")
	}
}
