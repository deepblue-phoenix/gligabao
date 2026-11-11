package investigate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// NotesStore is the small read/write interface the notes live behind, so that they can move (an object store) without touching the loop.
type NotesStore interface {
	Read(path string) (string, error)
	Write(path, text string) error
}

// NotesFile is the notes working copy as files in Dir.
type NotesFile struct {
	Dir string
}

// SanitizeUser makes the user_id of a run fit in a file name: every character outside A-Z a-z 0-9 . _ - becomes "_", the result is cut to 64
// characters, and an empty user becomes "anon". A user_id is any string (docs/identity.md), so "/", "..", spaces and non-ASCII must not
// reach the path. The name is for readability only: the uniqueness comes from the run id and the notes id.
func SanitizeUser(user string) string {
	if user == "" {
		return "anon"
	}
	var b strings.Builder
	n := 0
	for _, r := range user {
		if n == 64 {
			break
		}
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		n++
	}
	return b.String()
}

// Path is the file of the notes of a run: Dir/notes_<user>_<run>_<notes_id>.md. A run made before notes ids (notes_id "") has no notes
// file: Path returns "".
func (f NotesFile) Path(user string, runID int64, notesID string) string {
	if notesID == "" {
		return ""
	}
	return filepath.Join(f.Dir, fmt.Sprintf("notes_%s_%d_%s.md", SanitizeUser(user), runID, notesID))
}

// Read returns the text of the file; a missing file reads as the rendered empty notes.
func (f NotesFile) Read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Render(Notes{}), nil
	}
	if err != nil {
		return "", fmt.Errorf("read notes file: %w", err)
	}
	return string(b), nil
}

// Write writes the text to a temp file in the same directory and renames it over the file, so a reader never sees half a file. The
// directory is created on first write.
func (f NotesFile) Write(path, text string) error {
	if path == "" {
		return errors.New("write notes file: the run has no notes id")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("write notes file: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".notes-*.tmp")
	if err != nil {
		return fmt.Errorf("write notes file: %w", err)
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("write notes file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("write notes file: %w", err)
	}
	return nil
}
