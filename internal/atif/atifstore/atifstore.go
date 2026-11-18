// Package atifstore decides where ATIF trajectories are stored: a directory of the local file system, or an Amazon S3 bucket
// (set by atif_s3_bucket, see package config).
//
//   - FS keeps the layout of package atif: <dir>/<trajectory_id>.json, no directory per user.
//   - S3 keeps one directory per user: "<prefix>/<user>/<timestamp>_<trajectory_id>.json", where timestamp is the creation time
//     of the trajectory in UTC ("20060102T150405Z"), so a user's traces list in creation order. The S3 client is the one of this
//     package (s3.go), dedicated to the traces: it shares no code with the clients of the images and the ontologies. The user is
//     encoded as one path segment, so no user can reach the directory of another.
//
// Default is the store of the process (set once at startup by the server); nil means the local directory of the caller.
package atifstore

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
)

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

// IsS3Location says whether location looks like one returned by an S3 store.
func IsS3Location(location string) bool { return strings.HasPrefix(location, "s3://") }
