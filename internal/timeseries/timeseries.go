// Package timeseries contains the HTTP handlers for timeseries: attaching them to what is stored, and reading them
// back (see docs/timeseries.md). A series can also be supplied with POST /v1/gb/remember.
package timeseries

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

const (
	maxBodyBytes = 16 << 20 // 16 MiB: more than remember takes, since series can be big
	// DefaultPointLimit and MaxPointLimit are the number of points GET returns by default and at most.
	DefaultPointLimit = 1000
	MaxPointLimit     = memory_core.MaxSeriesPoints
)

type attachRequest struct {
	UserID  string                   `json:"user_id"`
	Dataset string                   `json:"dataset"`
	Series  []memory_core.SeriesSpec `json:"series"`
}

//---------------------------------------------------------------------------------------------

// dataset resolves the user and dataset of a request to the dataset's ID, writing the error response if it cannot.
func dataset(w http.ResponseWriter, r *http.Request, d *sql.DB, userID, name string) (int64, string, bool) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return 0, "", false
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		httputil.Error(w, http.StatusBadRequest, err.Error())
		return 0, "", false
	}
	id, err := memory_core.DatasetID(r.Context(), d, userID, name)
	if errors.Is(err, memory_core.ErrDatasetNotFound) {
		httputil.Error(w, http.StatusNotFound, "dataset not found")
		return 0, "", false
	}
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"user_id": userID, "dataset": name}).Error("timeseries: find dataset failed")
		httputil.Error(w, http.StatusInternalServerError, "failed to find dataset")
		return 0, "", false
	}
	return id, name, true
}

// Attach serves POST /v1/gb/timeseries: stores series in an existing dataset and anchors them to what it holds, by ID
// or by selector, in one transaction. A series with the key of a stored one adds its points to it.
func Attach(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req attachRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if len(req.Series) == 0 {
			httputil.Error(w, http.StatusBadRequest, "series is required")
			return
		}
		series, err := memory_core.ResolveSeries(req.Series, false)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		dsID, name, ok := dataset(w, r, d, req.UserID, req.Dataset)
		if !ok {
			return
		}
		start := time.Now()
		res, err := memory_timeseries.Attach(r.Context(), d, dsID, series)
		switch {
		case errors.Is(err, memory_timeseries.ErrAnchor):
			httputil.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		case err != nil:
			log.WithError(err).WithFields(log.Fields{"user_id": req.UserID, "dataset": name}).Error("attach timeseries failed")
			monitoring.EmitSystemEvent(r.Context(), "timeseries.failed", monitoring.Event{
				"user_id": req.UserID, "dataset": name, "series": len(series), "duration_ms": time.Since(start).Milliseconds(), "error": err.Error(),
			})
			httputil.Error(w, http.StatusInternalServerError, "failed to store timeseries")
			return
		}
		monitoring.EmitSystemEvent(r.Context(), "timeseries.completed", monitoring.Event{
			"user_id": req.UserID, "dataset": name, "series": len(res), "duration_ms": time.Since(start).Milliseconds(),
		})
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"user_id": req.UserID, "dataset": name, "series": res})
	}
}

//---------------------------------------------------------------------------------------------

type seriesView struct {
	ID         int64           `json:"id"`
	Key        string          `json:"key"`
	Field      string          `json:"field"`
	Name       string          `json:"name"`
	Unit       string          `json:"unit,omitempty"`
	Frequency  string          `json:"frequency,omitempty"`
	Source     string          `json:"source"`
	Provider   string          `json:"provider,omitempty"`
	PointCount int             `json:"point_count"`
	FirstTS    *time.Time      `json:"first_ts,omitempty"`
	LastTS     *time.Time      `json:"last_ts,omitempty"`
	Stats      json.RawMessage `json:"stats,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Anchors    []anchorView    `json:"anchors"`
}

type anchorView struct {
	Type        string     `json:"type"`
	ID          int64      `json:"id"`
	Role        string     `json:"role,omitempty"`
	WindowStart *time.Time `json:"window_start,omitempty"`
	WindowEnd   *time.Time `json:"window_end,omitempty"`
}

func view(s graph.Series, anchors []graph.SeriesAnchor) seriesView {
	v := seriesView{ID: s.ID, Key: s.Key, Field: s.Field, Name: s.Name, Unit: s.Unit, Frequency: s.Frequency, Source: s.Source,
		Provider: s.Provider, PointCount: s.PointCount, FirstTS: s.FirstTS, LastTS: s.LastTS, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		Anchors: []anchorView{}}
	if s.Stats != "" {
		v.Stats = json.RawMessage(s.Stats)
	}
	for _, a := range anchors {
		v.Anchors = append(v.Anchors, anchorView{Type: a.Type, ID: a.TargetID, Role: a.Role, WindowStart: a.WindowStart, WindowEnd: a.WindowEnd})
	}
	return v
}

// List serves GET /v1/gb/timeseries?user_id=...&dataset=...: the series of the dataset, with their stats and anchors
// but without points.
func List(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		dsID, name, ok := dataset(w, r, d, q.Get("user_id"), q.Get("dataset"))
		if !ok {
			return
		}
		list, err := graph.ListSeries(r.Context(), d, dsID)
		out := make([]seriesView, 0, len(list))
		for i := 0; err == nil && i < len(list); i++ {
			var anchors []graph.SeriesAnchor
			if anchors, err = graph.ListSeriesAnchors(r.Context(), d, list[i].ID); err == nil {
				out = append(out, view(list[i], anchors))
			}
		}
		if err != nil {
			log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": name}).Error("list timeseries failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list timeseries")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"dataset": name, "series": out})
	}
}

// Get serves GET /v1/gb/timeseries/{id}?user_id=...&dataset=...&from=...&to=...&limit=...: one series with its anchors
// and its points between from and to (RFC 3339, both optional and inclusive), oldest first, at most limit (default
// DefaultPointLimit, at most MaxPointLimit; truncated says there were more). With from or to it also has window_stats,
// the stats of the points in the window.
func Get(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httputil.Error(w, http.StatusNotFound, "timeseries not found")
			return
		}
		var from, to *time.Time
		for name, dst := range map[string]**time.Time{"from": &from, "to": &to} {
			if v := q.Get(name); v != "" {
				t, err := relational.ParseTimeStrict(v)
				if err != nil {
					httputil.Error(w, http.StatusBadRequest, name+" must be RFC 3339, e.g. 2025-01-31T09:00:00Z")
					return
				}
				t = relational.Normalize(t)
				*dst = &t
			}
		}
		limit := DefaultPointLimit
		if v := q.Get("limit"); v != "" {
			if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > MaxPointLimit {
				httputil.Error(w, http.StatusBadRequest, "limit must be between 1 and "+strconv.Itoa(MaxPointLimit))
				return
			}
		}
		dsID, name, ok := dataset(w, r, d, q.Get("user_id"), q.Get("dataset"))
		if !ok {
			return
		}
		s, err := graph.GetSeries(r.Context(), d, dsID, id)
		if errors.Is(err, graph.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "timeseries not found")
			return
		}
		var anchors []graph.SeriesAnchor
		var pts []graph.SeriesPoint
		if err == nil {
			anchors, err = graph.ListSeriesAnchors(r.Context(), d, id)
		}
		if err == nil {
			pts, err = graph.SeriesPoints(r.Context(), d, id, from, to, limit+1) // one more tells whether it was cut
		}
		if err != nil {
			log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": name, "series_id": id}).Error("get timeseries failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read timeseries")
			return
		}
		truncated := len(pts) > limit
		if truncated {
			pts = pts[:limit]
		}
		out := struct {
			Dataset string `json:"dataset"`
			seriesView
			Points      []memory_core.SeriesPoint `json:"points"`
			Truncated   bool                      `json:"truncated"`
			WindowStats *memory_timeseries.Stats  `json:"window_stats,omitempty"`
		}{Dataset: name, seriesView: view(*s, anchors), Points: make([]memory_core.SeriesPoint, 0, len(pts)), Truncated: truncated}
		for _, p := range pts {
			out.Points = append(out.Points, memory_core.SeriesPoint{TS: p.TS, Value: p.Value})
		}
		if (from != nil || to != nil) && !truncated {
			out.WindowStats = memory_timeseries.Summarize(pts)
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
