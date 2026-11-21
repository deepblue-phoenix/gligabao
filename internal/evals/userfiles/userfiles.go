// Package userfiles keeps the files of the user evals: the eval JSON files ("<name>.json") and the corpus files that the cases name
// with text_file, in one directory per user. There are two stores: S3 (the meta bucket, subdirectory "evals") and FS (a local
// directory, for local runs and tests). Every key or path is built here from the user (one encoded path segment, see userpath) and a
// validated relative path; nothing from a request becomes a key unchecked.
//
// Default is the store of the process (set once at startup, like atifstore.Default).
package userfiles

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxPathLen is the longest relative path.
const MaxPathLen = 200

// Store is where the files of the users are kept. relPath is relative to the directory of the user, e.g. "my_eval.json" or
// "corpus/doc.txt". It is safe for concurrent use.
type Store interface {
	// Put writes (creates or replaces) a file.
	Put(ctx context.Context,
		user,
		relPath string,
		data []byte) error
	// Get reads a file; ErrNotFound when it does not exist.
	Get(ctx context.Context,
		user,
		relPath string) ([]byte, error)
	// Delete removes a file; ErrNotFound when it does not exist.
	Delete(ctx context.Context,
		user,
		relPath string) error
	// List returns the relative paths (sorted) of the files of the user whose path starts with prefix ("" lists all).
	List(ctx context.Context,
		user,
		prefix string) ([]string, error)
}

var (
	// ErrNotFound is the error of a missing file.
	ErrNotFound = errors.New("userfiles: file not found")
	// ErrBadPath is the error of a relative path that is not valid (see ValidateRel).
	ErrBadPath = errors.New("userfiles: invalid path")

	// Default is the store the server uses. Set it once at startup; nil means no store was configured.
	Default Store

	relRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
)

// ValidateRel checks a relative path: at most MaxPathLen bytes, segments separated by single "/", each starting with a letter or a
// digit and made of letters, digits, ".", "_" and "-"; so no leading "/", no "//", no "." or ".." segment, and no "..".
func ValidateRel(rel string) error {
	switch {
	case rel == "" || len(rel) > MaxPathLen:
		return fmt.Errorf("%w: length must be 1-%d bytes",
			ErrBadPath,
			MaxPathLen)
	case strings.Contains(rel, ".."):
		return fmt.Errorf("%w: %q contains \"..\"",
			ErrBadPath,
			rel)
	case !relRe.MatchString(rel):
		return fmt.Errorf("%w: %q (use letters, digits, . _ - and / between segments)",
			ErrBadPath,
			rel)
	}
	return nil
}

// validatePrefix checks a List prefix: empty, or a valid relative path with an optional trailing "/".
func validatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	return ValidateRel(strings.TrimSuffix(prefix, "/"))
}
