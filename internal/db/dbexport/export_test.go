package dbexport

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

const dims = 3

func mustExec(t *testing.T,
	d *sql.DB,
	q string,
	args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v",
			q,
			err)
	}
}

// fill puts a little of everything into the databases: a dataset with a text, its chunk, entities, a fact, a place, an image, and
// the vectors of the entities and of the fact.
func fill(t *testing.T,
	d *sql.DB,
	vs vector.Store,
	imageDir string) {
	t.Helper()
	const ts = "2025-03-01T10:00:00Z"
	mustExec(t,
		d,
		`INSERT INTO gb_datasets (user_id, name, description, tags, created_at) VALUES (?, ?, ?, ?, ?)`,
		"u1",
		"main",
		"unicode: é ✓ \"quoted\"",
		`["a","b"]`,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_artifacts (dataset_id, kind, text, session_id, valid_at, created_at) VALUES (1, 'knowledge', ?, 'research-1', ?, ?)`,
		"Acme was founded in Paris in 1999.\nSecond line.",
		ts,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_artifacts (dataset_id, kind, text, parent_id, chunk_index, start_offset, end_offset, tokens, valid_at, created_at) VALUES (1, 'knowledge', 'Acme was', 1, 0, 0, 8, 2, ?, ?)`,
		ts,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_entities (dataset_id, name, name_key, type, description, created_at, updated_at) VALUES (1, 'Acme', 'acme', 'org', 'a company', ?, ?)`,
		ts,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_entities (dataset_id, name, name_key, type, description, created_at, updated_at) VALUES (1, 'Paris', 'paris', 'place', '', ?, ?)`,
		ts,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_edges (dataset_id, source_id, target_id, relation, description, valid_at, created_at, artifact_id) VALUES (1, 1, 2, 'founded_in', 'Acme was founded in Paris', ?, ?, 2)`,
		ts,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_edge_artifacts (edge_id, artifact_id) VALUES (1, 2)`)
	mustExec(t,
		d,
		`INSERT INTO gb_entity_geo (entity_id, kind, latitude, longitude, provider, created_at, updated_at) VALUES (2, 'city', 48.85, 2.35, 'test', ?, ?)`,
		ts,
		ts)
	mustExec(t,
		d,
		`INSERT INTO gb_image_artifacts (dataset_id, media_type, sha256, size_bytes, path, valid_at, created_at) VALUES (1, 'image/png', ?, 4, '1/img.png', ?, ?)`,
		strings.Repeat("a", 64),
		ts,
		ts)

	if err := os.MkdirAll(filepath.Join(imageDir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir,
		"1",
		"img.png"),
		[]byte{0x89, 'P', 'N', 'G'},
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir,
		"1",
		".tmp-123"),
		[]byte("half written"),
		0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := vs.Upsert(ctx,
		"dataset-1-entities",
		[]vector.Vector{
			{
				ID:       "entity:1",
				Values:   []float32{1, 0, 0},
				Metadata: map[string]any{"kind": "entity", "entity_id": float64(1)},
			},
			{
				ID:       "entity:2",
				Values:   []float32{0, 1, 0},
				Metadata: map[string]any{"kind": "entity", "entity_id": float64(2)},
			},
		}); err != nil {
		t.Fatal(err)
	}
	if err := vs.Upsert(ctx,
		"dataset-1-edges",
		[]vector.Vector{{
			ID:       "edge:1",
			Values:   []float32{0, 0, 1},
			Metadata: map[string]any{"edge_id": float64(1)},
		}}); err != nil {
		t.Fatal(err)
	}
	if err := vs.Upsert(ctx,
		"something-else",
		[]vector.Vector{{
			ID:     "s",
			Values: []float32{1, 1, 1},
		}}); err != nil {
		t.Fatal(err)
	}
}

func tableNames(m *Manifest) []string {
	var out []string
	for _, tb := range m.Tables {
		out = append(out, tb.Name)
	}
	return out
}

func indexOf(list []string,
	s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

func TestWriteAndRead(t *testing.T) {
	{
		d := dbtest.OpenSQLite(t)
		ctx := context.Background()
		const backend = config.DBBackendSQLite
		if ok, err := graph.EnableGeoIndex(ctx, d); err != nil || !ok {
			t.Fatalf("geo index: %v %v",
				ok,
				err)
		}
		vs, err := vector.NewChromem(filepath.Join(t.TempDir(), "vectors"), dims)
		if err != nil {
			t.Fatal(err)
		}
		imageDir := filepath.Join(t.TempDir(), "images")
		fill(t,
			d,
			vs,
			imageDir)
		src := Source{
			Relational:          d,
			Backend:             backend,
			Vector:              vs,
			VectorStore:         config.VectorStoreChromem,
			EmbeddingDimensions: dims,
			ImageDir:            imageDir,
		}

		path := filepath.Join(t.TempDir(),
			"out",
			"export.zip")
		m, err := WriteFile(ctx,
			path,
			src,
			Options{
				Source:  "test",
				Version: "9.9.9",
				Extra: []Extra{{
					Name: "trajectory.json",
					Data: []byte(`{"a":1}`),
				}},
			})
		if err != nil {
			t.Fatal(err)
		}
		if entries, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*")); len(entries) != 1 {
			t.Errorf("the temporary file was left behind: %v", entries)
		}
		r, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if !reflect.DeepEqual(r.Manifest, *m) && mustJSON(r.Manifest) != mustJSON(*m) {
			t.Errorf("the manifest in the archive differs from the returned one:\n%s\n%s",
				mustJSON(r.Manifest),
				mustJSON(*m))
		}
		if m.Format != Format || m.FormatVersion != FormatVersion || m.Backend != backend || m.EmbeddingDimensions != dims || m.Source != "test" || m.Version != "9.9.9" {
			t.Errorf("manifest: %s", mustJSON(m))
		}

		// Tables: ours only (no R*Tree, no PostGIS), the empty ones too, parents first.
		names := tableNames(m)
		for _, n := range names {
			if !strings.HasPrefix(n, "gb_") || strings.Contains(n, "rtree") {
				t.Errorf("table %s must not be exported", n)
			}
		}
		for _, want := range []string{"gb_datasets", "gb_artifacts", "gb_entities", "gb_edges", "gb_edge_artifacts", "gb_entity_geo", "gb_image_artifacts", "gb_models", "gb_inferences", "gb_remember_runs"} {
			if indexOf(names, want) < 0 {
				t.Errorf("table %s is missing from %v",
					want,
					names)
			}
		}
		for _, parent := range [][2]string{{"gb_datasets", "gb_artifacts"}, {"gb_datasets", "gb_entities"}, {"gb_entities", "gb_edges"}, {"gb_edges", "gb_edge_artifacts"}, {"gb_artifacts", "gb_edge_artifacts"}, {"gb_entities", "gb_entity_geo"}} {
			if indexOf(names, parent[0]) > indexOf(names, parent[1]) {
				t.Errorf("%s must come before %s: %v",
					parent[0],
					parent[1],
					names)
			}
		}
		edges, _ := r.Table("gb_edges")
		if edges.Rows != 1 || !reflect.DeepEqual(edges.PrimaryKey, []string{"id"}) || edges.File != "tables/gb_edges.jsonl" {
			t.Errorf("gb_edges: %+v", edges)
		}
		wantFK := map[string]string{"dataset_id": "gb_datasets", "source_id": "gb_entities", "target_id": "gb_entities", "artifact_id": "gb_artifacts"}
		for _, fk := range edges.ForeignKeys {
			if len(fk.Columns) == 1 && wantFK[fk.Columns[0]] == fk.RefTable && len(fk.RefColumns) == 1 && fk.RefColumns[0] == "id" {
				delete(wantFK, fk.Columns[0])
			}
		}
		if delete(wantFK, "model_id"); len(wantFK) != 0 {
			t.Errorf("foreign keys of gb_edges not found: %v in %+v",
				wantFK,
				edges.ForeignKeys)
		}
		if pk, _ := r.Table("gb_edge_artifacts"); !reflect.DeepEqual(pk.PrimaryKey, []string{"edge_id", "artifact_id"}) {
			t.Errorf("composite key: %+v", pk.PrimaryKey)
		}
		if empty, _ := r.Table("gb_series"); empty.Rows != 0 || len(empty.Columns) == 0 {
			t.Errorf("an empty table is still described: %+v", empty)
		}

		// Rows: in primary key order, with the types of JSON.
		var arts []Row
		if err := r.Rows("gb_artifacts", func(row Row) error { arts = append(arts, row); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(arts) != 2 || arts[0]["id"] != json.Number("1") || arts[1]["parent_id"] != json.Number("1") || arts[0]["parent_id"] != nil {
			t.Fatalf("artifacts: %v", arts)
		}
		if arts[0]["text"] != "Acme was founded in Paris in 1999.\nSecond line." || arts[0]["valid_at"] != "2025-03-01T10:00:00Z" || arts[0]["session_id"] != "research-1" {
			t.Errorf("artifact values: %v", arts[0])
		}
		var geo []Row
		r.Rows("gb_entity_geo", func(row Row) error { geo = append(geo, row); return nil })
		if len(geo) != 1 || geo[0]["latitude"] != json.Number("48.85") || geo[0]["bbox_south"] != nil {
			t.Errorf("geo: %v", geo)
		}
		var ds []Row
		r.Rows("gb_datasets", func(row Row) error { ds = append(ds, row); return nil })
		if len(ds) != 1 || ds[0]["description"] != `unicode: é ✓ "quoted"` || ds[0]["user_id"] != "u1" {
			t.Errorf("dataset: %v", ds)
		}

		// Vectors: every dataset, with the embeddings and the metadata, and the link to the gligabao dataset.
		if len(m.Vectors) != 3 {
			t.Fatalf("vector datasets: %s", mustJSON(m.Vectors))
		}
		var ents []vector.Vector
		if err := r.Vectors("dataset-1-entities", func(v vector.Vector) error { ents = append(ents, v); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(ents) != 2 || ents[0].ID != "entity:1" || !reflect.DeepEqual(ents[1].Values, []float32{0, 1, 0}) || ents[1].Metadata["entity_id"] != float64(2) {
			t.Errorf("entity vectors: %+v", ents)
		}
		for _, vd := range m.Vectors {
			switch vd.Name {
			case "dataset-1-edges":
				if vd.DatasetID == nil || *vd.DatasetID != 1 || vd.Index != "edges" || vd.Count != 1 {
					t.Errorf("%+v", vd)
				}
			case "something-else":
				if vd.DatasetID != nil || vd.Index != "" {
					t.Errorf("%+v", vd)
				}
			}
		}
		if err := r.Vectors("nope", func(vector.Vector) error { return nil }); err == nil {
			t.Error("unknown vector dataset")
		}

		// Images and extra files.
		var imgs []string
		if err := r.Images(func(p string,
			c io.Reader) error {
			b, _ := io.ReadAll(c)
			imgs = append(imgs, fmt.Sprintf("%s:%q",
				p,
				b))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(imgs, []string{"1/img.png:\"\\x89PNG\""}) {
			t.Errorf("images (the partial file must be left out): %q", imgs)
		}
		if b, err := r.Extra("trajectory.json"); err != nil || string(b) != `{"a":1}` {
			t.Errorf("extra: %q, %v",
				b,
				err)
		}
		if _, err := r.Extra("nope"); err == nil {
			t.Error("unknown extra file")
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// What the archive is for: its rows go into another database as they are, in the order of the manifest, and the vectors into another store.
func TestLoadIntoAnotherDatabase(t *testing.T) {
	ctx := context.Background()
	{
		d := dbtest.OpenSQLite(t)
		const backend = config.DBBackendSQLite
		vs, _ := vector.NewChromem(filepath.Join(t.TempDir(), "vectors"), dims)
		imageDir := t.TempDir()
		fill(t,
			d,
			vs,
			imageDir)
		var buf bytes.Buffer
		if _, err := Write(ctx,
			&buf,
			Source{
				Relational:          d,
				Backend:             backend,
				Vector:              vs,
				EmbeddingDimensions: dims,
				ImageDir:            imageDir,
			},
			Options{}); err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}

		// A second, empty database of the same kind (the package routes to the last one opened: the first is only read from here on).
		target := dbtest.OpenSQLite(t)
		for _, tb := range r.Manifest.Tables {
			cols := make([]string, len(tb.Columns))
			marks := make([]string, len(tb.Columns))
			for i, c := range tb.Columns {
				cols[i], marks[i] = quote(c.Name), "?"
			}
			insert := (`INSERT INTO ` + quote(tb.Name) + ` (` + strings.Join(cols, ", ") + `) VALUES (` + strings.Join(marks, ", ") + `) ON CONFLICT DO NOTHING`)
			err := r.Rows(tb.Name, func(row Row) error {
				args := make([]any, len(tb.Columns))
				for i, c := range tb.Columns {
					if n, ok := row[c.Name].(json.Number); ok {
						args[i] = n.String()
					} else {
						args[i] = row[c.Name]
					}
				}
				_, err := target.Exec(insert, args...)
				return err
			})
			if err != nil {
				t.Fatalf("loading %s: %v",
					tb.Name,
					err)
			}
		}
		for _, tb := range r.Manifest.Tables {
			var n int64
			if err := target.QueryRow(`SELECT COUNT(*) FROM ` + quote(tb.Name)).Scan(&n); err != nil {
				t.Fatal(err)
			}
			// gb_models is filled when a database is opened: the loaded rows replace, or are, those.
			if n < tb.Rows || (tb.Name != "gb_models" && n != tb.Rows) {
				t.Errorf("%s: %d rows loaded, %d in the archive",
					tb.Name,
					n,
					tb.Rows)
			}
		}

		target2, _ := vector.NewChromem(filepath.Join(t.TempDir(), "vectors2"), dims)
		for _, vd := range r.Manifest.Vectors {
			if err := r.Vectors(vd.Name, func(v vector.Vector) error {
				return target2.Upsert(ctx,
					vd.Name,
					[]vector.Vector{v})
			}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := target2.Query(ctx,
			"dataset-1-entities",
			[]float32{0, 1, 0},
			vector.QueryOptions{
				TopK: 1,
			})
		if err != nil || len(got) != 1 || got[0].ID != "entity:2" {
			t.Errorf("query of the loaded vectors: %+v, %v",
				got,
				err)
		}
	}
}

func TestWriteErrors(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenSQLite(t)
	vs, _ := vector.NewChromem(":memory:", dims)
	src := Source{
		Relational:          d,
		Backend:             config.DBBackendSQLite,
		Vector:              vs,
		EmbeddingDimensions: dims,
	}

	for _, name := range []string{"", "/abs", "../up", "a/../b", "a//b"} {
		if _, err := Write(ctx,
			io.Discard,
			src,
			Options{
				Extra: []Extra{{
					Name: name,
				}},
			}); err == nil {
			t.Errorf("extra file name %q accepted", name)
		}
	}
	if _, err := Write(ctx,
		io.Discard,
		Source{
			Backend: config.DBBackendSQLite,
			Vector:  vs,
		},
		Options{}); err == nil {
		t.Error("a SQLite source without a database accepted")
	}

	// A failed export leaves no file.
	path := filepath.Join(t.TempDir(), "x.zip")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := WriteFile(cancelled,
		path,
		src,
		Options{}); err == nil {
		t.Error("a done context should fail the export")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a failed export left a file")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 0 {
		t.Errorf("a failed export left %v", entries)
	}
}

// Only what is local is exported: a remote database and a remote vector store persist their data themselves.
func TestRemoteStoresAreSkipped(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenSQLite(t)
	vs, _ := vector.NewChromem(filepath.Join(t.TempDir(), "vectors"), dims)
	imageDir := t.TempDir()
	fill(t,
		d,
		vs,
		imageDir)
	remote := struct{ vector.Store }{vs} // a store that is not an Exporter, like Pinecone

	for name, tc := range map[string]struct {
		src     Source
		skipped []string
		tables  bool
		vectors bool
	}{
		"local": {Source{
			Relational: d,
			Backend:    config.DBBackendSQLite,
			Vector:     vs,
		}, nil, true, true},
		"remote database": {Source{
			Backend: config.DBBackendPostgres,
			Vector:  vs,
		}, []string{"tables"}, false, true},
		"remote vectors": {Source{
			Relational:  d,
			Backend:     config.DBBackendSQLite,
			Vector:      remote,
			VectorStore: config.VectorStorePinecone,
		}, []string{"vectors"}, true, false},
		"both remote": {Source{
			Backend:     config.DBBackendPostgres,
			Vector:      remote,
			VectorStore: config.VectorStorePinecone,
		}, []string{"tables", "vectors"}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			tc.src.ImageDir = imageDir
			path := filepath.Join(t.TempDir(), "x.zip")
			if _, err := WriteFile(ctx,
				path,
				tc.src,
				Options{
					Extra: []Extra{{
						Name: "t.json",
						Data: []byte("{}"),
					}},
				}); err != nil {
				t.Fatal(err)
			}
			r, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			m := r.Manifest
			var skipped []string
			for _, sk := range m.Skipped {
				skipped = append(skipped, sk.What)
				if !strings.Contains(sk.Reason, "remote") {
					t.Errorf("reason: %q", sk.Reason)
				}
			}
			if !reflect.DeepEqual(skipped, tc.skipped) || (len(m.Tables) > 0) != tc.tables || (len(m.Vectors) > 0) != tc.vectors {
				t.Errorf("skipped %v, %d tables, %d vector datasets",
					skipped,
					len(m.Tables),
					len(m.Vectors))
			}
			// The images and the extra files are local files: always in.
			if len(m.Images) != 1 || len(m.Extra) != 1 {
				t.Errorf("images %d, extra %v",
					len(m.Images),
					m.Extra)
			}
			if b := mustJSON(m); !strings.Contains(b, `"skipped":[`) || strings.Contains(b, `"tables":null`) || strings.Contains(b, `"vectors":null`) {
				t.Errorf("empty lists must be [] in the manifest: %s", b)
			}
		})
	}
}

func TestReaderErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.zip")); err == nil {
		t.Error("missing file")
	}
	if _, err := NewReader(strings.NewReader("not a zip"), 9); err == nil {
		t.Error("not a zip")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Create("other.txt")
	zw.Close()
	if _, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil || !strings.Contains(err.Error(), ManifestFile) {
		t.Errorf("no manifest: %v", err)
	}
	for name, manifest := range map[string]string{
		"other format": `{"format":"x","format_version":1}`,
		"newer format": `{"format":"gligabao-export","format_version":99}`,
	} {
		buf.Reset()
		zw = zip.NewWriter(&buf)
		w, _ := zw.Create(ManifestFile)
		w.Write([]byte(manifest))
		zw.Close()
		if _, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSortTables(t *testing.T) {
	fk := func(ref string) []ForeignKey {
		return []ForeignKey{{
			Columns:    []string{"x"},
			RefTable:   ref,
			RefColumns: []string{"id"},
		}}
	}
	got := tableNames(&Manifest{
		Tables: sortTables([]Table{
			{
				Name:        "c",
				ForeignKeys: fk("b"),
			},
			{
				Name:        "b",
				ForeignKeys: append(fk("a"), fk("b")...),
			}, // and itself
			{
				Name: "a",
			},
			{
				Name:        "z",
				ForeignKeys: fk("outside"),
			}, // a table that is not exported
			{
				Name:        "p",
				ForeignKeys: fk("q"),
			}, // a cycle
			{
				Name:        "q",
				ForeignKeys: fk("p"),
			},
		}),
	})
	if want := []string{"a", "z", "b", "c", "p", "q"}; !reflect.DeepEqual(got, want) {
		// "a" and "z" are ready first, then "b", then "c", and the cycle is broken by name.
		t.Errorf("got %v, want %v",
			got,
			want)
	}
}

func TestEncodeValue(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, `null`}, {int64(-7), `-7`}, {1.5, `1.5`}, {"é\n", `"é\n"`}, {true, `true`}, {[]byte("text"), `"text"`},
		{[]byte{0xff, 0x00}, `{"$base64":"/wA="}`},
	} {
		if b, err := encodeValue(tc.in); err != nil || string(b) != tc.want {
			t.Errorf("%#v: %s, %v (want %s)",
				tc.in,
				b,
				err,
				tc.want)
		}
	}
}
