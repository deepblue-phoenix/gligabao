// Package api contains HTTP handlers.
package api

import (
	"database/sql"
	"net/http"

	"github.com/ivan/gligabao/internal/api/recall"
	"github.com/ivan/gligabao/internal/api/remember"
	"github.com/ivan/gligabao/internal/memory"
)

// Register mounts the API routes on mux. llm may be nil, in which case LLM-backed routes return 503.
func Register(mux *http.ServeMux, d *sql.DB, llm memory.LLM) {
	mux.HandleFunc("POST /v1/gb/remember", remember.Handler(d, llm))
	mux.HandleFunc("POST /v1/gb/recall", recall.Handler(d, llm))
}
