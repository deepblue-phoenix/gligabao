package memory

// Datasets are hard logical containers: every memory, entity and edge belongs to exactly
// one dataset, and the same entity in two datasets is two separate rows. Nothing is ever linked or queried
// across datasets. Deleting a dataset is soft (gb_datasets.deleted = 1): its rows stay, but it can no longer be
// read or written, and its name can be reused by a new, empty dataset.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ivan/gligabao/internal/db"
)

// DefaultDataset is used when a request names no dataset.
const DefaultDataset = db.DefaultDataset

var (
	ErrInvalidDataset  = errors.New("invalid dataset")
	ErrInvalidUser     = errors.New("invalid user_id")
	ErrDatasetNotFound = errors.New("dataset not found")
	ErrDatasetExists   = errors.New("dataset already exists")
)

var datasetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// MaxUserIDLen is the maximum length of a user_id in bytes.
const MaxUserIDLen = 128

// Dataset describes a dataset.
type Dataset struct {
	ID          int64      `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags"`
	CreatedAt   time.Time  `json:"created_at"`
	Deleted     bool       `json:"deleted"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

//---------------------------------------------------------------------------------------------

// ResolveUserID trims userID and returns ErrInvalidUser if it is empty, too long or has control characters.
func ResolveUserID(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("%w: user_id is required", ErrInvalidUser)
	}
	if len(userID) > MaxUserIDLen {
		return "", fmt.Errorf("%w: at most %d bytes", ErrInvalidUser, MaxUserIDLen)
	}
	for _, r := range userID {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidUser)
		}
	}
	return userID, nil
}

// ResolveDatasetName trims name and applies the default for an empty one. It returns ErrInvalidDataset if the
// name is not 1-64 characters of letters, digits, '.', '_' or '-' (starting with a letter or digit).
func ResolveDatasetName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DefaultDataset, nil
	}
	if !datasetNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: name must be 1-64 characters of letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidDataset)
	}
	return name, nil
}

func normTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

//---------------------------------------------------------------------------------------------

// CreateDataset creates a dataset owned by userID. The name is required (no default is applied). It returns
// ErrDatasetExists if a live dataset with that name exists for the user.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*Dataset, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidDataset)
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	tagsJSON, _ := json.Marshal(normTags(tags))
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, description, tags, created_at)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM gb_datasets WHERE user_id = ? AND name = ? AND deleted = 0)`,
		userID, name, strings.TrimSpace(description), string(tagsJSON), db.FormatTime(db.Now()), userID, name)
	if err != nil {
		return nil, fmt.Errorf("create dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrDatasetExists
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return getDataset(ctx, d, id)
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]Dataset, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	q := datasetSelect + ` WHERE user_id = ?`
	if !includeDeleted {
		q += ` AND deleted = 0`
	}
	rows, err := d.QueryContext(ctx, q+` ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list datasets: %w", err)
	}
	defer rows.Close()
	out := []Dataset{}
	for rows.Next() {
		ds, err := scanDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

// DeleteDataset soft-deletes userID's live dataset called name (sets deleted = 1). It returns ErrDatasetNotFound
// if there is none.
func DeleteDataset(ctx context.Context, d *sql.DB, userID, name string) error {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return err
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return err
	}
	res, err := d.ExecContext(ctx, `UPDATE gb_datasets SET deleted = 1, deleted_at = ? WHERE user_id = ? AND name = ? AND deleted = 0`,
		db.FormatTime(db.Now()), userID, name)
	if err != nil {
		return fmt.Errorf("delete dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDatasetNotFound
	}
	return nil
}

//---------------------------------------------------------------------------------------------

const datasetSelect = `SELECT id, user_id, name, description, tags, created_at, deleted, deleted_at FROM gb_datasets`

func scanDataset(r scanner) (*Dataset, error) {
	var ds Dataset
	var tags, created string
	var deleted int
	var deletedAt sql.NullString
	if err := r.Scan(&ds.ID, &ds.UserID, &ds.Name, &ds.Description, &tags, &created, &deleted, &deletedAt); err != nil {
		return nil, err
	}
	ds.Tags = []string{}
	if err := json.Unmarshal([]byte(tags), &ds.Tags); err != nil || ds.Tags == nil {
		ds.Tags = []string{}
	}
	if t := db.ParseTime(created); t != nil {
		ds.CreatedAt = *t
	}
	ds.Deleted = deleted == 1
	ds.DeletedAt = db.ParseTime(deletedAt.String)
	return &ds, nil
}

func getDataset(ctx context.Context, d *sql.DB, id int64) (*Dataset, error) {
	return scanDataset(d.QueryRowContext(ctx, datasetSelect+` WHERE id = ?`, id))
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// datasetID returns the ID of userID's live dataset called name (both already resolved), or ErrDatasetNotFound.
func datasetID(ctx context.Context, q queryer, userID, name string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM gb_datasets WHERE user_id = ? AND name = ? AND deleted = 0`, userID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrDatasetNotFound
	}
	return id, err
}

// ensureDatasetID is datasetID, creating the dataset (owned by userID) first if it does not exist (remember
// creates on demand).
func ensureDatasetID(ctx context.Context, tx *sql.Tx, userID, name string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_datasets (user_id, name, created_at)
		SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM gb_datasets WHERE user_id = ? AND name = ? AND deleted = 0)`,
		userID, name, db.FormatTime(db.Now()), userID, name); err != nil {
		return 0, fmt.Errorf("create dataset: %w", err)
	}
	return datasetID(ctx, tx, userID, name)
}
