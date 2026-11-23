package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// a database that has gb_trained_models from before the model type, the user and the storage gets the columns (and the rows the
// defaults) when the training schema is applied.
func TestInitSchemaAddsTrainedModelColumns(t *testing.T) {
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := initSchema(d); err != nil {
		t.Fatal(err)
	}
	// make it the table of an older version: without the three columns
	for _, q := range []string{
		`DROP INDEX idx_gb_trained_models_user`,
		`ALTER TABLE gb_trained_models DROP COLUMN model_type`,
		`ALTER TABLE gb_trained_models DROP COLUMN user_id`,
		`ALTER TABLE gb_trained_models DROP COLUMN storage`,
		`INSERT INTO gb_trained_models (name, job_id, model_size, num_train_steps, path) VALUES ('old', 'j', '4b', 1, 'trained/j')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := initSchema(d); err != nil {
		t.Fatal(err)
	}
	if err := initSchema(d); err != nil { // idempotent
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('gb_trained_models') WHERE name = 'model_type'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("model_type column: %d %v", n, err)
	}
	var typ, user, storage string
	if err := d.QueryRow(`SELECT model_type, user_id, storage FROM gb_trained_models`).Scan(&typ, &user, &storage); err != nil {
		t.Fatal(err)
	}
	if typ != "lora" || user != "" || storage != "modal_volume" {
		t.Fatalf("got %q %q %q", typ, user, storage)
	}
}
