package recall

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/ivan/gligabao/internal/api/httputil"
	"github.com/ivan/gligabao/internal/memory"
)

const maxRecallBytes = 64 << 10 // 64 KiB

type recallRequest struct {
	Query string `json:"query"`
}

//---------------------------------------------------------------------------------------------

// Handler serves POST /v1/gb/recall.
func Handler(d *sql.DB, llm memory.LLM) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if llm == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "LLM not configured (set ANTHROPIC_API_KEY)")
			return
		}
		var req recallRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRecallBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		req.Query = strings.TrimSpace(req.Query)
		if req.Query == "" {
			httputil.Error(w, http.StatusBadRequest, "query is required")
			return
		}
		res, err := memory.Recall(r.Context(), d, llm, req.Query)
		if err != nil {
			log.Printf("recall: %v", err)
			httputil.Error(w, http.StatusBadGateway, "failed to answer query")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, res)
	}
}
