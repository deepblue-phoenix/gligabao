package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/ivan/gligabao/internal/memory"
)

const maxRecallBytes = 64 << 10 // 64 KiB

type recallRequest struct {
	Query string `json:"query"`
}

func recall(d *sql.DB, llm memory.LLM) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if llm == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "LLM not configured (set ANTHROPIC_API_KEY)"})
			return
		}
		var req recallRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRecallBytes)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
			return
		}
		req.Query = strings.TrimSpace(req.Query)
		if req.Query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query is required"})
			return
		}
		res, err := memory.Recall(r.Context(), d, llm, req.Query)
		if err != nil {
			log.Printf("recall: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to answer query"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
