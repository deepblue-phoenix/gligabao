package dbimport

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeFileAtomic writes content to the file at full (making its directory), so that it appears whole: written to a temporary file in
// the same directory, then renamed.
func writeFileAtomic(full string,
	content io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := io.Copy(tmp, content); err != nil {
		tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), full)
}
