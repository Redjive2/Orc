// Package guard asks Macmuffin whether a write is allowed.
//
// `Docs/Macmuffin/Vision.md` requires that a task's scope enforce editing "even
// via Anno". Anno reaches the filesystem through `anno write`, which from a
// Claude hook's point of view is an opaque `Bash` call — deciding what an
// arbitrary shell command will write is undecidable, so the check cannot live
// there. Here it is decidable: Anno knows exactly which file it is about to
// change, and asks before changing it.
//
// The question goes to `muff check-scope`, which exits 0 in scope and 9 outside
// and prints its reasoning on stderr. Anno holds no opinion about tasks, scopes,
// worktrees, or which of them is in force; it relays an answer.
//
// Everything except a definite "no" is a yes. Macmuffin missing, broken, slow,
// unauthenticated, or newer than Anno understands must not stop somebody editing
// their own files — Anno worked before Macmuffin existed and has to keep working
// where it does not. The cost is stated rather than hidden: while Macmuffin is
// broken, an out-of-scope write through Anno gets through.
package guard

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"orc/common/fault"
)

// Binary is the tool Anno asks.
const Binary = "muff"

// CodeOutOfScope is the exit status that means "no". It is Macmuffin's
// ErrScope code, and the only status treated as a refusal.
const CodeOutOfScope = 9

// Deadline bounds the question. A scope check is a read of a small store; a
// second is far longer than a healthy one takes, and short enough that a
// Macmuffin hanging on a stalled disk costs a pause rather than a session.
const Deadline = time.Second

// Check reports whether a path may be written. A nil error means yes.
type Check func(path string) error

// Refused reports a write that Macmuffin says is out of scope.
//
// The message is Macmuffin's own, verbatim. Anno does not know which task is in
// force or what its scope is, and reconstructing an explanation from an exit
// code would mean inventing one.
type Refused struct {
	Path   string
	Detail string
}

func (e Refused) Error() string {
	if e.Detail == "" {
		return e.Path + " is outside the scope of the task in force"
	}
	return e.Detail
}

// Unwrap makes this exit 9, the same status Macmuffin gave, so a caller
// branching on the code sees one answer from either tool.
func (e Refused) Unwrap() error { return fault.ErrScope }

// Exec asks the real `muff` binary, within Deadline.
func Exec(path string) error { return ExecWithin(Deadline, path) }

// ExecWithin is Exec with the deadline given.
//
// The bound is a parameter rather than a constant because it is the one thing
// here that a slow machine can make wrong: a check that takes longer than its
// deadline fails open, which is the safe direction and a silent one, so a test
// pinning the refusal path must not be racing a timer to observe it.
func ExecWithin(deadline time.Duration, path string) error {
	if _, err := exec.LookPath(Binary); err != nil {
		// Macmuffin is not installed. There is nothing to enforce.
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, Binary, "check-scope", path)
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return nil
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != CodeOutOfScope {
		// A missing store, no identity, a timeout, a Macmuffin that crashed:
		// none of these are an answer, and none of them may stop a write.
		return nil
	}
	return Refused{Path: path, Detail: clean(stderr.String())}
}

// clean turns Macmuffin's diagnostic into something that reads correctly after
// Anno's own `anno:` prefix — the tool name is already said once.
func clean(stderr string) string {
	out := strings.TrimSpace(stderr)
	out = strings.TrimPrefix(out, Binary+":")
	return strings.TrimSpace(out)
}
