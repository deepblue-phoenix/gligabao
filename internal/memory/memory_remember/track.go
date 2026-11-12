package memory_remember

import (
	"context"
	"errors"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
)

// trackFailure stores a GBerror (see package errors) for a key failure of the call, with the sink of its options (none: nothing is
// stored). A cancelled call (the caller gave up, or the manager is closing) is not a failure to track. The original error is what
// the caller returns, so the messages do not change.
func trackFailure(ctx context.Context,
	co memory_core.CallOptions,
	op string,
	cause error,
	meta gberrors.Meta) {
	if co.ErrSink == nil || errors.Is(cause, context.Canceled) || ctx.Err() != nil {
		return
	}
	gberrors.New(context.WithoutCancel(ctx),
		co.ErrSink,
		op,
		cause,
		meta)
}
