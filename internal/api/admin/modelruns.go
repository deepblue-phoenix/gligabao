package admin

// Model runs in the admin UI (see docs/model-runs.md): the totals of the model invocations recorded in a database, broken
// down by operation, stage, model and thinking level, and the list of the runs themselves (newest first, filterable).

import (
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	log "github.com/sirupsen/logrus"
)

const (
	modelRunsDefaultLimit = 100
	modelRunsMaxLimit     = 500
)

// ModelRunSums are the totals of a set of model runs.
type ModelRunSums struct {
	Runs         int64   `json:"runs"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ModelRunBreakdown is the totals of the runs that share one value of a dimension (Key, e.g. a stage; Label is what to
// show, the model's name for a model ID).
type ModelRunBreakdown struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	ModelRunSums
}

type modelRunsResponse struct {
	Totals      ModelRunSums        `json:"totals"`
	ByType      []ModelRunBreakdown `json:"by_type"`
	ByStage     []ModelRunBreakdown `json:"by_stage"`
	ByModel     []ModelRunBreakdown `json:"by_model"`
	ByReasoning []ModelRunBreakdown `json:"by_reasoning"`
	// Runs are the runs that match the filter, newest first (Limit of them, after Offset); Total is how many match.
	Runs   []relational.ModelRun `json:"runs"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

// breakdown sums the groups by the dimension key picks, keeping the order in which each key first appears (the groups
// come largest cost first).
func breakdown(groups []relational.ModelRunGroup, key func(relational.ModelRunGroup) (string, string)) []ModelRunBreakdown {
	out := []ModelRunBreakdown{}
	idx := map[string]int{}
	for _, g := range groups {
		k, label := key(g)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, ModelRunBreakdown{Key: k, Label: label})
		}
		s := &out[i].ModelRunSums
		s.Runs += g.Runs
		s.InputTokens += g.InputTokens
		s.OutputTokens += g.OutputTokens
		s.CostUSD += g.CostUSD
	}
	return out
}

// ModelRunsOf serves GET /admin/api/model-runs[?db=<name>][&type=..][&stage=..][&model_id=..][&reasoning=..][&limit=..][&offset=..]
// for one of dbs (the first without db): the totals of all the runs (never filtered), by type, stage, model and
// thinking level, and the runs that match the filters.
func ModelRunsOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		limit, offset := modelRunsDefaultLimit, 0
		if v := q.Get("limit"); v != "" {
			l, err := strconv.Atoi(v)
			if err != nil || l < 1 {
				httputil.Error(w, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = min(l, modelRunsMaxLimit)
		}
		if v := q.Get("offset"); v != "" {
			o, err := strconv.Atoi(v)
			if err != nil || o < 0 {
				httputil.Error(w, http.StatusBadRequest, "invalid offset")
				return
			}
			offset = o
		}
		f := relational.ModelRunFilter{Type: q.Get("type"), Stage: q.Get("stage"), ModelID: q.Get("model_id"), Reasoning: q.Get("reasoning")}
		ctx := r.Context()
		groups, err := relational.ModelRunGroups(ctx, n.DB)
		if err != nil {
			log.WithError(err).Error("admin: model run stats failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read model runs")
			return
		}
		runs, total, err := relational.ListModelRuns(ctx, n.DB, f, limit, offset)
		if err != nil {
			log.WithError(err).Error("admin: list model runs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read model runs")
			return
		}
		out := modelRunsResponse{Runs: runs, Total: total, Limit: limit, Offset: offset}
		for _, g := range groups {
			out.Totals.Runs += g.Runs
			out.Totals.InputTokens += g.InputTokens
			out.Totals.OutputTokens += g.OutputTokens
			out.Totals.CostUSD += g.CostUSD
		}
		out.ByType = breakdown(groups, func(g relational.ModelRunGroup) (string, string) { return g.Type, "" })
		out.ByStage = breakdown(groups, func(g relational.ModelRunGroup) (string, string) { return g.Stage, "" })
		out.ByModel = breakdown(groups, func(g relational.ModelRunGroup) (string, string) { return g.ModelID, g.Model })
		out.ByReasoning = breakdown(groups, func(g relational.ModelRunGroup) (string, string) { return g.Reasoning, "" })
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
