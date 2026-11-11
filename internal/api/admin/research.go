package admin

// Research agents in the admin UI (see docs/agents/research.md#admin-ui): how many agents work now, the list of the agents (the active ones
// and those that ended, any user's) with what each of them spent and how many sub-agents it spawned, one agent with its states,
// report and sub-agents, stopping an active agent, and the provenance of an agent: what it put in the knowledge base (the
// artifacts, entities and facts of its session "research-<run id>").

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

const (
	researchDefaultLimit = 100
	researchMaxLimit     = agent_research.MaxListLimit
	// researchStoppedBy is what the runs stopped from the admin UI say.
	researchStoppedBy = "the admin UI"
	// researchDefaultUser is the user of the research agents started from the admin UI when the request names none.
	researchDefaultUser = "admin"
	// researchStartMaxBody is the most bytes of the body of a start request.
	researchStartMaxBody = 64 << 10
	// provenanceTextRunes is how much of the text of an artifact the provenance returns.
	provenanceTextRunes = 600
)

// ResearchCounts is how many research agents there are with each status (sub-agents included); it is never filtered.
type ResearchCounts struct {
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Stopped   int `json:"stopped"`
	Total     int `json:"total"`
}

// ResearchAgent is a research run as the admin UI shows it: the run, and what is derived from it.
type ResearchAgent struct {
	core.Run
	// Children is how many sub-agents the agent spawned.
	Children int `json:"children"`
	// DurationMS is how long the agent has been working (running) or worked (ended); AgeMS how long ago it was started.
	DurationMS int64 `json:"duration_ms"`
	AgeMS      int64 `json:"age_ms"`
	// HasReport says the run has a report (it is left out of lists; GET one agent for it).
	HasReport bool `json:"has_report"`
}

func researchAgentOf(r core.Run, children int, now time.Time) ResearchAgent {
	end := now
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	}
	return ResearchAgent{
		Run: r, Children: children, DurationMS: max(end.Sub(r.CreatedAt).Milliseconds(), 0), AgeMS: max(now.Sub(r.CreatedAt).Milliseconds(), 0),
		HasReport: r.Report != "",
	}
}

// researchAgents turns runs into ResearchAgents, counting the sub-agents of each, without their reports.
func researchAgents(ctx context.Context, m *agent_research.Manager, runs []core.Run) ([]ResearchAgent, error) {
	ids := make([]int64, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	kids, err := m.ChildCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]ResearchAgent, len(runs))
	for i, r := range runs {
		out[i] = researchAgentOf(r, kids[r.ID], now)
		out[i].Report = ""
	}
	return out, nil
}

// researchFail answers with the status of the Kind of err (500 for any other error).
func researchFail(w http.ResponseWriter, err error, what string) {
	var e *agent_research.Error
	if !errors.As(err, &e) {
		log.WithError(err).Errorf("admin: %s failed", what)
		httputil.Error(w, http.StatusInternalServerError, "failed to "+what)
		return
	}
	status := http.StatusInternalServerError
	switch e.Kind {
	case agent_research.KindInvalid:
		status = http.StatusBadRequest
	case agent_research.KindNotFound:
		status = http.StatusNotFound
	case agent_research.KindConflict:
		status = http.StatusConflict
	case agent_research.KindUnavailable:
		status = http.StatusServiceUnavailable
	}
	if status == http.StatusInternalServerError {
		log.WithError(err).Errorf("admin: %s failed", what)
	}
	httputil.Error(w, status, e.Msg)
}

type researchListResponse struct {
	Counts ResearchCounts `json:"counts"`
	// Runs are the agents that match the filter, newest first (at most Limit of them); Total is how many match.
	Runs  []ResearchAgent `json:"runs"`
	Total int             `json:"total"`
	Limit int             `json:"limit"`
}

// ResearchListOf serves GET /admin/api/research[?status=running|completed|failed|stopped|finished][&user_id=..][&roots_only=true][&limit=..]:
// the counts of all the agents by status (never filtered) and the agents that match the filters, newest first: "running" is the active
// agents, "finished" those that ended in any way (completed, failed or stopped). Every agent has the number of its sub-agents, how
// long it has worked and how long ago it started. The reports are left out (see ResearchGetOf). m is the manager of the research
// agents of the standard database.
func ResearchListOf(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := researchDefaultLimit
		if v := q.Get("limit"); v != "" {
			l, err := strconv.Atoi(v)
			if err != nil || l < 1 {
				httputil.Error(w, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = min(l, researchMaxLimit)
		}
		opts := agent_research.ListOptions{Limit: limit}
		switch s := q.Get("status"); s {
		case "":
		case "finished":
			opts.Finished = true
		default:
			opts.Status = s
		}
		if v := q.Get("roots_only"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				httputil.Error(w, http.StatusBadRequest, "roots_only must be true or false")
				return
			}
			opts.RootsOnly = b
		}
		ctx := r.Context()
		counts, err := m.Counts(ctx, "")
		if err != nil {
			researchFail(w, err, "count the research agents")
			return
		}
		runs, err := m.List(ctx, q.Get("user_id"), opts)
		if err != nil {
			researchFail(w, err, "list the research agents")
			return
		}
		out := researchListResponse{Limit: limit}
		out.Counts = ResearchCounts{Running: counts[core.StatusRunning], Completed: counts[core.StatusCompleted], Failed: counts[core.StatusFailed], Stopped: counts[core.StatusStopped]}
		out.Counts.Total = out.Counts.Running + out.Counts.Completed + out.Counts.Failed + out.Counts.Stopped
		if out.Runs, err = researchAgents(ctx, m, runs); err != nil {
			researchFail(w, err, "list the research agents")
			return
		}
		out.Total = researchTotal(counts, opts, q.Get("user_id"), len(out.Runs))
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// researchTotal is how many agents match the filter of a list: read from the counts of all the agents when the filter is on the status
// only, else (a user or the researches only) the number of agents listed, which the limit may have cut.
func researchTotal(counts map[core.Status]int, opts agent_research.ListOptions, userID string, listed int) int {
	if userID != "" || opts.RootsOnly {
		return listed
	}
	switch {
	case opts.Finished:
		return counts[core.StatusCompleted] + counts[core.StatusFailed] + counts[core.StatusStopped]
	case opts.Status != "":
		return counts[core.Status(opts.Status)]
	}
	return counts[core.StatusRunning] + counts[core.StatusCompleted] + counts[core.StatusFailed] + counts[core.StatusStopped]
}

type researchGetResponse struct {
	ResearchAgent
	// Parent is the agent that spawned this one; nil for a research a user started.
	Parent *ResearchAgent `json:"parent"`
	// Subagents are the agents it spawned, in the order they were spawned.
	Subagents []ResearchAgent `json:"subagents"`
}

// ResearchGetOf serves GET /admin/api/research/{id}: one agent with its report, its states (in the order it entered them), its parent
// and its sub-agents (without their reports and states).
func ResearchGetOf(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		run, err := m.Get(ctx, "", id)
		if err != nil {
			researchFail(w, err, "read the research agent")
			return
		}
		kids, err := m.List(ctx, "", agent_research.ListOptions{ParentID: id, Limit: agent_research.MaxListLimit})
		if err != nil {
			researchFail(w, err, "read the research agent")
			return
		}
		runs := append([]core.Run{*run}, kids...)
		var parent *core.Run
		if run.ParentID != nil {
			if parent, err = m.Get(ctx, "", *run.ParentID); err != nil {
				researchFail(w, err, "read the research agent")
				return
			}
			parent.States = nil
			runs = append(runs, *parent)
		}
		agents, err := researchAgents(ctx, m, runs)
		if err != nil {
			researchFail(w, err, "read the research agent")
			return
		}
		out := researchGetResponse{ResearchAgent: agents[0], Subagents: []ResearchAgent{}}
		out.Report, out.HasReport, out.States = run.Report, run.Report != "", run.States
		n := 1 + len(kids)
		// The sub-agents are listed newest first; show them in the order they were spawned.
		for i := n - 1; i >= 1; i-- {
			out.Subagents = append(out.Subagents, agents[i])
		}
		if parent != nil {
			out.Parent = &agents[n]
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// ResearchStartOf serves POST /admin/api/research with the JSON body {"topic": "", "user_id": "", "max_steps": 20, "model": "", "dataset": ""}
// (agent_research.Options and the user; only topic is required, user_id is "admin" by default): it starts a deep-research agent, see
// agent_research.Manager.Start, and answers 202 with the agent (status "running", state "starting"). 400 for a bad body or options,
// 409 when the user has too many agents working, 503 without an Anthropic API key.
func ResearchStartOf(m *agent_research.Manager) http.HandlerFunc {
	type request struct {
		UserID string `json:"user_id"`
		agent_research.Options
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, researchStartMaxBody)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			req.UserID = researchDefaultUser
		}
		userID, err := memory_core.ResolveUserID(req.UserID)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		run, err := m.Start(r.Context(), userID, req.Options)
		if err != nil {
			researchFail(w, err, "start the research agent")
			return
		}
		agents, err := researchAgents(r.Context(), m, []core.Run{*run})
		if err != nil {
			researchFail(w, err, "start the research agent")
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, agents[0])
	}
}

// ResearchStopOf serves POST /admin/api/research/{id}/stop: stops an active agent (and its sub-agents), see agent_research.Manager.Stop,
// and answers with the agent as it is then (status "stopped"). 404 for an unknown agent, 409 if it has ended already.
func ResearchStopOf(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := m.Stop(r.Context(), "", id, researchStoppedBy)
		if err != nil {
			researchFail(w, err, "stop the research agent")
			return
		}
		agents, err := researchAgents(r.Context(), m, []core.Run{*run})
		if err != nil {
			researchFail(w, err, "stop the research agent")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, agents[0])
	}
}

// ProvenanceArtifact is an artifact that an agent's session stored: a text it remembered, a chunk of it, or the result of a web search or
// another enrichment made for it.
type ProvenanceArtifact struct {
	ID        int64  `json:"id"`
	DatasetID int64  `json:"dataset_id"`
	Kind      string `json:"kind"`
	// Text is the start of the text of the artifact.
	Text      string    `json:"text"`
	Query     string    `json:"query,omitempty"`
	ParentID  int64     `json:"parent_id,omitempty"`
	RunID     int64     `json:"run_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ProvenanceEntity is an entity that an agent's session discovered (its text mentioned it, even when another source created the entity first).
type ProvenanceEntity struct {
	ID          int64   `json:"id"`
	DatasetID   int64   `json:"dataset_id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Description string  `json:"description,omitempty"`
	RunIDs      []int64 `json:"run_ids"`
}

// ProvenanceEdge is a fact that an agent's session discovered.
type ProvenanceEdge struct {
	ID          int64      `json:"id"`
	DatasetID   int64      `json:"dataset_id"`
	SourceID    int64      `json:"source_id"`
	TargetID    int64      `json:"target_id"`
	Source      string     `json:"source"`
	Target      string     `json:"target"`
	Relation    string     `json:"relation"`
	Description string     `json:"description,omitempty"`
	ValidAt     *time.Time `json:"valid_at,omitempty"`
	InvalidAt   *time.Time `json:"invalid_at,omitempty"`
	ExpiredAt   *time.Time `json:"expired_at,omitempty"`
	// Created says that an artifact of the session created the fact; otherwise the session restated a fact that was stored already.
	Created bool    `json:"created"`
	RunIDs  []int64 `json:"run_ids"`
}

// ProvenanceDataset is a dataset that holds something the agent stored, with how much.
type ProvenanceDataset struct {
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Deleted   bool   `json:"deleted"`
	Artifacts int    `json:"artifacts"`
	Entities  int    `json:"entities"`
	Edges     int    `json:"edges"`
}

// ProvenanceAgent is an agent whose session is in the provenance.
type ProvenanceAgent struct {
	RunID     int64  `json:"run_id"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"session_id"`
}

type researchProvenanceResponse struct {
	RunID int64 `json:"run_id"`
	// IncludeSubagents says that the sessions of the agent's sub-agents (all generations) are included.
	IncludeSubagents bool                 `json:"include_subagents"`
	Agents           []ProvenanceAgent    `json:"agents"`
	Datasets         []ProvenanceDataset  `json:"datasets"`
	Artifacts        []ProvenanceArtifact `json:"artifacts"`
	Entities         []ProvenanceEntity   `json:"entities"`
	Edges            []ProvenanceEdge     `json:"edges"`
}

// researchRunOfSession is the run a session of agent_research.SessionID belongs to (0 if the session is not one of an agent).
func researchRunOfSession(session string) int64 {
	rest, ok := strings.CutPrefix(session, agent_research.SessionPrefix)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// researchAgentSessions returns the agent with the session it stores under, and with includeSubagents its sub-agents too (all
// generations, breadth first: the tree is at most 10 agents). An unknown agent is an error of Kind KindNotFound.
func researchAgentSessions(ctx context.Context, m *agent_research.Manager, id int64, includeSubagents bool) ([]ProvenanceAgent, error) {
	run, err := m.Get(ctx, "", id)
	if err != nil {
		return nil, err
	}
	agents := []ProvenanceAgent{{RunID: id, Name: run.Name, SessionID: agent_research.SessionID(id)}}
	for i := 0; includeSubagents && i < len(agents); i++ {
		kids, err := m.List(ctx, "", agent_research.ListOptions{ParentID: agents[i].RunID, Limit: agent_research.MaxListLimit})
		if err != nil {
			return nil, err
		}
		for j := len(kids) - 1; j >= 0; j-- {
			agents = append(agents, ProvenanceAgent{RunID: kids[j].ID, Name: kids[j].Name, SessionID: agent_research.SessionID(kids[j].ID)})
		}
	}
	return agents, nil
}

// ResearchProvenanceOf serves GET /admin/api/research/{id}/provenance[?include_subagents=true]: what the agent put in the knowledge
// base, whichever dataset it went to: the artifacts it stored, the entities it discovered and the facts (edges), read from its session
// "research-<run id>" (the remember tool attaches it to everything it stores). With include_subagents the sessions of its sub-agents
// are included too, and each item says which run (RunIDs) it comes from. The datasets that hold it are listed with counts, so that
// the UI can open them. Every item is of the dataset it has (DatasetID).
func ResearchProvenanceOf(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out := researchProvenanceResponse{RunID: id, Agents: []ProvenanceAgent{}, Datasets: []ProvenanceDataset{}, Artifacts: []ProvenanceArtifact{}, Entities: []ProvenanceEntity{}, Edges: []ProvenanceEdge{}}
		if v := r.URL.Query().Get("include_subagents"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				httputil.Error(w, http.StatusBadRequest, "include_subagents must be true or false")
				return
			}
			out.IncludeSubagents = b
		}
		ctx := r.Context()
		agents, err := researchAgentSessions(ctx, m, id, out.IncludeSubagents)
		if err != nil {
			researchFail(w, err, "read the research agent")
			return
		}
		out.Agents = agents
		sessions := make([]string, len(out.Agents))
		for i, a := range out.Agents {
			sessions[i] = a.SessionID
		}
		fail := func(err error) {
			log.WithError(err).WithField("run_id", id).Error("admin: research provenance failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the provenance")
		}
		arts, err := graph.ListArtifactsBySessions(ctx, m.DB, sessions)
		if err != nil {
			fail(err)
			return
		}
		ents, err := graph.ListEntitiesBySessions(ctx, m.DB, sessions)
		if err != nil {
			fail(err)
			return
		}
		edges, err := graph.ListEdgesBySessions(ctx, m.DB, sessions)
		if err != nil {
			fail(err)
			return
		}
		datasets := map[int64]*ProvenanceDataset{}
		dataset := func(id int64) *ProvenanceDataset {
			if d := datasets[id]; d != nil {
				return d
			}
			d := &ProvenanceDataset{ID: id}
			datasets[id] = d
			return d
		}
		inSession := map[int64]bool{}
		for _, a := range arts {
			inSession[a.ID] = true
			out.Artifacts = append(out.Artifacts, ProvenanceArtifact{
				ID: a.ID, DatasetID: a.DatasetID, Kind: a.Kind, Text: shorten(a.Text, provenanceTextRunes), Query: a.Query, ParentID: a.ParentID,
				RunID: researchRunOfSession(a.SessionID), CreatedAt: a.CreatedAt,
			})
			dataset(a.DatasetID).Artifacts++
		}
		for _, e := range ents {
			out.Entities = append(out.Entities, ProvenanceEntity{ID: e.ID, DatasetID: e.DatasetID, Name: e.Name, Type: e.Type, Description: e.Description, RunIDs: runsOfSessions(e.Sessions)})
			dataset(e.DatasetID).Entities++
		}
		for _, e := range edges {
			created := e.ArtifactID != nil && inSession[*e.ArtifactID]
			out.Edges = append(out.Edges, ProvenanceEdge{
				ID: e.ID, DatasetID: e.DatasetID, SourceID: e.SourceID, TargetID: e.TargetID, Source: e.Source, Target: e.Target, Relation: e.Relation, Description: e.Description,
				ValidAt: e.ValidAt, InvalidAt: e.InvalidAt, ExpiredAt: e.ExpiredAt, Created: created, RunIDs: runsOfSessions(e.Sessions),
			})
			dataset(e.DatasetID).Edges++
		}
		if len(datasets) > 0 {
			all, err := relational.ListAllDatasets(ctx, m.DB, true)
			if err != nil {
				fail(err)
				return
			}
			for _, ds := range all {
				if d := datasets[ds.ID]; d != nil {
					d.UserID, d.Name, d.Deleted = ds.UserID, ds.Name, ds.Deleted
				}
			}
			for _, ds := range all { // in the order of the datasets
				if d := datasets[ds.ID]; d != nil {
					out.Datasets = append(out.Datasets, *d)
				}
			}
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

func runsOfSessions(sessions []string) []int64 {
	out := make([]int64, 0, len(sessions))
	for _, s := range sessions {
		if id := researchRunOfSession(s); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

type researchTemporalResponse struct {
	RunID            int64 `json:"run_id"`
	IncludeSubagents bool  `json:"include_subagents"`
	// Artifacts are the artifacts that have events, by ID.
	Artifacts []rememberRunTemporalArtifact `json:"artifacts"`
	// Events are what the agent's remember calls did to the facts, oldest first, each with its fact in its current state.
	Events []graph.EdgeEvent `json:"events"`
}

// ResearchTemporalOf serves GET /admin/api/research/{id}/temporal[?include_subagents=true]: what the agent did to the temporal state of
// the facts (gb_edge_events: facts created, confirmed, ended, invalidated or superseded), as the temporal view of a remember run
// (RememberRunTemporalOf) gives it for one call: the events of the artifacts of the session "research-<run id>" (whichever dataset they
// are in) and of their chunks and enrichments, with the reference times of the texts. With include_subagents the sessions of its
// sub-agents are included too. An unknown agent is 404.
func ResearchTemporalOf(m *agent_research.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out := researchTemporalResponse{RunID: id, Artifacts: []rememberRunTemporalArtifact{}, Events: []graph.EdgeEvent{}}
		if v := r.URL.Query().Get("include_subagents"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				httputil.Error(w, http.StatusBadRequest, "include_subagents must be true or false")
				return
			}
			out.IncludeSubagents = b
		}
		ctx := r.Context()
		agents, err := researchAgentSessions(ctx, m, id, out.IncludeSubagents)
		if err != nil {
			researchFail(w, err, "read the research agent")
			return
		}
		sessions := make([]string, len(agents))
		for i, a := range agents {
			sessions[i] = a.SessionID
		}
		fail := func(err error) {
			log.WithError(err).WithField("run_id", id).Error("admin: research temporal failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the temporal events")
		}
		arts, err := graph.ListArtifactsBySessions(ctx, m.DB, sessions)
		if err != nil {
			fail(err)
			return
		}
		// The events of an artifact include those of its descendants (chunks, enrichments), which are in the session too: ask for the
		// roots only, so that no event is read twice.
		inSession := map[int64]bool{}
		for _, a := range arts {
			inSession[a.ID] = true
		}
		for _, a := range arts {
			if a.ParentID != 0 && inSession[a.ParentID] {
				continue
			}
			events, err := graph.ListEdgeEvents(ctx, m.DB, a.ID)
			if err != nil {
				fail(err)
				return
			}
			out.Events = append(out.Events, events...)
		}
		slices.SortFunc(out.Events, func(a, b graph.EdgeEvent) int { return cmp.Compare(a.ID, b.ID) })
		out.Events = slices.CompactFunc(out.Events, func(a, b graph.EdgeEvent) bool { return a.ID == b.ID })
		if out.Artifacts, err = temporalArtifacts(ctx, m.DB, out.Events); err != nil {
			fail(err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
