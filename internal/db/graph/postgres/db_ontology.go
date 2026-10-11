package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
)

//---------------------------------------------------------------------------------------------
// Ontologies

// LoadOntology stores the classes and edges of an ontology in one transaction, replacing any it had stored.
func LoadOntology(ctx context.Context, d *sql.DB, ontologyID int64, classes []model.OntologyClass, edges []model.OntologyEdge) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"gb_onto_edges", "gb_onto_classes"} {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM `+table+`
			WHERE ontology_id = $1`, ontologyID); err != nil {
			return fmt.Errorf("clear ontology: %w", err)
		}
	}
	if err := mergeOntology(ctx, tx, ontologyID, classes, edges); err != nil {
		return err
	}
	return tx.Commit()
}

// MergeOntology merges classes and edges into a stored ontology in one transaction and returns its size
// afterwards. The merge rules are documented on graph.MergeOntology.
func MergeOntology(ctx context.Context, d *sql.DB, ontologyID int64, classes []model.OntologyClass, edges []model.OntologyEdge) (model.OntologyCounts, error) {
	var c model.OntologyCounts
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if err := mergeOntology(ctx, tx, ontologyID, classes, edges); err != nil {
		return c, err
	}
	err = tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM gb_onto_classes WHERE ontology_id = $1),
			(
				SELECT COUNT(DISTINCT iri)
				FROM gb_onto_edges
				WHERE ontology_id = $1 AND kind = 'property'
			),
			(SELECT COUNT(*) FROM gb_onto_edges WHERE ontology_id = $1)`, ontologyID).
		Scan(&c.Classes, &c.Properties, &c.Edges)
	if err != nil {
		return c, fmt.Errorf("count ontology: %w", err)
	}
	return c, tx.Commit()
}

// mergeOntology upserts the classes, then the edges, inside tx. Upserting keeps the row of what exists already, so
// edges that point to it stay valid.
func mergeOntology(ctx context.Context, tx *sql.Tx, ontologyID int64, classes []model.OntologyClass, edges []model.OntologyEdge) error {
	ids := make(map[string]int64, len(classes))
	for _, c := range classes {
		// A declared class overwrites the stored one. A class that is only mentioned must not: its conflict clause
		// is a no-op update, which is there only so that RETURNING yields the ID of the existing row.
		onConflict := `DO UPDATE SET iri = gb_onto_classes.iri`
		if c.Declared {
			onConflict = `DO UPDATE SET
				name    = EXCLUDED.name,
				label   = EXCLUDED.label,
				comment = EXCLUDED.comment`
		}
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_onto_classes (ontology_id, iri, name, label, comment)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (ontology_id, iri) `+onConflict+`
			RETURNING id`,
			ontologyID, c.IRI, c.Name, c.Label, c.Comment).Scan(&id)
		if err != nil {
			return fmt.Errorf("store ontology class %q: %w", c.IRI, err)
		}
		ids[c.IRI] = id
	}
	for _, e := range edges {
		src, ok1 := ids[e.SourceIRI]
		tgt, ok2 := ids[e.TargetIRI]
		if !ok1 || !ok2 {
			return fmt.Errorf("ontology edge %q: unknown class %q or %q", e.Relation, e.SourceIRI, e.TargetIRI)
		}
		// An edge is identified by (source, target, kind, iri); a stored one is overwritten.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_onto_edges (ontology_id, source_id, target_id, relation, kind, iri, label, comment, inverse_of)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (ontology_id, source_id, target_id, kind, iri) DO UPDATE SET
				relation   = EXCLUDED.relation,
				label      = EXCLUDED.label,
				comment    = EXCLUDED.comment,
				inverse_of = EXCLUDED.inverse_of`,
			ontologyID, src, tgt, e.Relation, e.Kind, e.IRI, e.Label, e.Comment, e.InverseOf); err != nil {
			return fmt.Errorf("store ontology edge %q: %w", e.Relation, err)
		}
	}
	return nil
}

// ListOntologyClasses returns the classes of the ontology, in the order they were stored.
func ListOntologyClasses(ctx context.Context, d *sql.DB, ontologyID int64) ([]model.OntologyClass, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT iri, name, label, comment
		FROM gb_onto_classes
		WHERE ontology_id = $1
		ORDER BY id`, ontologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.OntologyClass{}
	for rows.Next() {
		var c model.OntologyClass
		if err := rows.Scan(&c.IRI, &c.Name, &c.Label, &c.Comment); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListOntologyEdges returns the edges of the ontology, in the order they were stored.
func ListOntologyEdges(ctx context.Context, d *sql.DB, ontologyID int64) ([]model.OntologyEdge, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT s.iri, t.iri, e.relation, e.kind, e.iri, e.label, e.comment, e.inverse_of
		FROM gb_onto_edges e
		JOIN gb_onto_classes s ON s.id = e.source_id
		JOIN gb_onto_classes t ON t.id = e.target_id
		WHERE e.ontology_id = $1
		ORDER BY e.id`, ontologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.OntologyEdge{}
	for rows.Next() {
		var e model.OntologyEdge
		if err := rows.Scan(&e.SourceIRI, &e.TargetIRI, &e.Relation, &e.Kind, &e.IRI, &e.Label, &e.Comment, &e.InverseOf); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
