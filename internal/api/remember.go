// Package api contains HTTP handlers.
package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/ivan/gligabao/internal/memory"
)

const maxRememberBytes = 1 << 20 // 1 MiB

type rememberRequest struct {
	Text string `json:"text"`
}

// Register mounts the API routes on mux. llm may be nil, in which case LLM-backed routes return 503.
func Register(mux *http.ServeMux, d *sql.DB, llm memory.LLM) {
	mux.HandleFunc("POST /v1/gb/remember", remember(d, llm))
	mux.HandleFunc("POST /v1/gb/recall", recall(d, llm))
}

//---------------------------------------------------------------------------------------------

func remember(d *sql.DB, llm memory.LLM) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if llm == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "LLM not configured (set ANTHROPIC_API_KEY)"})
			return
		}
		var req rememberRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRememberBytes)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
			return
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
			return
		}
		res, err := memory.Remember(r.Context(), d, llm, req.Text)
		if err != nil {
			log.Printf("remember: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to process text"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

//---------------------------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
