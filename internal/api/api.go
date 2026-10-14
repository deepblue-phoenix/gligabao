// Package api contains HTTP handlers.
package api

import (
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/api/ontologies"
	"github.com/deepblue-phoenix/gligabao/internal/api/recall"
	"github.com/deepblue-phoenix/gligabao/internal/api/remember"
	"github.com/deepblue-phoenix/gligabao/internal/api/timeseries"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_image"
)

// Register mounts the API routes on mux. llm may be nil, in which case LLM-backed routes return 503.
// d holds all databases; its vector store has one vector dataset per dataset and index (memory_core.VectorDataset). emb may be nil,
// in which case remember does not embed the entities it stores. jv may be nil (Jev not configured), in which case
// remember skips the steps that run on Jev. ws may be nil (no Anthropic API key), in which case a remember request that asks for
// web_search returns 503. Images are stored as files in d.ImageDir.
func Register(mux *http.ServeMux, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider) {
	mux.HandleFunc("POST /v1/gb/remember", remember.Handler(d.Relational, llm, memory.WithVectors(d.Vector, emb), memory_core.WithChunking(d.Chunking), memory_core.WithJev(jv), memory_core.WithWebSearcher(ws), memory_core.WithSeriesProviders(providers), memory_image.WithImageDir(d.ImageDir)))
	mux.HandleFunc("POST /v1/gb/recall", recall.Handler(d.Relational, llm))
	mux.HandleFunc("POST /v1/gb/datasets", datasets.Create(d.Relational))
	mux.HandleFunc("GET /v1/gb/datasets", datasets.List(d.Relational))
	mux.HandleFunc("DELETE /v1/gb/datasets/{name}", datasets.Delete(d.Relational, d.Vector))
	mux.HandleFunc("GET /v1/gb/datasets/{name}/graph", datasets.Graph(d.Relational))
	mux.HandleFunc("POST /v1/gb/timeseries", timeseries.Attach(d.Relational))
	mux.HandleFunc("GET /v1/gb/timeseries", timeseries.List(d.Relational))
	mux.HandleFunc("GET /v1/gb/timeseries/{id}", timeseries.Get(d.Relational))
	mux.HandleFunc("POST /v1/gb/ontologies", ontologies.Load(d.Relational))
	mux.HandleFunc("PUT /v1/gb/ontologies/{name}", ontologies.Update(d.Relational))
}

// RegisterEval mounts the eval-only routes: remember and recall that also return their ATIF trajectories.
// They are not part of Register; the server mounts them only in eval mode.
func RegisterEval(mux *http.ServeMux, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider) {
	mux.HandleFunc("POST /v1/gb/eval/remember", remember.EvalHandler(d.Relational, llm, memory.WithVectors(d.Vector, emb), memory_core.WithChunking(d.Chunking), memory_core.WithJev(jv), memory_core.WithWebSearcher(ws), memory_core.WithSeriesProviders(providers), memory_image.WithImageDir(d.ImageDir)))
	mux.HandleFunc("POST /v1/gb/eval/recall", recall.EvalHandler(d.Relational, llm))
}
