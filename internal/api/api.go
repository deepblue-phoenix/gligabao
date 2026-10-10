// Package api contains HTTP handlers.
package api

import (
	"database/sql"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/api/ontologies"
	"github.com/deepblue-phoenix/gligabao/internal/api/recall"
	"github.com/deepblue-phoenix/gligabao/internal/api/remember"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

// Register mounts the API routes on mux. llm may be nil, in which case LLM-backed routes return 503.
func Register(mux *http.ServeMux, d *sql.DB, llm memory.LLM) {
	mux.HandleFunc("POST /v1/gb/remember", remember.Handler(d, llm))
	mux.HandleFunc("POST /v1/gb/recall", recall.Handler(d, llm))
	mux.HandleFunc("POST /v1/gb/datasets", datasets.Create(d))
	mux.HandleFunc("GET /v1/gb/datasets", datasets.List(d))
	mux.HandleFunc("DELETE /v1/gb/datasets/{name}", datasets.Delete(d))
	mux.HandleFunc("GET /v1/gb/datasets/{name}/graph", datasets.Graph(d))
	mux.HandleFunc("POST /v1/gb/ontologies", ontologies.Load(d))
	mux.HandleFunc("PUT /v1/gb/ontologies/{name}", ontologies.Update(d))
}
