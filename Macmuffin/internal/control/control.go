// Package control asks Orc whether the caller may direct an agent.
//
// `muff assign <agent> <task>` gives work to somebody else, and the spec's
// condition is "given you control <agent>". Macmuffin has no view of the fleet
// and should not grow one: who reports to whom is Orc's model, kept in Orc's
// store, and a second copy here would be a second thing to keep right. The
// question goes to `orc check-control`, which exits 0 if the caller controls the
// agent and 8 if not.
//
// Unlike Anno's scope guard, this fails **closed**. That difference is the whole
// design: a scope check is a bystander in somebody's editing session and must
// not stop them working when Macmuffin is broken, while this one is a permission
// check standing between an agent and work it may not be allowed to direct.
// Orc missing, unreachable, or slow is not a yes.
package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
)

// Binary is the tool Macmuffin asks.
const Binary = "orc"

// CodeDenied is the exit status meaning "you do not control that agent". It is
// Orc's ErrDenied code, and the only status read as a definite no.
const CodeDenied = fault.CodeDenied

// CodeMissing is what Orc exits when the agent is not in the fleet at all.
const CodeMissing = fault.CodeNotFound

// Deadline bounds the question. A control check is a read of a small store; a
// caller waiting longer than this on `muff assign` would rather be told.
const Deadline = 5 * time.Second

// Check reports whether the caller may direct the agent. A nil error is a yes,
// and only a nil error is a yes.
type Check func(agent user.Name) error

// Exec asks the real `orc` binary, within Deadline.
func Exec(agent user.Name) error { return ExecWithin(Deadline, agent) }

// ExecWithin is Exec with the deadline given, so a test pinning a refusal is
// not racing a timer to observe it.
func ExecWithin(deadline time.Duration, agent user.Name) error {
	if agent.Zero() {
		return fault.Internal{Where: "control.Exec", Detail: "no agent named"}
	}

	if _, err := exec.LookPath(Binary); err != nil {
		// No Orc, no fleet, no way to know who reports to whom. Assigning work
		// on the strength of a missing answer is exactly what fail-closed
		// exists to prevent.
		return fault.Unavailable{Peer: Binary, Err: errors.New(
			"it is not installed, so nobody can be shown to control " + agent.String() +
				"; `muff claim` takes a task yourself")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, Binary, "check-control", agent.String())
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return nil
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fault.Unavailable{Peer: Binary, Err: fmt.Errorf("it could not be run: %w", err)}
	}

	switch exit.ExitCode() {
	case CodeDenied:
		return Refused{Agent: agent, Detail: reason(stderr.String(), agent)}
	case CodeMissing:
		return Unknown{Agent: agent}
	default:
		// Orc answered, but not with an answer: a broken store, no credential,
		// a defect. None of those are permission to direct somebody.
		return fault.Unavailable{Peer: Binary, Err: fmt.Errorf(
			"it could not say who controls %s (exit %d): %s", agent, exit.ExitCode(), reason(stderr.String(), agent))}
	}
}

// Refused reports that Orc says the caller does not control the agent.
//
// The message is Orc's own. Macmuffin has no view of the fleet, so restating
// the refusal in its own words would mean either inventing a reason or saying
// nothing useful — and Orc's already names the tree relationship that decided
// it. Wrapping it in a second "you may not…" only says it twice.
type Refused struct {
	Agent  user.Name
	Detail string
}

func (e Refused) Error() string { return e.Detail }

// Unwrap makes this exit 8, the code Orc gave it.
func (e Refused) Unwrap() error { return fault.ErrDenied }

// Unknown reports an agent no fleet has.
//
// It is its own type rather than a plain NotFound so the message says *agent*.
// "nothing matches" beside a command that took an agent and a task would leave
// the reader guessing which one was not found.
type Unknown struct {
	Agent user.Name
}

func (e Unknown) Error() string {
	return fmt.Sprintf("orc has no agent called %s; `orc status` lists the fleet", e.Agent)
}

func (e Unknown) Unwrap() error { return fault.ErrNotFound }

// reason turns Orc's diagnostic into something that reads after Macmuffin's own
// prefix. Orc's wording is kept: it knows why, and Macmuffin does not.
func reason(stderr string, agent user.Name) string {
	got := strings.TrimSpace(stderr)
	got = strings.TrimPrefix(got, Binary+":")
	got = strings.TrimSpace(got)
	if got == "" {
		return agent.String() + " is not below you in the fleet"
	}
	return got
}
