package atifstore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/imagestore"
)

// memStore is an in-memory imagestore.Store keyed like the S3 one: owner + "/" + rel.
type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *memStore) Put(_ context.Context,
	owner,
	rel string,
	data []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[owner+"/"+rel] = data
	return true, nil
}

func (m *memStore) Read(_ context.Context,
	owner,
	rel string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.objs[owner+"/"+rel]
	if !ok {
		return nil, imagestore.ErrNotFound
	}
	return d, nil
}

func (m *memStore) Remove(context.Context, string, string) error { return nil }

func (m *memStore) List(context.Context) ([]imagestore.Object, error) { return nil, nil }

func (m *memStore) Location() string { return "s3://bkt/traces/atif" }

func traj(id string) *atif.Trajectory {
	return &atif.Trajectory{
		TrajectoryID: id,
		Steps: []atif.Step{{
			StepID:    1,
			Timestamp: "2026-03-04T05:06:07Z",
			Source:    atif.SourceUser,
			Message:   "hi",
		}},
	}
}

func TestObjectNameHasCreationTimestamp(t *testing.T) {
	got := ObjectName(traj("abc"), time.Unix(0, 0))
	if got != "20260304T050607Z_abc.json" {
		t.Fatalf("name = %q", got)
	}
	empty := ObjectName(&atif.Trajectory{TrajectoryID: "x"}, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	if empty != "20250102T030405Z_x.json" {
		t.Fatalf("fallback name = %q", empty)
	}
}

func TestS3PutIsPerUserAndReadable(t *testing.T) {
	m := &memStore{objs: map[string][]byte{}}
	s := NewS3WithStore(m)
	loc, err := s.Put(context.Background(), "alice", traj("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if loc != "s3://bkt/traces/atif/alice/20260304T050607Z_t1.json" {
		t.Fatalf("location = %q", loc)
	}
	if _, ok := m.objs["alice/20260304T050607Z_t1.json"]; !ok {
		t.Fatalf("not stored under the user: %v", m.objs)
	}
	if _, err := s.Put(context.Background(), "bob", traj("t2")); err != nil {
		t.Fatal(err)
	}
	data, err := s.Read(context.Background(), loc)
	if err != nil || !strings.Contains(string(data), `"trajectory_id": "t1"`) {
		t.Fatalf("read: %v %s", err, data)
	}
	if _, err := s.Read(context.Background(), "s3://other/traces/atif/alice/x.json"); err == nil {
		t.Fatal("a location of another bucket must be refused")
	}
}

func TestFSKeepsTheFlatLayout(t *testing.T) {
	dir := t.TempDir()
	f := FS{Dir: dir}
	loc, err := f.Put(context.Background(), "alice", traj("t1"))
	if err != nil || !strings.HasSuffix(loc, "/t1.json") {
		t.Fatalf("put: %q %v", loc, err)
	}
	if _, err := f.Read(context.Background(), loc); err != nil {
		t.Fatal(err)
	}
}
