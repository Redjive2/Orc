package fault

import "errors"

// Exit codes, shared by every Orc tool.
//
// The numbering is deliberately not per-tool. A hook or a shell script that
// branches on a status has to be able to do so without knowing which binary it
// called, and a tool that invented its own `8` would silently mean something
// else to everything downstream.
//
// Adding a code is an edit to this block, to Code below, and to
// Claude/Docs/ExitCodes.md — in one commit, for all tools at once.
const (
	// CodeOK is success.
	CodeOK = 0
	// CodeUsage is a malformed command line.
	CodeUsage = 1
	// CodeNotFound is a target that does not exist.
	CodeNotFound = 2
	// CodeAmbiguous is a target that matched more than one thing.
	CodeAmbiguous = 3
	// CodeParse is malformed input or malformed stored data.
	CodeParse = 4
	// CodeIO is a filesystem failure.
	CodeIO = 5
	// CodeConflict is a state that changed underneath an operation.
	CodeConflict = 6
	// CodeAuth is a failed authentication.
	CodeAuth = 7
	// CodeDenied is authenticated, but not permitted.
	CodeDenied = 8
	// CodeScope is permitted, but not for that path. It is distinct from
	// CodeDenied because "you may not do this at all" and "you may do this, but
	// not to that file" are different problems with different fixes.
	CodeScope = 9
	// CodeUnavailable is a peer that could not be reached.
	CodeUnavailable = 10
	// CodeEscape is a path that resolved outside the root it was measured
	// against. It is distinct from CodeScope because an ordinary scope refusal
	// is routine and an escape is a containment failure — in a probe, the one
	// thing a monitor should alarm on. Sharing a code would make them
	// indistinguishable to the hook that has to tell them apart.
	CodeEscape = 11
	// CodeInternal is a bug in the tool. It is 70 rather than 12 so that a
	// defect can never be mistaken for a documented outcome, and so there is
	// room to add outcomes without renumbering.
	CodeInternal = 70
)

// Code maps an error to an exit code.
//
// Order matters: the most specific classification wins, and Internal is checked
// first so that a bug wrapped in something friendlier still reports as a bug. An
// error outside the vocabulary is Internal rather than a guess — a tool that
// returned an unclassified error has a hole in it, and exiting 1 would hide
// that behind what looks like a user mistake.
func Code(err error) int {
	switch {
	case err == nil:
		return CodeOK
	case errors.Is(err, ErrInternal):
		return CodeInternal
	case errors.Is(err, ErrUsage):
		return CodeUsage
	case errors.Is(err, ErrAuth):
		return CodeAuth
	case errors.Is(err, ErrDenied):
		return CodeDenied
	case errors.Is(err, ErrEscape):
		return CodeEscape
	case errors.Is(err, ErrScope):
		return CodeScope
	case errors.Is(err, ErrUnavailable):
		return CodeUnavailable
	case errors.Is(err, ErrAmbiguous):
		return CodeAmbiguous
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrConflict):
		return CodeConflict
	case errors.Is(err, ErrParse), errors.Is(err, ErrUnbalanced):
		return CodeParse
	case errors.Is(err, ErrIO):
		return CodeIO
	default:
		return CodeInternal
	}
}

// Sentinels lists the vocabulary in the order Code tests it. It exists so a
// test can assert the table is total — that every sentinel maps somewhere, and
// that no two of them collide except where the table says they should.
func Sentinels() []error {
	return []error{
		ErrUsage, ErrNotFound, ErrAmbiguous, ErrParse, ErrUnbalanced,
		ErrIO, ErrConflict, ErrAuth, ErrDenied, ErrScope, ErrEscape,
		ErrUnavailable, ErrInternal,
	}
}
