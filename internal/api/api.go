// Package api contains HTTP handlers.
package api

import (
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/api/ontologies"
	"github.com/deepblue-phoenix/gligabao/internal/api/recall"
	"github.com/deepblue-phoenix/gligabao/internal/api/remember"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

// Register mounts the API routes on mux. llm may be nil, in which case LLM-backed routes return 503.
// d holds all databases; its vector store has one namespace per dataset (memory.VectorNamespace).
func Register(mux *http.ServeMux, d *db.DB, llm memory.LLM) {
	mux.HandleFunc("POST /v1/gb/remember", remember.Handler(d.Relational, llm))
	mux.HandleFunc("POST /v1/gb/recall", recall.Handler(d.Relational, llm))
	mux.HandleFunc("POST /v1/gb/datasets", datasets.Create(d.Relational))
	mux.HandleFunc("GET /v1/gb/datasets", datasets.List(d.Relational))
	mux.HandleFunc("DELETE /v1/gb/datasets/{name}", datasets.Delete(d.Relational, d.Vector))
	mux.HandleFunc("GET /v1/gb/datasets/{name}/graph", datasets.Graph(d.Relational))
	mux.HandleFunc("POST /v1/gb/ontologies", ontologies.Load(d.Relational))
	mux.HandleFunc("PUT /v1/gb/ontologies/{name}", ontologies.Update(d.Relational))
}
