package memory

// Datasets are hard logical containers: every memory, entity and edge belongs to exactly
// one dataset, and the same entity in two datasets is two separate rows. Nothing is ever linked or queried
// across datasets. Deleting a dataset is soft (gb_datasets.deleted = 1): its rows stay, but it can no longer be
// read or written, and its name can be reused by a new, empty dataset.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
)

// DefaultDataset is used when a request names no dataset.
const DefaultDataset = relational.DefaultDataset

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
type Dataset = relational.Dataset

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
	ds, err := relational.CreateDataset(ctx, d, userID, name, strings.TrimSpace(description), normTags(tags))
	if errors.Is(err, relational.ErrExists) {
		return nil, ErrDatasetExists
	}
	return ds, err
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]Dataset, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	return relational.ListDatasets(ctx, d, userID, includeDeleted)
}

// VectorNamespace is the vector store namespace that holds the embeddings of a dataset. It is keyed by the dataset's
// ID (not its name), so datasets never share vectors, and a name reused after a soft delete starts with an empty
// namespace. In the chromem store a namespace is one collection.
func VectorNamespace(datasetID int64) string {
	return "dataset-" + strconv.FormatInt(datasetID, 10)
}

// DeleteDataset soft-deletes userID's live dataset called name (sets deleted = 1) and removes the dataset's vectors
// from vectors, which may be nil. The relational rows are kept; the vectors are not, because a deleted dataset can
// never be read again (its name gets a new dataset with a new ID) and they would only occupy memory/storage.
// It returns ErrDatasetNotFound if there is no such dataset.
//
// A failure to remove the vectors does not fail the call (the dataset is deleted either way and the namespace is
// unreachable); it is logged.
func DeleteDataset(ctx context.Context, d *sql.DB, vectors vector.Store, userID, name string) error {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return err
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return err
	}
	id, err := datasetID(ctx, d, userID, name)
	if err != nil {
		return err
	}
	if err := relational.DeleteDataset(ctx, d, userID, name); errors.Is(err, relational.ErrNotFound) {
		return ErrDatasetNotFound
	} else if err != nil {
		return err
	}
	if vectors != nil {
		if err := vectors.DeleteAll(ctx, VectorNamespace(id)); err != nil {
			log.Printf("delete dataset %d: removing vectors: %v", id, err)
		}
	}
	return nil
}

//---------------------------------------------------------------------------------------------

// datasetID returns the ID of userID's live dataset called name (both already resolved), or ErrDatasetNotFound.
func datasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	id, err := relational.DatasetID(ctx, d, userID, name)
	if errors.Is(err, relational.ErrNotFound) {
		return 0, ErrDatasetNotFound
	}
	return id, err
}
