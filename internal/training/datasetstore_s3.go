package training

// The S3 store of the training datasets: the layout of the training datasets directory under a prefix of a bucket (normally the meta
// bucket, prefix "training_datasets"), one "directory" per dataset:
//
//	<prefix>/<dir>/dataset.toml     the manifest
//	<prefix>/<dir>/<file>           the data file the manifest names (JSONL)
//
// An upload writes <prefix>/<name>/<name>.jsonl and a manifest next to it. Credentials come from the standard AWS chain.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	log "github.com/sirupsen/logrus"
)

// DefaultDatasetsS3Prefix is the "subdirectory" of the bucket the datasets are in when no prefix is configured.
const DefaultDatasetsS3Prefix = "training_datasets"

// S3Store is the DatasetStore of a bucket. Build it with NewS3Store.
type S3Store struct {
	client userfiles.S3API
	bucket string
	prefix string
}

var _ DatasetStore = (*S3Store)(nil)

// NewS3Store makes the store of the bucket of cfg (cfg.Prefix defaults to DefaultDatasetsS3Prefix), with an AWS client built from the
// default credential chain.
func NewS3Store(ctx context.Context,
	cfg userfiles.S3Config) (*S3Store, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("training: missing S3 bucket (set training_datasets_s3_bucket)")
	}
	client, err := userfiles.NewS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewS3StoreWithClient(client, cfg)
}

// NewS3StoreWithClient is NewS3Store with a given client (a *s3.Client, or a fake in tests).
func NewS3StoreWithClient(client userfiles.S3API,
	cfg userfiles.S3Config) (*S3Store, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("training: missing S3 bucket (set training_datasets_s3_bucket)")
	}
	if client == nil {
		return nil, errors.New("training: nil S3 client")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = DefaultDatasetsS3Prefix
	}
	return &S3Store{
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
	}, nil
}

// Location implements DatasetStore ("s3://bucket/prefix").
func (s *S3Store) Location() string { return "s3://" + path.Join(s.bucket, s.prefix) }

// ProblemDir implements DatasetStore: the name of the dataset's "directory".
func (s *S3Store) ProblemDir(ds LocalDataset) string { return path.Base(ds.Dir) }

// dirKey is the key prefix of the dataset directory dir, without the trailing "/".
func (s *S3Store) dirKey(dir string) string { return s.prefix + "/" + dir }

// head is the part of an object's metadata the store uses.
type objectHead struct {
	size     int64
	modified time.Time
}

func (s *S3Store) head(ctx context.Context,
	key string) (objectHead, bool, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if userfiles.IsS3Missing(err) {
			return objectHead{}, false, nil
		}
		return objectHead{}, false, fmt.Errorf("head %s: %w",
			key,
			err)
	}
	return objectHead{
		size:     aws.ToInt64(out.ContentLength),
		modified: aws.ToTime(out.LastModified),
	}, true, nil
}

// get reads at most limit bytes of the object key (all of it if limit < 0); a missing object is (nil, false, nil).
func (s *S3Store) get(ctx context.Context,
	key string,
	limit int64) ([]byte, bool, error) {
	in := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if limit >= 0 {
		in.Range = aws.String(fmt.Sprintf("bytes=0-%d", limit-1))
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		if userfiles.IsS3Missing(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get %s: %w",
			key,
			err)
	}
	defer out.Body.Close()
	var r io.Reader = out.Body
	if limit >= 0 {
		r = io.LimitReader(out.Body, limit)
	}
	data, err := io.ReadAll(r)
	return data, true, err
}

// dirs lists the dataset "directories" (the names between the prefix and the next "/").
func (s *S3Store) dirs(ctx context.Context) ([]string, error) {
	out := []string{}
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(s.prefix + "/"),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list %s: %w",
				s.Location(),
				err)
		}
		for _, p := range page.CommonPrefixes {
			if d := strings.Trim(strings.TrimPrefix(aws.ToString(p.Prefix), s.prefix+"/"), "/"); d != "" && !strings.HasPrefix(d, ".") {
				out = append(out, d)
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

// Discover implements DatasetStore, with the rules of DiscoverDatasets: a "directory" with a manifest is a dataset; the manifest must
// be valid, its data file must exist, and a name is taken once.
func (s *S3Store) Discover(ctx context.Context) ([]LocalDataset, []DatasetProblem) {
	dirs, err := s.dirs(ctx)
	if err != nil {
		log.WithError(err).Error("training: list the training datasets in S3 failed")
		return nil, []DatasetProblem{{
			Dir:   ".",
			Error: "the training datasets cannot be listed",
		}}
	}
	found := []LocalDataset{}
	problems := []DatasetProblem{}
	taken := map[string]string{}
	for _, d := range dirs {
		ds, ok, err := s.dataset(ctx, d)
		if err == nil && ok {
			if other, dup := taken[ds.Name]; dup {
				err = fmt.Errorf("the name %q is already taken by the dataset in %s",
					ds.Name,
					other)
			}
		}
		switch {
		case err != nil:
			problems = append(problems, DatasetProblem{
				Dir:   d,
				Error: err.Error(),
			})
		case ok:
			taken[ds.Name] = d
			found = append(found, *ds)
		}
	}
	sort.Slice(found, func(i,
		j int) bool {
		return found[i].Name < found[j].Name
	})
	return found, problems
}

// dataset reads the manifest of the "directory" dir: ok is false when there is none (not a dataset, not a problem); the error says
// why a dataset with a manifest is not usable. The messages are safe to show.
func (s *S3Store) dataset(ctx context.Context,
	dir string) (*LocalDataset, bool, error) {
	dirKey := s.dirKey(dir)
	raw, found, err := s.get(ctx,
		dirKey+"/"+ManifestFilename,
		-1)
	if err != nil {
		log.WithError(err).WithField("dir", dir).Error("training: read the training dataset manifest in S3 failed")
		return nil, true, errors.New("the manifest cannot be read")
	}
	if !found {
		return nil, false, nil
	}
	m, err := parseManifestBytes(raw, dir)
	if err != nil {
		return nil, true, err
	}
	ds := &LocalDataset{
		Manifest: m,
		Dir:      dirKey,
		Path:     path.Join(dirKey, m.File),
	}
	_, exists, err := s.head(ctx, ds.Path)
	switch {
	case err != nil:
		log.WithError(err).WithField("dir", dir).Error("training: look up the training dataset file in S3 failed")
		return nil, true, errors.New("the data file cannot be looked up")
	case !exists:
		return nil, true, fmt.Errorf("the data file %s is missing", m.File)
	}
	return ds, true, nil
}

// Stats implements DatasetStore. Like the files of the directories, the stats are cached while the object's size and modification time
// stay the same.
func (s *S3Store) Stats(ctx context.Context,
	ds LocalDataset) (FileStats, error) {
	h, exists, err := s.head(ctx, ds.Path)
	if err != nil {
		return FileStats{}, err
	}
	if !exists {
		return FileStats{}, errors.New("the data file is missing")
	}
	if h.size > MaxDatasetBytes {
		return FileStats{}, fmt.Errorf("the data file is over %d MB", MaxDatasetBytes>>20)
	}
	cacheKey := s.Location() + "|" + ds.Path
	if v, ok := statsCache.Load(cacheKey); ok {
		if c := v.(FileStats); c.Size == h.size && c.modTime.Equal(h.modified) {
			return c, nil
		}
	}
	data, found, err := s.get(ctx,
		ds.Path,
		MaxDatasetBytes+1)
	if err != nil {
		return FileStats{}, err
	}
	if !found {
		return FileStats{}, errors.New("the data file is missing")
	}
	st, err := ContentStats(data)
	if err != nil {
		return FileStats{}, fmt.Errorf("invalid data file: %w", err)
	}
	st.modTime = h.modified
	statsCache.Store(cacheKey, st)
	return st, nil
}

// Read implements DatasetStore.
func (s *S3Store) Read(ctx context.Context,
	ds LocalDataset) ([]byte, error) {
	data, found, err := s.get(ctx,
		ds.Path,
		MaxDatasetBytes+1)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("the data file is missing")
	}
	return data, nil
}

// ReadHead implements DatasetStore.
func (s *S3Store) ReadHead(ctx context.Context,
	ds LocalDataset,
	n int64) ([]byte, error) {
	data, found, err := s.get(ctx,
		ds.Path,
		n+1)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("the data file is missing")
	}
	return data, nil
}

// Stage implements DatasetStore: the dataset is the "directory" called name of the prefix. The data file is written first and the
// manifest after it, so a manifest never names a file that is not there. An upload of a name again replaces the file.
func (s *S3Store) Stage(ctx context.Context,
	name,
	filename string,
	content []byte) (LocalDataset, func() error, func(), error) {
	dirKey := s.dirKey(name)
	raw, hasManifest, err := s.get(ctx,
		dirKey+"/"+ManifestFilename,
		-1)
	if err != nil {
		log.WithError(err).Error("training: read the training dataset manifest in S3 failed")
		return LocalDataset{}, nil, nil, internalErr("failed to look up the dataset", err)
	}
	if hasManifest {
		if cur, err := parseManifestBytes(raw, name); err != nil || cur.Name != name || cur.File != filename {
			return LocalDataset{}, nil, nil, conflictErr("a dataset named " + name + " exists already with its own manifest and data file; upload under another name")
		}
	}
	found, _ := s.Discover(ctx)
	for _, f := range found {
		if f.Name == name && f.Dir != dirKey {
			return LocalDataset{}, nil, nil, conflictErr("a dataset named " + name + " exists already in " + path.Base(f.Dir))
		}
	}
	ds := newUploadedDataset(name,
		filename,
		dirKey,
		dirKey+"/"+filename)
	commit := func() error {
		if err := s.put(ctx, ds.Path, content); err != nil {
			log.WithError(err).Error("training: save training dataset file failed")
			return internalErr("failed to save the dataset file", err)
		}
		if hasManifest {
			return nil
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(ds.Manifest); err != nil {
			return internalErr("failed to save the dataset's manifest", err)
		}
		if err := s.put(ctx, dirKey+"/"+ManifestFilename, buf.Bytes()); err != nil {
			log.WithError(err).Error("training: write training dataset manifest failed")
			return internalErr("failed to save the dataset's manifest", err)
		}
		return nil
	}
	return ds, commit, func() {}, nil
}

func (s *S3Store) put(ctx context.Context,
	key string,
	data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("put %s: %w",
			key,
			err)
	}
	return nil
}
