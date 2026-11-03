// Package memory_gitexport turns a dataset into a git repository: a bare repository whose history is the history of the
// dataset. Every [remember] call (an artifact together with the chunks, web searches and observations that hang off
// it) becomes one commit, dated with the call's creation time, that adds the artifact's text, the entities and the
// facts (edges) it yielded, and edits the files of the facts it confirmed, ended or invalidated. See docs/git-export.md.
//
// The package does not use the git binary and has no dependencies: it writes git's loose objects itself (blobs,
// trees, commits), so a repository can be produced by the server and streamed as one archive. Input is already read
// from the databases (see memory.LoadGitExport); the output is deterministic.
package memory_gitexport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

const (
	authorName  = "gligabao"
	authorEmail = "gligabao@localhost"
	branch      = "main"
)

// Input is everything of a dataset that the repository is built from.
type Input struct {
	Name        string
	Description string
	Tags        []string
	CreatedAt   time.Time

	Artifacts       []graph.Artifact
	Entities        []graph.EvidenceEntity
	Edges           []graph.EvidenceEdge
	EntityArtifacts []graph.EntityArtifact
	EdgeArtifacts   []graph.EdgeArtifact
	// EdgeEvents are the edge events by the ID of the root artifact (the one that is nobody's child) of the call that made
	// them: the events of the artifact and of all its descendants, oldest first (see graph.ListEdgeEvents).
	EdgeEvents map[int64][]graph.EdgeEvent
	// Classes are the graph classes of the artifacts (gb_artifact_classes); they go into the front matter of their artifact.
	Classes []graph.ArtifactClass
	// RememberRuns and QueryRuns are the run logs of the dataset. A remember run that stored an artifact goes into the commit
	// of that artifact; the others (and every query run) are commits of their own, at the time the run started.
	RememberRuns []RememberRunLog
	QueryRuns    []QueryRunLog
	// Models are the names of the models (gb_models) by ID, for the model_id of entities and edges; optional.
	Models map[string]string
}

// RememberRunLog is a remember run (gb_remember_runs) with the inferences its stages made (by ID) and its stage outputs.
type RememberRunLog struct {
	Run        relational.RememberRun
	Inferences map[int64]relational.Inference
	Outputs    []relational.RememberStageOutput
}

// QueryRunLog is a query run (gb_query_runs) with the inferences its stages made (by ID).
type QueryRunLog struct {
	Run        relational.QueryRun
	Inferences map[int64]relational.Inference
}

// Result says what was written.
type Result struct {
	Commits   int
	Artifacts int
	Entities  int
	Edges     int
	Runs      int    // run logs written
	Head      string // ID of the last commit
}

type entState struct {
	e       graph.EvidenceEntity
	sources []int64 // artifacts that mention it, so far
	appear  int     // index of the commit it first appears in
	shown   bool
}

type edgeState struct {
	e       graph.EvidenceEdge
	invalid *time.Time
	expired *time.Time
	sources []int64
	appear  int
	shown   bool
}

type link struct{ id, artifact int64 }

// commitStats is what one commit did, for its message.
type commitStats struct {
	newEntities, newEdges int
	confirmed             int
	changes               []string // the edge events that changed a fact, as lines
}

// Write builds the repository of the dataset and writes it to sink. The repository has a root commit (a README), then one
// commit per remember call, oldest first, and, only if some entities or facts have no source artifact (their artifact
// was deleted), a last commit with them.
func Write(in Input, sink Sink) (*Result, error) {
	st := newStore(sink)
	byID := make(map[int64]graph.Artifact, len(in.Artifacts))
	for _, a := range in.Artifacts {
		byID[a.ID] = a
	}
	rootOf := func(id int64) int64 { // 0 if the artifact (or an ancestor) is not there
		for n := 0; n < 1000; n++ {
			a, ok := byID[id]
			if !ok {
				return 0
			}
			if a.ParentID == 0 {
				return id
			}
			id = a.ParentID
		}
		return 0
	}

	// The calls: the root artifacts, oldest first, each with its descendants.
	var roots []graph.Artifact
	members := map[int64][]graph.Artifact{}
	for _, a := range in.Artifacts {
		if r := rootOf(a.ID); r != 0 {
			members[r] = append(members[r], a)
			if r == a.ID {
				roots = append(roots, a)
			}
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if !roots[i].CreatedAt.Equal(roots[j].CreatedAt) {
			return roots[i].CreatedAt.Before(roots[j].CreatedAt)
		}
		return roots[i].ID < roots[j].ID
	})
	rootIdx := make(map[int64]int, len(roots))
	for i, r := range roots {
		rootIdx[r.ID] = i
	}
	commitOf := func(artifact int64) int { // -1 if unknown
		if i, ok := rootIdx[rootOf(artifact)]; ok && rootOf(artifact) != 0 {
			return i
		}
		return -1
	}
	orphan := len(roots) // the index of the last, extra commit

	// Entities and edges, with the commit each first shows up in.
	ents := make(map[int64]*entState, len(in.Entities))
	for _, e := range in.Entities {
		ents[e.ID] = &entState{e: e, appear: orphan}
	}
	edges := make(map[int64]*edgeState, len(in.Edges))
	for _, e := range in.Edges {
		edges[e.ID] = &edgeState{e: e, appear: orphan}
	}
	entLinks := make([][]link, len(roots))
	for _, l := range in.EntityArtifacts {
		s, i := ents[l.EntityID], commitOf(l.ArtifactID)
		if s == nil || i < 0 {
			continue
		}
		entLinks[i] = append(entLinks[i], link{l.EntityID, l.ArtifactID})
		if i < s.appear {
			s.appear = i
		}
	}
	edgeLinks := make([][]link, len(roots))
	for _, l := range in.EdgeArtifacts {
		s, i := edges[l.EdgeID], commitOf(l.ArtifactID)
		if s == nil || i < 0 {
			continue
		}
		edgeLinks[i] = append(edgeLinks[i], link{l.EdgeID, l.ArtifactID})
		if i < s.appear {
			s.appear = i
		}
	}
	// The fact shows up in the commit of the artifact that created it (the first link or event of it is the same call).
	for _, s := range edges {
		if s.e.ArtifactID != nil {
			if i := commitOf(*s.e.ArtifactID); i >= 0 && i < s.appear {
				s.appear = i
			}
		}
	}
	events := make([][]graph.EdgeEvent, len(roots))
	for root, evs := range in.EdgeEvents {
		i, ok := rootIdx[root]
		if !ok {
			continue
		}
		events[i] = evs
		for _, ev := range evs {
			if s := edges[ev.EdgeID]; s != nil && i < s.appear {
				s.appear = i
			}
		}
	}
	appearing := func(idx int) (es []*entState, ds []*edgeState) {
		for _, s := range ents {
			if s.appear == idx {
				es = append(es, s)
			}
		}
		for _, s := range edges {
			if s.appear == idx {
				ds = append(ds, s)
			}
		}
		return
	}
	orphanEnts, orphanEdges := appearing(orphan)

	r := newRenderer(in)
	tree := newDir()
	res := &Result{}
	parent := ""
	var lastTime time.Time

	commit := func(at time.Time, message string) error {
		id, err := tree.write(st)
		if err != nil {
			return err
		}
		if parent, err = st.commit(id, parent, at, message); err != nil {
			return err
		}
		res.Commits++
		lastTime = at
		return nil
	}
	put := func(path, content string) error {
		id, err := st.put("blob", []byte(content))
		if err != nil {
			return err
		}
		tree.set(path, id)
		return nil
	}

	// The run logs: a remember run that stored an artifact goes into the commit of that artifact; the others, and the query
	// runs, are commits of their own.
	sort.SliceStable(in.RememberRuns, func(i, j int) bool { return in.RememberRuns[i].Run.ID < in.RememberRuns[j].Run.ID })
	sort.SliceStable(in.QueryRuns, func(i, j int) bool { return in.QueryRuns[i].Run.ID < in.QueryRuns[j].Run.ID })
	callRuns := make([][]int, len(roots)) // by call: the remember runs (indexes of in.RememberRuns) of its artifact
	type single struct {
		at    time.Time
		query bool
		idx   int
	}
	var singles []single
	for k, l := range in.RememberRuns {
		if l.Run.ArtifactID != nil {
			if i, ok := rootIdx[*l.Run.ArtifactID]; ok {
				callRuns[i] = append(callRuns[i], k)
				continue
			}
		}
		singles = append(singles, single{l.Run.StartedAt, false, k})
	}
	for k, l := range in.QueryRuns {
		singles = append(singles, single{l.Run.StartedAt, true, k})
	}
	// The steps, in time order: the calls first, so that at the same time a call comes before a run.
	type step struct {
		at     time.Time
		root   int // the call, or -1
		single int
	}
	steps := make([]step, 0, len(roots)+len(singles))
	for i, root := range roots {
		steps = append(steps, step{root.CreatedAt, i, -1})
	}
	sort.SliceStable(singles, func(i, j int) bool { return singles[i].at.Before(singles[j].at) })
	for k, sg := range singles {
		steps = append(steps, step{sg.at, -1, k})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].at.Before(steps[j].at) })

	// The root commit.
	first := in.CreatedAt
	if len(steps) > 0 && (first.IsZero() || steps[0].at.Before(first)) {
		first = steps[0].at
	}
	if err := put("README.md", r.readme()); err != nil {
		return nil, err
	}
	if err := commit(first, r.rootMessage()); err != nil {
		return nil, err
	}

	// A flush writes the files of the entities and facts that changed in a commit.
	dirtyEnts, dirtyEdges := map[int64]bool{}, map[int64]bool{}
	flush := func() error {
		for _, id := range sortedKeys(dirtyEnts) {
			s := ents[id]
			sort.Slice(s.sources, func(i, j int) bool { return s.sources[i] < s.sources[j] })
			if err := put(r.entityPath(s.e), r.entityFile(s)); err != nil {
				return err
			}
		}
		for _, id := range sortedKeys(dirtyEdges) {
			s := edges[id]
			sort.Slice(s.sources, func(i, j int) bool { return s.sources[i] < s.sources[j] })
			if err := put(r.edgePath(s.e), r.edgeFile(s)); err != nil {
				return err
			}
		}
		dirtyEnts, dirtyEdges = map[int64]bool{}, map[int64]bool{}
		return nil
	}

	for _, stp := range steps {
		if stp.root < 0 { // a run that is not part of a call
			sg := singles[stp.single]
			var path, file, message string
			if sg.query {
				l := in.QueryRuns[sg.idx]
				path, file, message = r.queryRunPath(l.Run.ID), r.queryRunFile(l), r.queryRunMessage(l.Run)
			} else {
				l := in.RememberRuns[sg.idx]
				path, file, message = r.rememberRunPath(l.Run.ID), r.rememberRunFile(l), r.rememberRunMessage(l.Run)
			}
			if err := put(path, file); err != nil {
				return nil, err
			}
			res.Runs++
			if err := commit(sg.at, message); err != nil {
				return nil, err
			}
			continue
		}
		i := stp.root
		root := roots[i]
		var stats commitStats
		for _, a := range members[root.ID] {
			if err := put(r.artifactPath(root.ID, a), r.artifactFile(a)); err != nil {
				return nil, err
			}
			res.Artifacts++
		}
		var runIDs []int64
		for _, k := range callRuns[i] {
			l := in.RememberRuns[k]
			if err := put(r.rememberRunPath(l.Run.ID), r.rememberRunFile(l)); err != nil {
				return nil, err
			}
			runIDs = append(runIDs, l.Run.ID)
			res.Runs++
		}
		newEnts, newEdges := appearing(i)
		for _, s := range newEnts {
			s.shown = true
			dirtyEnts[s.e.ID] = true
			stats.newEntities++
			res.Entities++
		}
		for _, s := range newEdges {
			s.shown = true
			dirtyEdges[s.e.ID] = true
			stats.newEdges++
			res.Edges++
		}
		for _, l := range entLinks[i] {
			s := ents[l.id]
			s.sources = appendUnique(s.sources, l.artifact)
			dirtyEnts[l.id] = true
		}
		for _, l := range edgeLinks[i] {
			s := edges[l.id]
			s.sources = appendUnique(s.sources, l.artifact)
			dirtyEdges[l.id] = true
		}
		for _, ev := range events[i] {
			s := edges[ev.EdgeID]
			if s == nil {
				continue
			}
			s.apply(ev)
			dirtyEdges[ev.EdgeID] = true
			switch ev.Event {
			case graph.EdgeConfirmed:
				stats.confirmed++
			case graph.EdgeEnded, graph.EdgeInvalidated, graph.EdgeSuperseded:
				stats.changes = append(stats.changes, fmt.Sprintf("- %s edge %d: %s %s %s", ev.Event, ev.EdgeID, s.e.Source, s.e.Relation, s.e.Target))
			}
		}
		if err := flush(); err != nil {
			return nil, err
		}
		if err := commit(root.CreatedAt, r.callMessage(root, len(members[root.ID]), runIDs, stats)); err != nil {
			return nil, err
		}
	}

	if len(orphanEnts)+len(orphanEdges) > 0 {
		var stats commitStats
		for _, s := range orphanEnts {
			s.shown = true
			dirtyEnts[s.e.ID] = true
			stats.newEntities++
			res.Entities++
		}
		for _, s := range orphanEdges {
			s.shown = true
			s.invalid, s.expired = s.e.InvalidAt, s.e.ExpiredAt // no events to replay: the final state
			dirtyEdges[s.e.ID] = true
			stats.newEdges++
			res.Edges++
		}
		if err := flush(); err != nil {
			return nil, err
		}
		if lastTime.IsZero() {
			lastTime = first
		}
		if err := commit(lastTime, r.orphanMessage(stats)); err != nil {
			return nil, err
		}
	}

	res.Head = parent
	for _, f := range [][2]string{
		{"HEAD", "ref: refs/heads/" + branch + "\n"},
		{"config", "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n"},
		{"refs/heads/" + branch, parent + "\n"},
	} {
		if err := sink.WriteFile(f[0], []byte(f[1])); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// apply replays an edge event on the state of the fact.
func (s *edgeState) apply(ev graph.EdgeEvent) {
	switch ev.Event {
	case graph.EdgeCreated, graph.EdgeEnded:
		s.invalid = ev.InvalidAtAfter
	case graph.EdgeInvalidated, graph.EdgeSuperseded:
		s.invalid = ev.InvalidAtAfter
		t := ev.CreatedAt
		s.expired = &t
	}
}

func sortedKeys(m map[int64]bool) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func appendUnique(ids []int64, id int64) []int64 {
	for _, x := range ids {
		if x == id {
			return ids
		}
	}
	return append(ids, id)
}

func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
