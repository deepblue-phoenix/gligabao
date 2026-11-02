package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_memory"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

// app is what the server builds to run research agents, without the server: the databases (the knowledge base tables and the
// research run tables), the remember manager, the knowledge base of the agents over them, and the research manager that runs the
// agents and their sub-agents and records every run in the database.
type app struct {
	database *db.DB
	mem      *agent_memory.Memory
	remember *memory_remember.Manager
	research *agent_research.Manager
	stop     context.CancelFunc
}

// openApp opens the databases described by cfg (the SQLite file at sqlitePath, or its default) and builds the app over them. llm
// runs the agents and the knowledge base's own model calls; emb embeds. Call close when done (after the research ended).
func openApp(ctx context.Context, cfg *config.Config, llm *ai.Client, emb memory_core.Embedder, sqlitePath, vectorDir string) (*app, error) {
	dbCfg := db.ConfigFrom(cfg, sqlitePath, vectorDir, "")
	dbCfg.Geo = cfg.Geo
	dbCfg.Models = ai.SupportedModels(ai.NewPriceTable(cfg.AIPrices()))
	// Not dbfull: the tables of training and evals are not created here, only those of the research runs next to the knowledge base.
	dbCfg.Schemas = []db.Schema{agent_research.Open}
	database, err := db.Open(dbCfg)
	if err != nil {
		return nil, err
	}
	log.WithFields(log.Fields{"backend": database.Backend, "sqlite_path": database.SQLitePath, "vector_store": database.VectorStore, "vector_dir": database.VectorDir}).Info("databases opened")
	ops, err := agent_research.OpsFor(database.Backend)
	if err != nil {
		database.Close()
		return nil, err
	}

	var geo memory_geo.Resolver
	if cfg.Geo {
		geo = memory_geo.Default()
	}
	// The same options as the remember and query endpoints of the server (internal/api), less what the agents cannot ask for: images,
	// web search of remember, timeseries providers, the hooks.
	rememberOpts := []memory_core.Option{memory.WithVectors(database.Vector, emb), memory_core.WithChunking(database.Chunking), memory_core.WithGeoResolver(geo)}
	queryOpts := []memory_core.Option{memory.WithVectors(database.Vector, emb), memory_core.WithGeoResolver(geo), memory_core.WithGeoRadius(cfg.GeoRadiusKm)}

	mgrCtx, stop := context.WithCancel(ctx)
	a := &app{database: database, stop: stop}
	a.remember = memory_remember.NewManager(mgrCtx, database.Relational, llm)
	a.mem = &agent_memory.Memory{Remember: a.remember, DB: database.Relational, LLM: llm, RememberOpts: rememberOpts, QueryOpts: queryOpts}
	// KeepActiveRuns: the runs that are running in this database belong to other processes (a server, other runs of this binary).
	a.research = agent_research.NewManager(mgrCtx, database.Relational, ops, llm, agent_research.KeepActiveRuns())
	a.research.UseMemory(a.mem)
	return a, nil
}

// close stops the managers and closes the databases. The agents and the remember calls should be over (see runResearch).
func (a *app) close() {
	a.stop()
	a.research.Wait()
	a.remember.Wait()
	if err := a.database.Close(); err != nil {
		log.WithError(err).Warn("closing the databases")
	}
}

// captureTrajectory makes the agents of the researches started from now on record ATIF trajectories (the ones of the sub-agents merged
// into the main agent's), and returns a function that gives the trajectory of the last research that ended (nil until one does).
func (a *app) captureTrajectory() func() *atif.Trajectory {
	var mu sync.Mutex
	var got *atif.Trajectory
	a.research.UseTrajectories(func(_ int64, t *atif.Trajectory) {
		mu.Lock()
		got = t
		mu.Unlock()
	})
	return func() *atif.Trajectory {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// outcome is how a research ended: the run of the agent that was started (the root, with its report) and the runs of its sub-agents,
// at every depth.
type outcome struct {
	Root core.Run
	Kids []core.Run
	// DrainErr is set if the text the agents remembered without waiting was not all absorbed when the wait ended.
	DrainErr error
}

// totals are the steps, tool calls, tokens and cost of the whole tree of agents (the run of each agent has its own only).
func (o outcome) totals() (steps, toolCalls, in, out int, cost float64) {
	for _, r := range append([]core.Run{o.Root}, o.Kids...) {
		steps += r.Steps
		toolCalls += r.ToolCalls
		in += r.InputTokens
		out += r.OutputTokens
		cost += r.CostUSD
	}
	return
}

// runResearch starts an agent on opts.Topic and returns when it, its sub-agents and the remember calls it made without waiting are over
// (or ctx is done, which fails the agents). With state or usage set, they are called as the agents go (see followStates). The error
// is one of starting the research; how it went is in the Root run (Status, Report, Error).
func (a *app) runResearch(ctx context.Context, userID string, opts agent_research.Options, state func(stateEvent), usage func(totals)) (*outcome, error) {
	root, err := a.research.Start(ctx, userID, opts)
	if err != nil {
		return nil, err
	}
	var stopProgress func()
	if state != nil || usage != nil {
		stopProgress = a.followStates(ctx, userID, root.ID, state, usage)
	}
	a.research.Wait()
	out := &outcome{DrainErr: a.mem.Drain(ctx)}
	if stopProgress != nil {
		stopProgress()
	}
	dbCtx := context.WithoutCancel(ctx) // the runs are ended and recorded even if ctx is done
	run, err := a.research.Get(dbCtx, userID, root.ID)
	if err != nil {
		return nil, err
	}
	out.Root = *run
	if out.Kids, err = a.subagents(dbCtx, userID, root.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// subagents returns the runs of the sub-agents of run id, and theirs, at every depth.
func (a *app) subagents(ctx context.Context, userID string, id int64) ([]core.Run, error) {
	var all []core.Run
	queue := []int64{id}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		kids, err := a.research.List(ctx, userID, agent_research.ListOptions{ParentID: parent, Limit: agent_research.MaxListLimit})
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			all = append(all, k)
			queue = append(queue, k.ID)
		}
	}
	return all, nil
}

// stateEvent is a state an agent of the research entered (Update false), or the error a state that was reported before ended with
// (Update true; the same Agent and Seq).
type stateEvent struct {
	Agent  string // "main", or the name of the sub-agent
	Seq    int    // the number of the state among those of the agent
	State  string
	Step   int
	Detail string
	Error  string
	Update bool
}

// followStates calls state with the states the agents of the research enter, and usage with the totals of the whole tree of agents each
// time they change (either may be nil), from the records in the database, polled; until the returned function is called, which does
// the last poll.
func (a *app) followStates(ctx context.Context, userID string, rootID int64, state func(stateEvent), usage func(totals)) (stop func()) {
	seen := map[int64]int{}         // the last state of each run reported
	reported := map[[2]int64]bool{} // the states reported with their error: (run, seq)
	var last totals
	poll := func(ctx context.Context) {
		runs, err := a.subagents(ctx, userID, rootID)
		if err != nil {
			return
		}
		root, err := a.research.Get(ctx, userID, rootID)
		if err != nil {
			return
		}
		runs = append([]core.Run{*root}, runs...)
		var sum totals
		for _, r := range runs {
			full, err := a.research.Get(ctx, userID, r.ID)
			if err != nil {
				continue
			}
			sum.add(full)
			who := "main"
			if r.Name != "" {
				who = r.Name
			}
			for _, s := range full.States {
				key := [2]int64{r.ID, int64(s.Seq)}
				ev := stateEvent{Agent: who, Seq: s.Seq, State: s.State, Step: s.Step, Detail: s.Detail, Error: s.Error}
				switch {
				case s.Seq > seen[r.ID]:
					seen[r.ID] = s.Seq
					reported[key] = s.Error != ""
				case s.Error != "" && !reported[key]:
					reported[key], ev.Update = true, true
				default:
					continue
				}
				if state != nil {
					state(ev)
				}
			}
		}
		sum.subagents = len(runs) - 1
		if usage != nil && sum != last {
			last = sum
			usage(sum)
		}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				poll(ctx)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		poll(context.WithoutCancel(ctx))
	}
}

// totals are what the whole tree of agents of a research has spent so far.
type totals struct {
	steps, toolCalls, inputTokens, outputTokens, subagents int
	costUSD                                                float64
}

func (t *totals) add(r *core.Run) {
	t.steps += r.Steps
	t.toolCalls += r.ToolCalls
	t.inputTokens += r.InputTokens
	t.outputTokens += r.OutputTokens
	t.costUSD += r.CostUSD
}

// textProgress writes the states the agents enter as lines of text to w (--verbose); the errors that states end with later are not.
func textProgress(w io.Writer) func(stateEvent) {
	return func(ev stateEvent) {
		if ev.Update {
			return
		}
		who := "agent"
		if ev.Agent != "main" {
			who = "sub-agent " + ev.Agent
		}
		line := fmt.Sprintf("[%s] %s", who, ev.State)
		if ev.Detail != "" {
			line += ": " + ev.Detail
		}
		if ev.Error != "" {
			line += " (error: " + ev.Error + ")"
		}
		fmt.Fprintln(w, line)
	}
}

// jsonEvents writes the progress of the research as JSON lines to w (--events), the protocol of the Modal runner: a line is
// {"event":"state",...} for every state an agent enters (and once more, with the error, for a state that ended with one after it was
// reported) and {"event":"usage",...} whenever the totals of the tree of agents change. Lines are written one at a time.
func jsonEvents(w io.Writer) (state func(stateEvent), usage func(totals)) {
	var mu sync.Mutex
	write := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s\n", b)
	}
	state = func(ev stateEvent) {
		write(map[string]any{"event": "state", "agent": ev.Agent, "state_seq": ev.Seq, "state": ev.State, "step": ev.Step, "detail": ev.Detail, "error": ev.Error})
	}
	usage = func(t totals) {
		write(map[string]any{"event": "usage", "steps": t.steps, "tool_calls": t.toolCalls, "input_tokens": t.inputTokens, "output_tokens": t.outputTokens,
			"cost_usd": t.costUSD, "subagents": t.subagents})
	}
	return state, usage
}
