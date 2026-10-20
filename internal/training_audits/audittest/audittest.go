// Package audittest has what the tests of the training audits (and of what is built on them) need: a database per backend with the
// trained model tables and the audit tables, a fake sidecar, a fake judge and a training datasets directory. It is not used by
// anything else.
package audittest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/sqlite"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// EnvPostgresDSN is the environment variable that enables the PostgreSQL runs (as for internal/db/relational/dbtest).
const EnvPostgresDSN = "POSTGRES_TEST_DSN"

var counter atomic.Int64

// Run calls fn as a subtest once per backend ("sqlite", and "postgres" if POSTGRES_TEST_DSN is set), each time with a fresh, empty
// database that has the audit tables, and the Ops of the backend. The subtests run one after the other, because the training
// database package routes to the backend of the last Open.
func Run(t *testing.T, fn func(t *testing.T, d *sql.DB, ops training_audits.Ops)) {
	t.Helper()
	t.Run(config.DBBackendSQLite, func(t *testing.T) { fn(t, OpenSQLite(t), training_audits.SQLiteOps) })
	t.Run(config.DBBackendPostgres, func(t *testing.T) {
		dsn := os.Getenv(EnvPostgresDSN)
		if dsn == "" {
			t.Skipf("%s not set", EnvPostgresDSN)
		}
		fn(t, OpenPostgres(t, dsn), training_audits.PostgresOps)
	})
}

// OpenSQLite opens an empty SQLite database in a temporary directory, with the trained model tables and the audit tables.
func OpenSQLite(t *testing.T) *sql.DB {
	t.Helper()
	d, err := relational.Open(config.DBBackendSQLite, filepath.Join(t.TempDir(), "t.db"))
	if err == nil {
		if err = trainingdb.Open(config.DBBackendSQLite, d); err == nil {
			_, err = d.Exec(sqlite.Schema)
		}
		if err != nil {
			d.Close()
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// OpenPostgres opens an empty PostgreSQL database: a new schema in the database at dsn, dropped when the test ends.
func OpenPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("gbaudittest_%d_%d", time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	d, err := relational.Open(config.DBBackendPostgres, withSearchPath(dsn, schema))
	if err == nil {
		if err = trainingdb.Open(config.DBBackendPostgres, d); err == nil {
			_, err = d.Exec(postgres.Schema)
		}
		if err != nil {
			d.Close()
		}
	}
	if err != nil {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	return d
}

func withSearchPath(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return dsn + " search_path=" + schema
}

// RecordModel stores a trained model trained on dataset (a file name) and returns its ID.
func RecordModel(t *testing.T, d *sql.DB, jobID, dataset string) int64 {
	t.Helper()
	id, _, err := trainingdb.RecordTrainedModel(context.Background(), d, trainingdb.TrainedModel{
		JobID: jobID, ModelSize: "12b", Dataset: dataset, NumTrainSteps: 5, Path: "trained/" + jobID})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// DatasetDir makes a training datasets directory with the dataset "mine" of n examples: question "q<i>", answer "a<i>".
func DatasetDir(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	sub := filepath.Join(dir, "mine")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&data, "{\"instruction\":\"q%d\",\"output\":\"a%d\"}\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(sub, "data.jsonl"), []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "dataset.toml"), []byte("name = \"mine\"\nfile = \"data.jsonl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Sidecar is a fake trained model: it answers "a<i>" to "q<i>" (what the dataset of DatasetDir trained it to say), except for the
// prompts in Wrong, which it answers "wrong".
type Sidecar struct {
	mu      sync.Mutex
	reqs    []sidecar.InferRequest
	Wrong   map[string]bool
	Fail    map[string]bool // prompts that make it fail
	Block   chan struct{}   // if set, an answer waits for it to be closed (or for ctx)
	Started chan struct{}   // if set, gets a value when an answer begins
}

// Requests are the requests it got so far.
func (s *Sidecar) Requests() []sidecar.InferRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sidecar.InferRequest(nil), s.reqs...)
}

// Infer answers a prompt.
func (s *Sidecar) Infer(ctx context.Context, req sidecar.InferRequest) (sidecar.InferResponse, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	if s.Started != nil {
		select {
		case s.Started <- struct{}{}:
		default:
		}
	}
	if s.Block != nil {
		select {
		case <-s.Block:
		case <-ctx.Done():
			return sidecar.InferResponse{}, ctx.Err()
		}
	}
	switch {
	case s.Fail[req.Prompt]:
		return sidecar.InferResponse{}, &sidecar.InferError{Message: "out of memory"}
	case s.Wrong[req.Prompt]:
		return sidecar.InferResponse{Response: "wrong", InputTokens: 4, OutputTokens: 1}, nil
	}
	return sidecar.InferResponse{Response: "a" + strings.TrimPrefix(req.Prompt, "q"), InputTokens: 4, OutputTokens: 2}, nil
}

// Judge is a fake judge: it scores 5 when the answer is the reference and 1 otherwise, and counts its calls.
type Judge struct {
	mu    sync.Mutex
	Sizes []int  // the number of cases of each call
	Model string // the model the last call asked for
}

// Calls is the number of calls it got.
func (j *Judge) Calls() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.Sizes)
}

// Complete grades the cases of a request.
func (j *Judge) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	content := req.Messages[0].Content
	var cases []struct {
		ID        int    `json:"id"`
		Reference string `json:"reference"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(content[strings.Index(content, "["):]), &cases); err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.Sizes = append(j.Sizes, len(cases))
	j.Model = req.Model
	j.mu.Unlock()
	var out []map[string]any
	for _, it := range cases {
		score := 1
		if it.Answer == it.Reference {
			score = 5
		}
		out = append(out, map[string]any{"id": it.ID, "score": score, "reason": "because"})
	}
	b, _ := json.Marshal(out)
	return &ai.Response{Text: string(b), Model: "judge-model", Provider: "test", InputTokens: 50, OutputTokens: 20, CostUSD: 0.5}, nil
}
