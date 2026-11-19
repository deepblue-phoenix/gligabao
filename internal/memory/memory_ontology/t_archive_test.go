package memory_ontology

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeS3 struct {
	mu   sync.Mutex
	objs map[string]string
}

func (f *fakeS3) PutObject(_ context.Context,
	in *s3.PutObjectInput,
	_ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	buf := new(strings.Builder)
	b := make([]byte, 64)
	for {
		n, err := in.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[*in.Key] = buf.String()
	return &s3.PutObjectOutput{}, nil
}

func TestArchiveKey(t *testing.T) {
	s, err := NewS3WithClient(&fakeS3{objs: map[string]string{}},
		S3Config{Bucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := s.Key("a/b", "market", actionUpdate, "json", at)
	if want := "ontologies/a%2Fb/market/20260102T030405Z_update.json"; err != nil || got != want {
		t.Fatalf("got %q (%v), want %q", got, err, want)
	}
	if _, err := s.Key("", "market", actionCreate, "yaml", at); err == nil {
		t.Fatal("an empty user must be refused")
	}
	if _, err := s.Key("u", "../x", actionCreate, "yaml", at); err == nil {
		t.Fatal("a bad name must be refused")
	}
	if got, _ := s.Key("..", "m", actionCreate, "yaml", at); !strings.HasPrefix(got, "ontologies/%2E%2E/") {
		t.Fatalf("got %q", got)
	}
}

func TestNewS3NeedsBucket(t *testing.T) {
	if _, err := NewS3WithClient(&fakeS3{}, S3Config{}); err == nil {
		t.Fatal("no bucket must fail")
	}
}

func TestArchiveStoresDocument(t *testing.T) {
	f := &fakeS3{objs: map[string]string{}}
	s, err := NewS3WithClient(f,
		S3Config{
			Bucket: "b",
			Prefix: "/x/",
		})
	if err != nil {
		t.Fatal(err)
	}
	Archive = s
	t.Cleanup(func() { Archive = nil })
	archive(context.Background(), "u1", "market", actionCreate, "yaml", []byte("<doc/>"))
	if len(f.objs) != 1 {
		t.Fatalf("objects %v", f.objs)
	}
	for k, v := range f.objs {
		if !strings.HasPrefix(k, "x/u1/market/") || !strings.HasSuffix(k, "_create.yaml") || v != "<doc/>" {
			t.Fatalf("%q = %q", k, v)
		}
	}
	Archive = nil
	archive(context.Background(), "u1", "market", actionCreate, "yaml", []byte("<doc/>")) // no store: a no-op
}
