package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	"github.com/deepblue-phoenix/gligabao/internal/geo"
)

// The geo metadata of entities (gb_entity_geo, see sqlite/db_schema.go).

const geoColumns = `g.entity_id, g.kind, g.query, g.latitude, g.longitude, g.display_name, g.country, g.country_code, g.region,
	g.place_type, g.bbox_south, g.bbox_west, g.bbox_north, g.bbox_east, g.importance, g.provider, g.provider_ref, g.created_at, g.updated_at`

func (t *Tx) UpsertEntityGeo(ctx context.Context, g model.NewEntityGeo) error {
	var s, w, n, e any
	if g.BBox != nil {
		s, w, n, e = g.BBox[0], g.BBox[1], g.BBox[2], g.BBox[3]
	}
	now := g.Now.UTC()
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO gb_entity_geo (entity_id, kind, query, latitude, longitude, display_name, country, country_code, region,
			place_type, bbox_south, bbox_west, bbox_north, bbox_east, importance, provider, provider_ref, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18)
		ON CONFLICT (entity_id) DO UPDATE SET kind = EXCLUDED.kind, query = EXCLUDED.query, latitude = EXCLUDED.latitude,
			longitude = EXCLUDED.longitude, display_name = EXCLUDED.display_name, country = EXCLUDED.country,
			country_code = EXCLUDED.country_code, region = EXCLUDED.region, place_type = EXCLUDED.place_type,
			bbox_south = EXCLUDED.bbox_south, bbox_west = EXCLUDED.bbox_west, bbox_north = EXCLUDED.bbox_north,
			bbox_east = EXCLUDED.bbox_east, importance = EXCLUDED.importance, provider = EXCLUDED.provider,
			provider_ref = EXCLUDED.provider_ref, updated_at = EXCLUDED.updated_at`,
		g.EntityID, g.Kind, g.Query, g.Latitude, g.Longitude, g.DisplayName, g.Country, g.CountryCode, g.Region,
		g.PlaceType, s, w, n, e, g.Importance, g.Provider, g.ProviderRef, now)
	if err != nil {
		return fmt.Errorf("upsert entity geo %d: %w", g.EntityID, err)
	}
	return nil
}

func (t *Tx) GetEntityGeo(ctx context.Context, entityID int64) (*model.EntityGeo, error) {
	g, err := scanGeo(t.tx.QueryRowContext(ctx, `SELECT `+geoColumns+` FROM gb_entity_geo g WHERE g.entity_id = $1`, entityID), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return g, err
}

// GetEntityGeo returns the geo metadata of an entity; ErrNotFound if it has none.
func GetEntityGeo(ctx context.Context, d *sql.DB, entityID int64) (*model.EntityGeo, error) {
	g, err := scanGeo(d.QueryRowContext(ctx, `SELECT `+geoColumns+` FROM gb_entity_geo g WHERE g.entity_id = $1`, entityID), false)
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
		WHERE e.dataset_id = $1
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
		JOIN gb_datasets ds ON ds.id = e.dataset_id AND NOT ds.deleted
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
		JOIN gb_datasets ds ON ds.id = e.dataset_id AND NOT ds.deleted
		ORDER BY connections DESC, g.entity_id
		LIMIT $1`, limit)
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
	dest := []any{&g.EntityID, &g.Kind, &g.Query, &g.Latitude, &g.Longitude, &g.DisplayName, &g.Country, &g.CountryCode, &g.Region,
		&g.PlaceType, &s, &w, &n, &e, &g.Importance, &g.Provider, &g.ProviderRef, &g.CreatedAt, &g.UpdatedAt}
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
	g.CreatedAt, g.UpdatedAt = g.CreatedAt.UTC(), g.UpdatedAt.UTC()
	return &g, nil
}

// geoBoxMargin widens the bounding box (degrees) of the query without PostGIS, for rounding.
const geoBoxMargin = 1e-6

// EntityGeoWithin returns the geo entities of the dataset near (lat, lon), with their entity's name and type: those within
// radiusKm when PostGIS indexes them (ST_DWithin on the GiST index, see EnableGeoIndex), else those in the bounding box of the
// circle (the index on latitude and longitude), a superset: the caller refines with the distance.
func EntityGeoWithin(ctx context.Context, d *sql.DB, dsID int64, lat, lon, radiusKm float64) ([]model.EntityGeo, error) {
	// the schema of the extension, if the geo column that EnableGeoIndex adds exists
	var ns sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT quote_ident(n.nspname)
		FROM pg_extension x
		JOIN pg_namespace n ON n.oid = x.extnamespace
		WHERE x.extname = 'postgis'
			AND EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'gb_entity_geo' AND column_name = 'geo')`).Scan(&ns)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("look for the geo index: %w", err)
	}
	var where string
	args := []any{dsID}
	if ns.Valid {
		where = fmt.Sprintf(`%[1]s.ST_DWithin(g.geo, %[1]s.ST_SetSRID(%[1]s.ST_MakePoint($2, $3), 4326)::%[1]s.geography, $4)`, ns.String)
		args = append(args, lon, lat, radiusKm*1000)
	} else {
		box := geo.BoundingBox(lat, lon, radiusKm)
		args = append(args, box.MinLat-geoBoxMargin, box.MaxLat+geoBoxMargin)
		var lonConds []string
		for _, r := range box.Lon {
			lonConds = append(lonConds, fmt.Sprintf("(g.longitude BETWEEN $%d AND $%d)", len(args)+1, len(args)+2))
			args = append(args, r.Min-geoBoxMargin, r.Max+geoBoxMargin)
		}
		where = "g.latitude BETWEEN $2 AND $3 AND (" + strings.Join(lonConds, " OR ") + ")"
	}
	rows, err := d.QueryContext(ctx, `
		SELECT `+geoColumns+`, e.name, e.type
		FROM gb_entity_geo g
		JOIN gb_entities e ON e.id = g.entity_id
		WHERE e.dataset_id = $1 AND `+where, args...)
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
