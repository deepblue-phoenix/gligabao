package vector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
)

const (
	// s3vMaxPutBatch and s3vMaxDeleteBatch are the S3 Vectors limits of vectors / keys per PutVectors / DeleteVectors call.
	s3vMaxPutBatch    = 500
	s3vMaxDeleteBatch = 500
	// s3vMaxTopK is the largest topK a QueryVectors call accepts.
	s3vMaxTopK = 100
	// s3vMaxIndexName is the longest index name S3 Vectors accepts.
	s3vMaxIndexName = 63
	// s3vDefaultDataset is the index name part of the empty (default) dataset.
	s3vDefaultDataset = "default"
)

// S3VectorsAPI is the part of the AWS S3 Vectors client (*s3vectors.Client) that S3Vectors uses; tests pass a fake.
type S3VectorsAPI interface {
	CreateIndex(ctx context.Context, in *s3vectors.CreateIndexInput, opts ...func(*s3vectors.Options)) (*s3vectors.CreateIndexOutput, error)
	DeleteIndex(ctx context.Context, in *s3vectors.DeleteIndexInput, opts ...func(*s3vectors.Options)) (*s3vectors.DeleteIndexOutput, error)
	PutVectors(ctx context.Context, in *s3vectors.PutVectorsInput, opts ...func(*s3vectors.Options)) (*s3vectors.PutVectorsOutput, error)
	QueryVectors(ctx context.Context, in *s3vectors.QueryVectorsInput, opts ...func(*s3vectors.Options)) (*s3vectors.QueryVectorsOutput, error)
	DeleteVectors(ctx context.Context, in *s3vectors.DeleteVectorsInput, opts ...func(*s3vectors.Options)) (*s3vectors.DeleteVectorsOutput, error)
	ListVectors(ctx context.Context, in *s3vectors.ListVectorsInput, opts ...func(*s3vectors.Options)) (*s3vectors.ListVectorsOutput, error)
}

// S3Vectors is a Store backed by an Amazon S3 vector bucket (S3 Vectors).
//
// S3 Vectors has no directories: a vector bucket holds vector indexes, and every index has a fixed dimension.
// The wrapper gives the bucket a directory-like layout: a gligabao dataset is one index of the bucket, named
// "<prefix>.<dataset>" (see S3VectorsIndexName), so the datasets of a deployment sit under its prefix like
// subdirectories, and several deployments (environments, projects) can share one bucket with different prefixes.
// The index is created on the first write, with the configured dimension and the cosine metric.
type S3Vectors struct {
	client     S3VectorsAPI
	bucket     string
	prefix     string
	dimensions int
}

var (
	_ Store   = (*S3Vectors)(nil)
	_ Statter = (*S3Vectors)(nil)
)

// S3VectorsConfig describes the S3 vector bucket.
type S3VectorsConfig struct {
	// Bucket is the name of the (existing) vector bucket. Required.
	Bucket string
	// Region is the AWS region of the bucket. Empty: the default of the AWS SDK (AWS_REGION, the shared config).
	Region string
	// Prefix is the "subdirectory" of the bucket this store owns: the datasets are the indexes named "<Prefix>.<dataset>".
	// Empty: no prefix, the dataset name alone is the index name.
	Prefix string
	// Dimensions is the size of the embeddings; the index of a dataset is created with it. Required.
	Dimensions int
}

// NewS3Vectors creates a store for the vector bucket of cfg, with an AWS client built from the default credential chain
// (environment, shared config/credentials files, IAM role).
func NewS3Vectors(ctx context.Context, cfg S3VectorsConfig) (*S3Vectors, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("vector: load AWS config: %w", err)
	}
	return NewS3VectorsWithClient(s3vectors.NewFromConfig(awsCfg), cfg)
}

// NewS3VectorsWithClient is NewS3Vectors with a given client (a *s3vectors.Client, or a fake in tests).
func NewS3VectorsWithClient(client S3VectorsAPI, cfg S3VectorsConfig) (*S3Vectors, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("vector: missing S3 vector bucket (set s3_vectors_bucket)")
	}
	if cfg.Dimensions < 1 {
		return nil, fmt.Errorf("vector: S3 Vectors dimensions %d must be positive", cfg.Dimensions)
	}
	if client == nil {
		return nil, errors.New("vector: nil S3 Vectors client")
	}
	return &S3Vectors{client: client, bucket: cfg.Bucket, prefix: sanitizeIndexPart(cfg.Prefix), dimensions: cfg.Dimensions}, nil
}

//---------------------------------------------------------------------------------------------

var s3vInvalidChars = regexp.MustCompile(`[^a-z0-9.-]+`)

// sanitizeIndexPart lower-cases s and replaces what an S3 Vectors index name does not allow (anything but a-z, 0-9, '-', '.')
// with '-', trimming separators from the ends.
func sanitizeIndexPart(s string) string {
	return strings.Trim(s3vInvalidChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-.")
}

// S3VectorsIndexName is the S3 Vectors index that holds a dataset: "<prefix>.<dataset>" ("<dataset>" without a prefix, the
// empty dataset being "default"). S3 index names are 3-63 characters of a-z, 0-9, '-' and '.', so a dataset name that has to
// be altered to fit (upper case is just lowered; other characters, or a too long name, are replaced) gets a short hash of the
// original name at the end, so two different datasets never share an index.
func S3VectorsIndexName(prefix, dataset string) string {
	name := dataset
	if name == "" {
		name = s3vDefaultDataset
	}
	clean := sanitizeIndexPart(name)
	p := sanitizeIndexPart(prefix)
	join := func(part string) string {
		if p == "" {
			return part
		}
		return p + "." + part
	}
	full := join(clean)
	if clean == strings.ToLower(name) && len(full) >= 3 && len(full) <= s3vMaxIndexName {
		return full
	}
	sum := sha256.Sum256([]byte(dataset))
	suffix := "-" + hex.EncodeToString(sum[:4])
	room := max(s3vMaxIndexName-len(suffix)-len(join("")), 1)
	if len(clean) > room {
		clean = strings.TrimRight(clean[:room], "-.")
	}
	return join(clean) + suffix
}

func (s *S3Vectors) index(dataset string) string { return S3VectorsIndexName(s.prefix, dataset) }

func isS3VectorsNotFound(err error) bool {
	var nf *types.NotFoundException
	return errors.As(err, &nf)
}

func isS3VectorsConflict(err error) bool {
	var ce *types.ConflictException
	return errors.As(err, &ce)
}

// batches splits xs into consecutive slices of at most n elements.
func batches[T any](xs []T, n int) [][]T {
	if len(xs) == 0 {
		return nil
	}
	end := min(n, len(xs))
	return append([][]T{xs[:end]}, batches(xs[end:], n)...)
}

//---------------------------------------------------------------------------------------------

// ensureIndex creates the dataset's index; one that already exists is not an error.
func (s *S3Vectors) ensureIndex(ctx context.Context, dataset string) error {
	_, err := s.client.CreateIndex(ctx, &s3vectors.CreateIndexInput{
		VectorBucketName: aws.String(s.bucket),
		IndexName:        aws.String(s.index(dataset)),
		DataType:         types.DataTypeFloat32,
		Dimension:        aws.Int32(int32(s.dimensions)),
		DistanceMetric:   types.DistanceMetricCosine,
	})
	if err != nil && !isS3VectorsConflict(err) {
		return fmt.Errorf("vector: create S3 vector index %q: %w", s.index(dataset), err)
	}
	return nil
}

func (s *S3Vectors) put(ctx context.Context, dataset string, batch []types.PutInputVector) error {
	in := &s3vectors.PutVectorsInput{VectorBucketName: aws.String(s.bucket), IndexName: aws.String(s.index(dataset)), Vectors: batch}
	_, err := s.client.PutVectors(ctx, in)
	if isS3VectorsNotFound(err) {
		if err = s.ensureIndex(ctx, dataset); err != nil {
			return err
		}
		_, err = s.client.PutVectors(ctx, in)
	}
	if err != nil {
		return fmt.Errorf("vector: S3 put vectors: %w", err)
	}
	return nil
}

func toS3Vector(v Vector) types.PutInputVector {
	out := types.PutInputVector{Key: aws.String(v.ID), Data: &types.VectorDataMemberFloat32{Value: v.Values}}
	if len(v.Metadata) > 0 {
		out.Metadata = document.NewLazyDocument(v.Metadata)
	}
	return out
}

// Upsert inserts or overwrites vectors by ID, creating the dataset's index on the first write.
func (s *S3Vectors) Upsert(ctx context.Context, dataset string, vectors []Vector) error {
	for i, v := range vectors {
		if v.ID == "" {
			return fmt.Errorf("vector: vector %d has no ID", i)
		}
		if len(v.Values) != s.dimensions {
			return fmt.Errorf("vector: vector %q has %d values, want %d", v.ID, len(v.Values), s.dimensions)
		}
	}
	puts := make([]types.PutInputVector, len(vectors))
	for i, v := range vectors {
		puts[i] = toS3Vector(v)
	}
	for _, b := range batches(puts, s3vMaxPutBatch) {
		if err := s.put(ctx, dataset, b); err != nil {
			return err
		}
	}
	return nil
}

func decodeS3Metadata(d document.Interface) map[string]any {
	if d == nil {
		return nil
	}
	var m map[string]any
	if err := d.UnmarshalSmithyDocument(&m); err != nil {
		return nil
	}
	return m
}

// Query returns the closest vectors, best first. The score is the cosine similarity (1 - cosine distance). Querying a
// dataset that does not exist returns no matches. Filter is the S3 Vectors metadata filter (an equality map works).
func (s *S3Vectors) Query(ctx context.Context, dataset string, vector []float32, opts QueryOptions) ([]Match, error) {
	if len(vector) == 0 {
		return nil, errors.New("vector: empty query vector")
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	in := &s3vectors.QueryVectorsInput{
		VectorBucketName: aws.String(s.bucket),
		IndexName:        aws.String(s.index(dataset)),
		QueryVector:      &types.VectorDataMemberFloat32{Value: vector},
		TopK:             aws.Int32(int32(min(topK, s3vMaxTopK))),
		ReturnMetadata:   true,
		ReturnDistance:   true,
	}
	if len(opts.Filter) > 0 {
		in.Filter = document.NewLazyDocument(opts.Filter)
	}
	out, err := s.client.QueryVectors(ctx, in)
	if isS3VectorsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vector: S3 query vectors: %w", err)
	}
	matches := make([]Match, len(out.Vectors))
	for i, v := range out.Vectors {
		matches[i] = Match{ID: aws.ToString(v.Key), Score: 1 - aws.ToFloat32(v.Distance), Metadata: decodeS3Metadata(v.Metadata)}
	}
	return matches, nil
}

// Delete removes vectors by ID. Unknown IDs, and datasets that do not exist, are ignored.
func (s *S3Vectors) Delete(ctx context.Context, dataset string, ids []string) error {
	for _, b := range batches(ids, s3vMaxDeleteBatch) {
		_, err := s.client.DeleteVectors(ctx, &s3vectors.DeleteVectorsInput{
			VectorBucketName: aws.String(s.bucket), IndexName: aws.String(s.index(dataset)), Keys: b,
		})
		if err != nil && !isS3VectorsNotFound(err) {
			return fmt.Errorf("vector: S3 delete vectors: %w", err)
		}
	}
	return nil
}

// DeleteAll deletes the dataset's index with everything in it. A dataset that does not exist is not an error.
func (s *S3Vectors) DeleteAll(ctx context.Context, dataset string) error {
	_, err := s.client.DeleteIndex(ctx, &s3vectors.DeleteIndexInput{
		VectorBucketName: aws.String(s.bucket), IndexName: aws.String(s.index(dataset)),
	})
	if err != nil && !isS3VectorsNotFound(err) {
		return fmt.Errorf("vector: S3 delete index: %w", err)
	}
	return nil
}

// count pages through the keys of the index (S3 Vectors has no count call) and adds them up.
func (s *S3Vectors) count(ctx context.Context, dataset string, token *string, acc int) (int, error) {
	out, err := s.client.ListVectors(ctx, &s3vectors.ListVectorsInput{
		VectorBucketName: aws.String(s.bucket), IndexName: aws.String(s.index(dataset)), NextToken: token,
	})
	if err != nil {
		return 0, err
	}
	acc += len(out.Vectors)
	if out.NextToken == nil || *out.NextToken == "" {
		return acc, nil
	}
	return s.count(ctx, dataset, out.NextToken, acc)
}

// Stats returns the number of vectors of the dataset (a dataset that does not exist has none). It lists the keys of the
// index, so it costs one call per 500 vectors.
func (s *S3Vectors) Stats(ctx context.Context, dataset string) (Stats, error) {
	n, err := s.count(ctx, dataset, nil, 0)
	if isS3VectorsNotFound(err) {
		n, err = 0, nil
	}
	if err != nil {
		return Stats{}, fmt.Errorf("vector: S3 list vectors: %w", err)
	}
	return Stats{Backend: "s3vectors", Count: n, Dimensions: s.dimensions, Bytes: int64(n) * int64(s.dimensions) * 4}, nil
}
