package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
	gbdb "github.com/deepblue-phoenix/gligabao/internal/sys/errors/db"

	_ "modernc.org/sqlite"
)

func TestInsert(t *testing.T) {
	d, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := gbdb.Open("sqlite", d); err != nil {
		t.Fatal(err)
	}
	sink, err := gbdb.Sink("sqlite", d)
	if err != nil {
		t.Fatal(err)
	}
	e := gberrors.New(context.Background(), sink, "geo.index", errors.New("x"), gberrors.Meta{"dataset": "d1"})
	if e.ID == 0 {
		t.Fatal("no id")
	}
	var op, meta string
	if err := d.QueryRow(`SELECT op, meta FROM gb_errors WHERE id = ?`, e.ID).Scan(&op, &meta); err != nil || op != "geo.index" || meta != `{"dataset":"d1"}` {
		t.Fatalf("%q %q %v", op, meta, err)
	}
}
