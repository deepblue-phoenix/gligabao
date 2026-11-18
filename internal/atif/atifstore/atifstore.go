// Package atifstore decides where ATIF trajectories are stored: a directory of the local file system, or an Amazon S3 bucket
// (the atif_store setting, see package config).
//
//   - FS keeps the layout of package atif: <dir>/<trajectory_id>.json, no directory per user.
//   - S3 keeps one directory per user: "<prefix>/<user>/<timestamp>_<trajectory_id>.json", where timestamp is the creation time
//     of the trajectory in UTC ("20060102T150405Z"), so a user's traces list in creation order. The user directory and the
//     isolation between users are those of package imagestore, which does the S3 work (the owner is encoded as one path segment, so
//     no user can reach the directory of another).
//
// Default is the store of the process (set once at startup by the server); nil means the local directory of the caller.
package atifstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/imagestore"
)

// DefaultS3Prefix is the "subdirectory" of the bucket the traces go to when no prefix is configured.
const DefaultS3Prefix = "traces/atif"

// timeLayout is the creation timestamp of the object name: sortable, UTC, and safe in a file name.
const timeLayout = "20060102T150405Z"

// Store is where trajectories are kept. It is safe for concurrent use.
type Store interface {
	// Put stores t in the directory of user and returns where ("s3://bucket/key", or the file path of an FS store).
	Put(ctx context.Context,
		user string,
		t *atif.Trajectory) (location string, err error)
	// Read returns the JSON of a trajectory that Put stored, given the location Put returned. A location of another store
	// is an error.
	Read(ctx context.Context,
		location string) ([]byte, error)
}

// FS stores the trajectories in a directory.
type FS struct {
	Dir string
}

// S3 stores the trajectories in an Amazon S3 bucket, one directory per user.
type S3 struct {
	store imagestore.Store
	now   func() time.Time
}

var (
	_ Store = FS{}
	_ Store = (*S3)(nil)

	// Default is the store the server uses (nil: the local directory of each caller). Set it once at startup.
	Default Store
)

// Put implements Store; the user is not used (there is no directory per user in the local file system).
func (f FS) Put(_ context.Context,
	_ string,
	t *atif.Trajectory) (string, error) {
	return atif.WriteFile(f.Dir, t)
}

// Read implements Store.
func (f FS) Read(_ context.Context,
	location string) ([]byte, error) {
	return os.ReadFile(location)
}

// NewS3 creates the store of the bucket of cfg (credentials from the standard AWS chain). An empty Prefix is DefaultS3Prefix.
func NewS3(ctx context.Context,
	cfg imagestore.S3Config) (*S3, error) {
	s, err := imagestore.NewS3(ctx, withPrefix(cfg))
	if err != nil {
		return nil, fmt.Errorf("atif store: %w", err)
	}
	return NewS3WithStore(s), nil
}

// NewS3WithStore is NewS3 over a given store (a fake in tests).
func NewS3WithStore(s imagestore.Store) *S3 {
	return &S3{
		store: s,
		now:   time.Now,
	}
}

func withPrefix(cfg imagestore.S3Config) imagestore.S3Config {
	if strings.Trim(strings.TrimSpace(cfg.Prefix), "/") == "" {
		cfg.Prefix = DefaultS3Prefix
	}
	return cfg
}

// ObjectName is the file name of a trajectory: "<creation timestamp>_<trajectory_id>.json". The creation time is the time of its
// first step, or fallback if it has none (or not a valid one).
func ObjectName(t *atif.Trajectory,
	fallback time.Time) string {
	created := fallback
	if len(t.Steps) > 0 {
		if ts, err := time.Parse(time.RFC3339, t.Steps[0].Timestamp); err == nil {
			created = ts
		}
	}
	return created.UTC().Format(timeLayout) + "_" + t.TrajectoryID + ".json"
}

// Put implements Store: the object is "<prefix>/<user>/<timestamp>_<trajectory_id>.json".
func (s *S3) Put(ctx context.Context,
	user string,
	t *atif.Trajectory) (string, error) {
	data, err := atif.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("atif store: marshal: %w", err)
	}
	rel := ObjectName(t, s.now())
	if _, err := s.store.Put(ctx,
		user,
		rel,
		data); err != nil {
		return "", err
	}
	return s.location(user, rel), nil
}

// location is the "s3://bucket/prefix/<user>/rel" URL of an object of user, which Read understands.
func (s *S3) location(user,
	rel string) string {
	return strings.TrimSuffix(s.store.Location(), "/") + "/" + url.QueryEscape(user) + "/" + rel
}

// Read implements Store.
func (s *S3) Read(ctx context.Context,
	location string) ([]byte, error) {
	root := strings.TrimSuffix(s.store.Location(), "/") + "/"
	rest, ok := strings.CutPrefix(location, root)
	if !ok {
		return nil, fmt.Errorf("atif store: %q is not in %s", location, root)
	}
	seg, rel, ok := strings.Cut(rest, "/")
	owner, err := url.QueryUnescape(seg)
	if !ok || err != nil {
		return nil, errors.New("atif store: bad location " + location)
	}
	return s.store.Read(ctx,
		owner,
		rel)
}

// IsS3Location says whether location looks like one returned by an S3 store.
func IsS3Location(location string) bool { return strings.HasPrefix(location, "s3://") }
