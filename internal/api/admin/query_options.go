package admin

// What the query button of the admin UI's top bar offers to pick (see docs/query.md#admin-ui): the LLM models and the thinking levels.
// The query itself is POST /admin/api/query, the handler of POST /v1/gb/query without the API key check (registered by the server).

import (
	"net/http"
	"slices"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

type queryOptions struct {
	// Models are the names of the LLM models that can be picked (the price table, sorted), without the embedding models.
	Models []string `json:"models"`
	// DefaultModel is the model that answers when none is picked.
	DefaultModel string `json:"default_model"`
	// Reasoning are the thinking levels, from none to the most (ai.ReasoningLevels).
	Reasoning []string `json:"reasoning"`
}

// QueryOptionsOf serves GET /admin/api/query-options: the LLM models of models (the supported ones, see ai.SupportedModels), the one a
// query uses by default and the thinking levels, for the model and thinking pickers of the query modal. Never null lists.
func QueryOptionsOf(models []ai.ModelInfo) http.HandlerFunc {
	out := queryOptions{Models: []string{}, DefaultModel: ai.DefaultModel(), Reasoning: slices.Clone(ai.ReasoningLevels)}
	for _, m := range models {
		if m.Kind == ai.KindLLM {
			out.Models = append(out.Models, m.Name)
		}
	}
	slices.Sort(out.Models)
	return func(w http.ResponseWriter, _ *http.Request) { httputil.WriteJSON(w, http.StatusOK, out) }
}
