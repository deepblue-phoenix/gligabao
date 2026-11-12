package investigate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"io"
	"log"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type notesMirror struct {
	local investigate_core.NotesStore
	s3    *NotesS3
	user  string
}

// DefaultNotesS3Prefix is the "subdirectory" of the research bucket the notes go to when no prefix is configured.
const DefaultNotesS3Prefix = "investigative_notes"

// s3Timeout bounds one S3 call, so that a slow bucket cannot hold a research run.
const s3Timeout = 30 * time.Second

// S3API is the part of the AWS S3 client (*s3.Client) that the notes mirror uses; tests pass a fake.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// NotesS3Config describes where the notes are mirrored to.
type NotesS3Config struct {
	// Bucket is the (existing) research bucket, common to all the research agents. Required.
	Bucket string
	// Region is its AWS region; empty: the AWS SDK default (AWS_REGION, the shared config).
	Region string
	// Prefix is the subdirectory of the bucket for the investigative notes (DefaultNotesS3Prefix if empty). Each user has a subdirectory of it.
	Prefix string
}

// NotesS3 is a client of the research bucket; Mirror makes the NotesStore of one user's run.
type NotesS3 struct {
	client S3API
	bucket string
	prefix string
}

// NewNotesS3 creates the client with the default AWS credential chain (environment, shared config, instance role).
func NewNotesS3(ctx context.Context, cfg NotesS3Config) (*NotesS3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("notes s3: bucket is required")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("notes s3: load aws config: %w", err)
	}
	return NewNotesS3WithClient(s3.NewFromConfig(awsCfg), cfg), nil
}

// NewNotesS3WithClient is NewNotesS3 with a given client.
func NewNotesS3WithClient(client S3API, cfg NotesS3Config) *NotesS3 {
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = DefaultNotesS3Prefix
	}
	return &NotesS3{
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
	}
}

// Key is the object key of a notes file of a user: <prefix>/<sanitized user>/<file name>. "" for a run without notes file.
func (n *NotesS3) Key(user, localPath string) string {
	if localPath == "" {
		return ""
	}
	return path.Join(n.prefix, SanitizeUser(user), filepath.Base(localPath))
}

// Mirror returns the NotesStore of a user's runs: the local files stay the working copy, every write is also put to the bucket, and a
// read of a file missing locally (a container restarted with an empty disk) is served from the bucket.
func (n *NotesS3) Mirror(local investigate_core.NotesStore, user string) investigate_core.NotesStore {
	return notesMirror{
		local: local,
		s3:    n,
		user:  user,
	}
}

// Write writes the local file (an error there is the error of the write), then the object. A failure of the bucket is logged and does not
// stop the research: the local file and the database hold the notes anyway.
func (m notesMirror) Write(p, text string) error {
	if err := m.local.Write(p, text); err != nil {
		return err
	}
	if err := m.s3.put(m.s3.Key(m.user, p), text); err != nil {
		log.Printf("research notes: mirror to s3 failed: %v", err)
	}
	return nil
}

// Read reads the local file; if it is missing (it reads as the empty notes) the object of the bucket is used when there is one.
func (m notesMirror) Read(p string) (string, error) {
	got, err := m.local.Read(p)
	if err != nil || got != investigate_core.Render(investigate_core.Notes{}) {
		return got, err
	}
	if remote, ok := m.s3.get(m.s3.Key(m.user, p)); ok {
		return remote, nil
	}
	return got, nil
}

func (n *NotesS3) put(key, text string) error {
	if key == "" {
		return errors.New("the run has no notes id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3Timeout)
	defer cancel()
	_, err := n.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(n.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(text)),
		ContentType: aws.String("text/markdown; charset=utf-8"),
	})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", n.bucket, key, err)
	}
	return nil
}

func (n *NotesS3) get(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3Timeout)
	defer cancel()
	out, err := n.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(n.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var missing *s3types.NoSuchKey
		if !errors.As(err, &missing) {
			log.Printf("research notes: read from s3 failed: %v", err)
		}
		return "", false
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		log.Printf("research notes: read from s3 failed: %v", err)
		return "", false
	}
	return string(b), true
}
