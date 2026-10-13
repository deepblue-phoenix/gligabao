// Package remember is the HTTP handler for storing text in memory.
package remember

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_image"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

const (
	maxRememberBytes = 1 << 20 // 1 MiB: the largest text
	// maxBodyBytes is the largest request body: room for a base64 image of memory_image.MaxImageBytes (4/3 larger) plus
	// the rest of the JSON.
	maxBodyBytes = memory_image.MaxImageBytes/3*4 + 1<<16
)

type rememberRequest struct {
	// UserID owns the dataset. Required.
	UserID string `json:"user_id"`
	// Dataset is the dataset to store into (created if missing). Defaults to "main".
	Dataset string `json:"dataset"`
	// Text is the text to remember. Required unless Image is given.
	Text string `json:"text"`
	// Image (optional) is a base64-encoded JPEG, PNG, GIF or WebP, up to memory_image.MaxImageBytes: instead of text, an
	// image to remember with the image pipeline (see memory_image.RememberImage). It excludes Text and Kind.
	Image string `json:"image"`
	// Kind is the artifact kind: "knowledge" (default; not strictly episodic) or "episode" (a Graphiti-style episode,
	// anchored in time).
	Kind string `json:"kind"`
	// SessionID (optional) is the user/agent session the call belongs to: it is attached to the artifact (text or
	// image) and recorded against the entities and edges extracted from it. At most memory_core.MaxSessionIDLen bytes.
	SessionID string `json:"session_id"`
	// ReferenceTime (RFC 3339, optional) is when the text was written; relative dates in it ("yesterday") are
	// resolved against it. Defaults to now.
	ReferenceTime string `json:"reference_time"`
	// Models (optional) picks the LLM model of each stage of the pipeline ("extract_graph", "resolve_edges", "extract_image"); a stage left
	// out uses the default model. See memory_core.Models.
	Models map[string]string `json:"models"`
}

//---------------------------------------------------------------------------------------------

// Handler serves POST /v1/gb/remember.
// mo are extra options for every memory_remember.Remember call (e.g. memory.WithVectors).
func Handler(d *sql.DB, llm memory_core.LLM, mo ...memory_core.Option) http.HandlerFunc {
	return handler(d, llm, false, mo)
}

// EvalHandler serves POST /v1/gb/eval/remember: like Handler, but the response also carries the call's ATIF
// trajectory. It is only registered when the server runs in eval mode.
func EvalHandler(d *sql.DB, llm memory_core.LLM, mo ...memory_core.Option) http.HandlerFunc {
	return handler(d, llm, true, mo)
}

func handler(d *sql.DB, llm memory_core.LLM, eval bool, mo []memory_core.Option) http.HandlerFunc {
	opts := append([]memory_core.Option(nil), mo...)
	if eval {
		opts = append(opts, memory_core.WithEval())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if llm == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "LLM not configured (set ANTHROPIC_API_KEY)")
			return
		}
		var req rememberRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Image != "" {
			rememberImage(w, r, d, llm, req, opts)
			return
		}
		if req.Text == "" {
			httputil.Error(w, http.StatusBadRequest, "text is required")
			return
		}
		if len(req.Text) > maxRememberBytes {
			httputil.Error(w, http.StatusBadRequest, "text is larger than 1 MiB")
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
		if _, err := memory_core.ResolveUserID(req.UserID); err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := memory_core.ResolveDatasetName(req.Dataset); err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := memory_remember.ResolveKind(req.Kind); err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		sessionID, err := memory_core.ResolveSessionID(req.SessionID)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		models, err := memory_core.ResolveModels(req.Models)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		start := time.Now()
		res, err := memory_remember.Remember(r.Context(), d, llm, req.UserID, req.Dataset, req.Kind, req.Text, ref,
			append(opts[:len(opts):len(opts)], memory_core.WithSessionID(sessionID), memory_core.WithModels(models))...)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"user_id":    req.UserID,
				"dataset":    req.Dataset,
				"session_id": sessionID,
			}).Error("remember failed")
			monitoring.EmitSystemEvent(r.Context(), "remember.failed", monitoring.Event{
				"user_id":     req.UserID,
				"dataset":     req.Dataset,
				"session_id":  sessionID,
				"text_bytes":  len(req.Text),
				"duration_ms": time.Since(start).Milliseconds(),
				"error":       err.Error(),
			})
			httputil.Error(w, http.StatusBadGateway, "failed to process text")
			return
		}
		log.WithFields(log.Fields{
			"user_id":     res.UserID,
			"dataset":     res.Dataset,
			"artifact_id": res.ArtifactID,
			"session_id":  res.SessionID,
			"kind":        res.Kind,
			"chunks":      len(res.Chunks),
			"entities":    len(res.Entities),
			"edges":       len(res.Edges),
			"duration_ms": time.Since(start).Milliseconds(),
		}).Debug("remember completed")
		monitoring.EmitSystemEvent(r.Context(), "remember.completed", monitoring.Event{
			"user_id":           res.UserID,
			"dataset":           res.Dataset,
			"artifact_id":       res.ArtifactID,
			"session_id":        res.SessionID,
			"kind":              res.Kind,
			"text_bytes":        len(req.Text),
			"chunks":            len(res.Chunks),
			"entities":          len(res.Entities),
			"edges":             len(res.Edges),
			"invalidated_edges": len(res.InvalidatedEdges),
			"duration_ms":       time.Since(start).Milliseconds(),
		})
		httputil.WriteJSON(w, http.StatusOK, res)
	}
}

// rememberImage serves a remember request that carries an image: the image pipeline.
func rememberImage(w http.ResponseWriter, r *http.Request, d *sql.DB, llm memory_core.LLM, req rememberRequest, opts []memory_core.Option) {
	if req.Text != "" || req.Kind != "" {
		httputil.Error(w, http.StatusBadRequest, "image excludes text and kind")
		return
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.Image))
	if err != nil {
		httputil.Error(w, http.StatusBadRequest, "image must be base64: "+err.Error())
		return
	}
	var ref time.Time
	if req.ReferenceTime != "" {
		if ref, err = relational.ParseTimeStrict(req.ReferenceTime); err != nil {
			httputil.Error(w, http.StatusBadRequest, "reference_time must be RFC 3339, e.g. 2025-01-31T09:00:00Z")
			return
		}
	}
	if _, err := memory_core.ResolveUserID(req.UserID); err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := memory_core.ResolveDatasetName(req.Dataset); err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := memory_image.ResolveImage(data); err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionID, err := memory_core.ResolveSessionID(req.SessionID)
	if err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	models, err := memory_core.ResolveModels(req.Models)
	if err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	start := time.Now()
	res, err := memory_image.RememberImage(r.Context(), d, llm, req.UserID, req.Dataset, data, ref,
		append(opts[:len(opts):len(opts)], memory_core.WithSessionID(sessionID), memory_core.WithModels(models))...)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"user_id":    req.UserID,
			"dataset":    req.Dataset,
			"session_id": sessionID,
		}).Error("remember image failed")
		monitoring.EmitSystemEvent(r.Context(), "remember_image.failed", monitoring.Event{
			"user_id":     req.UserID,
			"dataset":     req.Dataset,
			"session_id":  sessionID,
			"image_bytes": len(data),
			"duration_ms": time.Since(start).Milliseconds(),
			"error":       err.Error(),
		})
		httputil.Error(w, http.StatusBadGateway, "failed to process image")
		return
	}
	log.WithFields(log.Fields{
		"user_id":           res.UserID,
		"dataset":           res.Dataset,
		"image_artifact_id": res.ImageArtifactID,
		"session_id":        res.SessionID,
		"theme":             res.Theme,
		"text_blocks":       len(res.TextBlocks),
		"duration_ms":       time.Since(start).Milliseconds(),
	}).Debug("remember image completed")
	monitoring.EmitSystemEvent(r.Context(), "remember_image.completed", monitoring.Event{
		"user_id":           res.UserID,
		"dataset":           res.Dataset,
		"image_artifact_id": res.ImageArtifactID,
		"session_id":        res.SessionID,
		"extraction_id":     res.ExtractionID,
		"media_type":        res.MediaType,
		"image_bytes":       res.SizeBytes,
		"theme":             res.Theme,
		"text_blocks":       len(res.TextBlocks),
		"duration_ms":       time.Since(start).Milliseconds(),
	})
	httputil.WriteJSON(w, http.StatusOK, res)
}
