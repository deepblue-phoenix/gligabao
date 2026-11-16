package imagestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3API is the part of the AWS S3 client (*s3.Client) that S3 uses; tests pass a fake.
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

// S3Config describes the bucket of an S3 store.
type S3Config struct {
	// Bucket is the name of the (existing) bucket. Required.
	Bucket string
	// Region is the AWS region of the bucket. Empty: the default of the AWS SDK (AWS_REGION, the shared config).
	Region string
	// Prefix is the "subdirectory" of the bucket the images live in ("<prefix>/<dataset id>/<sha256>.<ext>"), so
	// deployments can share a bucket. Empty: the bucket root.
	Prefix string
	// Endpoint is a custom S3 endpoint URL (MinIO, LocalStack, ...), used with path-style addressing. Empty: AWS.
	Endpoint string
}

// S3 keeps the images as objects of an Amazon S3 bucket, one directory per user: the key of an image is
// "<prefix>/<user>/<dataset id>/<sha256>.<ext>". Credentials come from the standard AWS chain.
//
// Access control: every key is built here from the owner and a cleaned relative path, never taken from the caller. The
// owner is encoded into exactly one path segment (so "/", ".." or a prefix of another user's id cannot reach another
// directory), and a relative path that is not plain (".." , absolute, empty segments) is refused. An operation of a user
// therefore cannot touch the directory of another one. This is enforced in the application; to also enforce it in AWS,
// give the credentials an IAM policy limited to the prefix.
type S3 struct {
	client S3API
	bucket string
	prefix string
}

var _ Store = (*S3)(nil)

// NewS3 creates a store for the bucket of cfg, with an AWS client built from the default credential chain.
func NewS3(ctx context.Context,
	cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("image store: missing S3 bucket (set image_s3_bucket)")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("image store: load AWS config: %w", err)
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
		return nil, errors.New("image store: missing S3 bucket (set image_s3_bucket)")
	}
	if client == nil {
		return nil, errors.New("image store: nil S3 client")
	}
	return &S3{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
	}, nil
}

// Location implements Store.
func (s *S3) Location() string {
	return "s3://" + path.Join(s.bucket, s.prefix)
}

// userSegment encodes a user id into one safe path segment (injective, and never "." or "..").
func userSegment(owner string) (string, error) {
	if strings.TrimSpace(owner) == "" {
		return "", errors.New("image store: the owner (user id) is required")
	}
	enc := url.QueryEscape(owner)
	switch enc {
	case ".":
		return "%2E", nil
	case "..":
		return "%2E%2E", nil
	}
	return enc, nil
}

// rootOf is the key prefix of all objects ("" or ending with "/").
func (s *S3) rootOf() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

// key is the object key of rel in the directory of owner.
func (s *S3) key(owner,
	rel string) (string, error) {
	seg, err := userSegment(owner)
	if err != nil {
		return "", err
	}
	clean, err := cleanPath(rel)
	if err != nil {
		return "", err
	}
	return s.rootOf() + seg + "/" + clean, nil
}

func isMissing(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchKey", "NoSuchBucket":
		return true
	}
	return false
}

// Put implements Store. S3 writes an object atomically.
func (s *S3) Put(ctx context.Context,
	owner,
	rel string,
	data []byte) (bool, error) {
	key, err := s.key(owner, rel)
	if err != nil {
		return false, err
	}
	_, headErr := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	switch {
	case headErr == nil:
		return false, nil
	case !isMissing(headErr):
		return false, fmt.Errorf("image store: head %s: %w", key, headErr)
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return false, fmt.Errorf("image store: put %s: %w", key, err)
	}
	return true, nil
}

// Read implements Store.
func (s *S3) Read(ctx context.Context,
	owner,
	rel string) ([]byte, error) {
	key, err := s.key(owner, rel)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isMissing(err) {
			return nil, fmt.Errorf("%w: %s (%w)",
				ErrNotFound,
				rel,
				err)
		}
		return nil, fmt.Errorf("image store: get %s: %w", key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// Remove implements Store (S3 does not fail on a key that is gone).
func (s *S3) Remove(ctx context.Context,
	owner,
	rel string) error {
	key, err := s.key(owner, rel)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil && !isMissing(err) {
		return fmt.Errorf("image store: delete %s: %w", key, err)
	}
	return nil
}

// List implements Store.
func (s *S3) List(ctx context.Context) ([]Object, error) {
	listPrefix := s.rootOf()
	var (
		out   []Object
		token *string
	)
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(listPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			if isMissing(err) {
				return nil, fmt.Errorf("%w: bucket %s (%w)",
					ErrNotFound,
					s.bucket,
					err)
			}
			return nil, fmt.Errorf("image store: list: %w", err)
		}
		for _, o := range page.Contents {
			seg, rel, ok := strings.Cut(strings.TrimPrefix(aws.ToString(o.Key), listPrefix), "/")
			owner, unescErr := url.QueryUnescape(seg)
			if !ok || rel == "" || unescErr != nil {
				continue // not an object of ours
			}
			out = append(out, Object{
				Owner: owner,
				Path:  rel,
				Size:  aws.ToInt64(o.Size),
			})
		}
		if !aws.ToBool(page.IsTruncated) || page.NextContinuationToken == nil {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}
