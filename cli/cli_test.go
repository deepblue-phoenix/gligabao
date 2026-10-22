package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao"
)

func TestNewHasServeFlags(t *testing.T) {
	c := New(Options{
		Use:          "embedder",
		ServeOptions: func(*cobra.Command, *gligabao.Config, *gligabao.ServeOptions) (func(), error) { return nil, nil },
	})
	if c.Root.Use != "embedder" {
		t.Fatalf("Use = %q", c.Root.Use)
	}
	for _, f := range []string{"db", "vector-dir", "image-dir", "training", "eval", "eval-db", "admin-ui", "training-dataset-dir", "training-eval-dir", "trained-model-export-dir"} {
		if c.Serve.Flags().Lookup(f) == nil {
			t.Errorf("serve has no --%s", f)
		}
	}
	if c.Root.PersistentFlags().Lookup("config") == nil {
		t.Error("root has no --config")
	}
}

func TestVersionString(t *testing.T) {
	c := New(Options{VersionString: func() string { return "x 1.2.3" }})
	if c.Root.Version == "" {
		t.Fatal("no --version flag")
	}
	var out strings.Builder
	c.Root.SetOut(&out)
	c.Root.SetArgs([]string{"version"})
	if err := c.Root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "x 1.2.3\n" {
		t.Fatalf("version printed %q", got)
	}
}
