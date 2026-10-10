// Package ontologies contains the HTTP handlers for loading ontologies.
package ontologies

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/ivan/gligabao/internal/api/httputil"
	"github.com/ivan/gligabao/internal/memory"
)

const maxBodyBytes = memory.MaxOntologyBytes + 64<<10 // the document is JSON-escaped, leave room for it and the other fields

type loadRequest struct {
	UserID      string `json:"user_id"`
	Name        string `json:"name"` // POST only; PUT takes the name from the path
	Description string `json:"description"`
	Format      string `json:"format"` // optional; only "rdfxml" is supported
	Content     string `json:"content"`
	Replace     bool   `json:"replace"` // PUT only: replace the stored ontology instead of merging into it
}

// Load serves POST /v1/gb/ontologies: parses the OWL/RDF-XML document in "content", stores its metadata in the
// relational database and the ontology itself in the graph database, under the given name.
func Load(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode(w, r)
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
func Update(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decode(w, r)
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

// decode reads and checks the request body; it writes the error response itself when it returns false.
func decode(w http.ResponseWriter, r *http.Request) (loadRequest, bool) {
	var req loadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return req, false
	}
	if req.Format != "" && req.Format != memory.OntologyFormatRDFXML {
		httputil.Error(w, http.StatusBadRequest, `unsupported format: only "rdfxml" is supported`)
		return req, false
	}
	if req.Content == "" {
		httputil.Error(w, http.StatusBadRequest, "content is required")
		return req, false
	}
	return req, true
}

func fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, memory.ErrInvalidOntologyName), errors.Is(err, memory.ErrInvalidUser), errors.Is(err, memory.ErrInvalidOntology):
		httputil.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, memory.ErrOntologyExists):
		httputil.Error(w, http.StatusConflict, "ontology already exists")
	case errors.Is(err, memory.ErrOntologyNotFound):
		httputil.Error(w, http.StatusNotFound, "ontology not found")
	default:
		log.Printf("%s ontology: %v", op, err)
		httputil.Error(w, http.StatusInternalServerError, "failed to "+op+" ontology")
	}
}
