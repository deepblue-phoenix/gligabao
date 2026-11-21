package evals_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// memStore is an atifstore.Store of fixed content.
type memStore struct{ data map[string][]byte }

func (m memStore) Put(context.Context,
	string,
	*atif.Trajectory) (string, error) {
	return "", nil
}

func (m memStore) Read(_ context.Context,
	location string) ([]byte, error) {
	if d, ok := m.data[location]; ok {
		return d, nil
	}
	return nil, os.ErrNotExist
}

// TestReadTrajectoryFile checks which trajectory files an owner may read: only those in the trajectory directory or in the S3 directory
// of that user; the admin (no owner) reads any.
func TestReadTrajectoryFile(t *testing.T) {
	ctx := context.Background()
	dir, other := t.TempDir(), t.TempDir()
	inDir := filepath.Join(dir, "t1.json")
	outside := filepath.Join(other, "t2.json")
	for p, v := range map[string]string{inDir: `"in"`, outside: `"out"`} {
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s3 := "s3://bucket/traces/alice/20260101T000000Z_t3.json"
	s3other := "s3://bucket/traces/bob/20260101T000000Z_t4.json"
	store := memStore{data: map[string][]byte{s3: []byte(`"alice"`), s3other: []byte(`"bob"`)}}
	read := func(ref core.KnowledgeTrajectory,
		owner string) string {
		data, err := evals.ReadTrajectoryFile(ctx,
			&ref,
			dir,
			owner,
			store)
		if err != nil {
			return ""
		}
		return string(data)
	}
	for _, c := range []struct {
		name  string
		ref   core.KnowledgeTrajectory
		owner string
		want  string
	}{
		{"file in the directory", core.KnowledgeTrajectory{TrajectoryID: "t1", Path: inDir}, "alice", `"in"`},
		{"file outside, as a user", core.KnowledgeTrajectory{TrajectoryID: "t2", Path: outside}, "alice", ""},
		{"file outside, as the admin", core.KnowledgeTrajectory{TrajectoryID: "t2", Path: outside}, "", `"out"`},
		{"s3 of the user", core.KnowledgeTrajectory{TrajectoryID: "t3", Path: s3}, "alice", `"alice"`},
		{"s3 of another user", core.KnowledgeTrajectory{TrajectoryID: "t4", Path: s3other}, "alice", ""},
		{"s3 of another user, as the admin", core.KnowledgeTrajectory{TrajectoryID: "t4", Path: s3other}, "", `"bob"`},
	} {
		if got := read(c.ref, c.owner); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	var _ atifstore.Store = memStore{}
}
