package memory_core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// DefaultDataset is used when a request names no dataset.
const DefaultDataset = relational.DefaultDataset

var (
	ErrInvalidDataset  = errors.New("invalid dataset")
	ErrDatasetNotFound = errors.New("dataset not found")
	ErrInvalidUser     = errors.New("invalid user_id")
)

var datasetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// MaxUserIDLen is the maximum length of a user_id in bytes.
const MaxUserIDLen = 128

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

// Key normalizes an entity name for comparison: lowercase, with whitespace collapsed.
func Key(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }

// DatasetID returns the ID of userID's live dataset called name (both already resolved), or ErrDatasetNotFound.
func DatasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	id, err := relational.DatasetID(ctx, d, userID, name)
	if errors.Is(err, relational.ErrNotFound) {
		return 0, ErrDatasetNotFound
	}
	return id, err
}
