package training

// Errors of the training operations that say what went wrong in terms a caller can act on (the HTTP API maps the Kind to a
// status). Msg is safe to show to the user; Err, if any, is the cause (already logged where it happened).

// Kind is what sort of failure an Error is.
type Kind int

const (
	KindInternal Kind = iota // something failed on our side
	KindInvalid              // the request is not acceptable as it is
	KindNotFound             // what was asked for does not exist
	KindConflict             // the request clashes with the current state
	KindUpstream             // the sidecar failed
)

// Error is a failure of a training operation.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

func internalErr(msg string, err error) *Error { return &Error{KindInternal, msg, err} }
func invalidErr(msg string) *Error             { return &Error{Kind: KindInvalid, Msg: msg} }
func notFoundErr(msg string) *Error            { return &Error{Kind: KindNotFound, Msg: msg} }
func conflictErr(msg string) *Error            { return &Error{Kind: KindConflict, Msg: msg} }
