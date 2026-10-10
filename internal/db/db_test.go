package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/config"
)

func TestOpenSQLiteChromem(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Config{SQLitePath: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Relational == nil || d.Graph == nil || d.Vector == nil {
		t.Fatalf("missing client: %+v", d)
	}
	if d.Backend != config.DBBackendSQLite || d.VectorStore != config.VectorStoreChromem ||
		d.EmbeddingDimensions != config.DefaultEmbeddingDimensions {
		t.Fatalf("defaults not applied: %s", d)
	}
	if err := d.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(Config{Backend: "mysql"}); err == nil {
		t.Error("unknown backend accepted")
	}
	if _, err := Open(Config{SQLitePath: filepath.Join(t.TempDir(), "t.db"), VectorStore: "nope"}); err == nil {
		t.Error("unknown vector store accepted")
	}
	var nilDB *DB
	if err := nilDB.Close(); err != nil {
		t.Error(err)
	}
}
