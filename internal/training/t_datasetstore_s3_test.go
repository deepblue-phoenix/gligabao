package training_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/training"
)

// fakeS3 is an in-memory S3 API with what the dataset store uses: a delimiter in the listing, a range in a get, and the size in a head.
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
	if in.Range != nil { // "bytes=0-<last>"
		last, _ := strconv.Atoi(strings.TrimPrefix(*in.Range, "bytes=0-"))
		if last+1 < len(b) {
			b = b[:last+1]
		}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeS3) HeadObject(_ context.Context,
	in *s3.HeadObjectInput,
	_ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(b))),
		LastModified:  aws.Time(time.Unix(1700000000, 0)),
	}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context,
	in *s3.DeleteObjectInput,
	_ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return nil, errors.New("the dataset store never deletes")
}

func (f *fakeS3) ListObjectsV2(_ context.Context,
	in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &s3.ListObjectsV2Output{}
	seen := map[string]bool{}
	for k := range f.objs {
		rest, ok := strings.CutPrefix(k, *in.Prefix)
		if !ok {
			continue
		}
		if i := strings.Index(rest, "/"); in.Delimiter != nil && i >= 0 {
			if p := *in.Prefix + rest[:i+1]; !seen[p] {
				seen[p] = true
				out.CommonPrefixes = append(out.CommonPrefixes, s3types.CommonPrefix{Prefix: aws.String(p)})
			}
			continue
		}
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	return out, nil
}

func newS3Store(t *testing.T,
	objs map[string][]byte) *training.S3Store {
	t.Helper()
	store, err := training.NewS3StoreWithClient(&fakeS3{objs: objs},
		userfiles.S3Config{Bucket: "meta"})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestS3StoreRequiresBucket(t *testing.T) {
	if _, err := training.NewS3StoreWithClient(&fakeS3{objs: map[string][]byte{}}, userfiles.S3Config{}); err == nil {
		t.Fatal("no bucket: want an error")
	}
	if _, err := training.NewS3Store(t.Context(), userfiles.S3Config{}); err == nil {
		t.Fatal("no bucket: want an error")
	}
}

func TestS3StoreDiscover(t *testing.T) {
	store := newS3Store(t, map[string][]byte{
		"training_datasets/mine/dataset.toml":     []byte("name = \"alpha\"\nfile = \"data/rows.jsonl\"\n"),
		"training_datasets/mine/data/rows.jsonl":  []byte(goodDataset),
		"training_datasets/nomanifest/data.jsonl": []byte(goodDataset),
		"training_datasets/nofile/dataset.toml":   []byte("file = \"x.jsonl\"\n"),
		"training_datasets/badtoml/dataset.toml":  []byte("file = "),
		"training_datasets/dup/dataset.toml":      []byte("name = \"alpha\"\nfile = \"x.jsonl\"\n"),
		"training_datasets/dup/x.jsonl":           []byte(goodDataset),
		"other_prefix/zzz/dataset.toml":           []byte("file = \"x.jsonl\"\n"),
	})
	if got := store.Location(); got != "s3://meta/training_datasets" {
		t.Errorf("location %q", got)
	}
	found, problems := store.Discover(t.Context())
	if len(found) != 1 || found[0].Name != "alpha" || found[0].Dir != "training_datasets/dup" && found[0].Dir != "training_datasets/mine" {
		t.Fatalf("found %+v", found)
	}
	// "dup" sorts before "mine", so it keeps the name and "mine" is the problem
	problemOf := map[string]string{}
	for _, p := range problems {
		problemOf[p.Dir] = p.Error
	}
	for dir, want := range map[string]string{
		"mine":    "already taken",
		"nofile":  "x.jsonl is missing",
		"badtoml": "invalid manifest",
	} {
		if !strings.Contains(problemOf[dir], want) {
			t.Errorf("problem of %s: %q, want %q",
				dir,
				problemOf[dir],
				want)
		}
	}
	if len(problems) != 3 {
		t.Errorf("problems %+v", problems)
	}
}

func TestS3StoreReadStatsAndHead(t *testing.T) {
	ctx := t.Context()
	store := newS3Store(t, map[string][]byte{
		"training_datasets/mine/dataset.toml": []byte("file = \"mine.jsonl\"\n"),
		"training_datasets/mine/mine.jsonl":   []byte(goodDataset),
	})
	found, problems := store.Discover(ctx)
	if len(found) != 1 || len(problems) != 0 || found[0].Name != "mine" {
		t.Fatalf("%+v %+v",
			found,
			problems)
	}
	ds := found[0]
	st, err := store.Stats(ctx, ds)
	if err != nil || st.Records != 2 || st.Size != int64(len(goodDataset)) || len(st.SHA256) != 64 {
		t.Fatalf("%+v %v",
			st,
			err)
	}
	if again, err := store.Stats(ctx, ds); err != nil || again.SHA256 != st.SHA256 {
		t.Fatalf("cached: %+v %v",
			again,
			err)
	}
	got, err := store.Read(ctx, ds)
	if err != nil || string(got) != goodDataset {
		t.Fatalf("read: %q %v",
			got,
			err)
	}
	head, err := store.ReadHead(ctx,
		ds,
		10)
	if err != nil || string(head) != goodDataset[:11] {
		t.Fatalf("head: %q %v",
			head,
			err)
	}
}

func TestS3StoreSaveAndList(t *testing.T) { dbtest.Run(t, testS3StoreSaveAndList) }

func testS3StoreSaveAndList(t *testing.T,
	d *sql.DB) {
	ctx := t.Context()
	objs := map[string][]byte{
		"training_datasets/taken/dataset.toml": []byte("name = \"taken\"\nfile = \"other.jsonl\"\n"),
		"training_datasets/taken/other.jsonl":  []byte(goodDataset),
		"training_datasets/dir/dataset.toml":   []byte("name = \"named\"\nfile = \"x.jsonl\"\n"),
		"training_datasets/dir/x.jsonl":        []byte(goodDataset),
	}
	store := newS3Store(t, objs)

	info, err := training.SaveDataset(ctx,
		d,
		store,
		"mine.jsonl",
		[]byte(goodDataset))
	if err != nil || info.Name != "mine" || info.Filename != "mine.jsonl" || info.NumRecords != 2 || info.Dir != "mine" {
		t.Fatalf("save: %+v %v",
			info,
			err)
	}
	if string(objs["training_datasets/mine/mine.jsonl"]) != goodDataset || !strings.Contains(string(objs["training_datasets/mine/dataset.toml"]), `file = "mine.jsonl"`) {
		t.Fatalf("objects: %v", objs)
	}
	// the same name again replaces the file, the manifest stays
	if _, err := training.SaveDataset(ctx,
		d,
		store,
		"mine.jsonl",
		[]byte("{\"instruction\":\"z\",\"output\":\"y\"}\n")); err != nil {
		t.Fatal(err)
	}

	for filename, want := range map[string]string{
		"taken.jsonl": "its own manifest",
		"named.jsonl": "exists already in dir",
	} {
		_, err := training.SaveDataset(ctx,
			d,
			store,
			filename,
			[]byte(goodDataset))
		var te *training.Error
		if !errors.As(err, &te) || te.Kind != training.KindConflict || !strings.Contains(te.Msg, want) {
			t.Errorf("%s: %v, want a conflict about %q",
				filename,
				err,
				want)
		}
	}

	list, problems, err := training.ListDatasets(ctx,
		d,
		store)
	if err != nil || len(problems) != 0 || len(list) != 3 {
		t.Fatalf("list: %+v %+v %v",
			list,
			problems,
			err)
	}
	for i, name := range []string{"mine", "named", "taken"} {
		if list[i].Name != name {
			t.Errorf("list[%d] = %s, want %s",
				i,
				list[i].Name,
				name)
		}
	}
	if list[0].NumRecords != 1 {
		t.Errorf("mine has %d records after the replacement", list[0].NumRecords)
	}

	rec, content, err := training.LoadTrainingDataset(ctx,
		d,
		store,
		"named")
	if err != nil || rec.Filename != "named.jsonl" || string(content) != goodDataset {
		t.Fatalf("load: %+v %q %v",
			rec,
			content,
			err)
	}
	if _, _, err := training.LoadTrainingDataset(ctx,
		d,
		store,
		"nope"); !errors.Is(err, training.ErrUnknownDataset) {
		t.Fatalf("unknown: %v", err)
	}
	p, err := training.PreviewDataset(ctx,
		store,
		"named",
		1)
	if err != nil || len(p.Lines) != 1 || !p.Truncated || p.NumRecords != 2 {
		t.Fatalf("preview: %+v %v",
			p,
			err)
	}
}

func TestDirsOpenDatasets(t *testing.T) {
	dirs := training.Dirs{Datasets: []string{t.TempDir()}}
	opened, err := dirs.OpenDatasets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opened.Store().(training.DirStore); !ok {
		t.Fatalf("no bucket: want the directories, got %T", opened.Store())
	}
	var _ training.DatasetStore = opened.Store()
}
