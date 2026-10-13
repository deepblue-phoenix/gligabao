package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

// InsertImage stores an image artifact and its extraction (with the text blocks) in one transaction.
func InsertImage(ctx context.Context, d *sql.DB, img model.NewImage) (artifactID, extractionID int64, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO gb_image_artifacts (dataset_id, session_id, media_type, sha256, size_bytes, path, valid_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		img.DatasetID, nullIfEmpty(img.SessionID), img.MediaType, img.SHA256, img.SizeBytes, img.Path,
		rsqlite.FormatTime(img.ValidAt), rsqlite.FormatTime(img.CreatedAt))
	if err != nil {
		return 0, 0, fmt.Errorf("insert image artifact: %w", err)
	}
	if artifactID, err = res.LastInsertId(); err != nil {
		return 0, 0, err
	}

	x := img.Extraction
	res, err = tx.ExecContext(ctx, `
		INSERT INTO gb_image_extractions
			(image_artifact_id, provider, model, theme, title, text, raw_output, input_tokens, output_tokens, cost_usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, x.Provider, x.Model, x.Theme, x.Title, x.Text, x.Raw, x.InputTokens, x.OutputTokens, x.CostUSD,
		rsqlite.FormatTime(img.CreatedAt))
	if err != nil {
		return 0, 0, fmt.Errorf("insert image extraction: %w", err)
	}
	if extractionID, err = res.LastInsertId(); err != nil {
		return 0, 0, err
	}
	for i, b := range x.TextBlocks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_image_text_blocks (extraction_id, position, kind, text)
			VALUES (?, ?, ?, ?)`, extractionID, i, b.Kind, b.Text); err != nil {
			return 0, 0, fmt.Errorf("insert image text block: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return artifactID, extractionID, nil
}

const imageArtifactColumns = `id, dataset_id, COALESCE(session_id, ''), media_type, sha256, size_bytes, path, valid_at, created_at`

func scanImageArtifact(r scanner) (model.ImageArtifact, error) {
	var a model.ImageArtifact
	var valid, created string
	if err := r.Scan(&a.ID, &a.DatasetID, &a.SessionID, &a.MediaType, &a.SHA256, &a.SizeBytes, &a.Path, &valid, &created); err != nil {
		return a, err
	}
	if t := rsqlite.ParseTime(valid); t != nil {
		a.ValidAt = *t
	}
	if t := rsqlite.ParseTime(created); t != nil {
		a.CreatedAt = *t
	}
	return a, nil
}

// ListImageArtifacts returns the image artifacts of the dataset by ID.
func ListImageArtifacts(ctx context.Context, d *sql.DB, dsID int64) ([]model.ImageArtifact, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+imageArtifactColumns+`
		FROM gb_image_artifacts
		WHERE dataset_id = ?
		ORDER BY id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ImageArtifact
	for rows.Next() {
		a, err := scanImageArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetImageArtifact returns one image artifact of the dataset.
func GetImageArtifact(ctx context.Context, d *sql.DB, dsID, artifactID int64) (*model.ImageArtifact, error) {
	a, err := scanImageArtifact(d.QueryRowContext(ctx, `
		SELECT `+imageArtifactColumns+`
		FROM gb_image_artifacts
		WHERE id = ? AND dataset_id = ?`, artifactID, dsID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("image artifact %d: %w", artifactID, model.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

const imageExtractionColumns = `x.id, x.image_artifact_id, x.provider, x.model, x.theme, x.title, x.text, x.raw_output,
	x.input_tokens, x.output_tokens, x.cost_usd, x.created_at`

func scanImageExtraction(r scanner) (model.ImageExtraction, error) {
	var x model.ImageExtraction
	var created string
	if err := r.Scan(&x.ID, &x.ImageArtifactID, &x.Provider, &x.Model, &x.Theme, &x.Title, &x.Text, &x.Raw,
		&x.InputTokens, &x.OutputTokens, &x.CostUSD, &created); err != nil {
		return x, err
	}
	if t := rsqlite.ParseTime(created); t != nil {
		x.CreatedAt = *t
	}
	return x, nil
}

// FindImageBySHA256 returns the newest image artifact of the dataset with the given content hash, with its newest
// extraction.
func FindImageBySHA256(ctx context.Context, d *sql.DB, dsID int64, sha string) (*model.ImageArtifact, *model.ImageExtraction, error) {
	a, err := scanImageArtifact(d.QueryRowContext(ctx, `
		SELECT `+imageArtifactColumns+`
		FROM gb_image_artifacts
		WHERE dataset_id = ? AND sha256 = ?
		ORDER BY id DESC
		LIMIT 1`, dsID, sha))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, model.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	x, err := scanImageExtraction(d.QueryRowContext(ctx, `
		SELECT `+imageExtractionColumns+`
		FROM gb_image_extractions x
		WHERE x.image_artifact_id = ?
		ORDER BY x.id DESC
		LIMIT 1`, a.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, model.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.QueryContext(ctx, `
		SELECT kind, text
		FROM gb_image_text_blocks
		WHERE extraction_id = ?
		ORDER BY position`, x.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b model.ImageTextBlock
		if err := rows.Scan(&b.Kind, &b.Text); err != nil {
			return nil, nil, err
		}
		x.TextBlocks = append(x.TextBlocks, b)
	}
	return &a, &x, rows.Err()
}

// ListImageExtractions returns the image extractions of the dataset, with their text blocks, by ID.
func ListImageExtractions(ctx context.Context, d *sql.DB, dsID int64) ([]model.ImageExtraction, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT x.id, x.image_artifact_id, x.provider, x.model, x.theme, x.title, x.text, x.raw_output,
			x.input_tokens, x.output_tokens, x.cost_usd, x.created_at
		FROM gb_image_extractions x
		JOIN gb_image_artifacts a ON a.id = x.image_artifact_id
		WHERE a.dataset_id = ?
		ORDER BY x.id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ImageExtraction
	byID := map[int64]int{}
	for rows.Next() {
		var x model.ImageExtraction
		var created string
		if err := rows.Scan(&x.ID, &x.ImageArtifactID, &x.Provider, &x.Model, &x.Theme, &x.Title, &x.Text, &x.Raw,
			&x.InputTokens, &x.OutputTokens, &x.CostUSD, &created); err != nil {
			return nil, err
		}
		if t := rsqlite.ParseTime(created); t != nil {
			x.CreatedAt = *t
		}
		byID[x.ID] = len(out)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	brows, err := d.QueryContext(ctx, `
		SELECT b.extraction_id, b.kind, b.text
		FROM gb_image_text_blocks b
		JOIN gb_image_extractions x ON x.id = b.extraction_id
		JOIN gb_image_artifacts a ON a.id = x.image_artifact_id
		WHERE a.dataset_id = ?
		ORDER BY b.extraction_id, b.position`, dsID)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var xid int64
		var b model.ImageTextBlock
		if err := brows.Scan(&xid, &b.Kind, &b.Text); err != nil {
			return nil, err
		}
		out[byID[xid]].TextBlocks = append(out[byID[xid]].TextBlocks, b)
	}
	return out, brows.Err()
}

// ImageStats returns the totals over all image artifacts and extractions, and the path of every image file.
func ImageStats(ctx context.Context, d *sql.DB) (*model.ImageStats, error) {
	var st model.ImageStats
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM gb_image_artifacts`).Scan(&st.Images, &st.Bytes); err != nil {
		return nil, err
	}
	if err := d.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0)
		FROM gb_image_extractions`).Scan(&st.Extractions, &st.InputTokens, &st.OutputTokens, &st.CostUSD); err != nil {
		return nil, err
	}
	var err error
	if st.MediaTypes, err = imageGroups(ctx, d, `
		SELECT media_type, COUNT(*), COALESCE(SUM(size_bytes), 0) FROM gb_image_artifacts
		GROUP BY media_type ORDER BY COUNT(*) DESC, media_type`); err != nil {
		return nil, err
	}
	if st.Themes, err = imageGroups(ctx, d, `
		SELECT theme, COUNT(*), 0 FROM gb_image_extractions
		GROUP BY theme ORDER BY COUNT(*) DESC, theme`); err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT path FROM gb_image_artifacts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		st.Paths = append(st.Paths, p)
	}
	return &st, rows.Err()
}

func imageGroups(ctx context.Context, d *sql.DB, query string) ([]model.ImageGroup, error) {
	rows, err := d.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ImageGroup{}
	for rows.Next() {
		var g model.ImageGroup
		if err := rows.Scan(&g.Name, &g.Count, &g.Bytes); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
