package memory_gitexport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// renderer makes the paths, the files and the commit messages of the repository.
type renderer struct {
	in      Input
	classes map[int64][]string // the graph classes of each artifact, sorted
}

func newRenderer(in Input) *renderer {
	r := &renderer{
		in:      in,
		classes: map[int64][]string{},
	}
	for _, c := range in.Classes {
		r.classes[c.ArtifactID] = appendUniqueString(r.classes[c.ArtifactID], c.Class)
	}
	for _, cs := range r.classes {
		sort.Strings(cs)
	}
	return r
}

func appendUniqueString(list []string,
	s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// quote returns s as a double-quoted string (JSON, which is also a valid YAML scalar), without HTML escaping.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(b.String(), "\n")
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// slug makes a safe, lowercase file name part of s: letters, digits, "_" and "-" only, at most 40 runes.
func slug(s string) string {
	var b strings.Builder
	dash := true
	n := 0
	for _, r := range strings.ToLower(s) {
		if n >= 40 {
			break
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			dash = false
			n++
		case !dash:
			b.WriteByte('-')
			dash = true
			n++
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	return out
}

func (r *renderer) model(id string) string {
	if n := r.in.Models[id]; n != "" {
		return n
	}
	return id
}

// kindName is the file name part of an artifact that hangs off another one.
func kindName(a graph.Artifact) string {
	switch a.Kind {
	case graph.ArtifactChunkSummary:
		return "summary"
	case graph.ArtifactWebSearch:
		return "web-search"
	case graph.ArtifactSeriesObservation:
		return "series-observation"
	}
	return fmt.Sprintf("chunk-%d", a.ChunkIndex)
}

func (r *renderer) artifactPath(root int64,
	a graph.Artifact) string {
	if a.ID == root {
		return fmt.Sprintf("artifacts/%06d.md", a.ID)
	}
	return fmt.Sprintf("artifacts/%06d/%06d-%s.md",
		root,
		a.ID,
		kindName(a))
}

func (r *renderer) artifactFile(a graph.Artifact) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b,
		"id: %d\nkind: %s\n",
		a.ID,
		a.Kind)
	if a.ParentID != 0 {
		fmt.Fprintf(&b,
			"parent: %d\n",
			a.ParentID)
		switch a.Kind {
		case graph.ArtifactChunkSummary, graph.ArtifactWebSearch, graph.ArtifactSeriesObservation:
		default: // a chunk
			fmt.Fprintf(&b,
				"chunk_index: %d\nstart_offset: %d\nend_offset: %d\ntokens: %d\n",
				a.ChunkIndex,
				a.StartOffset,
				a.EndOffset,
				a.Tokens)
		}
	}
	if a.Query != "" {
		fmt.Fprintf(&b,
			"query: %s\n",
			quote(a.Query))
	}
	if a.SessionID != "" {
		fmt.Fprintf(&b,
			"session: %s\n",
			quote(a.SessionID))
	}
	if cs := r.classes[a.ID]; len(cs) > 0 {
		quoted := make([]string, len(cs))
		for i, c := range cs {
			quoted[i] = quote(c)
		}
		fmt.Fprintf(&b,
			"classes: [%s]\n",
			strings.Join(quoted, ", "))
	}
	fmt.Fprintf(&b,
		"valid_at: %s\ncreated_at: %s\n---\n\n",
		ts(a.ValidAt),
		ts(a.CreatedAt))
	b.WriteString(a.Text)
	if !strings.HasSuffix(a.Text, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func (r *renderer) entityPath(e graph.EvidenceEntity) string {
	return fmt.Sprintf("entities/%s/%s-%d.md",
		slug(e.Type),
		slug(e.Name),
		e.ID)
}

func (r *renderer) entityFile(s *entState) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b,
		"id: %d\nname: %s\ntype: %s\n",
		s.e.ID,
		quote(s.e.Name),
		quote(s.e.Type))
	if s.e.ModelID != "" {
		fmt.Fprintf(&b,
			"model: %s\n",
			quote(r.model(s.e.ModelID)))
	}
	fmt.Fprintf(&b,
		"artifacts: %s\n---\n",
		idList(s.sources))
	if s.e.Description != "" {
		b.WriteString("\n" + s.e.Description + "\n")
	}
	return b.String()
}

func (r *renderer) edgePath(e graph.EvidenceEdge) string {
	return fmt.Sprintf("edges/%s/%06d.md",
		slug(e.Relation),
		e.ID)
}

func (r *renderer) edgeFile(s *edgeState) string {
	e := s.e
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b,
		"id: %d\nsource: %s\nsource_id: %d\nrelation: %s\ntarget: %s\ntarget_id: %d\n",
		e.ID,
		quote(e.Source),
		e.SourceID,
		quote(e.Relation),
		quote(e.Target),
		e.TargetID)
	if e.ValidAt != nil {
		fmt.Fprintf(&b,
			"valid_at: %s\n",
			ts(*e.ValidAt))
	}
	if s.invalid != nil {
		fmt.Fprintf(&b,
			"invalid_at: %s\n",
			ts(*s.invalid))
	}
	if e.CreatedAt != nil {
		fmt.Fprintf(&b,
			"created_at: %s\n",
			ts(*e.CreatedAt))
	}
	if s.expired != nil {
		fmt.Fprintf(&b,
			"expired_at: %s\n",
			ts(*s.expired))
	}
	if e.ModelID != "" {
		fmt.Fprintf(&b,
			"model: %s\n",
			quote(r.model(e.ModelID)))
	}
	fmt.Fprintf(&b,
		"artifacts: %s\n---\n",
		idList(s.sources))
	if e.Description != "" {
		b.WriteString("\n" + e.Description + "\n")
	}
	return b.String()
}

//---------------------------------------------------------------------------------------------

func (r *renderer) readme() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"# %s\n\n",
		r.in.Name)
	if r.in.Description != "" {
		b.WriteString(r.in.Description + "\n\n")
	}
	if len(r.in.Tags) > 0 {
		b.WriteString("Tags: " + strings.Join(r.in.Tags, ", ") + "\n\n")
	}
	b.WriteString(`This repository is a generated export of a gligabao dataset (a knowledge graph). It is read-only:
changes made here are not imported back. Every commit after this one is one ` + "`remember`" + ` call: its author date is when
gligabao learned the text (system time), not when what it says was true (that is ` + "`valid_at`" + ` in the files).

## Layout

- ` + "`artifacts/<id>.md`" + `: the text that was remembered. A long text, or one that was followed up with web searches or
  series observations, has its chunks and children in ` + "`artifacts/<id>/`" + `.
- ` + "`entities/<type>/<name>-<id>.md`" + `: an entity, with the artifacts that mention it (` + "`artifacts`" + `). The description is the
  entity's latest one, from its first commit on: gligabao does not keep the history of a description.
- ` + "`edges/<relation>/<id>.md`" + `: a fact (` + "`source -relation-> target`" + `), bi-temporal. A later commit that contradicts it
  edits its file (` + "`invalid_at`" + ` and ` + "`expired_at`" + ` appear); one that states it again adds the artifact to ` + "`artifacts`" + `.
  ` + "`git log -p edges/`" + ` is the history of the facts.
- ` + "`classes`" + ` in the front matter of an artifact: the graph classes an LLM gave to its graph.
- ` + "`runs/remember/<id>.json`" + `: the log of a remember call (parameters, stages, the models and tokens they used, the outputs
  the stages kept), in the commit of its artifact. ` + "`runs/query/<id>.json`" + `: the log of a query, a commit of its own
  (` + "`query #<id>: <question>`" + `) that changes nothing else. A remember call that stored nothing is a commit of its own too.

## Not included

Embeddings, timeseries, geo data, images, sessions as such (each artifact names its own) and usage counters.
`)
	return b.String()
}

func (r *renderer) rootMessage() string {
	return fmt.Sprintf("Create dataset %s\n\nGenerated by gligabao. One commit per remember call follows.", r.in.Name)
}

// subject is the first line of text, collapsed and cut to n runes.
func subject(text string,
	n int) string {
	line := strings.Join(strings.Fields(strings.SplitN(strings.TrimSpace(text),
		"\n",
		2)[0]), " ")
	if line == "" {
		return "(no text)"
	}
	if rs := []rune(line); len(rs) > n {
		return string(rs[:n]) + "..."
	}
	return line
}

func (r *renderer) callMessage(root graph.Artifact,
	artifacts int,
	runIDs []int64,
	st commitStats) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"remember #%d: %s\n\n",
		root.ID,
		subject(root.Text, 60))
	var parts []string
	if st.newEntities > 0 {
		parts = append(parts, fmt.Sprintf("+%d entities", st.newEntities))
	}
	if st.newEdges > 0 {
		parts = append(parts, fmt.Sprintf("+%d edges", st.newEdges))
	}
	if st.confirmed > 0 {
		parts = append(parts, fmt.Sprintf("%d facts confirmed", st.confirmed))
	}
	if len(st.changes) > 0 {
		parts = append(parts, fmt.Sprintf("%d facts changed", len(st.changes)))
	}
	if len(parts) == 0 {
		parts = append(parts, "no new entities or facts")
	}
	b.WriteString(strings.Join(parts, ", ") + "\n")
	if len(st.changes) > 0 {
		b.WriteString("\n" + strings.Join(st.changes, "\n") + "\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b,
		"Artifact-Id: %d\nKind: %s\n",
		root.ID,
		root.Kind)
	if root.SessionID != "" {
		fmt.Fprintf(&b,
			"Session-Id: %s\n",
			root.SessionID)
	}
	fmt.Fprintf(&b,
		"Valid-At: %s\n",
		ts(root.ValidAt))
	if artifacts > 1 {
		fmt.Fprintf(&b,
			"Artifacts: %d\n",
			artifacts)
	}
	for _, id := range runIDs {
		fmt.Fprintf(&b,
			"Remember-Run-Id: %d\n",
			id)
	}
	return b.String()
}

func (r *renderer) orphanMessage(st commitStats) string {
	return fmt.Sprintf("Entities and facts without a source artifact\n\n+%d entities, +%d edges whose artifact was deleted.",
		st.newEntities,
		st.newEdges)
}
