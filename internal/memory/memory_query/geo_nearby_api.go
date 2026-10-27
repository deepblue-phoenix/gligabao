package memory_query

// Nearby: the proximity search of Query, callable on its own for a stored geo entity or a point (GET /v1/gb/geo/nearby for the
// end user, GET /v1/gb/admin/api/geo/nearby for the geo view of the admin UI). It runs the same searchNearby as the proximity step
// of Query, with the same radius rules and limits, so it returns the entities Query would give the LLM around that anchor. Unlike
// Query it never asks the geo resolver and writes nothing: the anchor has to be stored (an entity with coordinates) or be a point.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// ErrInvalidGeoPoint means the coordinates given to NearbyPoint are not a latitude (-90..90) and a longitude (-180..180).
var ErrInvalidGeoPoint = errors.New("invalid geo point")

// NearbyEntityOf returns the geo entities of the dataset near the geo entity entityID, the way Query selects them: within the
// radius (memory_core.WithGeoRadius, else memory_core.DefaultGeoRadiusKm; memory_core.ErrInvalidGeoRadius if it is invalid), nearest
// first, at most 30, the entity itself left out. It is the single anchor of the result. graph.ErrNotFound if the dataset has no
// such entity, or it has no coordinates; memory_core.ErrDatasetNotFound if the dataset does not exist.
func NearbyEntityOf(ctx context.Context, d *sql.DB, userID, dataset string, entityID int64, opts ...memory_core.Option) (*GeoContext, error) {
	dsID, radiusKm, err := resolveNearby(ctx, d, userID, dataset, opts)
	if err != nil {
		return nil, err
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // only reads
	e, err := tx.GetEntity(ctx, dsID, entityID)
	if err != nil {
		return nil, err
	}
	g, err := tx.GetEntityGeo(ctx, entityID)
	if err != nil {
		return nil, err
	}
	return nearbyOf(ctx, d, dsID, []GeoAnchor{{Name: e.Name, EntityID: entityID, Latitude: g.Latitude, Longitude: g.Longitude}}, radiusKm)
}

// NearbyPoint is NearbyEntityOf for a point: the anchor is (lat, lon), named "point" and not an entity, so every geo entity in the
// radius is listed. ErrInvalidGeoPoint if the coordinates are out of range.
func NearbyPoint(ctx context.Context, d *sql.DB, userID, dataset string, lat, lon float64, opts ...memory_core.Option) (*GeoContext, error) {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, fmt.Errorf("%w: (%v, %v)", ErrInvalidGeoPoint, lat, lon)
	}
	dsID, radiusKm, err := resolveNearby(ctx, d, userID, dataset, opts)
	if err != nil {
		return nil, err
	}
	return nearbyOf(ctx, d, dsID, []GeoAnchor{{Name: "point", Latitude: lat, Longitude: lon}}, radiusKm)
}

// resolveNearby validates the call the way Query does and returns the ID of the dataset and the radius.
func resolveNearby(ctx context.Context, d *sql.DB, userID, dataset string, opts []memory_core.Option) (dsID int64, radiusKm float64, err error) {
	if userID, err = memory_core.ResolveUserID(userID); err != nil {
		return 0, 0, err
	}
	if dataset, err = memory_core.ResolveDatasetName(dataset); err != nil {
		return 0, 0, err
	}
	if radiusKm, err = geoRadius(memory_core.NewCallOptions(opts)); err != nil {
		return 0, 0, err
	}
	dsID, err = memory_core.DatasetID(ctx, d, userID, dataset)
	return dsID, radiusKm, err
}

// nearbyOf runs searchNearby; unlike Query, which only logs the failures of a step, it fails when the search failed for every anchor.
func nearbyOf(ctx context.Context, d *sql.DB, dsID int64, anchors []GeoAnchor, radiusKm float64) (*GeoContext, error) {
	nearby, errs := searchNearby(ctx, d, dsID, anchors, radiusKm)
	if len(errs) > 0 && len(errs) == len(anchors) {
		return nil, fmt.Errorf("nearby search failed: %s", errs[0])
	}
	if nearby == nil {
		nearby = []NearbyEntity{}
	}
	return &GeoContext{RadiusKm: radiusKm, Anchors: anchors, Nearby: nearby}, nil
}
