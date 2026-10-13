// This file keeps the bytes of image artifacts as files in a directory. The database only tracks them (see
// gb_image_artifacts.path), so images never go into it.
//
// A file is named after the SHA-256 of its content and lives in a directory per dataset:
// <dir>/<dataset id>/<sha256>.<ext>. Storing the same bytes again in a dataset is a no-op that returns the same
// path. Paths handed out (and stored in the database) are relative to dir, with forward slashes, so the directory
// can be moved.

package memory_image

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

var extensions = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// saveFile writes data, of the given media type and hex SHA-256, into dir for the dataset and returns its path relative
// to dir. created is false if the file already existed (same dataset, same content): it is left as it is, and a
// caller undoing its work must not removeFile it. The file appears atomically (written to a temporary file, then
// renamed), so a reader never sees a partial one.
func saveFile(dir string, datasetID int64, mediaType, sha256 string, data []byte) (rel string, created bool, err error) {
	ext, ok := extensions[mediaType]
	if !ok {
		return "", false, fmt.Errorf("image store: unsupported media type %q", mediaType)
	}
	if len(sha256) != 64 || strings.Trim(sha256, "0123456789abcdef") != "" {
		return "", false, fmt.Errorf("image store: bad SHA-256 %q", sha256)
	}
	rel = path.Join(strconv.FormatInt(datasetID, 10), sha256+"."+ext)
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err == nil {
		return rel, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", false, fmt.Errorf("image store: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return "", false, fmt.Errorf("image store: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("image store: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", false, fmt.Errorf("image store: write: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", false, fmt.Errorf("image store: %w", err)
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return "", false, fmt.Errorf("image store: %w", err)
	}
	return rel, true, nil
}

// readFile returns the file at rel (a path returned by saveFile) inside dir. It refuses paths that leave dir.
func readFile(dir, rel string) ([]byte, error) {
	full, err := resolveFile(dir, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// removeFile deletes the file at rel inside dir. A file that is already gone is not an error.
func removeFile(dir, rel string) error {
	full, err := resolveFile(dir, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func resolveFile(dir, rel string) (string, error) {
	clean := path.Clean("/" + filepath.ToSlash(rel))[1:] // rooted first, so ".." cannot climb out
	if clean == "" || clean != filepath.ToSlash(rel) {
		return "", fmt.Errorf("image store: bad path %q", rel)
	}
	return filepath.Join(dir, filepath.FromSlash(clean)), nil
}
