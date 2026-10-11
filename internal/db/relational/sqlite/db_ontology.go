package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Ontologies

const ontologyColumns = `
	id, user_id, name, description, iri, title, format, size_bytes, sha256,
	classes, properties, edges, created_at, updated_at`

func scanOntology(r scanner) (*model.Ontology, error) {
	var o model.Ontology
	var created, updated string
	if err := r.Scan(&o.ID, &o.UserID, &o.Name, &o.Description, &o.IRI, &o.Title, &o.Format, &o.SizeBytes, &o.SHA256,
		&o.Classes, &o.Properties, &o.Edges, &created, &updated); err != nil {
		return nil, err
	}
	if t := ParseTime(created); t != nil {
		o.CreatedAt = *t
	}
	if t := ParseTime(updated); t != nil {
		o.UpdatedAt = *t
	}
	return &o, nil
}

// CreateOntology stores the metadata of an ontology. It returns model.ErrExists if the user already has an
// ontology with that name.
func CreateOntology(ctx context.Context, d *sql.DB, o model.Ontology) (*model.Ontology, error) {
	now := Now()
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_ontologies (
			user_id, name, description, iri, title, format, size_bytes, sha256,
			classes, properties, edges, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, name) DO NOTHING`,
		o.UserID, o.Name, o.Description, o.IRI, o.Title, o.Format, o.SizeBytes, o.SHA256, o.Classes, o.Properties, o.Edges, FormatTime(now), FormatTime(now))
	if err != nil {
		return nil, fmt.Errorf("create ontology: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, model.ErrExists
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanOntology(d.QueryRowContext(ctx, `
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE id = ?`, id))
}

// GetOntology returns the metadata of userID's ontology called name, or model.ErrNotFound.
func GetOntology(ctx context.Context, d *sql.DB, userID, name string) (*model.Ontology, error) {
	o, err := scanOntology(d.QueryRowContext(ctx, `
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE user_id = ? AND name = ?`, userID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return o, err
}

// UpdateOntology overwrites the metadata of the ontology o.ID (not its owner, name or creation time).
func UpdateOntology(ctx context.Context, d *sql.DB, o model.Ontology) (*model.Ontology, error) {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_ontologies
		SET description = ?,
			iri         = ?,
			title       = ?,
			format      = ?,
			size_bytes  = ?,
			sha256      = ?,
			classes     = ?,
			properties  = ?,
			edges       = ?,
			updated_at  = ?
		WHERE id = ?`,
		o.Description, o.IRI, o.Title, o.Format, o.SizeBytes, o.SHA256, o.Classes, o.Properties, o.Edges, FormatTime(o.UpdatedAt), o.ID)
	if err != nil {
		return nil, fmt.Errorf("update ontology: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, model.ErrNotFound
	}
	return scanOntology(d.QueryRowContext(ctx, `
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE id = ?`, o.ID))
}

// ListOntologies returns userID's ontologies, oldest first.
func ListOntologies(ctx context.Context, d *sql.DB, userID string) ([]model.Ontology, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE user_id = ?
		ORDER BY id`, userID)
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
	if _, err := d.ExecContext(ctx, `
		DELETE FROM gb_ontologies
		WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete ontology: %w", err)
	}
	return nil
}
