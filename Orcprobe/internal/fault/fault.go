// Package fault defines Orcprobe's error vocabulary.
//
// The vocabulary mirrors Mailman's and Anno's, and the shared sentinels map to
// the same exit codes in all three, so a hook that branches on a status means
// the same thing whichever tool it called. One sentinel is new: ErrEscape,
// which is what a probe returns when something tried to reach the real world.
// It is separate from ErrDenied-style refusals on purpose — an escape is the
// one failure this tool exists to produce, and it should be greppable in a log
// without reading the message.
//
// Nothing here panics, and nothing in Orcprobe panics in its place: an
// invariant that cannot be satisfied becomes an Internal error and travels back
// up the call stack like any other failure.
package fault

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Every fault type below unwraps to exactly one of these.
var (
	ErrUsage     = errors.New("usage")
	ErrNotFound  = errors.New("not found")
	ErrAmbiguous = errors.New("ambiguous")
	ErrParse     = errors.New("parse")
	ErrIO        = errors.New("i/o")
	ErrConflict  = errors.New("conflict")
	ErrAuth      = errors.New("auth")
	ErrEscape    = errors.New("escape")
	ErrInternal  = errors.New("internal")
)

// Escape reports a refused attempt to reach outside a probe.
//
// It names what was attempted and what it would have touched, because the
// operator's next question is always "what would that have done" — and because
// an escape refusal that cannot be explained reads as a bug in orcprobe rather
// than as the guard doing its job.
type Escape struct {
	// Attempt is what was tried, in the operator's vocabulary: "cq sync",
	// "git push", "write outside the probe".
	Attempt string
	// Target is the real thing it would have reached, when there is one.
	Target string
	// Reason explains why that is refused.
	Reason string
}

func (e Escape) Error() string {
	var b strings.Builder
	b.WriteString("refused: ")
	if e.Attempt == "" {
		b.WriteString("that would reach outside the probe")
	} else {
		b.WriteString(e.Attempt)
	}
	if e.Target != "" {
		b.WriteString(" → ")
		b.WriteString(e.Target)
	}
	if e.Reason != "" {
		b.WriteString("\n  ")
		b.WriteString(e.Reason)
	}
	return b.String()
}

func (e Escape) Unwrap() error { return ErrEscape }

// Parse reports malformed stored data at a known position. Orcprobe parses
// things it wrote itself and things the other tools wrote, so a Parse fault
// means a probe is damaged or a tool's format moved — either way the position
// is what makes it repairable by hand.
type Parse struct {
	Path   string
	Line   int // 1-indexed; 0 when the fault is not tied to a line
	Reason string
}

func (e Parse) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "malformed data"
	}
	var b strings.Builder
	if e.Path == "" {
		b.WriteString("<input>")
	}
	b.WriteString(e.Path)
	if e.Line > 0 {
		fmt.Fprintf(&b, ":%d", e.Line)
	}
	b.WriteString(": ")
	b.WriteString(reason)
	return b.String()
}

func (e Parse) Unwrap() error { return ErrParse }

// Query reports a malformed query, carrying the column the parser gave up at.
// The rendered message underlines that column, so the report is a diagram of
// the mistake rather than a description of it. The shape is Mailman's, because
// the query language is Mailman's.
type Query struct {
	Query  string
	Col    int // 1-indexed rune column; 0 when unknown
	Reason string
}

func (e Query) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "malformed query"
	}
	if e.Col <= 0 {
		return "bad query: " + reason
	}
	// The caret line is built over runes so a multi-byte query still points at
	// the right character.
	runes := []rune(e.Query)
	col := e.Col
	if col > len(runes)+1 {
		col = len(runes) + 1
	}
	return fmt.Sprintf("bad query at column %d: %s\n  %s\n  %s^", e.Col, reason, e.Query, strings.Repeat(" ", col-1))
}

func (e Query) Unwrap() error { return ErrParse }

// NotFound reports a probe, identity, or path that matched nothing. Near lists
// close candidates, which is usually what the caller meant.
type NotFound struct {
	Target string
	Near   []string
}

func (e NotFound) Error() string {
	s := fmt.Sprintf("nothing matches %q", e.Target)
	if len(e.Near) > 0 {
		s += "\ndid you mean:\n  " + strings.Join(e.Near, "\n  ")
	}
	return s
}

func (e NotFound) Unwrap() error { return ErrNotFound }

// Ambiguous reports a selector that had to match one thing and matched several.
type Ambiguous struct {
	Target     string
	Candidates []string
}

func (e Ambiguous) Error() string {
	s := fmt.Sprintf("ambiguous %q — %d matches:", e.Target, len(e.Candidates))
	for _, c := range e.Candidates {
		s += "\n  " + c
	}
	return s
}

func (e Ambiguous) Unwrap() error { return ErrAmbiguous }

// IO reports a filesystem failure, naming the operation and path.
type IO struct {
	Op   string
	Path string
	Err  error
}

func (e IO) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s %s", e.Op, e.Path)
	}
	return fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err)
}

func (e IO) Unwrap() []error { return []error{ErrIO, e.Err} }

// Conflict reports that something already exists which must not be
// overwritten, or that state changed underneath an operation.
type Conflict struct {
	Path   string
	Reason string
}

func (e Conflict) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("%s: changed unexpectedly", e.Path)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

func (e Conflict) Unwrap() error { return ErrConflict }

// Auth reports a failed identity resolution inside a probe.
type Auth struct {
	Reason string
	Detail string
}

func (e Auth) Error() string {
	if e.Reason == "" {
		return "authentication failed"
	}
	return e.Reason
}

func (e Auth) Unwrap() error { return ErrAuth }

// Usage reports a malformed command line.
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

// Internal reports a violated invariant. Reaching one is a bug in Orcprobe
// rather than a problem with the caller's input, so the message names the check
// that failed. It is returned, never panicked.
type Internal struct {
	Where  string
	Detail string
}

func (e Internal) Error() string {
	return fmt.Sprintf("internal invariant violated in %s: %s (this is a bug in orcprobe)", e.Where, e.Detail)
}

func (e Internal) Unwrap() error { return ErrInternal }

// Check returns an Internal error when cond is false, and nil otherwise. It is
// Orcprobe's assertion primitive: assertions report, they do not abort.
func Check(cond bool, where, format string, args ...any) error {
	if cond {
		return nil
	}
	return Internal{Where: where, Detail: fmt.Sprintf(format, args...)}
}
