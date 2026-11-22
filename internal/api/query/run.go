package query

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// The typed layer of querying: Run takes a Request (the same struct the JSON body of POST /v1/gb/query decodes into) and does everything
// between the request and the pipeline: it validates the request, assembles the options of the call, runs it and records the monitoring
// events. The HTTP handler is a decoding of the body and a mapping of the errors of Run to status codes; the evals call Run directly,
// with no HTTP in between.

// InvalidError is the error of a request that is not valid (the handler answers 400): Err says what is wrong.
type InvalidError struct {
	Err error
}

func (e *InvalidError) Error() string { return e.Err.Error() }

func (e *InvalidError) Unwrap() error { return e.Err }

// invalid wraps err as an *InvalidError.
func invalid(err error) error { return &InvalidError{Err: err} }

// prepared is a query request that was validated: the question and the options of the call.
type prepared struct {
	query     string
	sessionID string
	opts      []memory_core.Option
}

// Run answers req as userID, from the dataset it names: it validates req, then runs the query pipeline (memory_query.Query). opts are
// the options of every call (see api.QueryOptions); memory_core.WithEval turns on the ATIF trajectory of the call, in the result.
//
// The errors: *InvalidError (the request is not valid), or those of memory_query.Query (memory_core.ErrInvalidDataset,
// ErrInvalidUser, ErrInvalidGeoRadius, ErrDatasetNotFound, or the failure of the pipeline).
func Run(ctx context.Context,
	d *sql.DB,
	llm memory_core.LLM,
	userID string,
	req Request,
	opts []memory_core.Option) (*memory_query.QueryResult, error) {
	p, err := prepare(req, opts)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := memory_query.Query(ctx,
		d,
		llm,
		userID,
		req.Dataset,
		p.query,
		p.opts...)
	if err != nil {
		monitoring.EmitSystemEvent(ctx,
			"query.failed",
			monitoring.Event{
				"user_id":     userID,
				"dataset":     req.Dataset,
				"session_id":  p.sessionID,
				"query_bytes": len(p.query),
				"duration_ms": time.Since(start).Milliseconds(),
				"error":       err.Error(),
			})
		if !errors.Is(err, memory_core.ErrInvalidDataset) && !errors.Is(err, memory_core.ErrInvalidUser) &&
			!errors.Is(err, memory_core.ErrInvalidGeoRadius) && !errors.Is(err, memory_core.ErrDatasetNotFound) {
			log.WithError(err).WithFields(log.Fields{
				"user_id":    userID,
				"dataset":    req.Dataset,
				"session_id": p.sessionID,
			}).Error("query failed")
		}
		return nil, err
	}
	log.WithFields(log.Fields{
		"user_id":     userID,
		"dataset":     req.Dataset,
		"session_id":  p.sessionID,
		"duration_ms": time.Since(start).Milliseconds(),
	}).Debug("query completed")
	monitoring.EmitSystemEvent(ctx,
		"query.completed",
		monitoring.Event{
			"user_id":           userID,
			"dataset":           req.Dataset,
			"session_id":        p.sessionID,
			"query_bytes":       len(p.query),
			"evidence_entities": len(res.Evidence.Entities),
			"evidence_edges":    len(res.Evidence.Edges),
			"duration_ms":       time.Since(start).Milliseconds(),
		})
	return res, nil
}

// prepare validates the request and turns it into the question and the options of the call. The checks run in a fixed order, so the
// first problem is the one reported.
func prepare(req Request,
	opts []memory_core.Option) (prepared, error) {
	question := strings.TrimSpace(req.Query)
	if question == "" {
		return prepared{}, invalid(errors.New("query is required"))
	}
	sessionID, err := memory_core.ResolveSessionID(req.SessionID)
	if err != nil {
		return prepared{}, invalid(err)
	}
	reasoning, err := memory_core.ResolveQueryReasoningStages(req.Reasoning)
	if err != nil {
		return prepared{}, invalid(err)
	}
	models, err := memory_core.ResolveQueryModels(req.Models)
	if err != nil {
		return prepared{}, invalid(err)
	}
	call := memory_core.NewCallOptions(opts)
	graphIndex, err := memory_core.ResolveGraphIndex(req.GraphIndex)
	if err == nil && graphIndex != "" && call.GraphEmbedder == nil {
		err = errors.New("graph_index needs the graph embedder hook: it is not set on this server")
	}
	if err != nil {
		return prepared{}, invalid(err)
	}
	callOpts := append(opts[:len(opts):len(opts)],
		memory_core.WithSessionID(sessionID),
		memory_core.WithGraphIndex(graphIndex),
		memory_core.WithModels(models),
		memory_core.WithReasoning(reasoning))
	if req.GeoRadiusKm != nil && req.GeoNearby != nil && !*req.GeoNearby {
		return prepared{}, invalid(errors.New("geo_radius_km turns the nearby search on, but geo_nearby is false"))
	}
	if nearby := (req.GeoNearby != nil && *req.GeoNearby) || req.GeoRadiusKm != nil; nearby {
		if call.GeoResolver == nil {
			return prepared{}, invalid(errors.New("the nearby search needs geo: it is not enabled on this server (geo = true in the config)"))
		}
		callOpts = append(callOpts, memory_core.WithGeoNearby(true))
	}
	if req.GeoRadiusKm != nil { // a later option wins over the default radius of the server
		if err := memory_core.ValidateGeoRadius(*req.GeoRadiusKm); err != nil {
			return prepared{}, invalid(err)
		}
		callOpts = append(callOpts, memory_core.WithGeoRadius(*req.GeoRadiusKm))
	}
	if req.EvidenceOnly {
		callOpts = append(callOpts, memory_core.WithEvidenceOnly(true))
	}
	return prepared{
		query:     question,
		sessionID: sessionID,
		opts:      callOpts,
	}, nil
}
