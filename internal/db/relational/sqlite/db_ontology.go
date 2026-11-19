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
	id, user_id, name, description, format, version, spec, size_bytes, sha256,
	classes, relations, properties, created_at, updated_at`

func scanOntology(r scanner) (*model.Ontology, error) {
	var o model.Ontology
	var spec, created, updated string
	if err := r.Scan(&o.ID,
		&o.UserID,
		&o.Name,
		&o.Description,
		&o.Format,
		&o.Version,
		&spec,
		&o.SizeBytes,
		&o.SHA256,
		&o.Classes,
		&o.Relations,
		&o.Properties,
		&created,
		&updated); err != nil {
		return nil, err
	}
	o.Spec = []byte(spec)
	if t := ParseTime(created); t != nil {
		o.CreatedAt = *t
	}
	if t := ParseTime(updated); t != nil {
		o.UpdatedAt = *t
	}
	return &o, nil
}

// CreateOntology stores an ontology as its version 1, with its document (o.Content) in the history. It returns model.ErrExists if the
// user already has an ontology with that name.
func CreateOntology(ctx context.Context,
	d *sql.DB,
	o model.Ontology) (*model.Ontology, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := FormatTime(Now())
	res, err := tx.ExecContext(ctx,
		`
		INSERT INTO gb_ontologies (
			user_id, name, description, format, version, spec, size_bytes, sha256,
			classes, relations, properties, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, name) DO NOTHING`,
		o.UserID,
		o.Name,
		o.Description,
		o.Format,
		string(o.Spec),
		o.SizeBytes,
		o.SHA256,
		o.Classes,
		o.Relations,
		o.Properties,
		now,
		now)
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
	if err := insertOntologyVersion(ctx,
		tx,
		id,
		1,
		o,
		now); err != nil {
		return nil, err
	}
	out, err := scanOntology(tx.QueryRowContext(ctx,
		`
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE id = ?`,
		id))
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func insertOntologyVersion(ctx context.Context,
	tx *sql.Tx,
	ontologyID int64,
	version int,
	o model.Ontology,
	now string) error {
	if _, err := tx.ExecContext(ctx,
		`
		INSERT INTO gb_ontology_versions (ontology_id, version, format, content, spec, sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ontologyID,
		version,
		o.Format,
		o.Content,
		string(o.Spec),
		o.SHA256,
		now); err != nil {
		return fmt.Errorf("record ontology version: %w", err)
	}
	return nil
}

// GetOntology returns userID's ontology called name, or model.ErrNotFound.
func GetOntology(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (*model.Ontology, error) {
	o, err := scanOntology(d.QueryRowContext(ctx,
		`
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE user_id = ? AND name = ?`,
		userID,
		name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return o, err
}

// UpdateOntology saves a new version of the ontology o.ID: the description, document and spec are overwritten, the version is
// incremented, and the document is added to the history. The owner, name and creation time stay.
func UpdateOntology(ctx context.Context,
	d *sql.DB,
	o model.Ontology) (*model.Ontology, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := FormatTime(Now())
	out, err := scanOntology(tx.QueryRowContext(ctx,
		`
		UPDATE gb_ontologies
		SET description = ?,
			format      = ?,
			version     = version + 1,
			spec        = ?,
			size_bytes  = ?,
			sha256      = ?,
			classes     = ?,
			relations   = ?,
			properties  = ?,
			updated_at  = ?
		WHERE id = ?
		RETURNING `+ontologyColumns,
		o.Description,
		o.Format,
		string(o.Spec),
		o.SizeBytes,
		o.SHA256,
		o.Classes,
		o.Relations,
		o.Properties,
		now,
		o.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update ontology: %w", err)
	}
	if err := insertOntologyVersion(ctx,
		tx,
		out.ID,
		out.Version,
		o,
		now); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// ListOntologies returns userID's ontologies, oldest first.
func ListOntologies(ctx context.Context,
	d *sql.DB,
	userID string) ([]model.Ontology, error) {
	rows, err := d.QueryContext(ctx,
		`
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		WHERE user_id = ?
		ORDER BY id`,
		userID)
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

// ListAllOntologies returns the ontologies of every user, oldest first, with their specs (for the admin UI).
func ListAllOntologies(ctx context.Context,
	d *sql.DB) ([]model.Ontology, error) {
	rows, err := d.QueryContext(ctx,
		`
		SELECT `+ontologyColumns+`
		FROM gb_ontologies
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list all ontologies: %w", err)
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

// DeleteOntology removes an ontology and its history.
func DeleteOntology(ctx context.Context,
	d *sql.DB,
	id int64) error {
	if _, err := d.ExecContext(ctx,
		`
		DELETE FROM gb_ontologies
		WHERE id = ?`,
		id); err != nil {
		return fmt.Errorf("delete ontology: %w", err)
	}
	return nil
}
