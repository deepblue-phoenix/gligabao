package memory_image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestImageStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "images") // does not exist yet
	data := []byte("fake png")
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])

	rel, created, err := saveFile(dir, 7, "image/png", h, data)
	if err != nil || !created || rel != "7/"+h+".png" {
		t.Fatalf("saveFile: %q, %v, %v", rel, created, err)
	}
	if got, err := readFile(dir, rel); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("readFile: %q, %v", got, err)
	}
	if rel2, created, err := saveFile(dir, 7, "image/png", h, data); err != nil || created || rel2 != rel {
		t.Errorf("second saveFile: %q, %v, %v", rel2, created, err)
	}
	if rel3, created, _ := saveFile(dir, 8, "image/png", h, data); !created || rel3 == rel {
		t.Errorf("another dataset shares the file: %q", rel3)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "7", ".tmp-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}

	if err := removeFile(dir, rel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Errorf("not removed: %v", err)
	}
	if err := removeFile(dir, rel); err != nil {
		t.Errorf("removing twice: %v", err)
	}
}

func TestRejects(t *testing.T) {
	dir := t.TempDir()
	h := hex.EncodeToString(make([]byte, 32))
	if _, _, err := saveFile(dir, 1, "text/plain", h, nil); err == nil {
		t.Error("bad media type accepted")
	}
	if _, _, err := saveFile(dir, 1, "image/png", "../../x", nil); err == nil {
		t.Error("bad hash accepted")
	}
	for _, p := range []string{"", "../x", "/etc/passwd", "1/../../x", "a//b"} {
		if _, err := readFile(dir, p); err == nil || os.IsNotExist(err) {
			t.Errorf("readFile(%q): %v", p, err)
		}
	}
}
