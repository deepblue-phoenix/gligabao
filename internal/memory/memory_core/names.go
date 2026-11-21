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
	ErrInvalidSession  = errors.New("invalid session_id")
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
		return "", fmt.Errorf("%w: at most %d bytes",
			ErrInvalidUser,
			MaxUserIDLen)
	}
	for _, r := range userID {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidUser)
		}
	}
	return userID, nil
}

// MaxSessionIDLen is the maximum length of a session_id in bytes.
const MaxSessionIDLen = 128

// ResolveSessionID trims sessionID and returns ErrInvalidSession if it is too long or has control characters. An
// empty one is valid and means "no session".
func ResolveSessionID(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if len(sessionID) > MaxSessionIDLen {
		return "", fmt.Errorf("%w: at most %d bytes",
			ErrInvalidSession,
			MaxSessionIDLen)
	}
	for _, r := range sessionID {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidSession)
		}
	}
	return sessionID, nil
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

// DatasetID returns the ID of userID's live dataset called name (both already resolved), or ErrDatasetNotFound: a regular dataset, or the
// eval dataset of the run set by WithEvalRun.
func DatasetID(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (int64, error) {
	var (
		id  int64
		err error
	)
	if runID, ok := EvalRunOf(ctx); ok {
		id, err = relational.EvalDatasetID(ctx,
			d,
			userID,
			name,
			runID)
	} else {
		id, err = relational.DatasetID(ctx,
			d,
			userID,
			name)
	}
	if errors.Is(err, relational.ErrNotFound) {
		return 0, ErrDatasetNotFound
	}
	return id, err
}

type evalRunKey struct{}

// WithEvalRun returns a context that makes DatasetID and EnsureDataset resolve the datasets of the eval run evalRunID (kind 'eval', see
// gb_datasets) instead of the regular ones. Only the eval path sets it, so it is the only one that can create or read an eval dataset.
func WithEvalRun(ctx context.Context,
	evalRunID int64) context.Context {
	return context.WithValue(ctx, evalRunKey{}, evalRunID)
}

// EvalRunOf returns the eval run set by WithEvalRun; ok is false for a regular context.
func EvalRunOf(ctx context.Context) (evalRunID int64, ok bool) {
	evalRunID, ok = ctx.Value(evalRunKey{}).(int64)
	return evalRunID, ok
}

// EnsureDataset returns the ID of userID's live dataset called name (both already resolved), creating it first if needed: a regular
// dataset, or the eval dataset of the run set by WithEvalRun.
func EnsureDataset(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (int64, error) {
	if runID, ok := EvalRunOf(ctx); ok {
		return relational.EnsureEvalDataset(ctx,
			d,
			userID,
			name,
			runID)
	}
	return relational.EnsureDataset(ctx,
		d,
		userID,
		name)
}
