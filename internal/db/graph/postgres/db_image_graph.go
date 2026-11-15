package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
)

// GetImageGraph returns the graph record of an extraction (its description and the artifact of the remembered text). It
// returns model.ErrNotFound if the extraction has none (it had no description).
func GetImageGraph(ctx context.Context,
	d *sql.DB,
	extractionID int64) (*model.ImageGraph, error) {
	var g model.ImageGraph
	var artifactID sql.NullInt64
	err := d.QueryRowContext(ctx,
		`
		SELECT description, artifact_id
		FROM gb_image_graphs
		WHERE extraction_id = $1`,
		extractionID).Scan(&g.Description, &artifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("image extraction %d graph: %w",
			extractionID,
			model.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	g.ArtifactID = artifactID.Int64
	return &g, nil
}

// SetImageGraphArtifact records the artifact that the description of an extraction was remembered as.
func SetImageGraphArtifact(ctx context.Context,
	d *sql.DB,
	extractionID,
	artifactID int64) error {
	res, err := d.ExecContext(ctx,
		`
		UPDATE gb_image_graphs
		SET artifact_id = $1
		WHERE extraction_id = $2`,
		artifactID,
		extractionID)
	if err != nil {
		return fmt.Errorf("set image graph artifact: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("image extraction %d graph: %w",
			extractionID,
			model.ErrNotFound)
	}
	return nil
}
