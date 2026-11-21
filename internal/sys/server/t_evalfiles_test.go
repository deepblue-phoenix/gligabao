package server

import (
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
)

// TestEvalFilesStore checks which store holds the eval files: the meta bucket when there is one, else the local directory (the
// configured one, or an evals_user directory next to the database).
func TestEvalFilesStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gligabao.db")

	local, err := evalFiles(t.Context(), EvalSettings{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if fs, ok := local.(userfiles.FS); !ok || fs.Dir != filepath.Join(filepath.Dir(dbPath), "evals_user") {
		t.Fatalf("default local store: %#v", local)
	}

	configured, err := evalFiles(t.Context(), EvalSettings{LocalDir: "/data/evals"}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if fs, ok := configured.(userfiles.FS); !ok || fs.Dir != "/data/evals" {
		t.Fatalf("configured local store: %#v", configured)
	}

	bucket, err := evalFiles(t.Context(), EvalSettings{S3: userfiles.S3Config{Bucket: "meta", Prefix: "evals"}, LocalDir: "/unused"}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s3, ok := bucket.(*userfiles.S3)
	if !ok || s3.Location() != "s3://meta/evals" {
		t.Fatalf("bucket store: %#v", bucket)
	}
}
