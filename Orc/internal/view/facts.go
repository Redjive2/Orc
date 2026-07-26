package view

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// The footer's facts: what an operator watching a fleet actually needs beside the
// feed — unread mail and the task in force (Plan.md §6.2).
//
// Both are read from the other tools' own stores, through their own binaries, the
// same way `cq sync` reads them: `mailman inbox --json` and `muff pool --json`. Orc
// does not learn either store's layout. Two tools reading one store's files is how a
// format change becomes a bug in the tool that did not change.
//
// Everything here degrades. A missing binary, a broken store, a slow disk, an agent
// with no mailbox — the footer loses a number and keeps the pane. Nothing about a
// status line is worth failing an attach for.

// FactsDeadline bounds each lookup. The footer is refreshed on a timer while somebody
// watches, so a slow answer must be abandoned rather than queued behind the next one.
const FactsDeadline = 2 * time.Second

// Facts are the things drawn around the feed.
type Facts struct {
	// Role, Authority, Model, Effort, and Load come from the fleet, which the
	// caller already has open — they are fields rather than lookups because Orc
	// knows them without asking anybody.
	Role      string
	Authority int
	Model     string
	Effort    string
	Load      int

	// Mail is unread messages, or -1 when nobody could say. The distinction
	// matters on a status line: "0" and "unknown" are different, and showing one
	// as the other is how a footer starts lying.
	Mail int
	// Task is the task in force, empty when there is none or nobody could say.
	Task string
}

// NoFacts is a Facts with nothing filled in, which is what a caller starts from.
func NoFacts() Facts { return Facts{Mail: -1} }

// Run executes another tool and returns its stdout. It is an interface so a test
// answers for itself and nothing execs.
type Run func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs the real binary, with the given environment.
//
// The environment is the identity's own credential, which is how the numbers end up
// being *that agent's* unread mail rather than the operator's. Orc mints those keys,
// so it is the one tool that can ask on somebody else's behalf without being handed
// a secret it did not already hold.
func Exec(env []string) Run {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if _, err := exec.LookPath(name); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = env

		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = nil
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		return stdout.Bytes(), nil
	}
}

// Ask fills in the mail and task counts, leaving whatever it cannot learn.
//
// It takes the Facts the caller already knows rather than returning a fresh one, so
// the fleet's own numbers — role, load — are never lost to a failed lookup of
// somebody else's store.
func Ask(run Run, known Facts) Facts {
	got := known
	got.Mail = -1

	if run == nil {
		return got
	}

	ctx, cancel := context.WithTimeout(context.Background(), FactsDeadline)
	defer cancel()

	if n, ok := unread(ctx, run); ok {
		got.Mail = n
	}
	if name, ok := inForce(ctx, run); ok {
		got.Task = name
	}
	return got
}

// message is the part of `mailman inbox --json` this reads.
type message struct {
	Unread bool `json:"unread"`
}

// unread counts what is waiting in the identity's mailbox.
func unread(ctx context.Context, run Run) (int, bool) {
	out, err := run(ctx, "mailman", "inbox", "--json")
	if err != nil {
		return 0, false
	}

	var got []message
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		return 0, false
	}
	// `inbox` without --all already lists only unread mail, so the count is the
	// length. The field is honoured anyway in case that ever changes: counting
	// the list would then silently overstate it.
	n := 0
	for _, m := range got {
		if m.Unread {
			n++
		}
	}
	if n == 0 && len(got) > 0 {
		n = len(got)
	}
	return n, true
}

// poolTask is the part of `muff pool --json` this reads.
type poolTask struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Completed bool   `json:"completed"`
}

// inForce names the task the identity is working on.
//
// Macmuffin decides what "in force" means — an environment variable, then a worktree
// binding — and it answers that question for a *process*, not for another agent. From
// out here the honest approximation is the task this identity owns and has not
// finished, and where there are several the footer says how many rather than picking
// one.
func inForce(ctx context.Context, run Run) (string, bool) {
	out, err := run(ctx, "muff", "pool", "--json")
	if err != nil {
		return "", false
	}

	var tasks []poolTask
	if err := json.Unmarshal(bytes.TrimSpace(out), &tasks); err != nil {
		return "", false
	}

	var mine []string
	for _, t := range tasks {
		if t.Completed || strings.TrimSpace(t.Owner) == "" {
			continue
		}
		mine = append(mine, t.Name)
	}

	switch len(mine) {
	case 0:
		return "", true
	case 1:
		return mine[0], true
	default:
		return mine[0] + " +" + itoa(len(mine)-1), true
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
