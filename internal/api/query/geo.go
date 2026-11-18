package query

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// geoEntity is one geo entity of the response of GeoHandler.
type geoEntity struct {
	EntityID    int64       `json:"entity_id"`
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Kind        string      `json:"kind"`
	Latitude    float64     `json:"latitude"`
	Longitude   float64     `json:"longitude"`
	DisplayName string      `json:"display_name,omitempty"`
	Country     string      `json:"country,omitempty"`
	CountryCode string      `json:"country_code,omitempty"`
	Region      string      `json:"region,omitempty"`
	BBox        *[4]float64 `json:"bbox,omitempty"`
	Provider    string      `json:"provider,omitempty"`
}

// geoResponse is the response of GeoHandler.
type geoResponse struct {
	Dataset  string      `json:"dataset"`
	Entities []geoEntity `json:"entities"`
	// Pending is how many places of the dataset the geo manager has not resolved yet: remember does not wait for it (unless it is asked
	// to, geo_sync), so call again later until it is 0.
	Pending int `json:"pending"`
}

// GeoHandler serves GET /v1/gb/geo: the geo entities of a dataset with their coordinates, which remember stores asynchronously (the places it
// finds are in its response as pending). Parameters (the user is the gb-user-id header): dataset (default "main"), entity_id (optional:
// only that entity; 404 if it has no coordinates yet). It needs no LLM and no geo resolver, and writes nothing.
func GeoHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		q := r.URL.Query()
		var only int64
		if v := q.Get("entity_id"); v != "" {
			id, err := strconv.ParseInt(v,
				10,
				64)
			if err != nil || id <= 0 {
				httputil.Error(w,
					http.StatusBadRequest,
					"entity_id must be a positive integer")
				return
			}
			only = id
		}
		userID, err := memory_core.ResolveUserID(httputil.UserID(r))
		if err == nil {
			var dataset string
			if dataset, err = memory_core.ResolveDatasetName(q.Get("dataset")); err == nil {
				var dsID int64
				if dsID, err = memory_core.DatasetID(r.Context(),
					d,
					userID,
					dataset); err == nil {
					var res *geoResponse
					if res, err = geoOf(r,
						d,
						dsID,
						dataset,
						only); err == nil {
						httputil.WriteJSON(w,
							http.StatusOK,
							res)
						return
					}
				}
			}
		}
		switch {
		case errors.Is(err, memory_core.ErrInvalidDataset), errors.Is(err, memory_core.ErrInvalidUser):
			httputil.Error(w,
				http.StatusBadRequest,
				err.Error())
		case errors.Is(err, memory_core.ErrDatasetNotFound):
			httputil.Error(w,
				http.StatusNotFound,
				"dataset not found")
		case errors.Is(err, graph.ErrNotFound):
			httputil.Error(w,
				http.StatusNotFound,
				"geo entity not found (no such entity in the dataset, or its coordinates are not resolved yet)")
		default:
			log.WithError(err).WithFields(log.Fields{"user_id": httputil.UserID(r), "dataset": q.Get("dataset")}).Error("geo entities failed")
			httputil.Error(w,
				http.StatusInternalServerError,
				"failed to read the geo entities")
		}
	}
}

// geoOf reads the geo entities of the dataset (only entity only if it is not 0) and the number of places still pending.
func geoOf(r *http.Request,
	d *sql.DB,
	dsID int64,
	dataset string,
	only int64) (*geoResponse, error) {
	all, err := graph.ListEntityGeo(r.Context(),
		d,
		dsID)
	if err != nil {
		return nil, err
	}
	tx, err := graph.Begin(r.Context(), d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // only reads
	pending, err := tx.CountGeoPending(r.Context(), dsID)
	if err != nil {
		return nil, err
	}
	out := make([]geoEntity,
		0,
		len(all))
	for _, g := range all {
		if only != 0 && g.EntityID != only {
			continue
		}
		out = append(out, geoEntity{
			EntityID:    g.EntityID,
			Name:        g.EntityName,
			Type:        g.EntityType,
			Kind:        g.Kind,
			Latitude:    g.Latitude,
			Longitude:   g.Longitude,
			DisplayName: g.DisplayName,
			Country:     g.Country,
			CountryCode: g.CountryCode,
			Region:      g.Region,
			BBox:        g.BBox,
			Provider:    g.Provider,
		})
	}
	if only != 0 && len(out) == 0 {
		return nil, graph.ErrNotFound
	}
	return &geoResponse{
		Dataset:  dataset,
		Entities: out,
		Pending:  pending,
	}, nil
}
