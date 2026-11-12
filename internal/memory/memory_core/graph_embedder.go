package memory_core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"

	log "github.com/sirupsen/logrus"
)

// The graph embedder hook: a callback of the program that embeds gligabao (gligabao.ServeOptions.GraphEmbedder, see docs/hooks.md) that
// turns a graph (the entities and facts of a remember call, or the context a query is answered from) into ONE embedding of the graph
// as a whole, which is different from the text embeddings of the entities, edges and chunks that the pipelines already write to the
// vector store (WithVectors). The embedding is meant to come from a custom model that is trained for graphs.
//
// IMPORTANT: the embeddings that the hook returns are NOT stored anywhere yet. For now gligabao calls the hook and discards the
// result. They MUST be stored in the vector store (CallOptions.Vectors, in a vector dataset of their own) before anything relies on them;
// until then the hook is only a place where the call happens, and nothing can be searched by it. Every call site below (and the docs)
// repeats this on purpose, so that it is not forgotten when the hook starts to do real work.

// Graph embedding events: which pipeline the graph is from (the values of graph.InContextEvent.Pipeline).
const (
	// GraphEmbedRemember: the graph a Remember call made, once it is fully constructed.
	GraphEmbedRemember = graph.PipelineRemember
	// GraphEmbedQuery: the graph context a Query call fetched for the question, before the answer is made from it.
	GraphEmbedQuery = graph.PipelineQuery
)

// GraphEmbedRequest is the graph a GraphEmbedder is asked to embed.
//
// IMPORTANT: whoever stores the result in the vector store (not done yet, see the note above) needs a key for it: use Pipeline with
// RunID (gb_remember_runs.id or gb_query_runs.id), and for a remember also ArtifactID, together with the dataset.
type GraphEmbedRequest struct {
	// Pipeline says what the graph is: GraphEmbedRemember or GraphEmbedQuery.
	Pipeline string
	// Index is the name of the graph embedding index the call asked for (the graph_index of the request, WithGraphIndex): which index
	// the embedding belongs to, e.g. the one of a model, a size bin or a tenant. It is validated (see ResolveGraphIndex). Empty means the
	// call named none: the hook uses its own default (the graph engine's is "default").
	Index string
	// UserID and Dataset (already resolved) are where the call ran.
	UserID, Dataset string
	// RunID is the run log of the call (gb_remember_runs.id or gb_query_runs.id, by Pipeline); 0 if it could not be recorded.
	RunID int64
	// ArtifactID is the artifact of the text that was remembered (the parent artifact of a document); 0 for a query.
	ArtifactID int64
	// Question is the question of a query; empty for a remember.
	Question string
	// Entities and Edges are the graph. For a remember they are what the text yielded, in their stored state (the entities its facts
	// mention included), not the whole dataset; for a query, the entities and edges of the context the LLM answers from (and the
	// passages are not part of it).
	Entities []graph.EvidenceEntity
	Edges    []graph.EvidenceEdge
}

// ErrInvalidGraphIndex means the name of a graph embedding index is malformed.
var ErrInvalidGraphIndex = errors.New("invalid graph_index")

var graphIndexRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ResolveGraphIndex trims the name of a graph embedding index and returns ErrInvalidGraphIndex if it is not 1-64 characters of letters,
// digits, '.', '_' or '-', starting with a letter or digit (the names the graph engine accepts, so one can never be a path). An empty
// name is valid and means "none given": the hook picks.
func ResolveGraphIndex(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name != "" && !graphIndexRe.MatchString(name) {
		return "", fmt.Errorf("%w: name must be 1-64 characters of letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidGraphIndex)
	}
	return name, nil
}

// GraphEmbedder embeds a graph. It returns the embedding of the whole graph, or an error. It must honour ctx and may be called
// concurrently. It runs on the goroutine of the call (the remember manager's, or the request's for a query, whose answer waits for it),
// so a slow model slows the call.
//
// IMPORTANT: gligabao does not store the embedding yet, see the note at the top of graph_embedder.go.
type GraphEmbedder func(ctx context.Context,
	req GraphEmbedRequest) ([]float32, error)

// WithGraphEmbedder sets the graph embedder hook of Remember (called once the graph of the text is fully constructed) and Query (called
// once the graph context of the question is fetched). nil (the default) leaves it off. The embedding is not used yet.
//
// IMPORTANT: the embeddings are NOT stored in the vector store yet; they must be (see the note at the top of graph_embedder.go).
func WithGraphEmbedder(f GraphEmbedder) Option { return func(o *CallOptions) { o.GraphEmbedder = f } }

// WithGraphIndex names the graph embedding index of the call: Remember and Query pass it to the graph embedder hook as
// GraphEmbedRequest.Index, so the hook knows which index the graph's embedding belongs to (when embedding) or is searched in. It is
// used as given: validate it with ResolveGraphIndex first. Empty (the default) names none. It has no effect without WithGraphEmbedder.
func WithGraphIndex(name string) Option { return func(o *CallOptions) { o.GraphIndex = name } }

// EmbedGraph calls the graph embedder of o, if it has one and the graph is not empty. Like the other advisory steps it never fails the
// call: an error or a panic of the hook is logged and dropped.
//
// IMPORTANT: for now the embedding is only counted in a debug log and then DISCARDED. It has to be written to the vector store
// (o.Vectors) by whoever completes this hook; that is the whole point of calling it.
func EmbedGraph(ctx context.Context,
	o CallOptions,
	req GraphEmbedRequest) {
	if o.GraphEmbedder == nil || len(req.Entities)+len(req.Edges) == 0 {
		return
	}
	fields := log.Fields{"pipeline": req.Pipeline, "index": req.Index, "run_id": req.RunID, "entities": len(req.Entities), "edges": len(req.Edges)}
	emb, err := callGraphEmbedder(ctx,
		o.GraphEmbedder,
		req)
	if err != nil {
		log.WithError(err).WithFields(fields).Warn("memory: the graph embedder hook failed; the call is not affected")
		return
	}
	// TODO(vectors): store emb in the vector store (o.Vectors). IMPORTANT: it is dropped here for now.
	fields["dimensions"] = len(emb)
	log.WithFields(fields).Debug("memory: graph embedded by the hook (the embedding is not stored yet)")
}

// callGraphEmbedder runs f, turning a panic into an error.
func callGraphEmbedder(ctx context.Context,
	f GraphEmbedder,
	req GraphEmbedRequest) (emb []float32, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			emb, err = nil, fmt.Errorf("panic: %v", rec)
		}
	}()
	return f(ctx, req)
}
