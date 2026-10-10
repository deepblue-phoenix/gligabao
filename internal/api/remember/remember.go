// Package remember is the HTTP handler for storing text in memory.
package remember

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

const maxRememberBytes = 1 << 20 // 1 MiB

type rememberRequest struct {
	// UserID owns the dataset. Required.
	UserID string `json:"user_id"`
	// Dataset is the dataset to store into (created if missing). Defaults to "main".
	Dataset string `json:"dataset"`
	Text    string `json:"text"`
	// ReferenceTime (RFC 3339, optional) is when the text was written; relative dates in it ("yesterday") are
	// resolved against it. Defaults to now.
	ReferenceTime string `json:"reference_time"`
}

//---------------------------------------------------------------------------------------------

// Handler serves POST /v1/gb/remember.
func Handler(d *sql.DB, llm memory.LLM) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if llm == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "LLM not configured (set ANTHROPIC_API_KEY)")
			return
		}
		var req rememberRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRememberBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			httputil.Error(w, http.StatusBadRequest, "text is required")
			return
		}
		var ref time.Time
		if req.ReferenceTime != "" {
			var err error
			if ref, err = relational.ParseTimeStrict(req.ReferenceTime); err != nil {
				httputil.Error(w, http.StatusBadRequest, "reference_time must be RFC 3339, e.g. 2025-01-31T09:00:00Z")
				return
			}
		}
		if _, err := memory.ResolveUserID(req.UserID); err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := memory.ResolveDatasetName(req.Dataset); err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := memory.Remember(r.Context(), d, llm, req.UserID, req.Dataset, req.Text, ref)
		if err != nil {
			log.Printf("remember: %v", err)
			httputil.Error(w, http.StatusBadGateway, "failed to process text")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, res)
	}
}
