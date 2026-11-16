package imagestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory S3API.
type fakeS3 struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fakeS3) PutObject(ctx context.Context,
	in *s3.PutObjectInput,
	opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, _ := io.ReadAll(in.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[*in.Key] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(ctx context.Context,
	in *s3.GetObjectInput,
	opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeS3) HeadObject(ctx context.Context,
	in *s3.HeadObjectInput,
	opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objs[*in.Key]; !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3) DeleteObject(ctx context.Context,
	in *s3.DeleteObjectInput,
	opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(ctx context.Context,
	in *s3.ListObjectsV2Input,
	opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objs {
		if strings.HasPrefix(k, *in.Prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := &s3.ListObjectsV2Output{}
	for _, k := range keys {
		k := k
		size := int64(len(f.objs[k]))
		out.Contents = append(out.Contents, s3types.Object{Key: &k, Size: &size})
	}
	return out, nil
}

func stores(t *testing.T) map[string]Store {
	s3s, err := NewS3WithClient(&fakeS3{objs: map[string][]byte{}},
		S3Config{
			Bucket: "b",
			Prefix: "/pre/",
		})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Store{
		"fs": NewFS(filepath.Join(t.TempDir(), "images")), // does not exist yet
		"s3": s3s,
	}
}

func TestStores(t *testing.T) {
	ctx := context.Background()
	data := []byte("fake png")
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := s.List(ctx); name == "fs" && !errors.Is(err, ErrNotFound) {
				t.Errorf("List of a missing root: %v", err)
			}
			rel, created, err := Save(ctx, s, "u1", 7, "image/png", h, data)
			if err != nil || !created || rel != "7/"+h+".png" {
				t.Fatalf("Save: %q, %v, %v", rel, created, err)
			}
			if got, err := s.Read(ctx, "u1", rel); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("Read: %q, %v", got, err)
			}
			if rel2, created, err := Save(ctx, s, "u1", 7, "image/png", h, data); err != nil || created || rel2 != rel {
				t.Errorf("second Save: %q, %v, %v", rel2, created, err)
			}
			if rel3, created, _ := Save(ctx, s, "u1", 8, "image/png", h, data); !created || rel3 == rel {
				t.Errorf("another dataset shares the file: %q", rel3)
			}
			objs, err := s.List(ctx)
			if err != nil || len(objs) != 2 || objs[0].Path != rel || objs[0].Size != int64(len(data)) {
				t.Errorf("List: %v, %v", objs, err)
			}
			if err := s.Remove(ctx, "u1", rel); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Read(ctx, "u1", rel); !errors.Is(err, ErrNotFound) {
				t.Errorf("Read after Remove: %v", err)
			}
			if err := s.Remove(ctx, "u1", rel); err != nil {
				t.Errorf("removing twice: %v", err)
			}
		})
	}
}

func TestFSNoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewFS(dir).Put(context.Background(), "u1", "7/a.png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "7", ".tmp-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "7", "a.png")); err != nil {
		t.Error(err)
	}
}

func TestS3Location(t *testing.T) {
	if got := stores(t)["s3"].Location(); got != "s3://b/pre" {
		t.Errorf("Location: %q", got)
	}
}

func TestRejects(t *testing.T) {
	ctx := context.Background()
	h := hex.EncodeToString(make([]byte, 32))
	if _, _, err := Save(ctx, NewFS(t.TempDir()), "u1", 1, "text/plain", h, nil); err == nil {
		t.Error("bad media type accepted")
	}
	if _, _, err := Save(ctx, NewFS(t.TempDir()), "u1", 1, "image/png", "../../x", nil); err == nil {
		t.Error("bad hash accepted")
	}
	for name, s := range stores(t) {
		for _, p := range []string{"", "../x", "/etc/passwd", "1/../../x", "a//b"} {
			if _, err := s.Read(ctx, "u1", p); err == nil || errors.Is(err, ErrNotFound) {
				t.Errorf("%s Read(%q): %v", name, p, err)
			}
		}
	}
}

func TestS3PerUserDirectories(t *testing.T) {
	ctx := context.Background()
	fake := &fakeS3{objs: map[string][]byte{}}
	s, err := NewS3WithClient(fake,
		S3Config{
			Bucket: "b",
			Prefix: "pre",
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "alice", "1/a.png", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "bob", "1/a.png", []byte("B")); err != nil {
		t.Fatal(err)
	}
	// Each user has a directory of their own.
	if string(fake.objs["pre/alice/1/a.png"]) != "A" || string(fake.objs["pre/bob/1/a.png"]) != "B" {
		t.Fatalf("keys: %v", fake.objs)
	}
	// The same path of another user is another object.
	if got, _ := s.Read(ctx, "bob", "1/a.png"); string(got) != "B" {
		t.Errorf("bob reads %q", got)
	}
	// A user cannot reach the directory of another one, whatever they send.
	for _, owner := range []string{"", "  ", "..", ".", "alice/../bob", "bob/", "../bob"} {
		for _, rel := range []string{"1/a.png", "../bob/1/a.png", "../../bob/1/a.png"} {
			got, err := s.Read(ctx, owner, rel)
			if string(got) == "B" || (string(got) == "A" && owner != "alice") {
				t.Errorf("owner %q rel %q reached another directory: %q", owner, rel, got)
			}
			if err == nil && owner != "alice" {
				t.Errorf("owner %q rel %q: no error", owner, rel)
			}
		}
	}
	if err := s.Remove(ctx, "mallory", "../bob/1/a.png"); err == nil {
		t.Error("remove outside the directory accepted")
	}
	if _, ok := fake.objs["pre/bob/1/a.png"]; !ok {
		t.Error("bob's object was deleted")
	}
	// Unusual user ids stay inside one segment, and List gives them back.
	if _, err := s.Put(ctx, "a/b..", "1/a.png", []byte("C")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "..", "1/a.png", []byte("D")); err != nil {
		t.Fatal(err)
	}
	objs, err := s.List(ctx)
	owners := map[string]bool{}
	for _, o := range objs {
		owners[o.Owner] = true
		if o.Path != "1/a.png" {
			t.Errorf("path %q", o.Path)
		}
	}
	if err != nil || len(objs) != 4 || !owners["alice"] || !owners["bob"] || !owners["a/b.."] || !owners[".."] {
		t.Errorf("List: %v, %v", objs, err)
	}
}
