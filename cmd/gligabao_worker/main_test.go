package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log.SetLevel(log.WarnLevel)
	os.Exit(m.Run())
}

func runArgs(t *testing.T,
	stdin string,
	args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(),
		args,
		strings.NewReader(stdin),
		&out,
		&errb)
	return code, out.String(), errb.String()
}

func TestUsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin     string
		anthropic string
		openai    string
		args      []string
		want      string
	}{
		"no command": {
			args: nil,
			want: "COMMAND",
		},
		"unknown command": {
			args: []string{"nope"},
			want: "unknown command",
		},
		"remember no text": {
			anthropic: "k",
			args:      []string{"remember"},
			want:      "exactly one text",
		},
		"query two args": {
			anthropic: "k",
			args:      []string{"query", "a", "b"},
			want:      "exactly one question",
		},
		"bad user": {
			anthropic: "k",
			args:      []string{"query", "--user", " ", "q"},
			want:      "user_id",
		},
		"bad dataset": {
			anthropic: "k",
			args:      []string{"query", "--dataset", "a b", "q"},
			want:      "dataset",
		},
		"bad time": {
			anthropic: "k",
			args:      []string{"remember", "--at", "yesterday", "t"},
			want:      "--at",
		},
		"no anthropic key": {
			args: []string{"query", "q"},
			want: config.EnvAnthropicKey,
		},
		"no openai key": {
			anthropic: "k",
			args:      []string{"query", "q"},
			want:      config.EnvOpenAIKey,
		},
		"flag of another cmd": {
			anthropic: "k",
			openai:    "k",
			args:      []string{"query", "--kind", "episode", "q"},
			want:      "not defined",
		},
		"missing config": {
			anthropic: "k",
			args:      []string{"query", "--config", "/nonexistent/gligabao.toml", "q"},
			want:      "nonexistent",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.EnvPostgresDSN, "postgres://u:p@localhost:5432/db")
			t.Setenv(config.EnvAnthropicKey, tc.anthropic)
			t.Setenv(config.EnvOpenAIKey, tc.openai)
			code, stdout, stderr := runArgs(t,
				tc.stdin,
				tc.args...)
			if code != exitUsage {
				t.Errorf("exit code %d, want %d (stderr: %s)",
					code,
					exitUsage,
					stderr)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty, got %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr %q does not contain %q",
					stderr,
					tc.want)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, stdout, _ := runArgs(t,
		"",
		"version")
	if code != exitOK || !strings.HasPrefix(stdout, "gligabao_worker ") {
		t.Errorf("version: code %d, stdout %q",
			code,
			stdout)
	}
	code, stdout, _ = runArgs(t,
		"",
		"--help")
	if code != exitOK || !strings.Contains(stdout, "remember") {
		t.Errorf("--help: code %d, stdout %q",
			code,
			stdout)
	}
}

func TestCheckRemote(t *testing.T) {
	for name, tc := range map[string]struct {
		backend, vectors, want string
	}{
		"postgres and pinecone":  {config.DBBackendPostgres, config.VectorStorePinecone, ""},
		"postgres and s3vectors": {config.DBBackendPostgres, config.VectorStoreS3Vectors, ""},
		"sqlite":                 {config.DBBackendSQLite, config.VectorStorePinecone, "db_backend"},
		"chromem":                {config.DBBackendPostgres, config.VectorStoreChromem, "vector_store"},
		"sqlite and chromem":     {config.DBBackendSQLite, config.VectorStoreChromem, "db_backend"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkRemote(&config.Config{
				DBBackend:   tc.backend,
				VectorStore: tc.vectors,
			})
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error %v, want one that mentions %q",
					err,
					tc.want)
			}
		})
	}
}

func TestLoadConfigFromEnvironment(t *testing.T) {
	t.Setenv(config.EnvPostgresDSN, "postgres://u:p@localhost:5432/db")
	t.Setenv(config.EnvS3VectorsBucket, "")
	cfg, err := loadConfig("")
	if err != nil || cfg.DBBackend != config.DBBackendPostgres || cfg.VectorStore != config.VectorStorePinecone {
		t.Fatalf("pinecone by default: %+v, %v",
			cfg,
			err)
	}
	t.Setenv(config.EnvS3VectorsBucket, "b")
	cfg, err = loadConfig("")
	if err != nil || cfg.VectorStore != config.VectorStoreS3Vectors {
		t.Fatalf("s3vectors with a bucket: %+v, %v",
			cfg,
			err)
	}
	t.Setenv(config.EnvPostgresDSN, "")
	if _, err := loadConfig(""); err == nil {
		t.Error("no postgres DSN should fail")
	}
}

// A config file that does not say postgres and a remote vector store (here: the defaults, sqlite and chromem) is refused.
func TestLoadConfigFileMustBeRemote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gligabao.toml")
	if err := os.WriteFile(path,
		[]byte("anthropic_api_key = \"k\"\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "db_backend") {
		t.Errorf("error %v", err)
	}
}

// The binary stays small: none of what the server has may get linked.
func TestMinimalDependencies(t *testing.T) {
	out, err := exec.Command("go",
		"list",
		"-deps",
		".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	const prefix = "github.com/deepblue-phoenix/gligabao/"
	forbidden := []string{
		"internal/api", "internal/agent_research", "internal/sys/server", "internal/sys/sidecar", "internal/training", "internal/training_audits", "internal/evals",
		"internal/db/dbfull", "internal/db/dbimport", "cli", "monitoring", "admin_ui",
	}
	for _, pkg := range strings.Fields(string(out)) {
		rest, ok := strings.CutPrefix(pkg, prefix)
		if !ok {
			continue
		}
		for _, f := range forbidden {
			if rest == f || strings.HasPrefix(rest, f+"/") {
				t.Errorf("gligabao_worker must not depend on %s (keep the binary minimal)", pkg)
			}
		}
	}
}
