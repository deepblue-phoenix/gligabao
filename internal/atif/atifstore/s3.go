package atifstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/sys/userpath"
)

// DefaultS3Prefix is the "subdirectory" of the bucket the traces go to when no prefix is configured.
const DefaultS3Prefix = "traces/atif"

// S3API is the part of the AWS S3 client (*s3.Client) that the trace store uses; tests pass a fake.
type S3API interface {
	PutObject(ctx context.Context,
		in *s3.PutObjectInput,
		opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context,
		in *s3.GetObjectInput,
		opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3Config describes the bucket of the traces.
type S3Config struct {
	// Bucket is the name of the (existing) bucket. Required.
	Bucket string
	// Region is the AWS region of the bucket. Empty: the default of the AWS SDK (AWS_REGION, the shared config).
	Region string
	// Prefix is the "subdirectory" of the bucket the traces live in (DefaultS3Prefix if empty).
	Prefix string
	// Endpoint is a custom S3 endpoint URL (MinIO, LocalStack, ...), used with path-style addressing. Empty: AWS.
	Endpoint string
}

// S3 stores the trajectories in an Amazon S3 bucket, one directory per user: "<prefix>/<user>/<timestamp>_<trajectory_id>.json". It is
// the S3 client of the traces, dedicated to them (credentials from the standard AWS chain). Every key is built here from the user and the
// file name, never taken from the caller, and the user is encoded as exactly one path segment, so a user cannot reach the directory of
// another. This is enforced in the application; to also enforce it in AWS, give the credentials an IAM policy limited to the prefix.
type S3 struct {
	client S3API
	bucket string
	prefix string
	now    func() time.Time
}

// NewS3 creates the store of the bucket of cfg, with an AWS client built from the default credential chain.
func NewS3(ctx context.Context,
	cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("atif store: missing S3 bucket (set atif_s3_bucket)")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("atif store: load AWS config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})
	return NewS3WithClient(client, cfg)
}

// NewS3WithClient is NewS3 with a given client (a *s3.Client, or a fake in tests).
func NewS3WithClient(client S3API,
	cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("atif store: missing S3 bucket (set atif_s3_bucket)")
	}
	if client == nil {
		return nil, errors.New("atif store: nil S3 client")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = DefaultS3Prefix
	}
	return &S3{
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
		now:    time.Now,
	}, nil
}

// userSegment encodes a user id into one safe path segment (see userpath.Segment).
func userSegment(user string) (string, error) {
	seg, err := userpath.Segment(user)
	if err != nil {
		return "", fmt.Errorf("atif store: %w", err)
	}
	return seg, nil
}

// root is the "s3://bucket/prefix" of the store.
func (s *S3) root() string { return "s3://" + path.Join(s.bucket, s.prefix) }

// Put implements Store: the object is "<prefix>/<user>/<timestamp>_<trajectory_id>.json".
func (s *S3) Put(ctx context.Context,
	user string,
	t *atif.Trajectory) (string, error) {
	seg, err := userSegment(user)
	if err != nil {
		return "", err
	}
	data, err := atif.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("atif store: marshal: %w", err)
	}
	key := s.prefix + "/" + seg + "/" + ObjectName(t, s.now())
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return "", fmt.Errorf("atif store: put %s: %w", key, err)
	}
	return "s3://" + s.bucket + "/" + key, nil
}

// Read implements Store: location is one that Put returned. A location outside the bucket and prefix of the store is refused.
func (s *S3) Read(ctx context.Context,
	location string) ([]byte, error) {
	rest, ok := strings.CutPrefix(location, s.root()+"/")
	if !ok {
		return nil, fmt.Errorf("atif store: %q is not in %s", location, s.root())
	}
	seg, name, ok := strings.Cut(rest, "/")
	if !ok || name == "" || seg == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return nil, errors.New("atif store: bad location " + location)
	}
	key := s.prefix + "/" + seg + "/" + name
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("atif store: get %s: %w", key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}
