package query

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// NearbyHandler serves GET /v1/gb/geo/nearby (the admin UI serves the same handler under /v1/gb/admin/api/geo/nearby): the geo
// entities near a stored geo entity or a point, selected by the same code as the proximity step of query (memory_query.NearbyEntityOf,
// NearbyPoint). Parameters: user_id, dataset (default "main"), and either entity_id or latitude and longitude; radius_km (optional,
// else the default radius of opts, memory_core.WithGeoRadius, else 50 km). The response is the `nearby` object of the query result.
// It needs no LLM and no geo resolver, and writes nothing.
func NearbyHandler(d *sql.DB, opts ...memory_core.Option) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		callOpts := opts[:len(opts):len(opts)]
		if v := q.Get("radius_km"); v != "" {
			km, err := strconv.ParseFloat(v, 64)
			if err != nil {
				httputil.Error(w, http.StatusBadRequest, "radius_km must be a number")
				return
			}
			if err := memory_core.ValidateGeoRadius(km); err != nil { // 0 would mean "the default" to WithGeoRadius
				httputil.Error(w, http.StatusBadRequest, err.Error())
				return
			}
			callOpts = append(callOpts, memory_core.WithGeoRadius(km))
		}
		var (
			res *memory_query.GeoContext
			err error
		)
		switch {
		case q.Get("entity_id") != "":
			id, perr := strconv.ParseInt(q.Get("entity_id"), 10, 64)
			if perr != nil || id <= 0 {
				httputil.Error(w, http.StatusBadRequest, "entity_id must be a positive integer")
				return
			}
			res, err = memory_query.NearbyEntityOf(r.Context(), d, q.Get("user_id"), q.Get("dataset"), id, callOpts...)
		case q.Get("latitude") != "" || q.Get("longitude") != "":
			lat, e1 := strconv.ParseFloat(strings.TrimSpace(q.Get("latitude")), 64)
			lon, e2 := strconv.ParseFloat(strings.TrimSpace(q.Get("longitude")), 64)
			if e1 != nil || e2 != nil {
				httputil.Error(w, http.StatusBadRequest, "latitude and longitude must both be numbers")
				return
			}
			res, err = memory_query.NearbyPoint(r.Context(), d, q.Get("user_id"), q.Get("dataset"), lat, lon, callOpts...)
		default:
			httputil.Error(w, http.StatusBadRequest, "entity_id, or latitude and longitude, is required")
			return
		}
		switch {
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory_core.ErrInvalidUser),
			errors.Is(err, memory_core.ErrInvalidGeoRadius), errors.Is(err, memory_query.ErrInvalidGeoPoint):
			httputil.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, memory_core.ErrDatasetNotFound):
			httputil.Error(w, http.StatusNotFound, "dataset not found")
		case errors.Is(err, graph.ErrNotFound):
			httputil.Error(w, http.StatusNotFound, "geo entity not found (no such entity in the dataset, or it has no coordinates)")
		case err != nil:
			log.WithError(err).WithFields(log.Fields{"user_id": q.Get("user_id"), "dataset": q.Get("dataset")}).Error("nearby geo entities failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to find the nearby geo entities")
		default:
			httputil.WriteJSON(w, http.StatusOK, res)
		}
	}
}
