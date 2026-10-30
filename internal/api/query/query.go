package query

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

const maxQueryBytes = 64 << 10 // 64 KiB

type queryRequest struct {
	// UserID owns the dataset. Required.
	UserID string `json:"user_id"`
	// Dataset is the dataset to search. Defaults to "main".
	Dataset string `json:"dataset"`
	Query   string `json:"query"`
	// SessionID (optional) is the user/agent session the call belongs to. It is echoed in the response and recorded
	// in the call's trajectory and monitoring events, so that a query can be connected to the artifacts remembered
	// in the same session. It does not change what is retrieved or answered. At most memory_core.MaxSessionIDLen bytes.
	SessionID string `json:"session_id"`
	// Reasoning (optional) is the reasoning (thinking) level of the LLM calls: "none", "low", "medium" or "high". Either a
	// string, the level of the call that answers, or an object with the level of each stage ("query", "detect_geo"). A stage
	// left out runs with "none" (no thinking). The calls are recorded with it as inferences (gb_inferences).
	Reasoning json.RawMessage `json:"reasoning"`
	// Models (optional) picks the LLM model of each stage of the query ("query": the one that answers; "detect_geo": the one that
	// finds the places in the question, only when the server has geo on); a stage left out uses the default model.
	Models map[string]string `json:"models"`
	// GeoNearby (optional) turns on the search for the geo entities near the places of the question, which are given to the LLM as
	// context. Off by default: the user asks for it. It needs the server to have geo on (400 otherwise). The places of the question
	// that the geo resolver finds are stored as entities with coordinates.
	GeoNearby *bool `json:"geo_nearby"`
	// GeoRadiusKm (optional) is the radius, in kilometers, of that search. Giving it turns the search on (same as geo_nearby: true;
	// with geo_nearby: false it is a 400). It overrides the default of the server (config geo_radius_km, 50 km unless set); it must
	// be greater than 0 and at most memory_core.MaxGeoRadiusKm.
	GeoRadiusKm *float64 `json:"geo_radius_km"`
	// EvidenceOnly (optional, default false) makes the call retrieval-only: no LLM writes the answer (the response has an empty
	// answer), and the evidence is everything that was retrieved. See memory_core.WithEvidenceOnly.
	EvidenceOnly bool `json:"evidence_only"`
}

// parseReasoning reads the reasoning field of the request: absent or null (the defaults), a string (the level of the answering
// call, the form of before the stages) or an object (the level of each stage).
func parseReasoning(raw json.RawMessage) (memory_core.Reasoning, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '{' {
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%w: reasoning must map each stage to a level: %v", memory_core.ErrInvalidReasoning, err)
		}
		return memory_core.ResolveQueryReasoningStages(m)
	}
	var level string
	if err := json.Unmarshal(raw, &level); err != nil {
		return nil, fmt.Errorf("%w: reasoning must be a level or an object with a level per stage", memory_core.ErrInvalidReasoning)
	}
	return memory_core.ResolveQueryReasoning(level)
}

//---------------------------------------------------------------------------------------------

// Handler serves POST /v1/gb/query. opts apply to every call; with memory_core.WithGeoResolver the places the question mentions are
// found; the geo entities near them are given to the LLM only when the request asks (geo_nearby, geo_radius_km; memory_core.WithGeoRadius sets the default radius).
func Handler(d *sql.DB, llm memory_core.LLM, opts ...memory_core.Option) http.HandlerFunc {
	return handler(d, llm, false, opts)
}

// EvalHandler serves POST /v1/gb/eval/query: like Handler, but the response also carries the call's ATIF
// trajectory. It is only registered when the server runs in eval mode.
func EvalHandler(d *sql.DB, llm memory_core.LLM, opts ...memory_core.Option) http.HandlerFunc {
	return handler(d, llm, true, opts)
}

func handler(d *sql.DB, llm memory_core.LLM, eval bool, opts []memory_core.Option) http.HandlerFunc {
	opts = opts[:len(opts):len(opts)]
	if eval {
		opts = append(opts, memory_core.WithEval())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if llm == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "LLM not configured (set ANTHROPIC_API_KEY)")
			return
		}
		var req queryRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQueryBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		req.Query = strings.TrimSpace(req.Query)
		if req.Query == "" {
			httputil.Error(w, http.StatusBadRequest, "query is required")
			return
		}
		sessionID, err := memory_core.ResolveSessionID(req.SessionID)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		reasoning, err := parseReasoning(req.Reasoning)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		models, err := memory_core.ResolveQueryModels(req.Models)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		callOpts := append(opts[:len(opts):len(opts)], memory_core.WithSessionID(sessionID), memory_core.WithModels(models), memory_core.WithReasoning(reasoning))
		if req.GeoRadiusKm != nil && req.GeoNearby != nil && !*req.GeoNearby {
			httputil.Error(w, http.StatusBadRequest, "geo_radius_km turns the nearby search on, but geo_nearby is false")
			return
		}
		if nearby := (req.GeoNearby != nil && *req.GeoNearby) || req.GeoRadiusKm != nil; nearby {
			if memory_core.NewCallOptions(opts).GeoResolver == nil {
				httputil.Error(w, http.StatusBadRequest, "the nearby search needs geo: it is not enabled on this server (geo = true in the config)")
				return
			}
			callOpts = append(callOpts, memory_core.WithGeoNearby(true))
		}
		if req.GeoRadiusKm != nil { // a later option wins over the default radius of the server
			if err := memory_core.ValidateGeoRadius(*req.GeoRadiusKm); err != nil {
				httputil.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			callOpts = append(callOpts, memory_core.WithGeoRadius(*req.GeoRadiusKm))
		}
		if req.EvidenceOnly {
			callOpts = append(callOpts, memory_core.WithEvidenceOnly(true))
		}
		start := time.Now()
		res, err := memory_query.Query(r.Context(), d, llm, req.UserID, req.Dataset, req.Query, callOpts...)
		if err != nil {
			monitoring.EmitSystemEvent(r.Context(), "query.failed", monitoring.Event{
				"user_id":     req.UserID,
				"dataset":     req.Dataset,
				"session_id":  sessionID,
				"query_bytes": len(req.Query),
				"duration_ms": time.Since(start).Milliseconds(),
				"error":       err.Error(),
			})
		}
		if errors.Is(err, memory_core.ErrInvalidDataset) || errors.Is(err, memory_core.ErrInvalidUser) || errors.Is(err, memory_core.ErrInvalidGeoRadius) {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, memory_core.ErrDatasetNotFound) {
			httputil.Error(w, http.StatusNotFound, "dataset not found")
			return
		}
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"user_id":    req.UserID,
				"dataset":    req.Dataset,
				"session_id": sessionID,
			}).Error("query failed")
			httputil.Error(w, http.StatusBadGateway, "failed to answer query")
			return
		}
		log.WithFields(log.Fields{
			"user_id":     req.UserID,
			"dataset":     req.Dataset,
			"session_id":  sessionID,
			"duration_ms": time.Since(start).Milliseconds(),
		}).Debug("query completed")
		monitoring.EmitSystemEvent(r.Context(), "query.completed", monitoring.Event{
			"user_id":           req.UserID,
			"dataset":           req.Dataset,
			"session_id":        sessionID,
			"query_bytes":       len(req.Query),
			"evidence_entities": len(res.Evidence.Entities),
			"evidence_edges":    len(res.Evidence.Edges),
			"duration_ms":       time.Since(start).Milliseconds(),
		})
		httputil.WriteJSON(w, http.StatusOK, res)
	}
}
