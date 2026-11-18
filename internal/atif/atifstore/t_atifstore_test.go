package atifstore

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
)

// fakeS3 is an in-memory S3API.
type fakeS3 struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fakeS3) PutObject(_ context.Context,
	in *s3.PutObjectInput,
	_ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, _ := io.ReadAll(in.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[*in.Key] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context,
	in *s3.GetObjectInput,
	_ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

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
	m := &fakeS3{objs: map[string][]byte{}}
	s, err := NewS3WithClient(m,
		S3Config{Bucket: "bkt"})
	if err != nil {
		t.Fatal(err)
	}
	loc, err := s.Put(context.Background(), "alice", traj("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if loc != "s3://bkt/traces/atif/alice/20260304T050607Z_t1.json" {
		t.Fatalf("location = %q", loc)
	}
	if _, ok := m.objs["traces/atif/alice/20260304T050607Z_t1.json"]; !ok {
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
