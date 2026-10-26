package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
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

// scanGeo reads a row of geoColumns, followed by the entity's name and type if withEntity.
func scanGeo(r scanner, withEntity bool) (*model.EntityGeo, error) {
	var g model.EntityGeo
	var s, w, n, e sql.NullFloat64
	var created, updated string
	dest := []any{&g.EntityID, &g.Kind, &g.Query, &g.Latitude, &g.Longitude, &g.DisplayName, &g.Country, &g.CountryCode, &g.Region,
		&g.PlaceType, &s, &w, &n, &e, &g.Importance, &g.Provider, &g.ProviderRef, &created, &updated}
	if withEntity {
		dest = append(dest, &g.EntityName, &g.EntityType)
	}
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
