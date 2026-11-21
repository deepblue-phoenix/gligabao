package userfiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/sys/userpath"
)

// FS stores the files in a local directory: "<Dir>/<user segment>/<relPath>".
type FS struct {
	Dir string
}

var _ Store = FS{}

// userDir is the directory of the user, after the checks that it (and the root) do not leave Dir through symlinks.
func (f FS) userDir(user string) (string, error) {
	seg, err := userpath.Segment(user)
	if err != nil {
		return "", fmt.Errorf("userfiles: %w", err)
	}
	if strings.TrimSpace(f.Dir) == "" {
		return "", errors.New("userfiles: no directory configured")
	}
	dir := filepath.Join(f.Dir, seg)
	if _, err := resolveWithin(f.Dir, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// pathOf is the local path of relPath of the user, checked to stay inside the directory of the user (symlinks resolved).
func (f FS) pathOf(user,
	relPath string) (string, error) {
	if err := ValidateRel(relPath); err != nil {
		return "", err
	}
	dir, err := f.userDir(user)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, filepath.FromSlash(relPath))
	if _, err := resolveWithin(dir, p); err != nil {
		return "", err
	}
	return p, nil
}

// Put implements Store: the file is written to a temporary one next to it, then renamed, so a reader never sees half of it.
func (f FS) Put(_ context.Context,
	user,
	relPath string,
	data []byte) error {
	p, err := f.pathOf(user, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("userfiles: %w", err)
	}
	// the directory now exists: check again, since MkdirAll follows symlinks
	if _, err := resolveWithin(filepath.Dir(p), p); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return fmt.Errorf("userfiles: %w", err)
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("userfiles: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("userfiles: %w", err)
	}
	return nil
}

// Get implements Store.
func (f FS) Get(_ context.Context,
	user,
	relPath string) ([]byte, error) {
	p, err := f.pathOf(user, relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if isMissing(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("userfiles: %w", err)
	}
	return data, nil
}

// Delete implements Store.
func (f FS) Delete(_ context.Context,
	user,
	relPath string) error {
	p, err := f.pathOf(user, relPath)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(p); err != nil || info.IsDir() {
		return ErrNotFound
	}
	if err := os.Remove(p); err != nil {
		if isMissing(err) {
			return ErrNotFound
		}
		return fmt.Errorf("userfiles: %w", err)
	}
	return nil
}

// List implements Store. Symlinks and temporary files are not listed.
func (f FS) List(_ context.Context,
	user,
	prefix string) ([]string, error) {
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	dir, err := f.userDir(user)
	if err != nil {
		return nil, err
	}
	out := []string{}
	walkErr := filepath.WalkDir(dir, func(p string,
		d fs.DirEntry,
		err error) error {
		switch {
		case err != nil:
			return err
		case d.Type()&fs.ModeSymlink != 0 || d.IsDir():
			return nil
		case strings.HasPrefix(d.Name(), ".tmp-"):
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, prefix) {
			out = append(out, rel)
		}
		return nil
	})
	if walkErr != nil {
		if isMissing(walkErr) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("userfiles: %w", walkErr)
	}
	sort.Strings(out)
	return out, nil
}

// isMissing says whether err means "no such file" (including a path through a regular file).
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), "not a directory")
}

// resolveWithin resolves the symlinks of the existing part of target (and of base) and fails if target ends up outside base.
// The part that does not exist yet is appended as is: it cannot be a link. A link that dangles is refused.
func resolveWithin(base,
	target string) (string, error) {
	rb, err := resolveExisting(base)
	if err != nil {
		return "", err
	}
	rt, err := resolveExisting(target)
	if err != nil {
		return "", err
	}
	if rt != rb && !strings.HasPrefix(rt, rb+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q leaves its directory",
			ErrBadPath,
			target)
	}
	return rt, nil
}

// resolveExisting is filepath.EvalSymlinks on the longest existing prefix of p, plus the rest of p.
func resolveExisting(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("userfiles: %w", err)
	}
	if _, lerr := os.Lstat(abs); lerr != nil {
		if !isMissing(lerr) {
			return "", fmt.Errorf("userfiles: %w", lerr)
		}
		parent, err := resolveExisting(filepath.Dir(abs))
		if err != nil {
			return "", err
		}
		return filepath.Join(parent, filepath.Base(abs)), nil
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v",
			ErrBadPath,
			p,
			err)
	}
	return resolved, nil
}
