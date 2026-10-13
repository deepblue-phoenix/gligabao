package memory_remember

// Remember for a text larger than one chunk (see docs/chunking.md): the text is stored as an artifact, split into
// overlapping chunks (package chunk) that are artifacts too, its children, and each chunk goes through the same
// steps as a short text.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// chunkPart says that a text stored by store is a chunk of a longer one.
type chunkPart struct {
	ParentID int64       // the artifact that holds the whole text
	Chunk    chunk.Chunk // the chunk, with its position in the text
	Count    int         // the number of chunks of the text
}

// label is appended to the messages of the trajectory steps of a chunk; "" for a text that is not a chunk (or nil).
func (p *chunkPart) label() string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" (chunk %d of %d)", p.Chunk.Index+1, p.Count)
}

// rememberDocument is Remember for a text of more than one chunk.
//
//  1. The text is split into chunks (chunk.Split).
//  2. The graph is extracted from every chunk, one LLM call each. Nothing is written yet, so a failure here stores
//     nothing.
//  3. The text is stored as the parent artifact (kind, session and reference time as given).
//  4. Each chunk, in order, is then reconciled with the stored facts, stored as a child artifact with its entities
//     and edges, and embedded (Store, with the chunk index): one transaction per chunk, so a chunk is also compared
//     with the facts the chunks before it stored.
//
// Entities and edges are linked to the chunk they came from, not to the parent. The Result is that of the document:
// ArtifactID is the parent's, Entities, Edges and InvalidatedEdges are those of the chunks together, and Chunks lists
// them. Step 4 cannot be one transaction (a chunk's candidates are read from the database), so if it fails the chunks
// stored so far (each of them complete) and the parent stay; the call returns an error naming the chunk.
func (j *job) rememberDocument(ctx context.Context, text string, cfg chunk.Config) (*Result, error) {
	chunks, err := chunk.Split(text, cfg)
	if err != nil {
		return nil, fmt.Errorf("memory: chunk text: %w", err)
	}
	summary := make([]map[string]any, 0, len(chunks))
	for _, c := range chunks {
		summary = append(summary, map[string]any{"index": c.Index, "tokens": c.Tokens, "overlap_tokens": c.OverlapTokens, "start": c.Start, "end": c.End})
	}
	j.tr.Tool("The text is larger than one chunk, so it was split into overlapping chunks at paragraph boundaries; each chunk is "+
		"extracted, stored and embedded on its own.", "chunk_text",
		map[string]any{"text_chars": len(text), "chunk_size": cfg.Size, "overlap_percent": cfg.OverlapPercent},
		map[string]any{"chunks": len(chunks), "details": summary})

	//--------------------------------------
	// EXTRACT every chunk
	exs := make([]*Extraction, len(chunks))
	for i, c := range chunks {
		part := &chunkPart{Chunk: c, Count: len(chunks)}
		ex, err := Extract(ctx, j.llm, c.Text, j.ref)
		if err != nil {
			return nil, fmt.Errorf("memory: extract chunk %d of %d: %w", i+1, len(chunks), err)
		}
		exs[i] = ex
		j.tr.Tool("The LLM extracted these entities and edges (new facts) from the chunk, before any stored fact is considered."+part.label(),
			"extract_graph", map[string]any{"chunk_index": i, "chunk_tokens": c.Tokens, "text_chars": len(c.Text)},
			map[string]any{"entities": ex.Entities, "edges": candidateFacts(ex.Edges, nil)})
	}

	//--------------------------------------
	// STORE the parent, then every chunk
	sessionID, err := memory_core.ResolveSessionID(j.co.SessionID)
	if err != nil {
		return nil, err
	}
	parentID, err := storeParent(ctx, j.d, j.userID, j.dataset, j.kind, text, sessionID, j.ref, j.now)
	if err != nil {
		return nil, err
	}
	j.tr.Tool("Stored the whole text as the parent artifact of the chunks.", "store_parent_artifact",
		map[string]any{"user_id": j.userID, "dataset": j.dataset, "kind": j.kind}, map[string]any{"artifact_id": parentID, "chunks": len(chunks)})

	doc := &Result{
		UserID: j.userID, Dataset: j.dataset, Kind: j.kind, ArtifactID: parentID, SessionID: sessionID,
		ValidAt: j.ref.UTC(), CreatedAt: j.now.UTC(),
		Entities: []Entity{}, Edges: []Edge{}, InvalidatedEdges: []int64{}, Chunks: make([]ChunkResult, 0, len(chunks)),
	}
	m := newMerger(doc)
	for i, c := range chunks {
		part := &chunkPart{ParentID: parentID, Chunk: c, Count: len(chunks)}
		res, err := j.resolveAndStore(ctx, c.Text, exs[i], part)
		if err != nil {
			return nil, fmt.Errorf("memory: chunk %d of %d (parent artifact %d; the %d chunks before it are stored): %w", i+1, len(chunks), parentID, i, err)
		}
		m.add(res, c, len(exs[i].Entities), len(exs[i].Edges))
	}
	return doc, nil
}

// storeParent stores the artifact that holds a whole chunked text, in a transaction of its own (the chunks are stored
// afterwards, each in theirs). Nothing links to it: entities and edges link to its chunks.
func storeParent(ctx context.Context, d *sql.DB, userID, dataset, kind, text, sessionID string, ref, now time.Time) (int64, error) {
	dsID, err := relational.EnsureDataset(ctx, d, userID, dataset)
	if err != nil {
		return 0, err
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := tx.InsertArtifact(ctx, dsID, kind, text, sessionID, ref, now)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// merger accumulates the Results of the chunks of a text into the Result of the text.
type merger struct {
	doc      *Result
	entities map[string]bool // normalized name + type
	edgeAt   map[int64]int   // edge ID -> index in doc.Edges
	inval    map[int64]bool
}

func newMerger(doc *Result) *merger {
	return &merger{doc: doc, entities: map[string]bool{}, edgeAt: map[int64]int{}, inval: map[int64]bool{}}
}

// add merges the Result of chunk c. Its entities are added once per (name, type). An edge stated by several chunks
// (a repeat is merged into the stored edge, so it has one ID) appears once, in its latest stored state, since a later
// chunk may have closed it. extracted* are what the chunk's extraction listed.
func (m *merger) add(r *Result, c chunk.Chunk, extractedEntities, extractedEdges int) {
	d := m.doc
	for _, e := range r.Entities {
		if k := memory_core.Key(e.Name) + "\x00" + e.Type; !m.entities[k] {
			m.entities[k] = true
			d.Entities = append(d.Entities, e)
		}
	}
	for _, e := range r.Edges {
		if i, ok := m.edgeAt[e.ID]; ok {
			d.Edges[i] = e
			continue
		}
		m.edgeAt[e.ID] = len(d.Edges)
		d.Edges = append(d.Edges, e)
	}
	for _, id := range r.InvalidatedEdges {
		if !m.inval[id] {
			m.inval[id] = true
			d.InvalidatedEdges = append(d.InvalidatedEdges, id)
		}
	}
	d.EmbeddedEntities += r.EmbeddedEntities
	d.EmbeddedEdges += r.EmbeddedEdges
	d.EmbeddedChunks += r.EmbeddedChunks
	d.Chunks = append(d.Chunks, ChunkResult{
		ArtifactID: r.ArtifactID, Index: c.Index, Tokens: c.Tokens, OverlapTokens: c.OverlapTokens,
		StartOffset: c.Start, EndOffset: c.End, Entities: extractedEntities, Edges: extractedEdges,
	})
}
