package memory_remember

// Remember for a text larger than one chunk (see docs/chunking.md): the text is stored as an artifact, split into
// overlapping chunks (package memory_chunk) that are artifacts too, its children, and each chunk goes through the same
// steps as a short text.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// chunkPart says that a text stored by store is a chunk of a longer one.
type chunkPart struct {
	ParentID int64              // the artifact that holds the whole text
	Chunk    memory_chunk.Chunk // the chunk, with its position in the text
	Count    int                // the number of chunks of the text
	// Summary is the summary of the chunk, stored as its child in the same transaction; nil if it has none.
	Summary *chunkSummary
}

// merger accumulates the Results of the chunks of a text into the Result of the text.
type merger struct {
	doc      *Result
	entities map[string]bool // normalized name + type
	edgeAt   map[int64]int   // edge ID -> index in doc.Edges
	inval    map[int64]bool
}

// label is appended to the messages of the trajectory steps of a chunk; "" for a text that is not a chunk (or nil).
func (p *chunkPart) label() string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" (chunk %d of %d)", p.Chunk.Index+1, p.Count)
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

func newMerger(doc *Result) *merger {
	return &merger{doc: doc, entities: map[string]bool{}, edgeAt: map[int64]int{}, inval: map[int64]bool{}}
}

// add merges the Result of chunk c. Its entities are added once per (name, type). An edge stated by several chunks
// (a repeat is merged into the stored edge, so it has one ID) appears once, in its latest stored state, since a later
// chunk may have closed it. extracted* are what the chunk's extraction listed.
func (m *merger) add(r *Result, c memory_chunk.Chunk, extractedEntities, extractedEdges int) {
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
	d.EmbeddedSummaries += r.EmbeddedSummaries
	d.Chunks = append(d.Chunks, ChunkResult{
		ArtifactID: r.ArtifactID, Index: c.Index, Tokens: c.Tokens, OverlapTokens: c.OverlapTokens,
		StartOffset: c.Start, EndOffset: c.End, Entities: extractedEntities, Edges: extractedEdges, Summary: r.summary,
	})
}
