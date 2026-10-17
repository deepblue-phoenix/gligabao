package admin

// Inferences in the admin UI (see docs/inferences.md): the totals of the model invocations recorded in a database, broken
// down by operation, stage, model and thinking level, and the list of the inferences themselves (newest first, filterable).

import (
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	log "github.com/sirupsen/logrus"
)

const (
	inferencesDefaultLimit = 100
	inferencesMaxLimit     = 500
)

// InferenceSums are the totals of a set of inferences.
type InferenceSums struct {
	Inferences   int64   `json:"inferences"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// InferenceBreakdown is the totals of the inferences that share one value of a dimension (Key, e.g. a stage; Label is what to
// show, the model's name for a model ID).
type InferenceBreakdown struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	InferenceSums
}

type inferencesResponse struct {
	Totals      InferenceSums        `json:"totals"`
	ByType      []InferenceBreakdown `json:"by_type"`
	ByStage     []InferenceBreakdown `json:"by_stage"`
	ByModel     []InferenceBreakdown `json:"by_model"`
	ByReasoning []InferenceBreakdown `json:"by_reasoning"`
	// Inferences are the inferences that match the filter, newest first (Limit of them, after Offset); Total is how many match.
	Inferences []relational.Inference `json:"inferences"`
	Total      int                    `json:"total"`
	Limit      int                    `json:"limit"`
	Offset     int                    `json:"offset"`
}

// breakdown sums the groups by the dimension key picks, keeping the order in which each key first appears (the groups
// come largest cost first).
func breakdown(groups []relational.InferenceGroup, key func(relational.InferenceGroup) (string, string)) []InferenceBreakdown {
	out := []InferenceBreakdown{}
	idx := map[string]int{}
	for _, g := range groups {
		k, label := key(g)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, InferenceBreakdown{Key: k, Label: label})
		}
		s := &out[i].InferenceSums
		s.Inferences += g.Inferences
		s.InputTokens += g.InputTokens
		s.OutputTokens += g.OutputTokens
		s.CostUSD += g.CostUSD
	}
	return out
}

// InferencesOf serves GET /admin/api/inferences[?db=<name>][&type=..][&stage=..][&model_id=..][&reasoning=..][&limit=..][&offset=..]
// for one of dbs (the first without db): the totals of all the inferences (never filtered), by type, stage, model and
// thinking level, and the inferences that match the filters.
func InferencesOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		limit, offset := inferencesDefaultLimit, 0
		if v := q.Get("limit"); v != "" {
			l, err := strconv.Atoi(v)
			if err != nil || l < 1 {
				httputil.Error(w, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = min(l, inferencesMaxLimit)
		}
		if v := q.Get("offset"); v != "" {
			o, err := strconv.Atoi(v)
			if err != nil || o < 0 {
				httputil.Error(w, http.StatusBadRequest, "invalid offset")
				return
			}
			offset = o
		}
		f := relational.InferenceFilter{Type: q.Get("type"), Stage: q.Get("stage"), ModelID: q.Get("model_id"), Reasoning: q.Get("reasoning")}
		ctx := r.Context()
		groups, err := relational.InferenceGroups(ctx, n.DB)
		if err != nil {
			log.WithError(err).Error("admin: inference stats failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read inferences")
			return
		}
		runs, total, err := relational.ListInferences(ctx, n.DB, f, limit, offset)
		if err != nil {
			log.WithError(err).Error("admin: list inferences failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read inferences")
			return
		}
		out := inferencesResponse{Inferences: runs, Total: total, Limit: limit, Offset: offset}
		for _, g := range groups {
			out.Totals.Inferences += g.Inferences
			out.Totals.InputTokens += g.InputTokens
			out.Totals.OutputTokens += g.OutputTokens
			out.Totals.CostUSD += g.CostUSD
		}
		out.ByType = breakdown(groups, func(g relational.InferenceGroup) (string, string) { return g.Type, "" })
		out.ByStage = breakdown(groups, func(g relational.InferenceGroup) (string, string) { return g.Stage, "" })
		out.ByModel = breakdown(groups, func(g relational.InferenceGroup) (string, string) { return g.ModelID, g.Model })
		out.ByReasoning = breakdown(groups, func(g relational.InferenceGroup) (string, string) { return g.Reasoning, "" })
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
