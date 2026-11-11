package agent_research

// The Modal runner: a research started with Options.Runner = core.RunnerModal does not run its agent in this process. The agent (and
// its sub-agents) is the gligabao_agent binary, run in a Modal container that the sidecar starts (see Remote). The server records the
// run as it does for a local one (gb_agent_research_runs), follows the events the container reports (the states the agents enter, what
// they have spent, and in the end the report) and writes them to the run and its states, so that the REST API, the CLI, MCP and the
// admin UI see no difference but the run's Runner.
//
// What differs from a local run:
//   - The sub-agents are not runs of their own on the server: they run inside the container (in a database of the container), and the
//     states they enter are in the timeline of the run, with the name of the sub-agent in front of the detail ("[worker] ...").
//   - The usage is that of the whole tree of agents (like the totals of gligabao_agent), updated as the container reports it.
//   - The agents work with the knowledge base of the container (empty, and gone when the container ends), not the user's. What they
//     learned comes back when they end: the container hands over an export of its databases (gligabao_agent --export), the follower
//     downloads it and the Importer loads it into the server's own databases (see "importing" below).
//   - The run is started and stopped through the sidecar: a stop asks the container to end, and the run ends when it has.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	log "github.com/sirupsen/logrus"
)

// ImportRun says whose research an export is from.
type ImportRun struct {
	UserID string
	// RunID is the run that the server recorded for the main agent of the export.
	RunID int64
}

// RemoteRequest is what a remote job is started with: the options of the research.
type RemoteRequest struct {
	Topic    string
	MaxSteps int
	Model    string
	Dataset  string
	// Agent is the name of the agent definition to run and Bundle holds it with the definitions its roles start (prompts and skills as
	// text): the container has no agent_defs directory of its own. MaxCostUSD is the cap on the cost of the whole research that the
	// request, the definition and the server give (0: none).
	Agent      string
	Bundle     *agent_defs.Bundle
	MaxCostUSD float64
}

// RemoteEvent is one event of a remote job.
type RemoteEvent struct {
	// Seq is the number of the event in the job's log.
	Seq  int
	Type string
	// Status (RemoteStatus): the job's status. Error says why a failed or cancelled job ended. A completed one has the Report and the
	// Usage of the whole tree.
	Status string
	Error  string
	Report string
	// Agent (RemoteState) is the agent that entered the state: "main" or the name of a sub-agent. StateSeq numbers the states of that
	// agent from 1; an event that repeats a (Agent, StateSeq) already sent adds the Error the state ended with.
	Agent    string
	StateSeq int
	State    string
	Step     int
	Detail   string
	// Message is the text of a RemoteLog event.
	Message string
	// Usage is set on the RemoteUsageEvent and on the completed status.
	Usage *RemoteUsage
	// Export is set on the last status (completed, failed or cancelled) when the agent handed back an export file: what Remote.Export
	// downloads. nil if there is none.
	Export *RemoteExport
}

// RemoteExport describes the export file of a job: its size in bytes and the hex SHA-256 of its content, to check the download.
type RemoteExport struct {
	Bytes  int64
	SHA256 string
}

// RemoteUsage is what the agents of a remote research spent, sub-agents included.
type RemoteUsage struct {
	Steps        int
	ToolCalls    int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Subagents    int
}

// remoteRun is the follower of one job in Modal: it writes what the job reports to the run. Only its own goroutine (work) touches
// the fields below the first group.
type remoteRun struct {
	m      *Manager
	run    *core.Run
	remote Remote
	jobID  string
	logf   *log.Entry
	dbCtx  context.Context
	// capUSD is the cap on the cost of the whole research (0: none). The agent in the container enforces it itself, and the follower
	// ends the job if the usage that it reports goes well over it (remoteCapGrace): a backstop for an agent that does not.
	capUSD float64

	// runCtx ends when the manager's context does or when a stop that could not reach the job ends the follower.
	runCtx context.Context
	stop   context.CancelCauseFunc
	done   chan struct{}
	// cause is the stop that was asked for (Manager.Stop); the job's "cancelled" status then ends the run as stopped.
	cause atomic.Pointer[stopCause]

	cur    *core.State
	states map[remoteStateKey]int64 // the states recorded, to find them again when they end with an error
	usage  RemoteUsage
	from   int  // the number of the next event wanted
	final  bool // the job's last event was handled
}

type remoteStateKey struct {
	agent string
	seq   int
}

// Remote runs researches somewhere else than in this process: the sidecar that starts the gligabao_agent binary in Modal
// (internal/sys/server has the implementation). Events come in the order they happened and are numbered from 0 (RemoteEvent.Seq), so a
// follower that was cut off resumes with the number of the first event it missed.
type Remote interface {
	// Start starts a job for req and returns its ID.
	Start(ctx context.Context, req RemoteRequest) (jobID string, err error)
	// Follow calls fn with every event of the job from the one numbered from on, as they happen. It returns nil when the stream ends
	// (after the job's last event, or when it has been open long enough: the caller reconnects), fn's error if fn fails (which ends it),
	// ctx's error when ctx is done, and an error for a connection that failed or broke.
	Follow(ctx context.Context, jobID string, from int, fn func(RemoteEvent) error) error
	// Cancel asks the job to end. The job reports a RemoteCancelled status when it has.
	Cancel(ctx context.Context, jobID string) error
	// Export writes the export file of the job (RemoteEvent.Export says there is one) to w and returns the number of bytes written. On an
	// error w may have got part of the file.
	Export(ctx context.Context, jobID string, w io.Writer) (int64, error)
}

// Importer loads what the agent of a remote research handed back into the databases of the server (see UseImporter). The server's
// implementation is internal/sys/server (it has the databases, and package dbimport does the work).
type Importer interface {
	// Import loads the export file at path (an archive of package dbexport, written by gligabao_agent --export) for the research run
	// of the user: the knowledge the agent gathered goes to the user's datasets, the runs of its sub-agents become children of run (see
	// dbimport). It returns a line that says what was loaded.
	Import(ctx context.Context, path string, run ImportRun) (summary string, err error)
}

// Types of a RemoteEvent.
const (
	// RemoteStatus: the job's status changed; it is over with RemoteCompleted, RemoteFailed or RemoteCancelled.
	RemoteStatus = "status"
	// RemoteState: an agent entered a state (or a state it was in ended with an error).
	RemoteState = "state"
	// RemoteUsageEvent: what the whole tree of agents has spent so far.
	RemoteUsageEvent = "usage"
	// RemoteLog: a line about what the job does (starting the container, ...).
	RemoteLog = "log"
)

// Statuses of a RemoteStatus event.
const (
	RemoteRunning   = "running"
	RemoteCompleted = "completed"
	RemoteFailed    = "failed"
	RemoteCancelled = "cancelled"
)

const (
	// remoteStartTimeout bounds the call that starts a job.
	remoteStartTimeout = 60 * time.Second
	// remoteCancelTimeout bounds a call that cancels a job.
	remoteCancelTimeout = 10 * time.Second
	// remoteRetryDelay is how long the follower waits before it reconnects after an error (doubled up to remoteMaxRetryDelay).
	remoteRetryDelay    = time.Second
	remoteMaxRetryDelay = 15 * time.Second
)

// Limits of following a job; variables so that tests can shorten them.
var (
	// remoteLostAfter is how long the follower puts up with not reaching the sidecar before it gives the run up as failed.
	remoteLostAfter = 5 * time.Minute
	// remoteWatchLimit is how long a job may take at all (the container's own timeout is shorter: SIDECAR_AGENT_TIMEOUT_S).
	remoteWatchLimit = 3 * time.Hour
	// remoteExportTimeout bounds the download and the import of the export of a job; remoteExportAttempts is how many times the download
	// is tried, remoteExportRetryDelay apart.
	remoteExportTimeout    = 10 * time.Minute
	remoteExportAttempts   = 3
	remoteExportRetryDelay = 2 * time.Second
)

// errRemoteDone ends Follow when the job's last event has been handled.
var errRemoteDone = errors.New("remote job ended")

// UseRemote gives the manager the way to run researches in Modal (see Remote), for the runs started from now on with Options.Runner
// set to core.RunnerModal. Without it (the default) such a start fails with KindUnavailable.
func (m *Manager) UseRemote(r Remote) {
	m.mu.Lock()
	m.remote = r
	m.mu.Unlock()
}

// UseImporter gives the manager the way to load the export of a research that ran in Modal into the server's databases (see Importer),
// for the runs that end from now on. Without it (the default) the export is not downloaded, and what the agent learned in its
// container is lost with it.
func (m *Manager) UseImporter(i Importer) {
	m.mu.Lock()
	m.importer = i
	m.mu.Unlock()
}

// startRemote is Start for a research that runs in Modal: it starts the job, records the run and follows the job in the background.
// The caller has counted the run in m.active (release undoes it). opts are resolved.
func (m *Manager) startRemote(ctx context.Context, userID string, opts Options, def *agent_defs.Def, remote Remote, release func()) (*core.Run, error) {
	bundle, err := m.registry().Bundle(def.Name)
	if err != nil {
		release()
		return nil, invalidErr(err.Error())
	}
	if def.Knowledge.Access == agent_defs.AccessReadOnly {
		log.WithField("agent", def.Name).Warn("agent research: the agent has read-only access to the knowledge base, but in Modal the knowledge base starts empty")
	}
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteStartTimeout)
	defer cancel()
	jobID, err := remote.Start(startCtx, RemoteRequest{Topic: opts.Topic, MaxSteps: opts.MaxSteps, Model: opts.Model, Dataset: opts.Dataset,
		Agent: def.Name, Bundle: bundle, MaxCostUSD: opts.treeCostCap})
	if err != nil {
		release()
		log.WithError(err).WithField("user_id", userID).Warn("agent research: starting the job in Modal failed")
		return nil, unavailableErr("could not start the research in Modal: " + err.Error())
	}

	run := &core.Run{UserID: userID, Topic: opts.Topic, Status: core.StatusRunning, State: core.StateStarting, Model: opts.Model, MaxSteps: opts.MaxSteps, Runner: core.RunnerModal,
		AgentName: def.Name, AgentVersion: def.Version, AgentHash: def.Hash}
	dbCtx := context.WithoutCancel(ctx)
	err = m.tx(dbCtx, func(q db.Execer) error {
		if err := m.Ops.Store.CreateRun(dbCtx, q, run); err != nil {
			return err
		}
		_, err := m.Ops.Store.EnterState(dbCtx, q, run.ID, core.StateStarting, 0, "waiting for the Modal container")
		return err
	})
	if err != nil {
		release()
		// Nobody will follow the job: end it, best effort.
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteCancelTimeout)
		defer cancel()
		if cancelErr := remote.Cancel(cancelCtx, jobID); cancelErr != nil {
			log.WithError(cancelErr).WithField("job_id", jobID).Warn("agent research: could not cancel the job of a run that was not recorded")
		}
		log.WithError(err).WithField("user_id", userID).Error("agent research: recording the run failed")
		return nil, internalErr("failed to record the research", err)
	}
	rec := *run

	rr := newRemoteRun(m, run, remote, jobID, opts.treeCostCap)
	m.mu.Lock()
	m.remotes[run.ID] = rr
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer func() {
			m.mu.Lock()
			delete(m.remotes, run.ID)
			m.mu.Unlock()
		}()
		rr.work()
	}()
	return &rec, nil
}

func newRemoteRun(m *Manager, run *core.Run, remote Remote, jobID string, capUSD float64) *remoteRun {
	rr := &remoteRun{
		m: m, run: run, remote: remote, jobID: jobID, capUSD: capUSD, dbCtx: context.WithoutCancel(m.ctx), done: make(chan struct{}),
		logf:   log.WithFields(log.Fields{"run_id": run.ID, "user_id": run.UserID, "runner": core.RunnerModal, "job_id": jobID}),
		states: map[remoteStateKey]int64{},
	}
	rr.runCtx, rr.stop = context.WithCancelCause(m.ctx)
	return rr
}

// requestStop asks the job to end, for Manager.Stop. If the job cannot be reached the follower ends the run as stopped at once
// (the job may go on in Modal until its own timeout, which the error of the run says).
func (rr *remoteRun) requestStop(ctx context.Context, by string) {
	cause := &stopCause{runID: rr.run.ID, by: by}
	rr.cause.Store(cause)
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteCancelTimeout)
	defer cancel()
	if err := rr.remote.Cancel(cancelCtx, rr.jobID); err != nil {
		rr.logf.WithError(err).Warn("agent research: could not ask the job in Modal to stop; ending the run")
		rr.stop(cause)
	}
}

// work is the goroutine of the run: it follows the job until it ends and ends the run's record.
func (rr *remoteRun) work() {
	defer close(rr.done)
	defer rr.stop(nil)
	defer func() {
		if p := recover(); p != nil {
			rr.logf.Errorf("agent research: following the job panicked: %v", p)
			rr.finish(core.StatusFailed, "", fmt.Sprintf("internal error: %v", p))
		}
	}()
	rr.logf.WithField("topic", truncate(rr.run.Topic, 100)).Info("agent research: started in Modal")

	deadline := time.Now().Add(remoteWatchLimit)
	lastContact := time.Now()
	delay := remoteRetryDelay
	for !rr.final {
		if time.Now().After(deadline) {
			rr.cancelJob()
			rr.finish(core.StatusFailed, "", fmt.Sprintf("the job in Modal did not end within %s", remoteWatchLimit))
			return
		}
		before := rr.from
		err := rr.remote.Follow(rr.runCtx, rr.jobID, rr.from, rr.handle)
		switch {
		case rr.final:
		case rr.ctx().Err() != nil:
			rr.cancelJob() // the job would go on spending money
			rr.finish(core.StatusFailed, "", "interrupted: the server stopped while the agent worked")
			return
		case rr.runCtx.Err() != nil:
			if c := stopCauseOf(rr.runCtx); c != nil {
				rr.finish(core.StatusStopped, "", c.text(rr.run.ID)+" (the job in Modal could not be asked to stop and may run until its timeout)")
			} else {
				rr.finish(core.StatusFailed, "", "cancelled")
			}
			return
		case err == nil || rr.from > before:
			// The stream ended after its time limit, or made progress before it broke: reconnect at once.
			lastContact, delay = time.Now(), remoteRetryDelay
			if err != nil {
				rr.logf.WithError(err).Debug("agent research: the event stream broke; reconnecting")
			}
		default:
			if time.Since(lastContact) > remoteLostAfter {
				rr.logf.WithError(err).Warn("agent research: lost the job in Modal")
				rr.cancelJob()
				rr.finish(core.StatusFailed, "", truncate(fmt.Sprintf("lost contact with the job in Modal: %v", err), maxTextLen))
				return
			}
			rr.logf.WithError(err).Warn("agent research: following the job in Modal failed; retrying")
			select {
			case <-time.After(delay):
			case <-rr.runCtx.Done():
			}
			delay = min(delay*2, remoteMaxRetryDelay)
		}
	}
}

func (rr *remoteRun) ctx() context.Context { return rr.m.ctx }

// cancelJob asks the job to end, best effort (nobody follows it any more).
func (rr *remoteRun) cancelJob() {
	ctx, cancel := context.WithTimeout(rr.dbCtx, remoteCancelTimeout)
	defer cancel()
	if err := rr.remote.Cancel(ctx, rr.jobID); err != nil {
		rr.logf.WithError(err).Warn("agent research: could not cancel the job in Modal")
	}
}

// handle is what a job's event does to the run.
func (rr *remoteRun) handle(ev RemoteEvent) error {
	rr.from = ev.Seq + 1
	switch ev.Type {
	case RemoteState:
		rr.state(ev)
	case RemoteUsageEvent:
		if ev.Usage != nil {
			rr.saveUsage(*ev.Usage)
			rr.checkCap(ev.Usage.CostUSD)
		}
	case RemoteLog:
		rr.logf.Info("agent research: modal: " + truncate(ev.Message, maxTextLen))
	case RemoteStatus:
		return rr.status(ev)
	}
	return nil
}

// remoteCapGrace is how far over the cost cap the usage of a remote job may go before the follower ends the job: the agent stops itself at
// the cap, but a call that is running when it is reached still adds its cost.
const remoteCapGrace = 1.25

// checkCap ends the job, as stopped by the cap, when it has spent well over what the cap allows.
func (rr *remoteRun) checkCap(costUSD float64) {
	if rr.capUSD <= 0 || costUSD <= rr.capUSD*remoteCapGrace || rr.cause.Load() != nil {
		return
	}
	rr.logf.Warnf("agent research: the job in Modal spent $%.4f, over its cost cap of $%.2f: ending it", costUSD, rr.capUSD)
	rr.cause.Store(&stopCause{runID: rr.run.ID, by: fmt.Sprintf("the cost cap ($%.2f): the agent went well over it", rr.capUSD)})
	rr.cancelJob()
}

// status handles a status event; the terminal ones end the run.
func (rr *remoteRun) status(ev RemoteEvent) error {
	if ev.Usage != nil {
		rr.saveUsage(*ev.Usage)
	}
	switch ev.Status {
	case RemoteCompleted, RemoteFailed, RemoteCancelled:
		// What the agent learned comes back before the run ends, also if it failed or was stopped: the run is not over for the user
		// until the knowledge is in.
		if ev.Export != nil {
			rr.importExport(*ev.Export)
		}
	}
	switch ev.Status {
	case RemoteCompleted:
		if strings.TrimSpace(ev.Report) == "" {
			rr.finish(core.StatusFailed, "", "the agent ended without a report")
		} else {
			rr.finish(core.StatusCompleted, ev.Report, "")
		}
	case RemoteFailed:
		rr.finish(core.StatusFailed, "", truncate(orDefault(ev.Error, "the job in Modal failed"), maxTextLen))
	case RemoteCancelled:
		if c := rr.cause.Load(); c != nil {
			rr.finish(core.StatusStopped, "", c.text(rr.run.ID))
		} else {
			rr.finish(core.StatusFailed, "", truncate("cancelled: "+orDefault(ev.Error, "the job in Modal was cancelled"), maxTextLen))
		}
	default:
		return nil // running
	}
	rr.final = true
	return errRemoteDone
}

// importExport downloads the export of the job and has the Importer load it, as the state "importing" of the run. A failure is said on
// that state and in the log, and does not change how the run ends: the agent's report is the result, the export is its by-product.
func (rr *remoteRun) importExport(ex RemoteExport) {
	rr.m.mu.Lock()
	importer := rr.m.importer
	rr.m.mu.Unlock()
	if importer == nil {
		rr.logf.Info("agent research: the agent handed back an export, but this server cannot import it")
		return
	}
	detail := fmt.Sprintf("loading what the agent learned (%s)", byteSize(ex.Bytes))
	err := rr.tx(func(q db.Execer) (err error) {
		rr.cur, err = rr.m.Ops.Store.EnterState(rr.dbCtx, q, rr.run.ID, core.StateImporting, 0, detail)
		return err
	})
	if err != nil {
		rr.logf.WithError(err).Warn("agent research: recording the state failed")
		rr.cur = nil
	}
	started := time.Now()
	summary, err := rr.fetchAndImport(importer, ex)
	var errText string
	if err != nil {
		errText = truncate("the export of the agent was not imported: "+err.Error(), maxTextLen)
		rr.logf.WithError(err).Warn("agent research: the export of the agent was not imported")
	} else {
		rr.logf.WithField("took", time.Since(started).Round(time.Millisecond)).Info("agent research: the export of the agent was imported: " + summary)
	}
	if rr.cur != nil {
		rr.closeCurrent(errText)
	}
}

// fetchAndImport downloads the export to a temporary file, checks it against what the job said it is, and imports it.
func (rr *remoteRun) fetchAndImport(importer Importer, ex RemoteExport) (string, error) {
	ctx, cancel := context.WithTimeout(rr.runCtx, remoteExportTimeout)
	defer cancel()
	f, err := os.CreateTemp("", "gligabao-agent-export-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()

	var lastErr error
	for attempt := 1; attempt <= remoteExportAttempts; attempt++ {
		if lastErr = rr.download(ctx, f, ex); lastErr == nil {
			break
		}
		rr.logf.WithError(lastErr).Warnf("agent research: downloading the export failed (attempt %d of %d)", attempt, remoteExportAttempts)
		if attempt == remoteExportAttempts {
			return "", lastErr
		}
		select {
		case <-time.After(remoteExportRetryDelay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return importer.Import(ctx, f.Name(), ImportRun{UserID: rr.run.UserID, RunID: rr.run.ID})
}

// download writes the export to f (from its start) and checks its size and SHA-256.
func (rr *remoteRun) download(ctx context.Context, f *os.File, ex RemoteExport) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	n, err := rr.remote.Export(ctx, rr.jobID, io.MultiWriter(f, h))
	if err != nil {
		return err
	}
	if ex.Bytes > 0 && n != ex.Bytes {
		return fmt.Errorf("the export is %d bytes, the job said %d", n, ex.Bytes)
	}
	if want := strings.ToLower(strings.TrimSpace(ex.SHA256)); want != "" && hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New("the export does not match its SHA-256")
	}
	return nil
}

// byteSize is a size for a person: 12 bytes, 3.4 KB, 5.6 MB.
func byteSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// state records a state an agent of the job entered, or the error a state it entered before ended with. The starting and terminal
// states of the agents are not recorded: the run has its own.
func (rr *remoteRun) state(ev RemoteEvent) {
	switch ev.State {
	case "", core.StateStarting, core.StateCompleted, core.StateFailed, core.StateStopped:
		return
	}
	agent := orDefault(ev.Agent, "main")
	key := remoteStateKey{agent, ev.StateSeq}
	if id, known := rr.states[key]; known {
		// The state ended with an error after it was reported. Only the state the run is in can still be told about it.
		if ev.Error != "" && rr.cur != nil && rr.cur.ID == id {
			rr.closeCurrent(ev.Error)
		}
		return
	}
	detail := ev.Detail
	if agent != "main" {
		detail = "[" + agent + "] " + detail
	}
	detail = truncate(detail, maxTextLen)
	err := rr.tx(func(q db.Execer) (err error) {
		rr.cur, err = rr.m.Ops.Store.EnterState(rr.dbCtx, q, rr.run.ID, ev.State, ev.Step, detail)
		return err
	})
	if err != nil {
		rr.logf.WithError(err).WithField("state", ev.State).Warn("agent research: recording the state failed")
		return
	}
	rr.states[key] = rr.cur.ID
	if ev.Error != "" {
		rr.closeCurrent(ev.Error)
	}
}

func (rr *remoteRun) closeCurrent(errText string) {
	if err := rr.m.Ops.Store.CloseState(rr.dbCtx, rr.m.DB, rr.cur.ID, truncate(errText, maxTextLen)); err != nil {
		rr.logf.WithError(err).Warn("agent research: ending the state failed")
	}
}

func (rr *remoteRun) saveUsage(u RemoteUsage) {
	rr.usage = u
	if err := rr.m.Ops.Store.UpdateUsage(rr.dbCtx, rr.m.DB, rr.run.ID, usageOfRemote(u)); err != nil {
		rr.logf.WithError(err).Warn("agent research: recording the usage failed")
	}
}

func usageOfRemote(u RemoteUsage) db.Usage {
	return db.Usage{Steps: u.Steps, ToolCalls: u.ToolCalls, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: u.CostUSD}
}

// finish ends the run's record, like researcher.finish.
func (rr *remoteRun) finish(status core.Status, report, errText string) {
	err := rr.tx(func(q db.Execer) error {
		if err := rr.m.Ops.Store.UpdateUsage(rr.dbCtx, q, rr.run.ID, usageOfRemote(rr.usage)); err != nil {
			return err
		}
		_, err := rr.m.Ops.Store.FinishRun(rr.dbCtx, q, rr.run.ID, status, report, errText)
		return err
	})
	if err != nil {
		rr.logf.WithError(err).Error("agent research: ending the run failed")
	}
	rr.logf.WithField("status", status).Info("agent research: ended")
}

func (rr *remoteRun) tx(fn func(q db.Execer) error) error { return rr.m.tx(rr.dbCtx, fn) }
