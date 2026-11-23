package userfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/deepblue-phoenix/gligabao/internal/sys/userpath"
)

// DefaultS3Prefix is the "subdirectory" of the bucket the files go to when no prefix is configured.
const DefaultS3Prefix = "evals"

// S3API is the part of the AWS S3 client (*s3.Client) that the store uses; tests pass a fake.
type S3API interface {
	PutObject(ctx context.Context,
		in *s3.PutObjectInput,
		opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context,
		in *s3.GetObjectInput,
		opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context,
		in *s3.HeadObjectInput,
		opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context,
		in *s3.DeleteObjectInput,
		opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context,
		in *s3.ListObjectsV2Input,
		opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// S3Config describes the bucket of the files.
type S3Config struct {
	// Bucket is the name of the (existing) bucket. Required.
	Bucket string
	// Region is the AWS region of the bucket. Empty: the default of the AWS SDK.
	Region string
	// Prefix is the "subdirectory" of the bucket (DefaultS3Prefix if empty).
	Prefix string
	// Endpoint is a custom S3 endpoint URL (MinIO, LocalStack, ...), used with path-style addressing. Empty: AWS.
	Endpoint string
}

// S3 stores the files in an Amazon S3 bucket: "<prefix>/<user segment>/<relPath>". The credentials come from the standard AWS chain.
type S3 struct {
	client S3API
	bucket string
	prefix string
}

var _ Store = (*S3)(nil)

// NewS3Client builds the AWS S3 client of cfg (region and custom endpoint) from the default credential chain.
func NewS3Client(ctx context.Context,
	cfg S3Config) (*s3.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("userfiles: load AWS config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	}), nil
}

// NewS3 creates the store of the bucket of cfg, with an AWS client built from the default credential chain.
func NewS3(ctx context.Context,
	cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("userfiles: missing S3 bucket (set evals_s3_bucket)")
	}
	client, err := NewS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewS3WithClient(client, cfg)
}

// NewS3WithClient is NewS3 with a given client (a *s3.Client, or a fake in tests).
func NewS3WithClient(client S3API,
	cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("userfiles: missing S3 bucket (set evals_s3_bucket)")
	}
	if client == nil {
		return nil, errors.New("userfiles: nil S3 client")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = DefaultS3Prefix
	}
	return &S3{
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
	}, nil
}

// Location describes where the files are, without secrets ("s3://bucket/prefix").
func (s *S3) Location() string { return "s3://" + path.Join(s.bucket, s.prefix) }

// dirOf is the key prefix of the directory of the user, ending with "/".
func (s *S3) dirOf(user string) (string, error) {
	seg, err := userpath.Segment(user)
	if err != nil {
		return "", fmt.Errorf("userfiles: %w", err)
	}
	return s.prefix + "/" + seg + "/", nil
}

// keyOf is the object key of relPath of the user.
func (s *S3) keyOf(user,
	relPath string) (string, error) {
	if err := ValidateRel(relPath); err != nil {
		return "", err
	}
	dir, err := s.dirOf(user)
	if err != nil {
		return "", err
	}
	return dir + relPath, nil
}

// Put implements Store.
func (s *S3) Put(ctx context.Context,
	user,
	relPath string,
	data []byte) error {
	key, err := s.keyOf(user, relPath)
	if err != nil {
		return err
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return fmt.Errorf("userfiles: put %s: %w", key, err)
	}
	return nil
}

// Get implements Store.
func (s *S3) Get(ctx context.Context,
	user,
	relPath string) ([]byte, error) {
	key, err := s.keyOf(user, relPath)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if IsS3Missing(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("userfiles: get %s: %w", key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// Delete implements Store (S3 deletes silently, so the object is looked up first).
func (s *S3) Delete(ctx context.Context,
	user,
	relPath string) error {
	key, err := s.keyOf(user, relPath)
	if err != nil {
		return err
	}
	if _, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		if IsS3Missing(err) {
			return ErrNotFound
		}
		return fmt.Errorf("userfiles: head %s: %w", key, err)
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("userfiles: delete %s: %w", key, err)
	}
	return nil
}

// List implements Store.
func (s *S3) List(ctx context.Context,
	user,
	prefix string) ([]string, error) {
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	dir, err := s.dirOf(user)
	if err != nil {
		return nil, err
	}
	out := []string{}
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(dir + prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("userfiles: list %s: %w", dir, err)
		}
		for _, o := range page.Contents {
			if rel, ok := strings.CutPrefix(aws.ToString(o.Key), dir); ok && rel != "" && !strings.HasSuffix(rel, "/") {
				out = append(out, rel)
			}
		}
		if !aws.ToBool(page.IsTruncated) || page.NextContinuationToken == nil {
			break
		}
		token = page.NextContinuationToken
	}
	sort.Strings(out)
	return out, nil
}

// IsS3Missing says whether err is S3's "no such object".
func IsS3Missing(err error) bool {
	var noKey *s3types.NoSuchKey
	var notFound *s3types.NotFound
	var apiErr smithy.APIError
	return errors.As(err, &noKey) || errors.As(err, &notFound) ||
		(errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound"))
}
