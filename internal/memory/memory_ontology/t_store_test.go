package memory_ontology_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	. "github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology/ontospec"
)

const animals = `
id: animals
description: Animals.
classes:
  Animal:
    properties:
      age: {type: integer}
  Dog:
    subClassOf: Animal
relations:
  chases: {domain: Dog, range: Animal}
`

const animalsV2 = animals + `
  barks_at: {domain: Dog, range: Animal}
`

func TestCreateUpdateGet(t *testing.T) { dbtest.Run(t, testCreateUpdateGet) }

func testCreateUpdateGet(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	meta, err := Create(ctx, d, "u1", "animals", "", "", []byte(animals))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Version != 1 || meta.Format != "yaml" || meta.Classes != 2 || meta.Relations != 1 || meta.Properties != 1 || meta.Description != "Animals." ||
		meta.SizeBytes != len(animals) || len(meta.Spec) != 0 || meta.CreatedAt.IsZero() || !meta.UpdatedAt.Equal(meta.CreatedAt) {
		t.Fatalf("created = %+v", meta)
	}
	if _, err := Create(ctx, d, "u1", "animals", "", "", []byte(animals)); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate name: %v", err)
	}
	if _, err := Create(ctx, d, "u2", "animals", "mine", "yaml", []byte(animals)); err != nil {
		t.Errorf("another user may reuse the name: %v", err)
	}

	// Invalid input is refused and stores nothing; a spec with problems lists them.
	_, err = Create(ctx, d, "u1", "bad", "", "", []byte("classes:\n  A:\n    subClassOf: B"))
	var invalid *ontospec.InvalidError
	if !errors.Is(err, ErrInvalid) || !errors.As(err, &invalid) || len(invalid.Problems) != 1 {
		t.Errorf("a spec with a problem: %v", err)
	}
	for name, content := range map[string]string{"empty": "", "junk": "classes: [", "none": "id: x"} {
		if _, err := Create(ctx, d, "u1", name, "", "", []byte(content)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Create(ctx, d, "u1", "bad name!", "", "", []byte(animals)); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := Create(ctx, d, "u1", "x", "", "toml", []byte(animals)); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad format: %v", err)
	}
	if list, _ := List(ctx, d, "u1"); len(list) != 1 {
		t.Fatalf("u1 has %d ontologies after failures, want 1", len(list))
	}

	// An update is the next version; the old document stays in the history; the owner, name and creation time stay.
	up, err := Update(ctx, d, "u1", "animals", "", "json", []byte(`{"classes":{"Cat":{}}}`))
	if err != nil || up.Version != 2 || up.Format != "json" || up.Classes != 1 || up.Description != "Animals." || up.ID != meta.ID || !up.CreatedAt.Equal(meta.CreatedAt) {
		t.Fatalf("updated = %+v, %v", up, err)
	}
	if up, err = Update(ctx, d, "u1", "animals", "new", "", []byte(animalsV2)); err != nil || up.Version != 3 || up.Description != "new" || up.Relations != 2 {
		t.Fatalf("updated again = %+v, %v", up, err)
	}
	if _, err := Update(ctx, d, "u1", "animals", "", "", []byte("classes: [")); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid update: %v", err)
	}
	if _, err := Update(ctx, d, "u1", "missing", "", "", []byte(animals)); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing ontology: %v", err)
	}
	if _, err := Update(ctx, d, "u3", "animals", "", "", []byte(animals)); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of another user's ontology: %v", err)
	}
	got, err := Get(ctx, d, "u1", "animals")
	if err != nil || got.Version != 3 || !strings.Contains(string(got.Spec), "barks_at") {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := Get(ctx, d, "u3", "animals"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	var versions int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_ontology_versions WHERE ontology_id = `+fmt.Sprint(meta.ID)).Scan(&versions); err != nil || versions != 3 {
		t.Errorf("%d versions in the history (%v), want 3", versions, err)
	}
	var content string
	if err := d.QueryRowContext(ctx, `SELECT content FROM gb_ontology_versions WHERE ontology_id = `+fmt.Sprint(meta.ID)+` AND version = 1`).Scan(&content); err != nil || content != animals {
		t.Errorf("version 1 content = %q (%v)", content, err)
	}
	list, _ := List(ctx, d, "u1")
	if len(list) != 1 || len(list[0].Spec) != 0 {
		t.Errorf("list = %+v", list)
	}
	// Deleting an ontology deletes its history.
	if err := relational.DeleteOntology(ctx, d, meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_ontology_versions`).Scan(&versions); err != nil || versions != 1 {
		t.Errorf("deleting the ontology left %d versions (%v), want only the other user's one", versions, err)
	}
}

func TestResolve(t *testing.T) { dbtest.Run(t, testResolve) }

func testResolve(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	if _, err := Resolve(ctx, d, "u1", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user with no ontologies: %v", err)
	}
	mustCreate := func(name,
		doc string) {
		t.Helper()
		if _, err := Create(ctx, d, "u1", name, "", "", []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate("animals", animals)
	mustCreate("zoo", "classes:\n  Keeper: {}\n  Animal:\n    properties:\n      age: {type: integer}\n  Dog:\n    subClassOf: Animal\nrelations:\n  keeps: {domain: Keeper, range: Animal}")
	mustCreate("clash", "classes:\n  Dog:\n    description: another dog")

	reg, err := Resolve(ctx, d, "u1", []string{"animals"})
	if err != nil || len(reg.Classes()) != 2 || reg.Refs[0].Name != "animals" || reg.Refs[0].Version != 1 || reg.Refs[0].ID == 0 {
		t.Fatalf("one ontology: %v %v", reg, err)
	}
	reg, err = Resolve(ctx, d, "u1", []string{"animals", "zoo", "animals"})
	if err != nil || len(reg.Classes()) != 3 || len(reg.Refs) != 2 || reg.ClassRef("Keeper").Name != "zoo" || reg.ClassRef("Dog").Name != "animals" {
		t.Fatalf("merged: %v %v", reg, err)
	}
	if _, err := Resolve(ctx, d, "u1", []string{"animals", "clash"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"animals" and "clash"`) {
		t.Errorf("a conflict: %v", err)
	}
	if _, err := Resolve(ctx, d, "u1", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("all the ontologies include the conflict: %v", err)
	}
	if _, err := Resolve(ctx, d, "u1", []string{"nope"}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown name: %v", err)
	}
	if _, err := Resolve(ctx, d, "u2", []string{"animals"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's ontology: %v", err)
	}
	if _, err := Resolve(ctx, d, "u1", []string{"bad name!"}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad name: %v", err)
	}
}

// A save swaps the compiled ontology: the calls that run keep the one they got, the next ones get the new one, and none sees a torn state
// (run with -race).
func TestCompiledOntologySwapUnderLoad(t *testing.T) {
	dbtest.Run(t, testCompiledOntologySwapUnderLoad)
}

func testCompiledOntologySwapUnderLoad(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	if _, err := Create(ctx, d, "u1", "animals", "", "", []byte(animals)); err != nil {
		t.Fatal(err)
	}
	old, err := Resolve(ctx, d, "u1", []string{"animals"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := 0
			for i := 0; i < 30; i++ {
				reg, err := Resolve(ctx, d, "u1", []string{"animals"})
				if err != nil {
					errs <- err
					return
				}
				v := reg.Refs[0].Version
				// versions only go up, and a compiled ontology is whole: version 1 has one relation, later ones two
				if v < last || (v == 1) != (len(reg.Relations()) == 1) {
					errs <- fmt.Errorf("version %d after %d with %d relations", v, last, len(reg.Relations()))
					return
				}
				last = v
			}
		}()
	}
	if _, err := Update(ctx, d, "u1", "animals", "", "", []byte(animalsV2)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(old.Relations()) != 1 || old.Refs[0].Version != 1 {
		t.Error("the compiled ontology a call held changed under it")
	}
	now, _ := Resolve(ctx, d, "u1", []string{"animals"})
	if now.Refs[0].Version != 2 || len(now.Relations()) != 2 {
		t.Errorf("after the save: version %d, %d relations", now.Refs[0].Version, len(now.Relations()))
	}
	// A compiled ontology cached at an older version is replaced from the database (a save by another server).
	CompiledOntologies.Forget("u1", "animals")
	if reg, _ := Resolve(ctx, d, "u1", []string{"animals"}); reg.Refs[0].Version != 2 {
		t.Errorf("lazy load: version %d", reg.Refs[0].Version)
	}
}
