package query

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

const maxQueryBytes = 64 << 10 // 64 KiB

// Request is a query request: the body of POST /v1/gb/query, and what Run takes from the callers that are not HTTP (the evals).
type Request struct {
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
	Reasoning Reasoning `json:"reasoning"`
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
	// GraphIndex (optional) names the graph embedding index of the call: the graph embedder hook (ServeOptions.GraphEmbedder) gets it with the
	// graph context of the question, to know which index to embed for or search. 400 if it is malformed or the server has no graph
	// embedder hook. See memory_core.WithGraphIndex.
	GraphIndex string `json:"graph_index"`
	// EvidenceOnly (optional, default false) makes the call retrieval-only: no LLM writes the answer (the response has an empty
	// answer), and the evidence is everything that was retrieved. See memory_core.WithEvidenceOnly.
	EvidenceOnly bool `json:"evidence_only"`
}

// Reasoning is the reasoning field of a Request: the level of each stage. In JSON it is an object with a level per stage, or a string,
// the level of the call that answers (the form of before the stages; it reads as {"query": level}); absent or null is none.
type Reasoning map[string]string

// UnmarshalJSON implements json.Unmarshaler.
func (r *Reasoning) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*r = nil
		return nil
	}
	if data[0] == '{' {
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("%w: reasoning must map each stage to a level: %v",
				memory_core.ErrInvalidReasoning,
				err)
		}
		*r = m
		return nil
	}
	var level string
	if err := json.Unmarshal(data, &level); err != nil {
		return fmt.Errorf("%w: reasoning must be a level or an object with a level per stage", memory_core.ErrInvalidReasoning)
	}
	*r = Reasoning{memory_core.StageQuery: level}
	return nil
}

//---------------------------------------------------------------------------------------------

// Handler serves POST /v1/gb/query. opts apply to every call; with memory_core.WithGeoResolver the places the question mentions are
// found; the geo entities near them are given to the LLM only when the request asks (geo_nearby, geo_radius_km; memory_core.WithGeoRadius sets the default radius).
// With memory_core.WithEval the response also carries the call's ATIF trajectory, and the 502 says why the call failed.
func Handler(d *sql.DB,
	llm memory_core.LLM,
	opts ...memory_core.Option) http.HandlerFunc {
	eval := memory_core.NewCallOptions(opts).Eval
	return func(w http.ResponseWriter,
		r *http.Request) {
		if llm == nil {
			httputil.Error(w,
				http.StatusServiceUnavailable,
				"LLM not configured (set ANTHROPIC_API_KEY)")
			return
		}
		var req Request
		if err := json.NewDecoder(http.MaxBytesReader(w,
			r.Body,
			maxQueryBytes)).Decode(&req); err != nil {
			httputil.Error(w,
				http.StatusBadRequest,
				"invalid JSON body: "+err.Error())
			return
		}
		res, err := Run(r.Context(),
			d,
			llm,
			httputil.UserID(r),
			req,
			opts)
		var invalid *InvalidError
		switch {
		case err == nil:
			httputil.WriteJSON(w,
				http.StatusOK,
				res)
		case errors.As(err, &invalid),
			errors.Is(err, memory_core.ErrInvalidDataset),
			errors.Is(err, memory_core.ErrInvalidUser),
			errors.Is(err, memory_core.ErrInvalidGeoRadius):
			httputil.Error(w,
				http.StatusBadRequest,
				err.Error())
		case errors.Is(err, memory_core.ErrDatasetNotFound):
			httputil.Error(w,
				http.StatusNotFound,
				"dataset not found")
		default:
			msg := "failed to answer query"
			if eval { // the caller is the eval runner: it records the cause as the reason the case errored
				msg += ": " + err.Error()
			}
			httputil.Error(w,
				http.StatusBadGateway,
				msg)
		}
	}
}
