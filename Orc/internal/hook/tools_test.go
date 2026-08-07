package hook_test

import (
	"strings"
	"testing"

	"orc/orc/internal/hook"
)

// Reading a document without holding read on it.
//
// Reported from a live fleet: `dock read` on a file outside the workspace was
// refused, and `dock index`, `dock overview` and `dock check` on the same file
// were not. The reporter mapped a whole documentation tree that way — section
// names, line counts, and from `check`, parser errors quoting the text.
//
// The cause was a default. `dock` was read as "`dock <path>`, and `dock read
// <path>`", so the second word became the path whenever it was not the word
// `read`: `dock index Docs/Vision.md` checked a file called `index`, resolved
// against the workspace where it passed, and never looked at the document.
//
// `dock write` went the same way, which is worse than the leak that was reported:
// an unguarded write.
//
// Every verb of both tools is here, because the bug was the *unnamed* case and a
// test that checked three of them would leave the next one to be found the same
// way.

// outside is a path no agent's workspace contains.
const outside = "/etc/orc-probe/Vision.md"

func bashAs(t *testing.T, r *rig, command string) hook.Outcome {
	t.Helper()
	return r.call(r.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "0f9a1a6a-0000-4000-8000-000000000000",
		"tool_name":       "Bash",
		"cwd":             r.workspace(),
		"tool_input":      map[string]any{"command": command},
	})
}

func TestEveryDocumentVerbIsGuarded(t *testing.T) {
	r := newRig(t)
	// A shell wide enough that nothing here is refused for being an unknown
	// command: what is under test is the path, not the verb.
	r.permit("wide-shell", "shell(**)")

	for _, verb := range []string{"index", "overview", "read", "find", "links", "check", "write"} {
		command := "dock " + verb + " " + outside
		if verb == "write" {
			command += ` "text"`
		}
		if out := bashAs(t, r, command); out.Code != hook.CodeBlock {
			t.Errorf("`dock %s` reached a file outside the workspace:\n%s", verb, out.Stderr)
		}
	}
	for _, verb := range []string{"index", "overview", "read", "find", "write"} {
		command := "anno " + verb + " " + outside
		if verb == "write" {
			command += ` "text"`
		}
		if out := bashAs(t, r, command); out.Code != hook.CodeBlock {
			t.Errorf("`anno %s` reached a file outside the workspace:\n%s", verb, out.Stderr)
		}
	}
}

func TestASectionAddressIsNotPartOfThePath(t *testing.T) {
	// Both tools address a section after the path. The refusal has to name the
	// file, not a file with an address stuck to it — one exists and the other does
	// not, and a refusal naming the second sends somebody looking for it.
	r := newRig(t)
	r.permit("wide-shell", "shell(**)")

	for _, command := range []string{
		"dock read " + outside + "§1",
		"dock read " + outside + "#1",
		"anno read " + outside + "@types",
		"anno read " + outside + "@types:Pair^fields",
	} {
		out := bashAs(t, r, command)
		if out.Code != hook.CodeBlock {
			t.Errorf("%q was allowed:\n%s", command, out.Stderr)
		}
		if strings.Contains(out.Stderr, "§1") || strings.Contains(out.Stderr, "@types") {
			t.Errorf("the refusal names an address rather than a file:\n%s", out.Stderr)
		}
	}
}

func TestTheseVerbsStillWorkInsideTheWorkspace(t *testing.T) {
	// The point of guarding them is not to stop them. An agent reading its own
	// documents is the ordinary case and must stay ordinary.
	r := newRig(t)
	r.permit("wide-shell", "shell(**)")
	ws := r.workspace()

	for _, command := range []string{
		"dock index " + ws + "/Docs/Vision.md",
		"dock overview " + ws + "/Docs",
		"dock read " + ws + "/Docs/Vision.md§1",
		"dock check " + ws + "/Docs",
		"dock write " + ws + "/Docs/Vision.md§1 \"text\"",
		"anno read " + ws + "/main.go@types",
	} {
		if out := bashAs(t, r, command); out.Code != hook.CodeOK {
			t.Errorf("%q was blocked in its own workspace:\n%s", command, out.Stderr)
		}
	}
}

func TestABareCheckIsTheDirectoryItStandsIn(t *testing.T) {
	// `dock check` with no operand reads the tree at the working directory. That
	// is still a read of something the agent chose, by standing there.
	r := newRig(t)
	r.permit("wide-shell", "shell(**)")
	if out := bashAs(t, r, "dock check"); out.Code != hook.CodeOK {
		t.Errorf("a bare check in its own workspace was blocked:\n%s", out.Stderr)
	}
}

func TestAVerbThisBuildDoesNotKnowNamesNoPath(t *testing.T) {
	// The failure being replaced was a default that treated an unrecognised word
	// as a path. A form this build has never heard of must fall through to where
	// every other unrecognised command goes, not to a confident check of a file
	// called `whatever`.
	r := newRig(t)
	r.permit("wide-shell", "shell(**)")
	if out := bashAs(t, r, "dock rumpus something"); out.Code != hook.CodeOK {
		t.Errorf("an unknown dock verb was refused as though it named a path:\n%s", out.Stderr)
	}
}
