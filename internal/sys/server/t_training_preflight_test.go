package server

import (
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
)

func TestBackendsProblem(t *testing.T) {
	ok := ml_sidecar.BackendStatus{"name": "modal", "configured": true, "healthy": true}
	bad := ml_sidecar.BackendStatus{"name": "modal", "configured": true, "healthy": false, "error": "bad token"}
	unconf := ml_sidecar.BackendStatus{"name": "x", "configured": false}
	cases := []struct {
		name string
		in   []ml_sidecar.BackendStatus
		want string
	}{
		{"none", nil, "no training backend"},
		{"ok", []ml_sidecar.BackendStatus{ok}, ""},
		{"unhealthy", []ml_sidecar.BackendStatus{bad}, "bad token"},
		{"unconfigured", []ml_sidecar.BackendStatus{unconf}, "not configured"},
	}
	for _, c := range cases {
		got := backendsProblem(c.in)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTrainingChecksError(t *testing.T) {
	if err := trainingChecksError([]trainingCheck{{name: "a"}}); err != nil {
		t.Fatalf("no problem must give nil: %v", err)
	}
	err := trainingChecksError([]trainingCheck{{name: "a", problem: "p1"}, {name: "b"}, {name: "c", problem: "p2"}})
	if err == nil || !strings.Contains(err.Error(), "a: p1") || !strings.Contains(err.Error(), "c: p2") || strings.Contains(err.Error(), "b:") {
		t.Fatalf("bad error: %v", err)
	}
}

func TestMissingKeyProblem(t *testing.T) {
	if missingKeyProblem(" ") == "" || missingKeyProblem("k") != "" {
		t.Fatal("wrong key check")
	}
}
