package investigate

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeS3 struct{ objs map[string]string }

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objs[*in.Bucket+"/"+*in.Key] = string(b)
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	v, ok := f.objs[*in.Bucket+"/"+*in.Key]
	if !ok {
		return nil, io.EOF
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(v)),
	}, nil
}

func TestNotesS3Mirror(t *testing.T) {
	fake := &fakeS3{
		objs: map[string]string{},
	}
	n := NewNotesS3WithClient(fake, NotesS3Config{
		Bucket: "b",
	})
	files := NotesFile{
		Dir: t.TempDir(),
	}
	p := files.Path("a/b", 7, "abc")
	m := n.Mirror(files, "a/b")
	if err := m.Write(p, "hello"); err != nil {
		t.Fatal(err)
	}
	key := "b/investigative_notes/a_b/" + filepath.Base(p)
	if fake.objs[key] != "hello" {
		t.Fatalf("objects: %v", fake.objs)
	}
	// local copy lost: served from the bucket
	other := n.Mirror(NotesFile{
		Dir: t.TempDir(),
	}, "a/b")
	if got, err := other.Read(p); err != nil || got != "hello" {
		t.Fatalf("read: %q %v", got, err)
	}
}
