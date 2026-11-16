package imagestore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FS keeps the images as files in a directory: <dir>/<dataset id>/<sha256>.<ext>. The directory is created with the
// first image. Dataset ids are unique, so the files of different users never meet; there is no directory per user and
// the owner argument is not used (use S3 for per-user directories).
type FS struct {
	dir string
}

var _ Store = (*FS)(nil)

// NewFS returns the store of the directory dir.
func NewFS(dir string) *FS {
	return &FS{
		dir: dir,
	}
}

// Dir is the directory of the store.
func (f *FS) Dir() string {
	return f.dir
}

// Location implements Store.
func (f *FS) Location() string {
	return f.dir
}

func (f *FS) full(rel string) (string, error) {
	clean, err := cleanPath(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(f.dir, filepath.FromSlash(clean)), nil
}

// Put implements Store. The file appears atomically (written to a temporary file, then renamed).
func (f *FS) Put(ctx context.Context,
	_,
	rel string,
	data []byte) (bool, error) {
	full, err := f.full(rel)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(full); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, fmt.Errorf("image store: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return false, fmt.Errorf("image store: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, fmt.Errorf("image store: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("image store: write: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return false, fmt.Errorf("image store: %w", err)
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return false, fmt.Errorf("image store: %w", err)
	}
	return true, nil
}

// Read implements Store.
func (f *FS) Read(ctx context.Context,
	_,
	rel string) ([]byte, error) {
	full, err := f.full(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s (%w)",
			ErrNotFound,
			rel,
			err)
	}
	return data, err
}

// Remove implements Store.
func (f *FS) Remove(ctx context.Context,
	_,
	rel string) error {
	full, err := f.full(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List implements Store. Files being written (".tmp-*") are not listed.
func (f *FS) List(ctx context.Context) ([]Object, error) {
	if fi, err := os.Stat(f.dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: directory %s", ErrNotFound, f.dir)
	}
	var out []Object
	walkErr := filepath.WalkDir(f.dir, func(p string,
		e fs.DirEntry,
		err error) error {
		if err != nil || !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".tmp-") {
			return nil // unreadable entries are skipped
		}
		rel, err := filepath.Rel(f.dir, p)
		if err != nil {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		out = append(out, Object{
			Path: filepath.ToSlash(rel),
			Size: fi.Size(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("image store: %w", walkErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
