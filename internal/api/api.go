// Package api contains HTTP handlers.
package api

import (
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/api/mcp"
	"github.com/deepblue-phoenix/gligabao/internal/api/ontologies"
	"github.com/deepblue-phoenix/gligabao/internal/api/query"
	"github.com/deepblue-phoenix/gligabao/internal/api/remember"
	"github.com/deepblue-phoenix/gligabao/internal/api/research"
	"github.com/deepblue-phoenix/gligabao/internal/api/timeseries"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_image"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// Register mounts the API routes on mux. mgr is the remember manager, the one goroutine that runs the remember pipeline (shared with
// RegisterEval, so that all remember runs are serialized together); it must have been made with the same d and llm. llm may be nil, in which case LLM-backed routes return 503.
// d holds all databases; its vector store has one vector dataset per dataset and index (memory_dataset.VectorDataset). emb is what remember embeds with and what query
// embeds the question with: it must not be nil (a query with a nil emb panics). jv may be nil (Jev not configured), in which case
// remember skips the steps that run on Jev. ws may be nil (no Anthropic API key), in which case a remember request that asks for
// web_search returns 503. Images are stored as files in d.ImageDir.
func Register(mux *http.ServeMux, mgr *memory_remember.Manager, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, opts ...Option) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	a := func(h http.HandlerFunc) http.Handler { return httputil.Authenticate(o.auth, h) } // routes of users: with WithAuth they need an API key
	mux.Handle("POST /v1/gb/remember", a(rememberHandler(mgr, d, llm, emb, jv, ws, providers, o)))
	mux.Handle("POST /v1/gb/query", a(query.Handler(d.Relational, llm, queryOptions(d, emb, o)...)))
	mux.Handle("GET /v1/gb/geo/nearby", a(query.NearbyHandler(d.Relational, memory_core.WithGeoRadius(o.geoRadiusKm))))
	mux.Handle("POST /v1/gb/datasets", a(datasets.Create(d.Relational)))
	mux.Handle("GET /v1/gb/datasets", a(datasets.List(d.Relational)))
	mux.Handle("DELETE /v1/gb/datasets/{name}", a(datasets.Delete(d.Relational, d.Vector)))
	mux.Handle("GET /v1/gb/datasets/{name}/graph", a(datasets.Graph(d.Relational)))
	mux.Handle("POST /v1/gb/timeseries", a(timeseries.Attach(d.Relational)))
	mux.Handle("GET /v1/gb/timeseries", a(timeseries.List(d.Relational)))
	mux.Handle("GET /v1/gb/timeseries/{id}", a(timeseries.Get(d.Relational)))
	mux.Handle("POST /v1/gb/ontologies", a(ontologies.Load(d.Relational, o.ontologyDirs)))
	mux.Handle("PUT /v1/gb/ontologies/{name}", a(ontologies.Update(d.Relational, o.ontologyDirs)))
	if o.research != nil {
		o.research.UseMemory(researchMemory(mgr, d, llm, emb, jv, ws, providers, o))
		mux.Handle("POST /v1/gb/research", a(research.Start(o.research)))
		mux.Handle("GET /v1/gb/research", a(research.List(o.research)))
		mux.Handle("GET /v1/gb/research/{id}", a(research.Get(o.research)))
	}
}

// rememberHandler is the handler of POST /v1/gb/remember (Register and RegisterMCP use the same one).
func rememberHandler(mgr *memory_remember.Manager, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, o options) http.HandlerFunc {
	return remember.Handler(mgr, d.Relational, llm, rememberOptions(d, emb, jv, ws, providers, o)...)
}

// rememberOptions are the options of every remember call of the API (and of the research agents).
func rememberOptions(d *db.DB, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, o options) []memory_core.Option {
	return []memory_core.Option{memory.WithVectors(d.Vector, emb), memory_core.WithChunking(d.Chunking), memory_core.WithJev(jv), memory_core.WithWebSearcher(ws), memory_core.WithSeriesProviders(providers), memory_core.WithGeoResolver(o.geo), memory_image.WithImageDir(d.ImageDir)}
}

// queryOptions are the options of every query call of the API (and of the research agents).
func queryOptions(d *db.DB, emb memory_core.Embedder, o options) []memory_core.Option {
	return []memory_core.Option{memory.WithVectors(d.Vector, emb), memory_core.WithGeoResolver(o.geo), memory_core.WithGeoRadius(o.geoRadiusKm)}
}

// researchMemory is the knowledge base the research agents work with: the remember manager and the query pipeline, with the same
// options as the remember and query endpoints. With no LLM the agents cannot run anyway, and get none.
func researchMemory(mgr *memory_remember.Manager, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, o options) *agent_research.Memory {
	if llm == nil || mgr == nil {
		return nil
	}
	return &agent_research.Memory{Remember: mgr, DB: d.Relational, LLM: llm, RememberOpts: rememberOptions(d, emb, jv, ws, providers, o), QueryOpts: queryOptions(d, emb, o)}
}

// RegisterMCP mounts the MCP endpoint (POST /v1/gb/mcp, see package mcp) with the tools remember and query (and, with WithResearch, those of the research agents). They run the same handlers
// as POST /v1/gb/remember and POST /v1/gb/query, so the arguments are the same as those of Register (mgr, d, llm, ...) and the same
// options apply (WithGeoResolver). There is no authentication: the WithAuth option is not applied, a caller sets the user_id itself.
// version is what the server reports to MCP clients.
func RegisterMCP(mux *http.ServeMux, mgr *memory_remember.Manager, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, version string, opts ...Option) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	var mcpOpts []mcp.Option
	if o.research != nil {
		o.research.UseMemory(researchMemory(mgr, d, llm, emb, jv, ws, providers, o))
		mcpOpts = append(mcpOpts, mcp.WithResearch(research.Start(o.research), research.List(o.research), research.Get(o.research)))
	}
	mcp.Register(mux, rememberHandler(mgr, d, llm, emb, jv, ws, providers, o), query.Handler(d.Relational, llm, queryOptions(d, emb, o)...), version, mcpOpts...)
}

type options struct {
	ontologyDirs []string
	auth         httputil.Authenticator
	geo          memory_geo.Resolver
	geoRadiusKm  float64
	research     *agent_research.Manager
}

// Option configures Register.
type Option func(*options)

// WithAuth makes every route of Register require an API key ("Authorization: Bearer <key>"): auth is called with it and returns
// the ID of the user it belongs to, who the request then acts as (the user_id the caller sent is replaced). A request without a key,
// or whose key auth rejects (any error), gets 401. Without it, routes trust the user_id of the request. The eval routes are not covered.
func WithAuth(auth httputil.Authenticator) Option { return func(o *options) { o.auth = auth } }

// WithGeoResolver makes remember (Register and RegisterEval) find the places among the entities it extracts and store their
// coordinates (see package memory_geo and memory_remember's resolve_geo stage), and query find the places the question mentions and give the
// geo entities near them to the LLM that answers. Without it (or with nil) neither looks at places.
func WithGeoResolver(r memory_geo.Resolver) Option { return func(o *options) { o.geo = r } }

// WithGeoRadius sets the default radius, in kilometers, of the proximity search of query (a request can override it with
// geo_radius_km); 0 (the default) means memory_core.DefaultGeoRadiusKm.
func WithGeoRadius(km float64) Option { return func(o *options) { o.geoRadiusKm = km } }

// WithResearch adds the routes of the research agents to Register (POST and GET /v1/gb/research, GET /v1/gb/research/{id}, under the
// same WithAuth as the other routes of users) and their tools to RegisterMCP (research_start, research_list, research_get): m starts
// the agents and reads their runs. Without it (or with nil) neither has them.
func WithResearch(m *agent_research.Manager) Option { return func(o *options) { o.research = m } }

// WithOntologyDirs sets the directories ontology documents can be loaded from by file name (POST and PUT /v1/gb/ontologies with
// "file"), searched in order. Without them only the "content" form works.
func WithOntologyDirs(dirs []string) Option { return func(o *options) { o.ontologyDirs = dirs } }

// RegisterEval mounts the eval-only routes: remember and query that also return their ATIF trajectories.
// They are not part of Register; the server mounts them only in eval mode.
func RegisterEval(mux *http.ServeMux, mgr *memory_remember.Manager, d *db.DB, llm memory_core.LLM, emb memory_core.Embedder, jv memory_core.Classifier, ws memory_core.WebSearcher, providers []memory_core.SeriesProvider, opts ...Option) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	mux.HandleFunc("POST /v1/gb/eval/remember", remember.EvalHandler(mgr, d.Relational, llm, memory.WithVectors(d.Vector, emb), memory_core.WithChunking(d.Chunking), memory_core.WithJev(jv), memory_core.WithWebSearcher(ws), memory_core.WithSeriesProviders(providers), memory_core.WithGeoResolver(o.geo), memory_image.WithImageDir(d.ImageDir)))
	mux.HandleFunc("POST /v1/gb/eval/query", query.EvalHandler(d.Relational, llm, memory.WithVectors(d.Vector, emb), memory_core.WithGeoResolver(o.geo), memory_core.WithGeoRadius(o.geoRadiusKm)))
}
