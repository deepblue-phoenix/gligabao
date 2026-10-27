// Package ontologies contains the HTTP handlers for loading ontologies.
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
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

const maxBodyBytes = memory.MaxOntologyBytes + 64<<10 // the document is JSON-escaped, leave room for it and the other fields

type loadRequest struct {
	UserID      string `json:"user_id"`
	Name        string `json:"name"` // POST only; PUT takes the name from the path
	Description string `json:"description"`
	Format      string `json:"format"` // optional; only "rdfxml" is supported
	Content     string `json:"content"`
	// File is the name of a file of the configured ontology directories (ontology_dirs), the first that has it, to load instead
	// of Content. Exactly one of the two is set.
	File    string `json:"file"`
	Replace bool   `json:"replace"` // PUT only: replace the stored ontology instead of merging into it
}

// Load serves POST /v1/gb/ontologies: parses the OWL/RDF-XML document in "content" (or in the file "file" of the ontology
// directories dirs), stores its metadata in the relational database and the ontology itself in the graph database, under the given
// name. The ontology belongs to the request's user_id.
func Load(d *sql.DB, dirs []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode(w, r, dirs)
		if !ok {
			return
		}
		o, err := memory.LoadOntology(r.Context(), d, req.UserID, req.Name, req.Description, []byte(req.Content))
		if err != nil {
			fail(w, "load", err)
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, o)
	}
}

// Update serves PUT /v1/gb/ontologies/{name}. The name comes from the path. By default the document in "content"
// is merged into the ontology stored under that name: new classes and edges are added and ones that exist already
// are overwritten, nothing is deleted. With "replace": true the document replaces the stored ontology entirely.
// The merge rules are documented on memory.UpdateOntology.
func Update(d *sql.DB, dirs []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode(w, r, dirs)
		if !ok {
			return
		}
		o, err := memory.UpdateOntology(r.Context(), d, req.UserID, r.PathValue("name"), req.Description, []byte(req.Content), req.Replace)
		if err != nil {
			fail(w, "update", err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, o)
	}
}

// decode reads and checks the request body, and reads the document of "file" into Content; it writes the error response itself when
// it returns false.
func decode(w http.ResponseWriter, r *http.Request, dirs []string) (loadRequest, bool) {
	var req loadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return req, false
	}
	if req.Format != "" && req.Format != memory.OntologyFormatRDFXML {
		httputil.Error(w, http.StatusBadRequest, `unsupported format: only "rdfxml" is supported`)
		return req, false
	}
	switch {
	case req.Content != "" && req.File != "":
		httputil.Error(w, http.StatusBadRequest, `set either "content" or "file", not both`)
		return req, false
	case req.Content == "" && req.File == "":
		httputil.Error(w, http.StatusBadRequest, `"content" or "file" is required`)
		return req, false
	case req.File != "":
		doc, err := readFile(dirs, req.File)
		if err != nil {
			fail(w, "read", err)
			return req, false
		}
		req.Content = string(doc)
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
func readFile(dirs []string, name string) ([]byte, error) {
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

// readDocument reads a regular file, at most memory.MaxOntologyBytes of it; a bigger one is an invalid ontology.
func readDocument(f *os.File) ([]byte, error) {
	if st, err := f.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, errFileNotFound
	}
	doc, err := io.ReadAll(io.LimitReader(f, memory.MaxOntologyBytes+1))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, memory.ErrInvalidOntologyName), errors.Is(err, memory_core.ErrInvalidUser), errors.Is(err, memory.ErrInvalidOntology),
		errors.Is(err, errNoDirs), errors.Is(err, errBadFileName):
		httputil.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errFileNotFound):
		httputil.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, memory.ErrOntologyExists):
		httputil.Error(w, http.StatusConflict, "ontology already exists")
	case errors.Is(err, memory.ErrOntologyNotFound):
		httputil.Error(w, http.StatusNotFound, "ontology not found")
	default:
		log.WithError(err).WithField("op", op).Error("ontology request failed")
		httputil.Error(w, http.StatusInternalServerError, "failed to "+op+" ontology")
	}
}
