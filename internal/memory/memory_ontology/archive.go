package memory_ontology

// Archive keeps the documents of the ontologies in an Amazon S3 bucket, next to the database copy (the classes and edges). It is on when
// the server has an ontology bucket (config.OntologyS3Bucket). Every user has a directory:
// "<prefix>/<user>/<name>/<timestamp>_<action>.owl", where timestamp is UTC ("20060102T150405Z") and action is load, merge or replace, so
// the documents of an ontology list in the order they were sent and none is overwritten.
//
// The S3 client (S3) is dedicated to the ontologies: it shares no code with the clients of the images and the ATIF traces. Every key is
// built here from the user, the ontology name and the action, never taken from the caller, and the user is encoded as exactly one path
// segment, so a user cannot reach the directory of another. This is enforced in the application; to also enforce it in AWS, give the
// credentials an IAM policy limited to the prefix.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	log "github.com/sirupsen/logrus"
)

// DefaultS3Prefix is the "subdirectory" of the bucket the ontology documents go to when no prefix is configured.
const DefaultS3Prefix = "ontologies"

// The actions of an archived document.
const (
	actionLoad    = "load"
	actionMerge   = "merge"
	actionReplace = "replace"
)

const archiveTimeLayout = "20060102T150405Z"

// S3API is the part of the AWS S3 client (*s3.Client) that the ontology store uses; tests pass a fake.
type S3API interface {
	PutObject(ctx context.Context,
		in *s3.PutObjectInput,
		opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3Config describes the bucket of the ontology documents.
type S3Config struct {
	// Bucket is the name of the (existing) bucket. Required.
	Bucket string
	// Region is the AWS region of the bucket. Empty: the default of the AWS SDK (AWS_REGION, the shared config).
	Region string
	// Prefix is the "subdirectory" of the bucket the ontologies live in (DefaultS3Prefix if empty).
	Prefix string
	// Endpoint is a custom S3 endpoint URL (MinIO, LocalStack, ...), used with path-style addressing. Empty: AWS.
	Endpoint string
}

// S3 is the S3 client of the ontology documents.
type S3 struct {
	client S3API
	bucket string
	prefix string
}

// Archive is where the ontology documents are also stored (set once at startup by the server); nil: only the database keeps the ontologies.
var Archive *S3

// NewS3 creates the client for the bucket of cfg, with an AWS client built from the default credential chain.
func NewS3(ctx context.Context,
	cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("ontology store: missing S3 bucket (set ontology_s3_bucket)")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("ontology store: load AWS config: %w", err)
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
		return nil, errors.New("ontology store: missing S3 bucket (set ontology_s3_bucket)")
	}
	if client == nil {
		return nil, errors.New("ontology store: nil S3 client")
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

// Location describes where the documents are, without secrets ("s3://bucket/prefix").
func (s *S3) Location() string { return "s3://" + s.bucket + "/" + s.prefix }

// userSegment encodes a user id into one safe path segment (injective, and never "." or "..").
func userSegment(user string) (string, error) {
	if strings.TrimSpace(user) == "" {
		return "", errors.New("ontology store: the user id is required")
	}
	switch enc := url.QueryEscape(user); enc {
	case ".":
		return "%2E", nil
	case "..":
		return "%2E%2E", nil
	default:
		return enc, nil
	}
}

// Key is the object key of a document: "<prefix>/<user>/<name>/<timestamp>_<action>.owl". The name must be a valid ontology name.
func (s *S3) Key(user,
	name,
	action string,
	at time.Time) (string, error) {
	seg, err := userSegment(user)
	if err != nil {
		return "", err
	}
	if name, err = ResolveName(name); err != nil {
		return "", err
	}
	return s.prefix + "/" + seg + "/" + name + "/" + at.UTC().Format(archiveTimeLayout) + "_" + action + ".owl", nil
}

// Put stores a document of the ontology called name of user and returns its key. Objects are never overwritten (the key has the time).
func (s *S3) Put(ctx context.Context,
	user,
	name,
	action string,
	at time.Time,
	content []byte) (string, error) {
	key, err := s.Key(user,
		name,
		action,
		at)
	if err != nil {
		return "", err
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}); err != nil {
		return "", fmt.Errorf("ontology store: put %s: %w", key, err)
	}
	return key, nil
}

// archive stores the document of an ontology that is stored in the database already. A failure is logged and does not fail the request:
// the ontology is stored and the S3 copy is an extra.
func archive(ctx context.Context,
	userID,
	name,
	action string,
	content []byte) {
	if Archive == nil {
		return
	}
	if _, err := Archive.Put(context.WithoutCancel(ctx),
		userID,
		name,
		action,
		time.Now(),
		content); err != nil {
		log.WithError(err).WithField("ontology", name).Error("ontology: storing the document in S3")
	}
}
