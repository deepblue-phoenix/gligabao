package datasets_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/datasets"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

// TestGitExport remembers two texts, the second contradicting the first, and exports the dataset as a git repository through the API.
func TestGitExport(t *testing.T) { dbtest.Run(t, testGitExport) }

func testGitExport(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ds, err := relational.CreateDataset(ctx,
		d,
		userID,
		"gitds",
		"for the git export",
		[]string{"x"})
	must(err)

	t1 := time.Date(2025,
		3,
		1,
		9,
		0,
		0,
		0,
		time.UTC)
	t2 := t1.Add(48 * time.Hour)
	tx, err := graph.Begin(ctx, d)
	must(err)
	defer tx.Rollback()
	a1, err := tx.InsertArtifact(ctx,
		ds.ID,
		graph.ArtifactEpisode,
		"Alice works at Acme.",
		"s1",
		t1,
		t1)
	must(err)
	alice, err := tx.UpsertEntity(ctx,
		ds.ID,
		graph.Entity{
			Name: "Alice",
			Type: "person",
		},
		"alice")
	must(err)
	acme, err := tx.UpsertEntity(ctx,
		ds.ID,
		graph.Entity{
			Name: "Acme",
			Type: "organization",
		},
		"acme")
	must(err)
	must(tx.LinkEntity(ctx,
		alice,
		a1))
	must(tx.LinkEntity(ctx,
		acme,
		a1))
	e1, err := tx.InsertEdge(ctx, graph.NewEdge{
		DatasetID:   ds.ID,
		SourceID:    alice,
		TargetID:    acme,
		Relation:    "works_at",
		Description: "Alice works at Acme.",
		ValidAt:     t1,
		CreatedAt:   t1,
		ArtifactID:  a1,
	})
	must(err)
	must(tx.LinkEdge(ctx,
		e1,
		a1))
	must(tx.InsertEdgeEvent(ctx, graph.EdgeEvent{
		DatasetID:  ds.ID,
		EdgeID:     e1,
		ArtifactID: a1,
		Event:      graph.EdgeCreated,
		CreatedAt:  t1,
	}))

	a2, err := tx.InsertArtifact(ctx,
		ds.ID,
		graph.ArtifactEpisode,
		"Alice now works at Beta.",
		"",
		t2,
		t2)
	must(err)
	beta, err := tx.UpsertEntity(ctx,
		ds.ID,
		graph.Entity{
			Name: "Beta",
			Type: "organization",
		},
		"beta")
	must(err)
	must(tx.LinkEntity(ctx,
		alice,
		a2))
	must(tx.LinkEntity(ctx,
		beta,
		a2))
	e2, err := tx.InsertEdge(ctx, graph.NewEdge{
		DatasetID:   ds.ID,
		SourceID:    alice,
		TargetID:    beta,
		Relation:    "works_at",
		Description: "Alice works at Beta.",
		ValidAt:     t2,
		CreatedAt:   t2,
		ArtifactID:  a2,
	})
	must(err)
	must(tx.LinkEdge(ctx,
		e2,
		a2))
	must(tx.InvalidateEdge(ctx,
		e1,
		t2,
		t2))
	must(tx.InsertEdgeEvent(ctx, graph.EdgeEvent{
		DatasetID:  ds.ID,
		EdgeID:     e2,
		ArtifactID: a2,
		Event:      graph.EdgeCreated,
		CreatedAt:  t2,
	}))
	must(tx.InsertEdgeEvent(ctx, graph.EdgeEvent{
		DatasetID:       ds.ID,
		EdgeID:          e1,
		ArtifactID:      a2,
		Event:           graph.EdgeInvalidated,
		InvalidAtBefore: nil,
		InvalidAtAfter:  &t2,
		CausedByEdgeID:  e2,
		CreatedAt:       t2,
	}))
	must(tx.Commit())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gb/datasets/{name}/git", datasets.Git(d))
	get := func(path string,
		user string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httputiltest.NewRequest("GET",
			path+"?user_id="+user,
			nil)
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/v1/gb/datasets/nope/git", userID); rec.Code != 404 {
		t.Errorf("unknown dataset: %d", rec.Code)
	}
	if rec := get("/v1/gb/datasets/gitds/git", "someone-else"); rec.Code != 404 {
		t.Errorf("another user's dataset: %d", rec.Code)
	}
	rec := get("/v1/gb/datasets/gitds/git", userID)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("status %d, type %q: %s",
			rec.Code,
			rec.Header().Get("Content-Type"),
			rec.Body)
	}

	// Unpack the repository (the archive's top directory is gitds.git).
	repo := filepath.Join(t.TempDir(), "gitds.git")
	gz, err := gzip.NewReader(rec.Body)
	must(err)
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(err)
		rel := strings.TrimPrefix(h.Name, "gitds.git/")
		if rel == h.Name {
			t.Fatalf("entry %q is not under gitds.git/", h.Name)
		}
		full := filepath.Join(repo, filepath.FromSlash(rel))
		must(os.MkdirAll(filepath.Dir(full), 0o755))
		b, err := io.ReadAll(tr)
		must(err)
		must(os.WriteFile(full,
			b,
			0o644))
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary")
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"--git-dir", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s",
				args,
				err,
				out)
		}
		return string(out)
	}
	run("fsck", "--strict")
	// The root commit is dated at the earliest of the dataset's creation and the first call (here the call, which is in the past).
	if log := run("log",
		"--reverse",
		"--format=%ad|%s",
		"--date=format-local:%Y-%m-%d"); log !=
		"2025-03-01|Create dataset gitds\n2025-03-01|remember #1: Alice works at Acme.\n2025-03-03|remember #2: Alice now works at Beta.\n" {
		t.Errorf("log:\n%s", log)
	}
	if f := run("show", "HEAD:edges/works_at/000001.md"); !strings.Contains(f, "invalid_at: 2025-03-03T09:00:00Z") || !strings.Contains(f, "expired_at: 2025-03-03T09:00:00Z") {
		t.Errorf("closed fact:\n%s", f)
	}
	if f := run("show", "HEAD~1:edges/works_at/000001.md"); strings.Contains(f, "invalid_at") {
		t.Errorf("fact in the first call:\n%s", f)
	}
	run("cat-file",
		"-e",
		"HEAD:entities/organization/beta-3.md")
}
