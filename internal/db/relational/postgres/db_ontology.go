package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Ontologies

const ontologyColumns = `id, user_id, name, description, iri, title, format, size_bytes, sha256, classes, properties, edges, created_at, updated_at`

func scanOntology(r scanner) (*model.Ontology, error) {
	var o model.Ontology
	if err := r.Scan(&o.ID, &o.UserID, &o.Name, &o.Description, &o.IRI, &o.Title, &o.Format, &o.SizeBytes, &o.SHA256,
		&o.Classes, &o.Properties, &o.Edges, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.CreatedAt, o.UpdatedAt = o.CreatedAt.UTC(), o.UpdatedAt.UTC()
	return &o, nil
}

// CreateOntology stores the metadata of an ontology. It returns model.ErrExists if the user already has an
// ontology with that name.
func CreateOntology(ctx context.Context, d *sql.DB, o model.Ontology) (*model.Ontology, error) {
	res, err := scanOntology(d.QueryRowContext(ctx, `
		INSERT INTO gb_ontologies (user_id, name, description, iri, title, format, size_bytes, sha256, classes, properties, edges, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		ON CONFLICT (user_id, name) DO NOTHING
		RETURNING `+ontologyColumns,
		o.UserID, o.Name, o.Description, o.IRI, o.Title, o.Format, o.SizeBytes, o.SHA256, o.Classes, o.Properties, o.Edges, Now()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrExists
	}
	if err != nil {
		return nil, fmt.Errorf("create ontology: %w", err)
	}
	return res, nil
}

// GetOntology returns the metadata of userID's ontology called name, or model.ErrNotFound.
func GetOntology(ctx context.Context, d *sql.DB, userID, name string) (*model.Ontology, error) {
	o, err := scanOntology(d.QueryRowContext(ctx, `SELECT `+ontologyColumns+` FROM gb_ontologies WHERE user_id = $1 AND name = $2`, userID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return o, err
}

// UpdateOntology overwrites the metadata of the ontology o.ID (not its owner, name or creation time).
func UpdateOntology(ctx context.Context, d *sql.DB, o model.Ontology) (*model.Ontology, error) {
	res, err := scanOntology(d.QueryRowContext(ctx, `
		UPDATE gb_ontologies SET description = $1, iri = $2, title = $3, format = $4, size_bytes = $5, sha256 = $6,
			classes = $7, properties = $8, edges = $9, updated_at = $10
		WHERE id = $11
		RETURNING `+ontologyColumns,
		o.Description, o.IRI, o.Title, o.Format, o.SizeBytes, o.SHA256, o.Classes, o.Properties, o.Edges, Normalize(o.UpdatedAt), o.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update ontology: %w", err)
	}
	return res, nil
}

// ListOntologies returns userID's ontologies, oldest first.
func ListOntologies(ctx context.Context, d *sql.DB, userID string) ([]model.Ontology, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+ontologyColumns+` FROM gb_ontologies WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list ontologies: %w", err)
	}
	defer rows.Close()
	out := []model.Ontology{}
	for rows.Next() {
		o, err := scanOntology(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// DeleteOntology removes the metadata row of an ontology.
func DeleteOntology(ctx context.Context, d *sql.DB, id int64) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM gb_ontologies WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete ontology: %w", err)
	}
	return nil
}
