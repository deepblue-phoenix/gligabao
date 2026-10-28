package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
)

// The geo metadata of entities (gb_entity_geo, see db_schema.go).

const geoColumns = `g.entity_id, g.kind, g.query, g.latitude, g.longitude, g.display_name, g.country, g.country_code, g.region,
	g.place_type, g.bbox_south, g.bbox_west, g.bbox_north, g.bbox_east, g.importance, g.provider, g.provider_ref, g.created_at, g.updated_at`

func (t *Tx) UpsertEntityGeo(ctx context.Context, g model.NewEntityGeo) error {
	var s, w, n, e any
	if g.BBox != nil {
		s, w, n, e = g.BBox[0], g.BBox[1], g.BBox[2], g.BBox[3]
	}
	now := rsqlite.FormatTime(g.Now)
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_entity_geo (entity_id, kind, query, latitude, longitude, display_name, country, country_code, region,
			place_type, bbox_south, bbox_west, bbox_north, bbox_east, importance, provider, provider_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id) DO UPDATE SET kind = excluded.kind, query = excluded.query, latitude = excluded.latitude,
			longitude = excluded.longitude, display_name = excluded.display_name, country = excluded.country,
			country_code = excluded.country_code, region = excluded.region, place_type = excluded.place_type,
			bbox_south = excluded.bbox_south, bbox_west = excluded.bbox_west, bbox_north = excluded.bbox_north,
			bbox_east = excluded.bbox_east, importance = excluded.importance, provider = excluded.provider,
			provider_ref = excluded.provider_ref, updated_at = excluded.updated_at`,
		g.EntityID, g.Kind, g.Query, g.Latitude, g.Longitude, g.DisplayName, g.Country, g.CountryCode, g.Region,
		g.PlaceType, s, w, n, e, g.Importance, g.Provider, g.ProviderRef, now, now)
	if err != nil {
		return fmt.Errorf("upsert entity geo %d: %w", g.EntityID, err)
	}
	return nil
}

func (t *Tx) GetEntityGeo(ctx context.Context, entityID int64) (*model.EntityGeo, error) {
	g, err := scanGeo(t.tx.QueryRowContext(ctx, `SELECT `+geoColumns+` FROM gb_entity_geo g WHERE g.entity_id = ?`, entityID), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return g, err
}

// GetEntityGeo returns the geo metadata of an entity; ErrNotFound if it has none.
func GetEntityGeo(ctx context.Context, d *sql.DB, entityID int64) (*model.EntityGeo, error) {
	g, err := scanGeo(d.QueryRowContext(ctx, `SELECT `+geoColumns+` FROM gb_entity_geo g WHERE g.entity_id = ?`, entityID), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("entity %d geo: %w", entityID, model.ErrNotFound)
	}
	return g, err
}

// ListEntityGeo returns the geo metadata of the entities of the dataset, by entity ID, with the entity's name and type.
func ListEntityGeo(ctx context.Context, d *sql.DB, dsID int64) ([]model.EntityGeo, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+geoColumns+`, e.name, e.type
		FROM gb_entity_geo g
		JOIN gb_entities e ON e.id = g.entity_id
		WHERE e.dataset_id = ?
		ORDER BY g.entity_id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EntityGeo
	for rows.Next() {
		g, err := scanGeo(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// GeoStats counts the geo entities of the live datasets and lists the limit most connected ones: the connections of an entity
// are its live (not expired) edges, as source or as target (a self-loop counts once).
func GeoStats(ctx context.Context, d *sql.DB, limit int) (*model.GeoStats, error) {
	var st model.GeoStats
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(g.entity_id)
		FROM gb_entities e
		JOIN gb_datasets ds ON ds.id = e.dataset_id AND ds.deleted = 0
		LEFT JOIN gb_entity_geo g ON g.entity_id = e.id`).Scan(&st.AllEntities, &st.Entities)
	if err != nil {
		return nil, fmt.Errorf("count geo entities: %w", err)
	}
	rows, err := d.QueryContext(ctx, `
		SELECT `+geoColumns+`, e.name, e.type, ds.user_id, ds.name,
			(SELECT COUNT(*) FROM gb_edges x WHERE x.source_id = e.id AND x.expired_at IS NULL)
			+ (SELECT COUNT(*) FROM gb_edges x WHERE x.target_id = e.id AND x.source_id <> e.id AND x.expired_at IS NULL) AS connections
		FROM gb_entity_geo g
		JOIN gb_entities e ON e.id = g.entity_id
		JOIN gb_datasets ds ON ds.id = e.dataset_id AND ds.deleted = 0
		ORDER BY connections DESC, g.entity_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list connected geo entities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c model.ConnectedGeo
		g, err := scanGeo(rows, true, &c.UserID, &c.Dataset, &c.Connections)
		if err != nil {
			return nil, err
		}
		c.EntityGeo = *g
		st.Top = append(st.Top, c)
	}
	return &st, rows.Err()
}

// scanGeo reads a row of geoColumns, followed by the entity's name and type if withEntity, then the extra columns.
func scanGeo(r scanner, withEntity bool, extra ...any) (*model.EntityGeo, error) {
	var g model.EntityGeo
	var s, w, n, e sql.NullFloat64
	var created, updated string
	dest := []any{&g.EntityID, &g.Kind, &g.Query, &g.Latitude, &g.Longitude, &g.DisplayName, &g.Country, &g.CountryCode, &g.Region,
		&g.PlaceType, &s, &w, &n, &e, &g.Importance, &g.Provider, &g.ProviderRef, &created, &updated}
	if withEntity {
		dest = append(dest, &g.EntityName, &g.EntityType)
	}
	dest = append(dest, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	if s.Valid && w.Valid && n.Valid && e.Valid {
		g.BBox = &[4]float64{s.Float64, w.Float64, n.Float64, e.Float64}
	}
	if t := rsqlite.ParseTime(created); t != nil {
		g.CreatedAt = *t
	}
	if t := rsqlite.ParseTime(updated); t != nil {
		g.UpdatedAt = *t
	}
	return &g, nil
}

// geoBoxMargin widens the bounding box (degrees) so that the 32-bit floats of the R*Tree never lose a point at its edge; the
// candidates are refined with the exact distance by the caller.
const geoBoxMargin = 1e-3

// EntityGeoWithin returns the geo entities of the dataset in the bounding box of the circle of radiusKm around (lat, lon) (so a
// superset of those within the radius: the caller refines with the distance), with their entity's name and type. It asks the
// R*Tree when the geo index exists (EnableGeoIndex), else the index on (latitude, longitude).
func EntityGeoWithin(ctx context.Context, d *sql.DB, dsID int64, lat, lon, radiusKm float64) ([]model.EntityGeo, error) {
	box := memory_geo.BoundingBox(lat, lon, radiusKm)
	var has int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'gb_entity_geo_rtree'`).Scan(&has)
	if err != nil {
		return nil, fmt.Errorf("look for the geo index: %w", err)
	}
	var minLat, maxLat, minLon, maxLon string
	if has > 0 {
		minLat, maxLat, minLon, maxLon = "min_lat", "max_lat", "min_lon", "max_lon"
	} else {
		minLat, maxLat, minLon, maxLon = "g.latitude", "g.latitude", "g.longitude", "g.longitude"
	}
	args := []any{dsID, box.MinLat - geoBoxMargin, box.MaxLat + geoBoxMargin}
	var lonConds []string
	for _, r := range box.Lon {
		lonConds = append(lonConds, fmt.Sprintf("(%s >= ? AND %s <= ?)", minLon, maxLon))
		args = append(args, r.Min-geoBoxMargin, r.Max+geoBoxMargin)
	}
	inBox := fmt.Sprintf("%s >= ? AND %s <= ? AND (%s)", minLat, maxLat, strings.Join(lonConds, " OR "))
	var q string
	if has > 0 {
		q = `g.entity_id IN (SELECT entity_id FROM gb_entity_geo_rtree WHERE ` + inBox + `)`
	} else {
		q = inBox
	}
	rows, err := d.QueryContext(ctx, `
		SELECT `+geoColumns+`, e.name, e.type
		FROM gb_entity_geo g
		JOIN gb_entities e ON e.id = g.entity_id
		WHERE e.dataset_id = ? AND `+q, args...)
	if err != nil {
		return nil, fmt.Errorf("entity geo within %v km: %w", radiusKm, err)
	}
	defer rows.Close()
	var out []model.EntityGeo
	for rows.Next() {
		g, err := scanGeo(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}
