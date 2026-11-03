package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractRepo(t *testing.T) {
	archive := func(names ...string) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, n := range names {
			_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte("x\n"))
		}
		_ = tw.Close()
		_ = gz.Close()
		return buf.Bytes()
	}

	dest := filepath.Join(t.TempDir(), "out.git")
	n, err := extractRepo(archive("main.git/HEAD", "main.git/objects/ab/cdef"), dest)
	if err != nil || n != 2 {
		t.Fatalf("extract: %d, %v", n, err)
	}
	for _, p := range []string{"HEAD", "objects/ab/cdef"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := extractRepo(archive("main.git/HEAD"), dest); err == nil {
		t.Error("extracting into a non-empty directory succeeded")
	}
	if _, err := extractRepo(archive("main.git/../../evil"), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("an unsafe path was extracted")
	}
	if _, err := extractRepo([]byte("not gzip"), filepath.Join(t.TempDir(), "y")); err == nil {
		t.Error("garbage was extracted")
	}
}
