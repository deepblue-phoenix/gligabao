package vector

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
)

func TestS3VectorsIndexName(t *testing.T) {
	cases := []struct{ prefix, dataset, want string }{
		{"", "dataset-12-entities", "dataset-12-entities"},
		{"prod", "dataset-12-entities", "prod.dataset-12-entities"},
		{"Prod/A", "dataset-1-edges", "prod-a.dataset-1-edges"},
		{"", "", "default"},
		{"p", "", "p.default"},
	}
	for _, c := range cases {
		if got := S3VectorsIndexName(c.prefix, c.dataset); got != c.want {
			t.Errorf("(%q,%q) = %q, want %q",
				c.prefix,
				c.dataset,
				got,
				c.want)
		}
	}
	a, b := S3VectorsIndexName("p", "My Data"), S3VectorsIndexName("p", "My_Data")
	if a == b {
		t.Errorf("distinct datasets share %q", a)
	}
	long := S3VectorsIndexName("prod", strings.Repeat("x", 100))
	if len(long) > 63 || !strings.HasPrefix(long, "prod.x") {
		t.Errorf("long name %q (%d)",
			long,
			len(long))
	}
}

func TestBatches(t *testing.T) {
	got := batches([]int{1, 2, 3, 4, 5}, 2)
	if len(got) != 3 || !slices.Equal(got[2], []int{5}) || batches([]int{}, 2) != nil {
		t.Fatalf("got %v", got)
	}
}

// fakeS3 records what the store sends; indexes that were not created answer NotFound.
type fakeS3 struct {
	indexes map[string]int // name -> number of vectors put
	puts    []int
	deleted []string
	keys    []string
}

func (f *fakeS3) CreateIndex(_ context.Context,
	in *s3vectors.CreateIndexInput,
	_ ...func(*s3vectors.Options)) (*s3vectors.CreateIndexOutput, error) {
	f.indexes[aws.ToString(in.IndexName)] = 0
	return &s3vectors.CreateIndexOutput{}, nil
}
func (f *fakeS3) DeleteIndex(_ context.Context,
	in *s3vectors.DeleteIndexInput,
	_ ...func(*s3vectors.Options)) (*s3vectors.DeleteIndexOutput, error) {
	if _, ok := f.indexes[aws.ToString(in.IndexName)]; !ok {
		return nil, &types.NotFoundException{}
	}
	delete(f.indexes, aws.ToString(in.IndexName))
	return &s3vectors.DeleteIndexOutput{}, nil
}
func (f *fakeS3) PutVectors(_ context.Context,
	in *s3vectors.PutVectorsInput,
	_ ...func(*s3vectors.Options)) (*s3vectors.PutVectorsOutput, error) {
	name := aws.ToString(in.IndexName)
	if _, ok := f.indexes[name]; !ok {
		return nil, &types.NotFoundException{}
	}
	f.indexes[name] += len(in.Vectors)
	f.puts = append(f.puts, len(in.Vectors))
	return &s3vectors.PutVectorsOutput{}, nil
}
func (f *fakeS3) QueryVectors(_ context.Context,
	in *s3vectors.QueryVectorsInput,
	_ ...func(*s3vectors.Options)) (*s3vectors.QueryVectorsOutput, error) {
	if _, ok := f.indexes[aws.ToString(in.IndexName)]; !ok {
		return nil, &types.NotFoundException{}
	}
	return &s3vectors.QueryVectorsOutput{
		Vectors: []types.QueryOutputVector{{
			Key:      aws.String("a"),
			Distance: aws.Float32(0.25),
		}},
	}, nil
}
func (f *fakeS3) DeleteVectors(_ context.Context,
	in *s3vectors.DeleteVectorsInput,
	_ ...func(*s3vectors.Options)) (*s3vectors.DeleteVectorsOutput, error) {
	if _, ok := f.indexes[aws.ToString(in.IndexName)]; !ok {
		return nil, &types.NotFoundException{}
	}
	f.deleted = append(f.deleted, in.Keys...)
	return &s3vectors.DeleteVectorsOutput{}, nil
}
func (f *fakeS3) ListVectors(_ context.Context,
	in *s3vectors.ListVectorsInput,
	_ ...func(*s3vectors.Options)) (*s3vectors.ListVectorsOutput, error) {
	n, ok := f.indexes[aws.ToString(in.IndexName)]
	if !ok {
		return nil, &types.NotFoundException{}
	}
	return &s3vectors.ListVectorsOutput{
		Vectors: make([]types.ListOutputVector, n),
	}, nil
}

func TestS3Vectors(t *testing.T) {
	ctx := context.Background()
	f := &fakeS3{
		indexes: map[string]int{},
	}
	s, err := NewS3VectorsWithClient(f, S3VectorsConfig{
		Bucket:     "b",
		Prefix:     "prod",
		Dimensions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ms, err := s.Query(ctx,
		"ds",
		[]float32{1, 0},
		QueryOptions{}); err != nil || len(ms) != 0 {
		t.Fatalf("query on a missing dataset: %v %v",
			ms,
			err)
	}
	if err := s.Upsert(ctx,
		"ds",
		[]Vector{{
			ID:     "a",
			Values: []float32{1},
		}}); err == nil {
		t.Fatal("wrong dimension accepted")
	}
	vs := make([]Vector, 501)
	for i := range vs {
		vs[i] = Vector{
			ID:       "v",
			Values:   []float32{1, 0},
			Metadata: map[string]any{"kind": "entity"},
		}
	}
	if err := s.Upsert(ctx,
		"ds",
		vs); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.puts, []int{500, 1}) || f.indexes["prod.ds"] != 501 {
		t.Fatalf("puts %v indexes %v",
			f.puts,
			f.indexes)
	}
	ms, err := s.Query(ctx,
		"ds",
		[]float32{1, 0},
		QueryOptions{})
	if err != nil || len(ms) != 1 || ms[0].Score != 0.75 {
		t.Fatalf("query: %v %v",
			ms,
			err)
	}
	st, err := s.Stats(ctx, "ds")
	if err != nil || st.Count != 501 || st.Backend != "s3vectors" {
		t.Fatalf("stats %+v %v",
			st,
			err)
	}
	if err := s.Delete(ctx,
		"ds",
		[]string{"a"}); err != nil || !slices.Equal(f.deleted, []string{"a"}) {
		t.Fatalf("delete %v %v",
			f.deleted,
			err)
	}
	if err := s.DeleteAll(ctx, "ds"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAll(ctx, "ds"); err != nil {
		t.Fatalf("second DeleteAll: %v", err)
	}
	if st, err := s.Stats(ctx, "ds"); err != nil || st.Count != 0 {
		t.Fatalf("stats after delete %+v %v",
			st,
			err)
	}
}

func TestNewS3VectorsErrors(t *testing.T) {
	if _, err := NewS3VectorsWithClient(&fakeS3{}, S3VectorsConfig{
		Dimensions: 2,
	}); err == nil {
		t.Error("missing bucket accepted")
	}
	if _, err := NewS3VectorsWithClient(&fakeS3{}, S3VectorsConfig{
		Bucket: "b",
	}); err == nil {
		t.Error("missing dimensions accepted")
	}
}
