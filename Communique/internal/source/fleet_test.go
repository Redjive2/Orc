package source_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"orc/cq/internal/protocol"
	"orc/cq/internal/source"
)

// Every fleet operation, and the `orc` command line it becomes.
//
// cq mirrors Orc's API rather than reimplementing the model. Authority is the one
// thing in this tree that must have a single source, so what matters is that each
// operation turns into exactly the command a person would have typed — and that
// the table is walked against protocol.FleetOps, so a verb added without a
// mapping fails here rather than at the next sync.
func TestOrcApplyRunsTheRightCommand(t *testing.T) {
	cases := map[protocol.Op]struct {
		args protocol.Args
		want []string
	}{
		protocol.OpOrcNewIdentity: {
			protocol.Args{Identity: "atlas"}, []string{"orc", "new", "identity", "atlas"}},
		protocol.OpOrcNewRole: {
			protocol.Args{Role: "engineer", Authority: 60, Description: "writes the code"},
			[]string{"orc", "new", "role", "engineer", "60", "writes the code"}},
		protocol.OpOrcNewPermission: {
			protocol.Args{Permission: "edit", Floor: 40, Patterns: []string{"read(A/**)", "write(A/i/**)"}},
			[]string{"orc", "new", "permission", "edit", "40", "read(A/**)", "write(A/i/**)"}},
		protocol.OpOrcEditPermission: {
			protocol.Args{Permission: "edit", Floor: 40, Patterns: []string{"read(A/**)", "write(A/**)"}},
			[]string{"orc", "edit", "permission", "edit", "--floor", "40", "read(A/**)", "write(A/**)"}},
		protocol.OpOrcAssignRole: {
			protocol.Args{Identity: "atlas", Role: "engineer"},
			[]string{"orc", "assign", "role", "atlas", "engineer"}},
		protocol.OpOrcAssignAuthority: {
			protocol.Args{Role: "engineer", Authority: 55},
			[]string{"orc", "assign", "authority", "engineer", "55"}},
		protocol.OpOrcAssignPerm: {
			protocol.Args{Role: "engineer", Permission: "edit"},
			[]string{"orc", "assign", "permission", "engineer", "edit"}},
		// --yes on everything that asks for it: orc requires it off a terminal,
		// which a queued action always is.
		protocol.OpOrcRemoveIdentity: {
			protocol.Args{Identity: "atlas"}, []string{"orc", "remove", "identity", "atlas", "--yes"}},
		protocol.OpOrcRemoveRole: {
			protocol.Args{Role: "engineer"}, []string{"orc", "remove", "role", "engineer", "--yes"}},
		protocol.OpOrcRemovePerm: {
			protocol.Args{Permission: "edit"}, []string{"orc", "remove", "permission", "edit", "--yes"}},
		protocol.OpOrcGrant: {
			protocol.Args{Identity: "atlas", Permission: "edit"},
			[]string{"orc", "grant", "permission", "atlas", "edit"}},
		protocol.OpOrcRevoke: {
			protocol.Args{Identity: "atlas", Permission: "edit"},
			[]string{"orc", "revoke", "permission", "atlas", "edit"}},
		protocol.OpOrcMove: {
			protocol.Args{Identity: "atlas", Boss: "boss"}, []string{"orc", "move", "atlas", "boss"}},
		protocol.OpOrcEmploy: {
			protocol.Args{Identity: "atlas"}, []string{"orc", "employ", "atlas"}},
		protocol.OpOrcFire: {
			protocol.Args{Identity: "atlas"}, []string{"orc", "fire", "atlas", "--yes"}},
		protocol.OpOrcBudget: {
			protocol.Args{Role: "engineer", Load: 24}, []string{"orc", "budget", "engineer", "24"}},
		protocol.OpOrcPoke: {
			protocol.Args{Identity: "atlas"}, []string{"orc", "poke", "atlas"}},
		protocol.OpOrcRefresh: {
			protocol.Args{Identity: "atlas"}, []string{"orc", "refresh", "atlas"}},
		protocol.OpOrcTend: {protocol.Args{}, []string{"orc", "tend"}},
		// The only verb that runs two commands: it reads where the identity works
		// before it moves it, because `from` is a claim about a snapshot and this
		// is where that claim is checked against the machine.
		// The text travels through a temporary file, so its path is different every
		// run. `<file>` in the expectation stands for "one argument, whatever it
		// is" — what is worth pinning is that orc is asked to read the prompt from
		// a file rather than take it on the command line, and the content is
		// checked in TestInstructSetPassesTheText.
		protocol.OpOrcInstructSet: {
			protocol.Args{Prompt: "identity", PromptName: "atlas", Text: "you write the parser"},
			[]string{"orc", "instruct", "identity", "atlas", "--set", "<file>"}},
		protocol.OpOrcInstructClear: {
			protocol.Args{Prompt: "role", PromptName: "engineer"},
			[]string{"orc", "instruct", "role", "engineer", "--clear"}},
		protocol.OpOrcWorkspace: {
			protocol.Args{Identity: "atlas", Workspace: "/trees/parser", From: "/old/workspace"},
			[]string{"orc", "workspace", "atlas", "/trees/parser"}},
	}

	for _, op := range protocol.FleetOps {
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
			// Most verbs are one command. `orc.workspace` reads before it writes,
			// so what is pinned here is the command that *changes* something —
			// the last one — rather than the count.
			if len(f.calls) == 0 {
				t.Fatalf("ran nothing")
			}
			got := f.calls[len(f.calls)-1]
			want := tc.want
			if len(want) > 0 && want[len(want)-1] == "<file>" && len(got) == len(want) {
				// A path nobody can predict: matched by shape, and by the file's
				// contents in a test of its own.
				got = append(append([]string{}, got[:len(got)-1]...), "<file>")
			}
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("ran %v, want %v", got, want)
			}
		})
	}
}

// TestOrcApplyCarriesTheOptionalOperands: the flags that change what a verb does
// rather than which verb it is.
func TestOrcApplyCarriesTheOptionalOperands(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   protocol.Op
		args protocol.Args
		want []string
	}{
		{"narrow one role instead of deleting", protocol.OpOrcRemovePerm,
			protocol.Args{Permission: "edit", Role: "engineer"},
			[]string{"orc", "remove", "permission", "edit", "--from", "engineer", "--yes"}},
		{"a grant with a wall-clock expiry", protocol.OpOrcGrant,
			protocol.Args{Identity: "atlas", Permission: "edit", Until: "2h"},
			[]string{"orc", "grant", "permission", "atlas", "edit", "--until", "2h"}},
		{"employ at a chosen model and effort", protocol.OpOrcEmploy,
			protocol.Args{Identity: "atlas", Model: "opus", Effort: "high"},
			[]string{"orc", "employ", "atlas", "--model", "opus", "--effort", "high"}},
		{"poke with something to say", protocol.OpOrcPoke,
			protocol.Args{Identity: "atlas", Message: "the tests are failing"},
			[]string{"orc", "poke", "atlas", "the tests are failing"}},
		{"a budget of nothing", protocol.OpOrcBudget,
			protocol.Args{Role: "engineer", Load: 0},
			[]string{"orc", "budget", "engineer", "0"}},
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

// TestFleetReadsWhatOrcDerived: cq carries Orc's derivation rather than the raw
// records, so what arrives is what `orc status --json` computed — capped
// authority included.
func TestFleetReadsWhatOrcDerived(t *testing.T) {
	o := &source.Orc{Command: "orc", Run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{
			"root": "/store", "operator": "boss",
			"identities": [
				{"name": "boss", "operator": true, "authority": 100, "asked_for": 100},
				{"name": "atlas", "boss": "boss", "role": "engineer",
				 "authority": 40, "asked_for": 80, "capped": true,
				 "employed": true, "model": "sonnet", "effort": "medium", "load": 4}
			],
			"roles": [{"name": "engineer", "authority": 80, "held_by": ["atlas"]}],
			"permissions": [{"name": "edit", "floor": 40, "patterns": ["read(A/**)"]}]
		}`), nil
	}}

	got := o.Fleet(t.Context())
	if got.Unreachable != "" {
		t.Fatalf("a readable fleet reported %q", got.Unreachable)
	}
	if got.Operator != "boss" || len(got.Identities) != 2 {
		t.Fatalf("the fleet did not arrive: %+v", got)
	}
	atlas := got.Identities[1]
	if atlas.Authority != 40 || atlas.AskedFor != 80 || !atlas.Capped {
		t.Errorf("the capped authority was not carried: %+v", atlas)
	}
	if !atlas.Employed || atlas.Load != 4 {
		t.Errorf("the worklist half was not carried: %+v", atlas)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("a fleet orc printed does not validate: %v", err)
	}
}

// TestFleetSaysWhyRatherThanShowingNothing: a machine with no orc is not a failed
// sync, and an empty panel says neither "no agents here" nor "orc is broken".
func TestFleetSaysWhyRatherThanShowingNothing(t *testing.T) {
	o := &source.Orc{Command: "orc", Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errNoOrc{}
	}}
	got := o.Fleet(t.Context())
	if got.Unreachable == "" {
		t.Errorf("an unreadable fleet reported no reason")
	}
	if len(got.Identities) != 0 {
		t.Errorf("an unreadable fleet carried identities anyway")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("an unreachable fleet does not validate: %v", err)
	}
}

type errNoOrc struct{}

func (errNoOrc) Error() string { return "orc: no fleet at /store" }

// TestInstructSetPassesTheText: the prompt reaches orc through the file, and does
// not travel on the command line where `ps` would show it.
func TestInstructSetPassesTheText(t *testing.T) {
	const text = "ask before you guess\nand never force-push\n"

	f := newFakeRun()
	var seen string
	f.before = func(args []string) {
		// The file exists while orc is running, and not afterwards.
		if len(args) < 2 || args[len(args)-2] != "--set" {
			return
		}
		body, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			t.Errorf("orc was given a file it cannot read: %v", err)
			return
		}
		seen = string(body)
	}

	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpOrcInstructSet, Queued: at(),
		Args: protocol.Args{Prompt: "system", Text: text},
	}
	if err := newCLI(f).Apply(t.Context(), action); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if seen != text {
		t.Errorf("orc was given %q, want %q", seen, text)
	}
	for _, call := range f.calls {
		for _, arg := range call {
			if strings.Contains(arg, "force-push") {
				t.Errorf("the prompt travelled on the command line: %v", call)
			}
		}
	}
}
