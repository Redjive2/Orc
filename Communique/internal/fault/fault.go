// Package fault defines cq's error vocabulary.
//
// It is Anno's vocabulary with two additions the networked tool needs —
// Unauthenticated and Unavailable — and one change: every fault also carries an
// HTTP status, because cq reports failures down two channels rather than one.
// The exit code and the status code are derived from the same classification, in
// one place each, so a new failure mode cannot exit zero or answer 200 by
// omission.
//
// Nothing here panics, and nothing in cq panics in its place: an invariant that
// cannot be satisfied becomes an Internal error and travels back up the stack
// like any other failure.
package fault

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	commonfault "orc/common/fault"
)

// Sentinel errors. Every fault type below unwraps to exactly one of these.
var (
	ErrUsage           = errors.New("usage")
	ErrNotFound        = errors.New("not found")
	ErrAmbiguous       = errors.New("ambiguous")
	ErrParse           = errors.New("parse")
	ErrIO              = errors.New("i/o")
	ErrConflict        = errors.New("conflict")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUnavailable     = errors.New("unavailable")
	// ErrEscape is a path that resolved outside the root it was measured
	// against — in practice, cq inside an Orcprobe probe being pointed at the
	// real agent state. It is separate from every code above because it means
	// containment failed, which is the one thing a monitor watching a probe
	// should alarm on, and it matches `orc/common/fault.ErrEscape` so the two
	// vocabularies agree on what happened.
	ErrEscape   = errors.New("escape")
	ErrInternal = errors.New("internal")
)

// Code is the machine-readable classification carried in an API error body and
// used to pick an exit status. Clients branch on it rather than on prose.
type Code string

const (
	CodeUsage           Code = "usage"
	CodeNotFound        Code = "not_found"
	CodeAmbiguous       Code = "ambiguous"
	CodeParse           Code = "parse"
	CodeIO              Code = "io"
	CodeConflict        Code = "conflict"
	CodeUnauthenticated Code = "unauthenticated"
	CodeUnavailable     Code = "unavailable"
	CodeEscape          Code = "escape"
	CodeInternal        Code = "internal"
)

// Valid reports whether c is one of the defined codes. It exists so a decoded
// error body can be checked rather than trusted.
func (c Code) Valid() bool {
	switch c {
	case CodeUsage, CodeNotFound, CodeAmbiguous, CodeParse, CodeIO,
		CodeConflict, CodeUnauthenticated, CodeUnavailable, CodeEscape, CodeInternal:
		return true
	default:
		return false
	}
}

// Exit codes. They are stable: scripts and the nudge path branch on them.
const (
	ExitOK              = 0
	ExitUsage           = 1
	ExitNotFound        = 2
	ExitAmbiguous       = 3
	ExitParse           = 4
	ExitIO              = 5
	ExitConflict        = 6
	ExitUnauthenticated = 7
	ExitUnavailable     = 8
	// ExitEscape is 11 to match the shared table in Claude/Docs/ExitCodes.md,
	// so a hook that sees 11 from any Orc tool knows containment failed.
	ExitEscape   = 11
	ExitInternal = 70
)

// Classify reduces any error to its code. Order matters: the most specific
// classification wins, and an unrecognised error is Internal rather than
// something reassuring.
func Classify(err error) Code {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInternal):
		return CodeInternal
	case errors.Is(err, ErrUsage):
		return CodeUsage
	case errors.Is(err, ErrUnauthenticated):
		return CodeUnauthenticated
	case errors.Is(err, ErrAmbiguous):
		return CodeAmbiguous
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrConflict):
		return CodeConflict
	case errors.Is(err, ErrParse):
		return CodeParse
	case errors.Is(err, ErrEscape), errors.Is(err, commonfault.ErrEscape):
		// Both spellings: cq's own, and the one orc/common/sandbox returns when
		// it refuses a store that is not part of the probe this process is in.
		return CodeEscape
	case errors.Is(err, ErrUnavailable):
		return CodeUnavailable
	case errors.Is(err, ErrIO):
		return CodeIO
	default:
		return CodeInternal
	}
}

// Exit maps an error to a process exit status.
func Exit(err error) int {
	switch Classify(err) {
	case "":
		return ExitOK
	case CodeUsage:
		return ExitUsage
	case CodeNotFound:
		return ExitNotFound
	case CodeAmbiguous:
		return ExitAmbiguous
	case CodeParse:
		return ExitParse
	case CodeIO:
		return ExitIO
	case CodeConflict:
		return ExitConflict
	case CodeUnauthenticated:
		return ExitUnauthenticated
	case CodeUnavailable:
		return ExitUnavailable
	case CodeEscape:
		return ExitEscape
	default:
		return ExitInternal
	}
}

// Status maps an error to an HTTP status.
//
// Parse and Usage both mean "the caller sent something wrong", so both are 400;
// they stay distinct in the body's code, where the difference is actionable.
func Status(err error) int {
	switch Classify(err) {
	case "":
		return http.StatusOK
	case CodeUsage, CodeParse:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict, CodeAmbiguous:
		return http.StatusConflict
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Parse reports malformed input at a known position.
type Parse struct {
	Where  string // a file, a field path, or a wire type name
	Line   int    // 1-indexed; 0 when the fault is not tied to a line
	Reason string
}

func (e Parse) Error() string {
	where := e.Where
	if where == "" {
		where = "<input>"
	}
	reason := e.Reason
	if reason == "" {
		reason = "malformed input"
	}
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", where, e.Line, reason)
	}
	return where + ": " + reason
}

func (e Parse) Unwrap() error { return ErrParse }

// Usage reports a malformed command line or request.
type Usage struct {
	Reason string
}

func (e Usage) Error() string {
	if e.Reason == "" {
		return "invalid usage"
	}
	return e.Reason
}

func (e Usage) Unwrap() error { return ErrUsage }

// NotFound reports something addressed that does not exist.
type NotFound struct {
	What string
	Name string
}

func (e NotFound) Error() string {
	what := e.What
	if what == "" {
		what = "item"
	}
	if e.Name == "" {
		return "no such " + what
	}
	return fmt.Sprintf("no such %s %q", what, e.Name)
}

func (e NotFound) Unwrap() error { return ErrNotFound }

// Ambiguous reports an address that matched more than one thing. Candidates are
// listed in full, so each line of the message is itself a usable address.
type Ambiguous struct {
	Target     string
	Candidates []string
}

func (e Ambiguous) Error() string {
	s := fmt.Sprintf("ambiguous target %q — %d matches:", e.Target, len(e.Candidates))
	for _, c := range e.Candidates {
		s += "\n  " + c
	}
	return s
}

func (e Ambiguous) Unwrap() error { return ErrAmbiguous }

// IO reports a filesystem or process failure, naming the operation and subject.
type IO struct {
	Op      string
	Subject string
	Err     error
}

func (e IO) Error() string {
	op, subject := e.Op, e.Subject
	if op == "" {
		op = "operation on"
	}
	if subject == "" {
		subject = "<unnamed>"
	}
	if e.Err == nil {
		return fmt.Sprintf("failed %s %s", op, subject)
	}
	return fmt.Sprintf("%s %s: %v", op, subject, e.Err)
}

func (e IO) Unwrap() []error { return []error{ErrIO, e.Err} }

// Conflict reports state that changed underneath an operation.
type Conflict struct {
	Subject string
	Reason  string
}

func (e Conflict) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "conflicting change"
	}
	// A conflict need not be about a named thing. The queue refuses a retry with
	// a sentence and no subject, and prefixing that with ": " made every such
	// message start with punctuation.
	if e.Subject == "" {
		return reason
	}
	return e.Subject + ": " + reason
}

func (e Conflict) Unwrap() error { return ErrConflict }

// Unauthenticated reports a missing or rejected credential.
//
// Reason is for the operator's log, never for the response body: telling a
// caller which half of a credential was wrong is telling them half the answer.
type Unauthenticated struct {
	Reason string
}

func (e Unauthenticated) Error() string {
	if e.Reason == "" {
		return "not authenticated"
	}
	return "not authenticated: " + e.Reason
}

func (e Unauthenticated) Unwrap() error { return ErrUnauthenticated }

// Public returns the message safe to send to an unauthenticated caller.
func (e Unauthenticated) Public() string { return "not authenticated" }

// Unavailable reports a peer that could not be reached.
type Unavailable struct {
	Peer string
	Err  error
}

func (e Unavailable) Error() string {
	if e.Err == nil {
		return "cannot reach " + e.Peer
	}
	return fmt.Sprintf("cannot reach %s: %v", e.Peer, e.Err)
}

func (e Unavailable) Unwrap() []error { return []error{ErrUnavailable, e.Err} }

// Internal reports a violated invariant. Reaching one is a bug in cq rather than
// a problem with the caller's input, so the message names the check that failed
// and asks for a report. It is returned, never panicked.
type Internal struct {
	Where  string
	Detail string
}

func (e Internal) Error() string {
	return fmt.Sprintf("internal invariant violated in %s: %s (this is a bug in cq)", e.Where, e.Detail)
}

func (e Internal) Unwrap() error { return ErrInternal }

// Check returns an Internal error when cond is false, and nil otherwise. It is
// cq's assertion primitive: assertions report, they do not abort.
func Check(cond bool, where, format string, args ...any) error {
	if cond {
		return nil
	}
	return Internal{Where: where, Detail: fmt.Sprintf(format, args...)}
}

// Field builds a Parse fault for a named field of a wire type, which is the
// shape almost every protocol validation failure takes.
func Field(typ, field, format string, args ...any) error {
	return Parse{
		Where:  typ + "." + field,
		Reason: fmt.Sprintf(format, args...),
	}
}

// Public renders an error for a caller across the network. Internal detail is
// replaced with a neutral message, because an invariant's text can name paths,
// user names, and store layout — none of which a client needs and some of which
// it should not have.
func Public(err error) string {
	if err == nil {
		return ""
	}
	var unauth Unauthenticated
	if errors.As(err, &unauth) {
		return unauth.Public()
	}
	if errors.Is(err, ErrInternal) {
		return "internal error"
	}
	return strings.TrimSpace(err.Error())
}
