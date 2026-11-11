package errors_test

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
)

var errCause = stderrors.New("boom")

func fail(sink gberrors.Sink, meta gberrors.Meta) *gberrors.GBerror {
	return gberrors.New(context.Background(), sink, "remember.extract", errCause, meta)
}

func TestNew(t *testing.T) {
	var stored *gberrors.GBerror
	e := fail(func(_ context.Context, e *gberrors.GBerror) error { stored = e; return nil }, gberrors.Meta{"k": "v"})
	if stored != e {
		t.Fatal("not stored")
	}
	if e.Op != "remember.extract" || e.Function != "fail" || !strings.HasSuffix(e.Package, "sys/errors_test") {
		t.Fatalf("where: %q %q %q", e.Op, e.Package, e.Function)
	}
	if !strings.Contains(e.Stack, "TestNew") || e.Message != "boom" || e.Meta["k"] != "v" {
		t.Fatalf("%+v", e)
	}
	if !stderrors.Is(e, errCause) || e.Error() != "remember.extract: boom" {
		t.Fatal("unwrap/text")
	}
}

func TestInvalidOpAndLimits(t *testing.T) {
	meta := gberrors.Meta{"long": strings.Repeat("x", 2000)}
	for i := 0; i < 30; i++ {
		meta[string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	e := gberrors.New(context.Background(), nil, "Bad Op", nil, meta)
	if e.Op != gberrors.InvalidOp || e.Meta[gberrors.MetaOriginalOp] != "Bad Op" || len(e.Meta) > gberrors.MaxMetaKeys+1 {
		t.Fatalf("%+v", e)
	}
	if len(meta) != 31 {
		t.Fatal("input meta changed")
	}
	if e.Meta[gberrors.MetaDropped] == nil {
		t.Fatal("no dropped count")
	}
}
