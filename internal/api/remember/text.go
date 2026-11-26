package remember

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// The typed layer of remembering a text: Remember takes a Request (the same struct the JSON body of POST /v1/gb/remember decodes
// into) and does everything between the request and the pipeline: it validates the request, assembles the options of the call, runs
// it on the remember manager and records the monitoring events. The HTTP handler is a decoding of the body and a mapping of the
// errors of Remember to status codes; the evals call Remember directly, with no HTTP in between.

// InvalidError is the error of a request that is not valid (the handler answers 400): Err says what is wrong.
type InvalidError struct {
	Err error
}

func (e *InvalidError) Error() string { return e.Err.Error() }

func (e *InvalidError) Unwrap() error { return e.Err }

// UnavailableError is the error of a request that needs something this server does not have (the handler answers 503), e.g. web
// search or timeseries providers.
type UnavailableError struct {
	Err error
}

func (e *UnavailableError) Error() string { return e.Err.Error() }

func (e *UnavailableError) Unwrap() error { return e.Err }

// ErrNotQueued is the error of a run that the remember manager did not accept (the handler answers 503). The error returned wraps
// the reason too, which the handler does not tell.
var ErrNotQueued = errors.New("remember is not available")

// invalid wraps err as an *InvalidError.
func invalid(err error) error { return &InvalidError{Err: err} }

// prepared is a remember request that was validated, as the manager takes it.
type prepared struct {
	req       memory_remember.Request
	sessionID string
}

// Remember stores the text of req as userID: it validates req, sends it to the remember manager mgr, which runs the pipeline, and
// waits for the run to end. d is the database the ontologies of a grounding request are read from. opts are the options of every
// call (see api.RememberOptions); memory_core.WithEval turns on the ATIF trajectory of the call, in the result, and makes
// classify_graph default to off, because the trajectories and costs of an eval must not change under an eval that did not ask for it.
//
// The errors: *InvalidError (the request is not valid), *UnavailableError (it needs something this server does not have),
// ErrNotQueued (the manager did not take it), memory_timeseries.ErrAnchor (a series could not be anchored; nothing was stored), or
// the error of the pipeline. An image is not remembered here (see Handler).
func Remember(ctx context.Context,
	mgr *memory_remember.Manager,
	d *sql.DB,
	userID string,
	req Request,
	opts []memory_core.Option) (*memory_remember.Result, error) {
	p, err := prepare(ctx,
		d,
		userID,
		req,
		opts)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	// Send the request to the remember manager, which answers at once with the ID of the run; then block on the outcome
	// channel, which the manager sends the result to when the run is over.
	outcome := make(chan memory_remember.Outcome, 1)
	p.req.Ctx, p.req.Outcome = ctx, outcome
	runID, err := mgr.Submit(p.req)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"user_id": userID, "dataset": req.Dataset}).Error("remember not queued")
		return nil, fmt.Errorf("%w: %v",
			ErrNotQueued,
			err)
	}
	out := <-outcome
	res, err := out.Result, out.Err
	if errors.Is(err, memory_timeseries.ErrAnchor) { // nothing was stored
		return nil, err
	}
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"user_id":         userID,
			"dataset":         req.Dataset,
			"session_id":      p.sessionID,
			"remember_run_id": runID,
		}).Error("remember failed")
		monitoring.EmitSystemEvent(ctx,
			"remember.failed",
			monitoring.Event{
				"user_id":         userID,
				"dataset":         req.Dataset,
				"session_id":      p.sessionID,
				"remember_run_id": runID,
				"text_bytes":      len(p.req.Text),
				"duration_ms":     time.Since(start).Milliseconds(),
				"error":           err.Error(),
			})
		return nil, err
	}
	log.WithFields(log.Fields{
		"user_id":             res.UserID,
		"dataset":             res.Dataset,
		"artifact_id":         res.ArtifactID,
		"session_id":          res.SessionID,
		"kind":                res.Kind,
		"chunks":              len(res.Chunks),
		"entities":            len(res.Entities),
		"edges":               len(res.Edges),
		"web_searches":        len(res.WebSearches),
		"series":              len(res.Series),
		"series_fetches":      len(res.SeriesFetches),
		"series_observations": len(res.SeriesObservations),
		"duration_ms":         time.Since(start).Milliseconds(),
	}).Debug("remember completed")
	monitoring.EmitSystemEvent(ctx,
		"remember.completed",
		monitoring.Event{
			"user_id":             res.UserID,
			"dataset":             res.Dataset,
			"artifact_id":         res.ArtifactID,
			"session_id":          res.SessionID,
			"kind":                res.Kind,
			"text_bytes":          len(p.req.Text),
			"chunks":              len(res.Chunks),
			"entities":            len(res.Entities),
			"edges":               len(res.Edges),
			"invalidated_edges":   len(res.InvalidatedEdges),
			"web_searches":        len(res.WebSearches),
			"series":              len(res.Series),
			"series_fetches":      len(res.SeriesFetches),
			"series_observations": len(res.SeriesObservations),
			"duration_ms":         time.Since(start).Milliseconds(),
		})
	return res, nil
}

// prepare validates the request for a text and turns it into the manager's request, with the options of the call. The checks run in a
// fixed order, so the first problem is the one reported. Its errors are those of Remember before the run.
func prepare(ctx context.Context,
	d *sql.DB,
	userID string,
	req Request,
	opts []memory_core.Option) (prepared, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return prepared{}, invalid(errors.New("text is required"))
	}
	if len(text) > maxRememberBytes {
		return prepared{}, invalid(errors.New("text is larger than 1 MiB"))
	}
	var ref time.Time
	if req.ReferenceTime != "" {
		var err error
		if ref, err = relational.ParseTimeStrict(req.ReferenceTime); err != nil {
			return prepared{}, invalid(errors.New("reference_time must be RFC 3339, e.g. 2025-01-31T09:00:00Z"))
		}
	}
	if _, err := memory_core.ResolveUserID(userID); err != nil {
		return prepared{}, invalid(err)
	}
	if _, err := memory_core.ResolveDatasetName(req.Dataset); err != nil {
		return prepared{}, invalid(err)
	}
	if _, err := memory_remember.ResolveKind(req.Kind); err != nil {
		return prepared{}, invalid(err)
	}
	sessionID, err := memory_core.ResolveSessionID(req.SessionID)
	if err != nil {
		return prepared{}, invalid(err)
	}
	graphIndex, err := resolveGraphIndex(req.GraphIndex, opts)
	if err != nil {
		return prepared{}, invalid(err)
	}
	contentType, err := memory_core.ResolveContentType(req.ContentType)
	if err != nil {
		return prepared{}, invalid(err)
	}
	models, err := memory_core.ResolveModels(req.Models)
	if err != nil {
		return prepared{}, invalid(err)
	}
	reasoning, err := memory_core.ResolveReasoning(req.Reasoning)
	if err != nil {
		return prepared{}, invalid(err)
	}
	call := memory_core.NewCallOptions(opts)
	// On by default, except on a call made with WithEval, whose trajectories and costs must not change under an eval that did not ask for it.
	classifyGraph := req.ClassifyGraph != nil && *req.ClassifyGraph || req.ClassifyGraph == nil && !call.Eval
	graphClasses, err := resolveGraphClasses(classifyGraph, req.GraphClasses)
	if err != nil {
		return prepared{}, invalid(err)
	}
	groundOpts, err := resolveGrounding(ctx,
		d,
		userID,
		req)
	if err != nil {
		return prepared{}, invalid(err)
	}
	if req.WebSearch && call.WebSearcher == nil {
		return prepared{}, &UnavailableError{Err: memory_core.ErrWebSearchUnavailable}
	}
	series, fetch, observe, err := resolveTimeseries(req.Timeseries, call.SeriesProviders)
	if errors.Is(err, memory_core.ErrSeriesProvidersUnavailable) {
		return prepared{}, &UnavailableError{Err: err}
	}
	if err != nil {
		return prepared{}, invalid(err)
	}
	return prepared{
		req: memory_remember.Request{
			UserID:  userID,
			Dataset: req.Dataset,
			Kind:    req.Kind,
			Text:    text,
			Ref:     ref,
			Opts: append(append(opts[:len(opts):len(opts)], groundOpts...),
				memory_core.WithSessionID(sessionID),
				memory_core.WithGraphIndex(graphIndex),
				memory_core.WithContentType(contentType),
				memory_core.WithModels(models),
				memory_core.WithReasoning(reasoning),
				memory_core.WithWebSearch(req.WebSearch),
				memory_core.WithGeoSync(req.GeoSync),
				memory_core.WithClassifyGraph(classifyGraph, graphClasses),
				memory_core.WithSeries(series),
				memory_core.WithSeriesFetch(fetch),
				memory_core.WithSeriesObservations(observe)),
		},
		sessionID: sessionID,
	}, nil
}
