package server

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbimport"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
)

// sidecarRemote is how the research manager runs researches in Modal (agent_research.Remote): through the sidecar, which runs the
// gligabao_agent binary in a Modal container. The Go server never talks to Modal itself. It uses the client of the configured sidecar
// (sidecarClient), looked up at each call, so a sidecar that was down at startup is used once it is up.
type sidecarRemote struct{}

var _ agent_research.Remote = sidecarRemote{}

func (sidecarRemote) client() (*sidecar.Client, error) {
	c := sidecarClient.Load()
	if c == nil {
		return nil, errors.New("sidecar not configured")
	}
	return c, nil
}

func (r sidecarRemote) Start(ctx context.Context, req agent_research.RemoteRequest) (string, error) {
	c, err := r.client()
	if err != nil {
		return "", err
	}
	sreq := sidecar.StartResearchJobRequest{Topic: req.Topic, MaxSteps: req.MaxSteps, Model: req.Model, Dataset: req.Dataset, Agent: req.Agent, MaxCostUSD: req.MaxCostUSD}
	if req.Bundle != nil {
		if sreq.AgentBundle, err = req.Bundle.JSON(); err != nil {
			return "", err
		}
	}
	job, err := c.StartResearchJob(ctx, sreq)
	var se *sidecar.StatusError
	if errors.As(err, &se) {
		return "", errors.New(se.Message()) // the sidecar's own words (e.g. the agent is not deployed), not the whole request line
	}
	return job.JobID, err
}

func (r sidecarRemote) Follow(ctx context.Context, jobID string, from int, fn func(agent_research.RemoteEvent) error) error {
	c, err := r.client()
	if err != nil {
		return err
	}
	return c.StreamResearchJobEvents(ctx, jobID, from, func(ev sidecar.ResearchEvent) error {
		return fn(remoteEventOf(ev))
	})
}

// Cancel asks the sidecar to stop the job. A job that is over already (409) is not an error: its last event is on its way.
func (r sidecarRemote) Cancel(ctx context.Context, jobID string) error {
	c, err := r.client()
	if err != nil {
		return err
	}
	err = c.CancelResearchJob(ctx, jobID)
	var se *sidecar.StatusError
	if errors.As(err, &se) && se.Code == http.StatusConflict {
		return nil
	}
	return err
}

// Export downloads the export file of the job from the sidecar (what the agent in the container wrote with --export).
func (r sidecarRemote) Export(ctx context.Context, jobID string, w io.Writer) (int64, error) {
	c, err := r.client()
	if err != nil {
		return 0, err
	}
	n, err := c.DownloadResearchExport(ctx, jobID, w)
	var se *sidecar.StatusError
	if errors.As(err, &se) {
		return n, errors.New(se.Message())
	}
	return n, err
}

// dbImporter loads the export of an agent that ran in Modal into the server's databases (agent_research.Importer): its knowledge base
// goes to the datasets of the user that started the research, and the runs of its sub-agents are recorded under the research's run.
type dbImporter struct{ d *db.DB }

var _ agent_research.Importer = dbImporter{}

func (i dbImporter) Import(ctx context.Context, path string, run agent_research.ImportRun) (string, error) {
	r, err := dbexport.Open(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	res, err := dbimport.Import(ctx, r, dbimport.Target{
		Relational:          i.d.Relational,
		Backend:             i.d.Backend,
		Vector:              i.d.Vector,
		EmbeddingDimensions: i.d.EmbeddingDimensions,
		ImageDir:            i.d.ImageDir,
	}, dbimport.Options{UserID: run.UserID, RootRunID: run.RunID, SessionPrefix: agent_research.SessionPrefix, Runner: core.RunnerModal})
	if err != nil {
		return "", err
	}
	return res.Summary(), nil
}

func remoteEventOf(ev sidecar.ResearchEvent) agent_research.RemoteEvent {
	out := agent_research.RemoteEvent{
		Seq: ev.Seq, Type: ev.Type, Status: ev.Status, Error: ev.Error, Report: ev.Report,
		Agent: ev.Agent, StateSeq: ev.StateSeq, State: ev.State, Step: ev.Step, Detail: ev.Detail, Message: ev.Message,
	}
	if x := ev.Export; x != nil {
		out.Export = &agent_research.RemoteExport{Bytes: x.Bytes, SHA256: x.SHA256}
	}
	if u := ev.Usage; u != nil {
		out.Usage = &agent_research.RemoteUsage{Steps: u.Steps, ToolCalls: u.ToolCalls, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: u.CostUSD, Subagents: u.Subagents}
	}
	return out
}
