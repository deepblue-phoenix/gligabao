package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	log "github.com/sirupsen/logrus"
)

const (
	// geoTopLimit is the number of most connected geo entities GeoOf lists.
	geoTopLimit     = 100
	geoStatsTimeout = 15 * time.Second
)

// GeoEntity is a geo entity (an entity that is a place, with coordinates) and how connected it is.
type GeoEntity struct {
	EntityID    int64   `json:"entity_id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	UserID      string  `json:"user_id"`
	Dataset     string  `json:"dataset"`
	Connections int     `json:"connections"`
	Kind        string  `json:"kind"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	DisplayName string  `json:"display_name,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	Region      string  `json:"region,omitempty"`
	PlaceType   string  `json:"place_type,omitempty"`
	// BBox is south, west, north, east in degrees; absent if unknown.
	BBox       *[4]float64 `json:"bbox,omitempty"`
	Importance float64     `json:"importance,omitempty"`
	Provider   string      `json:"provider,omitempty"`
}

// GeoView is the response of GeoOf.
type GeoView struct {
	// Entities is the number of geo entities (with coordinates) of the live datasets, AllEntities the number of entities there.
	Entities    int `json:"entities"`
	AllEntities int `json:"all_entities"`
	// Top are the most connected geo entities (at most 100), most connected first.
	Top []GeoEntity `json:"top"`
}

// GeoOf serves GET /admin/api/geo[?db=<name>] for one of dbs (the first without db): how many entities are geo entities and the
// 100 most connected ones (by live edges).
func GeoOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name != name {
				continue
			}
			ctx, cancel := context.WithTimeout(r.Context(), geoStatsTimeout)
			defer cancel()
			st, err := graph.GeoStatistics(ctx, n.DB, geoTopLimit)
			if err != nil {
				log.WithError(err).Error("admin: geo statistics failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read geo entities")
				return
			}
			out := GeoView{Entities: st.Entities, AllEntities: st.AllEntities, Top: make([]GeoEntity, 0, len(st.Top))}
			for _, c := range st.Top {
				out.Top = append(out.Top, GeoEntity{
					EntityID: c.EntityID, Name: c.EntityName, Type: c.EntityType, UserID: c.UserID, Dataset: c.Dataset,
					Connections: c.Connections, Kind: c.Kind, Latitude: c.Latitude, Longitude: c.Longitude,
					DisplayName: c.DisplayName, Country: c.Country, CountryCode: c.CountryCode, Region: c.Region,
					PlaceType: c.PlaceType, BBox: c.BBox, Importance: c.Importance, Provider: c.Provider,
				})
			}
			httputil.WriteJSON(w, http.StatusOK, out)
			return
		}
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
}
