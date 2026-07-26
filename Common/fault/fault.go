// Package fault is the error vocabulary every Orc tool shares.
//
// It exists because five tools had grown five copies of it. Copies drift, and a
// drifted error vocabulary is worse than a duplicated one: exit code 8 meaning
// "denied" in one tool and "escape" in another is a bug that only shows up in
// the hook or the shell script that branches on it, long after the divergence.
//
// So there is one vocabulary, one set of sentinels, and — in Codes.go — one
// table mapping sentinels to exit codes. A tool that references a fault it never
// returns costs nothing. A fourth tool inventing its own numbering costs a
// debugging session every time.
//
// Nothing here panics, and nothing in any Orc tool panics in its place: an
// invariant that cannot be satisfied becomes an Internal error and travels back
// up the call stack like any other failure.
package fault

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Every fault type below unwraps to exactly one of these, and
// every one of them appears in the exit-code table.
var (
	// ErrUsage is a malformed command line.
	ErrUsage = errors.New("usage")
	// ErrNotFound is a target that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAmbiguous is a target that matched more than one thing.
	ErrAmbiguous = errors.New("ambiguous")
	// ErrParse is malformed input or malformed stored data.
	ErrParse = errors.New("parse")
	// ErrUnbalanced is Anno's: a close marker with no matching open.
	ErrUnbalanced = errors.New("unbalanced")
	// ErrIO is a filesystem failure.
	ErrIO = errors.New("i/o")
	// ErrConflict is a state that changed underneath an operation, or a write
	// that would overwrite something that must not be overwritten.
	ErrConflict = errors.New("conflict")
	// ErrAuth is a failed authentication.
	ErrAuth = errors.New("auth")
	// ErrDenied is a caller who is authenticated but not permitted.
	ErrDenied = errors.New("denied")
	// ErrScope is a path outside the surface a task is allowed to edit.
	ErrScope = errors.New("out of scope")
	// ErrEscape is a path that resolves outside the root it was measured
	// against. It is separate from ErrScope because an escape is a malformed
	// request rather than a permitted request aimed at the wrong file.
	ErrEscape = errors.New("escape")
	// ErrUnavailable is a peer that cannot be reached. It is separate from
	// ErrIO because a dead network and a bad disk are fixed by different
	// people, and cq is the tool that has to tell them apart.
	ErrUnavailable = errors.New("unavailable")
	// ErrInternal is a violated invariant: a bug in the tool, not the input.
	ErrInternal = errors.New("internal")
)

// Parse reports malformed input at a known position.
type Parse struct {
	Path   string
	Line   int // 1-indexed; 0 when the fault is not tied to a line
	Col    int // 1-indexed rune column; 0 when unknown
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
		if e.Col > 0 {
			fmt.Fprintf(&b, ":%d", e.Col)
		}
	}
	b.WriteString(": ")
	b.WriteString(reason)
	return b.String()
}

func (e Parse) Unwrap() error { return ErrParse }

// Query reports a malformed query, carrying the column at which the parser gave
// up. The rendered message underlines that column, so the report is a diagram of
// the mistake rather than a description of it.
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
		return fmt.Sprintf("bad query: %s", reason)
	}
	// The caret is placed over runes so a multi-byte query still points at the
	// right character.
	runes := []rune(e.Query)
	col := e.Col
	if col > len(runes)+1 {
		col = len(runes) + 1
	}
	return fmt.Sprintf("bad query at column %d: %s\n  %s\n  %s^", e.Col, reason, e.Query, strings.Repeat(" ", col-1))
}

func (e Query) Unwrap() error { return ErrParse }

// Unbalanced reports a close marker with no matching open marker.
type Unbalanced struct {
	Path string
	Line int
	Name string
	Open []string // names currently open, outermost first
}

func (e Unbalanced) Error() string {
	s := fmt.Sprintf("%s:%d: close of %q matches no open annotation", e.Path, e.Line, e.Name)
	if len(e.Open) > 0 {
		s += " (open here: " + strings.Join(e.Open, ", ") + ")"
	}
	return s
}

func (e Unbalanced) Unwrap() error { return ErrUnbalanced }

// NotFound reports a target that matched nothing. Near lists close candidates,
// which is usually what the caller meant.
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

// Ambiguous reports a target that had to match one thing and matched several.
// Candidates are listed in a form the caller can paste back, so the fix is a
// copy rather than a guess.
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

// Conflict reports that something changed underneath an operation, or that an
// operation would overwrite what must not be overwritten.
type Conflict struct {
	Path   string
	Reason string
}

func (e Conflict) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "changed unexpectedly"
	}
	// A conflict need not be about a named path. A refusal that is a sentence
	// about two policies disagreeing has nothing to put here, and prefixing that
	// with ": " made every such message begin with punctuation.
	if e.Path == "" {
		return reason
	}
	return fmt.Sprintf("%s: %s", e.Path, reason)
}

func (e Conflict) Unwrap() error { return ErrConflict }

// Auth reports a failed authentication.
//
// Reason is what the operator sees; Detail is what actually happened. They are
// kept apart on purpose: distinguishing "no such user" from "wrong key" in the
// visible message is an enumeration oracle, and every agent on a machine shares
// one store. Detail travels with the error for logs and tests but is never part
// of Error.
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

// Denied reports a caller who is who they say they are, but may not do this.
//
// It names the owner, because an agent that wanted a task needs to know who to
// ask — a refusal that ends the conversation is worse than one that redirects
// it. Owner is empty when the thing has none, which is a different situation
// and gets a different message.
type Denied struct {
	Actor  string
	Action string
	Target string
	Owner  string
	Reason string
}

func (e Denied) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s may not %s %s", or(e.Actor, "you"), or(e.Action, "touch"), or(e.Target, "that"))
	switch {
	case e.Reason != "":
		b.WriteString(": " + e.Reason)
	case e.Owner != "":
		fmt.Fprintf(&b, "; it belongs to %s", e.Owner)
	}
	return b.String()
}

func (e Denied) Unwrap() error { return ErrDenied }

// Scope reports an edit aimed outside the surface a task declared.
//
// InScope is carried so the message can show what *is* allowed: an agent told
// only that it may not touch a file has to guess, and an agent shown the scope
// can act.
type Scope struct {
	Path    string
	Task    string
	InScope []string
}

func (e Scope) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is outside the scope of %s", or(e.Path, "that path"), or(e.Task, "the task in force"))
	if len(e.InScope) > 0 {
		b.WriteString("\n\n  in scope:  " + strings.Join(e.InScope, "  "))
	}
	return b.String()
}

func (e Scope) Unwrap() error { return ErrScope }

// Escape reports a path that resolves outside the root it was measured against.
type Escape struct {
	Path string
	Root string
}

func (e Escape) Error() string {
	return fmt.Sprintf("%s resolves outside %s", or(e.Path, "that path"), or(e.Root, "the root"))
}

func (e Escape) Unwrap() error { return ErrEscape }

// Unavailable reports a peer that could not be reached, naming it and the
// underlying failure. Err is unwrapped alongside the sentinel so a caller can
// still reach the network error when it needs to.
type Unavailable struct {
	Peer string
	Err  error
}

func (e Unavailable) Error() string {
	if e.Err == nil {
		return "cannot reach " + or(e.Peer, "the server")
	}
	return fmt.Sprintf("cannot reach %s: %v", or(e.Peer, "the server"), e.Err)
}

func (e Unavailable) Unwrap() []error { return []error{ErrUnavailable, e.Err} }

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

// Internal reports a violated invariant. Reaching one is a bug in the tool
// rather than a problem with the caller's input, so the message names the check
// that failed and asks for a report. It is returned, never panicked.
type Internal struct {
	Where  string
	Detail string
}

func (e Internal) Error() string {
	return fmt.Sprintf("internal invariant violated in %s: %s (this is a bug)", e.Where, e.Detail)
}

func (e Internal) Unwrap() error { return ErrInternal }

// Check returns an Internal error when cond is false, and nil otherwise. It is
// the assertion primitive every Orc tool uses: assertions report, they do not
// abort.
func Check(cond bool, where, format string, args ...any) error {
	if cond {
		return nil
	}
	return Internal{Where: where, Detail: fmt.Sprintf(format, args...)}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
