package source_test

import (
	"strings"
	"testing"

	"orc/cq/internal/protocol"
)

// Every task operation, and the `muff` command line it becomes.
//
// This is the whole of the agent's half: cq mirrors Macmuffin's API rather than
// reimplementing the pool, so what matters is that each operation turns into
// exactly the command a person would have typed — and that the table below is
// checkable against protocol.TaskOps, so an operation added without a mapping
// fails here rather than at a customer's next sync.
func TestApplyRunsTheRightTaskCommand(t *testing.T) {
	cases := map[protocol.Op]struct {
		args protocol.Args
		want []string
	}{
		protocol.OpTaskCreate: {
			protocol.Args{Task: "parser", Priority: 4, Difficulty: 3},
			[]string{"muff", "create", "parser", "4", "3"}},
		protocol.OpTaskSubtask: {
			protocol.Args{Task: "parser", Sub: "tests"},
			[]string{"muff", "create", "parser", "--sub", "tests"}},
		protocol.OpTaskPush: {
			protocol.Args{Task: "parser"}, []string{"muff", "push", "parser"}},
		protocol.OpTaskClaim: {
			protocol.Args{Task: "parser"}, []string{"muff", "claim", "parser"}},
		// The agent comes first, as `muff assign <agent> <task>` takes it. Getting
		// this round the wrong way would assign the task to itself and read as a
		// Macmuffin bug.
		protocol.OpTaskAssign: {
			protocol.Args{Task: "parser", User: "bob"},
			[]string{"muff", "assign", "bob", "parser"}},
		protocol.OpTaskInvite: {
			protocol.Args{Task: "parser", User: "bob"},
			[]string{"muff", "invite", "bob", "parser"}},
		protocol.OpTaskKick: {
			protocol.Args{Task: "parser", User: "bob"},
			[]string{"muff", "kick", "bob", "parser"}},
		protocol.OpTaskLeave: {
			protocol.Args{Task: "parser"}, []string{"muff", "leave", "parser"}},
		protocol.OpTaskScope: {
			protocol.Args{Task: "parser", Paths: []string{"internal/tree", "internal/lex"}},
			[]string{"muff", "scope", "parser", "internal/tree", "internal/lex"}},
		protocol.OpTaskWorktree: {
			protocol.Args{Task: "parser", Path: "work/parser"},
			[]string{"muff", "worktree", "parser", "work/parser"}},
		protocol.OpTaskStatus: {
			protocol.Args{Task: "parser", Status: 2},
			[]string{"muff", "status", "parser", "2"}},
		protocol.OpTaskComplete: {
			protocol.Args{Task: "parser"}, []string{"muff", "complete", "parser"}},
		// --yes always: Macmuffin requires it off a terminal, which a queued
		// action always is. The confirmation happened in the browser.
		protocol.OpTaskDelete: {
			protocol.Args{Task: "parser"}, []string{"muff", "delete", "parser", "--yes"}},
	}

	for _, op := range protocol.TaskOps {
		tc, ok := cases[op]
		if !ok {
			t.Errorf("%s has no command mapping in this test", op)
			continue
		}
		t.Run(string(op), func(t *testing.T) {
			f := newFakeRun()
			action := protocol.Action{
				ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
				Op: op, Args: tc.args, Queued: at(),
			}
			if err := newCLI(f).Apply(t.Context(), action); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("ran %d commands, want 1: %v", len(f.calls), f.calls)
			}
			if strings.Join(f.calls[0], "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("ran %v, want %v", f.calls[0], tc.want)
			}
		})
	}
}

// TestApplyCarriesTheOptionalOperands: the two verbs that take a subtask, and the
// one that takes --force. They are separate because the table above holds one
// sample per operation and these are the same operation carrying more.
func TestApplyCarriesTheOptionalOperands(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   protocol.Op
		args protocol.Args
		want []string
	}{
		{"complete one step", protocol.OpTaskComplete,
			protocol.Args{Task: "parser", Sub: "tests"},
			[]string{"muff", "complete", "parser", "--sub", "tests"}},
		{"complete a task with steps outstanding", protocol.OpTaskComplete,
			protocol.Args{Task: "parser", Force: true},
			[]string{"muff", "complete", "parser", "--force"}},
		{"delete one step", protocol.OpTaskDelete,
			protocol.Args{Task: "parser", Sub: "tests"},
			[]string{"muff", "delete", "parser", "--sub", "tests", "--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRun()
			action := protocol.Action{
				ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
				Op: tc.op, Args: tc.args, Queued: at(),
			}
			if err := newCLI(f).Apply(t.Context(), action); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if strings.Join(f.calls[0], "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("ran %v, want %v", f.calls[0], tc.want)
			}
		})
	}
}

// TestTaskArgumentsAreNotAShell: a task name is data, however it is punctuated —
// the same property the mail verbs hold, and worth pinning separately because
// these arguments reach a different program.
func TestTaskArgumentsAreNotAShell(t *testing.T) {
	f := newFakeRun()
	nasty := `parser"; rm -rf / ;#`
	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpTaskPush, Args: protocol.Args{Task: nasty}, Queued: at(),
	}
	// The action itself is refused before it runs — a name like that is not a name
	// Macmuffin would accept — which is the first of the two defences.
	if err := newCLI(f).Apply(t.Context(), action); err == nil {
		t.Fatalf("a task name that is not a name was accepted")
	}
	if len(f.calls) != 0 {
		t.Errorf("a refused action still ran something: %v", f.calls)
	}

	// And the second: an argument that is legitimately punctuated travels intact
	// rather than through a shell.
	f = newFakeRun()
	scoped := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op:     protocol.OpTaskScope,
		Args:   protocol.Args{Task: "parser", Paths: []string{"internal/a b", "internal/c"}},
		Queued: at(),
	}
	if err := newCLI(f).Apply(t.Context(), scoped); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0][3] != "internal/a b" {
		t.Errorf("a path with a space was not passed intact: %v", f.calls)
	}
}
