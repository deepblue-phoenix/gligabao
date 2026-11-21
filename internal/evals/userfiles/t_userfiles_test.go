package userfiles

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory S3API (list pages of 2 objects, to test the pagination).
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

func (f *fakeS3) HeadObject(_ context.Context,
	in *s3.HeadObjectInput,
	_ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objs[*in.Key]; !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context,
	in *s3.DeleteObjectInput,
	_ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context,
	in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := []string{}
	for k := range f.objs {
		if strings.HasPrefix(k, *in.Prefix) && (in.ContinuationToken == nil || k > *in.ContinuationToken) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := &s3.ListObjectsV2Output{}
	if len(keys) > 2 {
		keys = keys[:2]
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(keys[1])
	}
	for _, k := range keys {
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	return out, nil
}

// stores returns the two implementations on empty backends.
func stores(t *testing.T) map[string]Store {
	t.Helper()
	s3Store, err := NewS3WithClient(&fakeS3{objs: map[string][]byte{}}, S3Config{Bucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Store{
		"s3": s3Store,
		"fs": FS{Dir: t.TempDir()},
	}
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for _, rel := range []string{"e1.json", "corpus/a.txt", "corpus/sub/b.txt", "c.txt"} {
				if err := st.Put(ctx, "alice", rel, []byte("data "+rel)); err != nil {
					t.Fatalf("put %s: %v", rel, err)
				}
			}
			if err := st.Put(ctx, "alice", "e1.json", []byte("v2")); err != nil {
				t.Fatal(err)
			}
			got, err := st.Get(ctx, "alice", "e1.json")
			if err != nil || string(got) != "v2" {
				t.Fatalf("get = %q, %v", got, err)
			}
			all, err := st.List(ctx, "alice", "")
			if err != nil || !slices.Equal(all, []string{"c.txt", "corpus/a.txt", "corpus/sub/b.txt", "e1.json"}) {
				t.Fatalf("list = %v, %v", all, err)
			}
			sub, err := st.List(ctx, "alice", "corpus/")
			if err != nil || !slices.Equal(sub, []string{"corpus/a.txt", "corpus/sub/b.txt"}) {
				t.Fatalf("list prefix = %v, %v", sub, err)
			}
			if err := st.Delete(ctx, "alice", "corpus/a.txt"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Get(ctx, "alice", "corpus/a.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("get deleted: %v", err)
			}
			if err := st.Delete(ctx, "alice", "corpus/a.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("delete twice: %v", err)
			}
			if _, err := st.Get(ctx, "alice", "nope.json"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("get missing: %v", err)
			}
			empty, err := st.List(ctx, "nobody", "")
			if err != nil || len(empty) != 0 {
				t.Fatalf("list of an unknown user = %v, %v", empty, err)
			}
		})
	}
}

func TestStoreUsersAreIsolated(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			// "a/b" must not reach the directory of "a", and ".." must not reach anything
			for _, u := range []string{"a", "a/b", "..", ".", "a/../b"} {
				if err := st.Put(ctx, u, "f.txt", []byte("of "+u)); err != nil {
					t.Fatalf("put %q: %v", u, err)
				}
			}
			for _, u := range []string{"a", "a/b", "..", ".", "a/../b"} {
				got, err := st.Get(ctx, u, "f.txt")
				if err != nil || string(got) != "of "+u {
					t.Fatalf("get %q = %q, %v", u, got, err)
				}
				l, _ := st.List(ctx, u, "")
				if !slices.Equal(l, []string{"f.txt"}) {
					t.Fatalf("list %q = %v", u, l)
				}
			}
			if _, err := st.Get(ctx, "b", "f.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("other user: %v", err)
			}
			for _, u := range []string{"", "  "} {
				if err := st.Put(ctx, u, "f.txt", nil); err == nil {
					t.Fatalf("put with user %q succeeded", u)
				}
				if _, err := st.List(ctx, u, ""); err == nil {
					t.Fatalf("list with user %q succeeded", u)
				}
			}
		})
	}
}

func TestStoreRejectsBadPaths(t *testing.T) {
	ctx := context.Background()
	bad := []string{
		"", "/abs.txt", "../x.txt", "a/../x.txt", "a//b.txt", "a/./b.txt", "./a.txt", ".hidden", "a/.hidden", "a/", "a b.txt",
		"a\\b.txt", "a/..", "..", "a..b", strings.Repeat("a", MaxPathLen+1), "é.txt", "a\x00b",
	}
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for _, rel := range bad {
				if err := st.Put(ctx, "u", rel, []byte("x")); !errors.Is(err, ErrBadPath) {
					t.Errorf("put %q: %v", rel, err)
				}
				if _, err := st.Get(ctx, "u", rel); !errors.Is(err, ErrBadPath) {
					t.Errorf("get %q: %v", rel, err)
				}
				if err := st.Delete(ctx, "u", rel); !errors.Is(err, ErrBadPath) {
					t.Errorf("delete %q: %v", rel, err)
				}
			}
			for _, prefix := range []string{"../", "/a", "a//", "a/../"} {
				if _, err := st.List(ctx, "u", prefix); !errors.Is(err, ErrBadPath) {
					t.Errorf("list %q: %v", prefix, err)
				}
			}
			if err := st.Put(ctx, "u", strings.Repeat("a", MaxPathLen), []byte("x")); err != nil {
				t.Errorf("a path of the longest length: %v", err)
			}
		})
	}
}

func TestS3KeysAreUnderPrefixAndUser(t *testing.T) {
	m := &fakeS3{objs: map[string][]byte{}}
	st, err := NewS3WithClient(m, S3Config{Bucket: "b", Prefix: "/custom/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(context.Background(), "a/b", "corpus/x.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.objs["custom/a%2Fb/corpus/x.txt"]; !ok || len(m.objs) != 1 {
		t.Fatalf("keys = %v", m.objs)
	}
	def, _ := NewS3WithClient(m, S3Config{Bucket: "b"})
	if def.prefix != DefaultS3Prefix || def.Location() != "s3://b/evals" {
		t.Fatalf("default prefix: %q %q", def.prefix, def.Location())
	}
	if _, err := NewS3WithClient(m, S3Config{}); err == nil {
		t.Fatal("no bucket accepted")
	}
	if _, err := NewS3WithClient(nil, S3Config{Bucket: "b"}); err == nil {
		t.Fatal("nil client accepted")
	}
}

func TestFSSymlinkEscapes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := FS{Dir: filepath.Join(root, "evals")}
	if err := st.Put(ctx, "u", "ok/a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(root, "evals", "u")

	// a directory link and a file link inside the user directory, both pointing out
	if err := os.Symlink(outside, filepath.Join(userDir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(userDir, "filelink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing.txt"), filepath.Join(userDir, "dangling.txt")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"dirlink/secret.txt", "filelink.txt", "dirlink/new.txt", "dangling.txt"} {
		if _, err := st.Get(ctx, "u", rel); !errors.Is(err, ErrBadPath) {
			t.Errorf("get %s: %v", rel, err)
		}
		if err := st.Put(ctx, "u", rel, []byte("x")); !errors.Is(err, ErrBadPath) {
			t.Errorf("put %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("a file was written outside the user directory")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "secret" {
		t.Error("the outside file changed")
	}
	if l, err := st.List(ctx, "u", ""); err != nil || !slices.Equal(l, []string{"ok/a.txt"}) {
		t.Errorf("links are listed: %v, %v", l, err)
	}

	// the user directory itself is a link out
	if err := os.Symlink(outside, filepath.Join(root, "evals", "v")); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "v", "a.txt", []byte("x")); !errors.Is(err, ErrBadPath) {
		t.Errorf("put through a linked user directory: %v", err)
	}
	if _, err := st.Get(ctx, "v", "secret.txt"); !errors.Is(err, ErrBadPath) {
		t.Errorf("get through a linked user directory: %v", err)
	}

	if err := (FS{}).Put(ctx, "u", "a.txt", nil); err == nil {
		t.Error("a store without directory works")
	}
}
