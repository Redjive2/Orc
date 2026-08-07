package hook_test

import (
	"strings"
	"testing"

	"orc/orc/internal/hook"
)

// An agent's workspace is its own.
//
// This is the rule that was broken, and it broke in the way that costs most: an
// agent is given a directory to work in, told to work, and then refused the
// ordinary acts of working in it. Every refusal arrived mid-task, and every one
// of them needed a clause somebody had to have thought of in advance.
//
// What must still hold is everything that is not the workspace, so each of those
// is here too. A rule that opened a directory is one thing; a rule that quietly
// opened the fleet's store beside it would be another.
func TestTheWorkspaceIsTheAgentsEntirely(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	// ember holds read(Anno/**) and write(Anno/internal/**) and nothing else.
	// None of these is named by either, and every one of them is ordinary work.
	for _, path := range []string{
		"/notes.md",
		"/go.mod",
		"/scratch/build.log",
		"/Common/user/user.go",
		"/Docs/Vision.md",
		"/a/deep/path/nobody/anticipated.txt",
	} {
		for _, verb := range []string{"Write", "Edit", "Read"} {
			out := r.call(opts, tool(verb, ws+path, ws))
			if out.Code != hook.CodeOK {
				t.Errorf("%s %s in its own workspace was blocked:\n%s", verb, path, out.Stderr)
			}
		}
	}
}

func TestAnAgentWithNoPathClauseAtAllStillWorks(t *testing.T) {
	// The worst case of the old rule: a fresh identity, hired and employed, could
	// not write a single file anywhere. Nothing about that was visible until an
	// agent tried.
	r := newRig(t)
	opts := r.as("bare", nil)
	ws := r.store.WorkspaceDir(mustUser(t, "bare"))

	if out := r.call(opts, tool("Write", ws+"/first.md", ws)); out.Code != hook.CodeOK {
		t.Errorf("an agent with no clauses cannot write in its own workspace:\n%s", out.Stderr)
	}
}

func TestOpeningTheWorkspaceDidNotOpenAnythingElse(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	// Outside it. Still the boundary, and now the only one for paths.
	if out := r.call(opts, tool("Write", "/etc/hosts", ws)); out.Code != hook.CodeBlock {
		t.Error("a write outside the workspace was allowed")
	}
	// Another agent's workspace is outside this one, which is what makes giving
	// two agents two directories the way to keep them apart.
	other := r.store.WorkspaceDir(mustUser(t, "boss"))
	if out := r.call(opts, tool("Write", other+"/theirs.md", ws)); out.Code != hook.CodeBlock {
		t.Error("one agent wrote into another's workspace")
	}
	// The fleet's own store, which no clause could ever permit.
	if out := r.call(opts, tool("Write", r.root+"/identities/ember/identity.jsonl", ws)); out.Code != hook.CodeBlock {
		t.Error("an agent wrote into the fleet's store")
	}
	// And the shell is still shut by default: opening a directory is not opening
	// every capability that could reach into it.
	shell := map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "0f9a1a6a-0000-4000-8000-000000000000",
		"tool_name":       "Bash",
		"cwd":             ws,
		"tool_input":      map[string]any{"command": "curl http://example.com"},
	}
	if out := r.call(opts, shell); out.Code != hook.CodeBlock {
		t.Error("the shell gate opened along with the workspace")
	}
}

func TestARefusalStillNamesTheWayForward(t *testing.T) {
	r := newRig(t)
	out := r.call(r.as("ember", nil), tool("Write", "/etc/hosts", r.workspace()))
	if !strings.Contains(out.Stderr, "workspace") {
		t.Errorf("the refusal does not say what the boundary is:\n%s", out.Stderr)
	}
}
