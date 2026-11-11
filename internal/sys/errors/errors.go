// Package errors is the unified way to create the errors of gligabao that are key to track: a GBerror holds where the error
// happened (package, function, stack trace), what failed (Op) and a small map of the parameters that help debugging later (Meta),
// and New stores it in the database (tables gb_errors, see the sqlite and postgres subpackages; package db applies the schema).
//
// Create a GBerror only for the most important errors, the ones that are key to be tracked (see AGENTS.md); an ordinary error is
// still a plain error wrapped with fmt.Errorf.
//
// The storage is given to New as a Sink, a function, so there is no global state: a Sink that stores nothing is NopSink.
package errors

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	// MaxMetaKeys is the most keys of the caller's Meta that are kept (counting MetaDropped); the other keys are dropped (in key
	// order) and the count of the dropped is stored under MetaDropped. MetaOriginalOp may be added on top.
	MaxMetaKeys = 16
	// MaxMetaString is the longest string a Meta value keeps (in bytes); a longer one is cut and marked with "...".
	MaxMetaString = 512
	// MaxStackFrames is the most frames the stack trace keeps.
	MaxStackFrames = 32
	// MetaDropped is the Meta key that holds how many keys were dropped over MaxMetaKeys.
	MetaDropped = "meta_dropped"
	// MetaOriginalOp is the Meta key that holds the Op given to New when it is not a valid Op (the Op is then InvalidOp).
	MetaOriginalOp = "original_op"
	// InvalidOp is the Op of a GBerror created with an Op that is not valid.
	InvalidOp = "invalid_op"
)

// opRE is a valid Op: one word (lower case letters, digits, underscore), or several of them separated by ".".
var opRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// ValidOp reports whether op is a valid Op: one word, or several separated by "." (for example "remember.extract").
func ValidOp(op string) bool { return opRE.MatchString(op) }

// Meta is the failure parameters, arguments and other metadata of one error that help debugging it later. Keep it small: New
// limits it (MaxMetaKeys, MaxMetaString). The values must be JSON-encodable.
type Meta map[string]any

// GBerror is an error that is key to track. It is created by New, which also stores it.
type GBerror struct {
	// ID is the ID of the row in the database; 0 if it was not stored.
	ID int64 `json:"id"`
	// CreatedAt is when the error was created (UTC).
	CreatedAt time.Time `json:"created_at"`
	// Op is the symbolic name of the operation that failed, domain specific: one word, or several separated by "."
	// (for example "remember.extract", "geo.index").
	Op string `json:"op"`
	// Package is the Go package (import path) where the error occurred.
	Package string `json:"package"`
	// Function is the function where the error occurred (without the package).
	Function string `json:"function"`
	// Message is the text of the cause (empty if there was none).
	Message string `json:"message"`
	// Stack is the stack trace at creation, one frame per line ("function\n\tfile:line").
	Stack string `json:"stack"`
	// Meta is the unique failure parameters, arguments and metadata of this error.
	Meta Meta `json:"meta,omitempty"`

	cause error
}

// Error is the text of the error: the Op and the message of the cause.
func (e *GBerror) Error() string {
	if e.Message == "" {
		return e.Op
	}
	return e.Op + ": " + e.Message
}

// Unwrap returns the cause, so errors.Is and errors.As see through a GBerror.
func (e *GBerror) Unwrap() error { return e.cause }

// Execer is what *sql.DB and *sql.Tx share for running a statement that returns a row; the Insert functions of the backends take it.
type Execer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Sink stores a GBerror (and sets its ID). It is given the error to store and returns why it could not.
type Sink func(ctx context.Context, e *GBerror) error

// NopSink stores nothing.
func NopSink(context.Context, *GBerror) error { return nil }

// New creates a GBerror for the operation op that failed with cause (may be nil), at the caller of New, and stores it with sink.
// meta is the unique failure parameters (see Meta; it is limited, and not changed). The error is returned even if the storing
// failed (that failure is logged), so the caller can always return it. A nil sink stores nothing.
func New(ctx context.Context, sink Sink, op string, cause error, meta Meta) *GBerror {
	return store(ctx, sink, build(2, time.Now(), op, cause, meta))
}

// Newf is New with the cause made by fmt.Errorf(format, args...) (so %w works).
func Newf(ctx context.Context, sink Sink, op string, meta Meta, format string, args ...any) *GBerror {
	return store(ctx, sink, build(2, time.Now(), op, fmt.Errorf(format, args...), meta))
}

// store stores e with sink (if any), logging a failure, and returns e.
func store(ctx context.Context, sink Sink, e *GBerror) *GBerror {
	if sink == nil {
		return e
	}
	if sinkErr := sink(ctx, e); sinkErr != nil {
		slog.Error("store GBerror", "op", e.Op, "package", e.Package, "function", e.Function, "err", sinkErr)
	}
	return e
}

// build makes the GBerror without storing it; skip is the number of stack frames above build to leave out (1 is build's caller).
func build(skip int, at time.Time, op string, cause error, meta Meta) *GBerror {
	pcs := make([]uintptr, MaxStackFrames)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(skip+1, pcs)])
	stack, first := formatStack(frames)
	pkg, fn := splitFunction(first)
	cleanOp, opMeta := normalizeOp(op)
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return &GBerror{
		CreatedAt: at.UTC(), Op: cleanOp, Package: pkg, Function: fn, Message: message, Stack: stack,
		Meta: nilIfEmpty(merge(limitMeta(meta), opMeta)), cause: cause,
	}
}

// formatStack formats the frames, one per line, and returns the name of the first function.
func formatStack(frames *runtime.Frames) (stack, first string) {
	var b strings.Builder
	for {
		f, more := frames.Next()
		if f.Function != "" {
			if first == "" {
				first = f.Function
			}
			fmt.Fprintf(&b, "%s\n\t%s:%d\n", f.Function, f.File, f.Line)
		}
		if !more {
			return b.String(), first
		}
	}
}

// splitFunction splits a runtime function name ("github.com/x/y/pkg.(*T).Func.func1") into the package import path and the rest.
func splitFunction(full string) (pkg, fn string) {
	slash := strings.LastIndex(full, "/")
	dot := strings.Index(full[slash+1:], ".")
	if dot < 0 {
		return full, ""
	}
	return full[:slash+1+dot], full[slash+2+dot:]
}

// normalizeOp returns op if valid, else InvalidOp and the Meta that keeps the original.
func normalizeOp(op string) (string, Meta) {
	if ValidOp(op) {
		return op, nil
	}
	return InvalidOp, Meta{MetaOriginalOp: truncate(op)}
}

// merge returns a new Meta with the keys of a and then b.
func merge(a, b Meta) Meta {
	out := make(Meta, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// nilIfEmpty returns nil for an empty m.
func nilIfEmpty(m Meta) Meta {
	if len(m) == 0 {
		return nil
	}
	return m
}

// limitMeta returns m with at most MaxMetaKeys keys (the first in key order; the dropped count is under MetaDropped) and strings cut
// to MaxMetaString. It returns nil for an empty m.
func limitMeta(m Meta) Meta {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kept := keys
	if len(keys) > MaxMetaKeys {
		kept = keys[:MaxMetaKeys-1] // room for MetaDropped
	}
	out := make(Meta, len(kept)+1)
	for _, k := range kept {
		out[k] = limitValue(m[k])
	}
	if dropped := len(keys) - len(kept); dropped > 0 {
		out[MetaDropped] = dropped
	}
	return out
}

func limitValue(v any) any {
	if s, ok := v.(string); ok {
		return truncate(s)
	}
	if err, ok := v.(error); ok {
		return truncate(err.Error())
	}
	return v
}

func truncate(s string) string {
	if len(s) <= MaxMetaString {
		return s
	}
	return strings.ToValidUTF8(s[:MaxMetaString], "") + "..."
}
