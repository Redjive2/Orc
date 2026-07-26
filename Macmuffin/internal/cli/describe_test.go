package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/cli"
)

// okStdin runs a command with something on standard input.
//
// It builds the App here rather than growing the shared rig, because `--set -` is
// the only command in the tool that reads stdin and a local helper is cheaper than
// a field every other test carries.
func okStdin(t *testing.T, r *rig, who, stdin string, args ...string) result {
	t.Helper()
	env := map[string]string{"ORC_USER": who, "ORC_KEY": key}

	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errOut,
		Env: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
		Home:     r.root + "/home",
		Root:     r.root + "/store",
		Clock:    r.now,
		Colour:   true,
		Cwd:      r.cwd,
		Notify:   r.mail.run,
		Control:  r.control,
		Identity: r.identity,
		Operator: r.operator,
	}, args)

	got := result{code: code, stdout: out.String(), stderr: errOut.String()}
	if got.code != fault.CodeOK {
		t.Fatalf("%v exited %d\n%s", args, got.code, got.stderr)
	}
	return got
}

// `muff describe` is the only place a task says what it is *for*, so the tests are
// about the two things that would quietly lose that: prose that does not survive the
// round trip, and a refusal that arrives after somebody has typed it.

func spec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// described is a rig with one owned task.
func describable(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "claim", "fix-the-parser")
	return r
}

// TestItRoundTripsThroughTheShell. `muff describe x > spec.md` and
// `muff describe x --set spec.md` are the whole export and import story, which only
// works if printing puts the text alone on stdout.
func TestDescribeRoundTrips(t *testing.T) {
	r := describable(t)
	const text = "# the parser\n\nIt drops the last token when there is no trailing newline.\n"

	r.ok("alice", "describe", "fix-the-parser", "--set", spec(t, text))

	got := r.ok("alice", "describe", "fix-the-parser")
	if got.stdout != text {
		t.Errorf("printing gave %q, want the text alone", got.stdout)
	}
}

// Markdown, and it survives being markdown: the headings, lists and fences that make
// it worth writing in a file are exactly what a stricter check would have refused.
func TestDescribeKeepsMarkdown(t *testing.T) {
	r := describable(t)
	const text = "# heading\n\n- a point\n- another\n\n```go\nfunc main() {}\n```\n\n> a quote\n"

	r.ok("alice", "describe", "fix-the-parser", "--set", spec(t, text))
	if got := r.ok("alice", "describe", "fix-the-parser"); got.stdout != text {
		t.Errorf("markdown did not survive:\n%s", got.stdout)
	}
}

// Setting from stdin, which is how a program hands one over.
func TestDescribeFromStdin(t *testing.T) {
	r := describable(t)

	okStdin(t, r, "alice", "what to do\n", "describe", "fix-the-parser", "--set", "-")
	if got := r.ok("alice", "describe", "fix-the-parser"); !strings.Contains(got.stdout, "what to do") {
		t.Errorf("stdin was not read:\n%s", got.stdout)
	}
}

// A task with none says so on stderr and prints nothing, so a redirect that captured
// nothing captures nothing rather than a sentence pretending to be a description.
func TestDescribeWithNoneSaysSoWithoutPrintingIt(t *testing.T) {
	r := describable(t)

	got := r.ok("alice", "describe", "fix-the-parser")
	if got.stdout != "" {
		t.Errorf("stdout should be empty, got %q", got.stdout)
	}
	if !strings.Contains(got.stderr, "no description") {
		t.Errorf("it does not say there is none:\n%s", got.stderr)
	}
}

func TestDescribeClear(t *testing.T) {
	r := describable(t)
	r.ok("alice", "describe", "fix-the-parser", "--set", spec(t, "something"))

	r.ok("alice", "describe", "fix-the-parser", "--clear")

	if got := r.ok("alice", "describe", "fix-the-parser"); got.stdout != "" {
		t.Errorf("a cleared description still prints:\n%s", got.stdout)
	}
}

// TestTheCardSaysThereIsOne. A card that quietly omitted a task's specification
// would be a card somebody reads *instead of* the spec.
func TestInfoNamesTheDescription(t *testing.T) {
	r := describable(t)

	if got := r.ok("alice", "info", "fix-the-parser"); !strings.Contains(got.stdout, "none yet") {
		t.Errorf("the card does not say there is no description:\n%s", got.stdout)
	}

	r.ok("alice", "describe", "fix-the-parser", "--set", spec(t, "what to do"))

	got := r.ok("alice", "info", "fix-the-parser")
	if !strings.Contains(got.stdout, "described") || !strings.Contains(got.stdout, "by alice") {
		t.Errorf("the card does not say who described it:\n%s", got.stdout)
	}
}

// The board carries whether there is one; `info` carries the prose. That split is
// what keeps a listing of forty tasks from being forty specifications.
func TestJSONCarriesTheDescriptionWhereItBelongs(t *testing.T) {
	r := describable(t)
	const text = "the lexer eats the last token"
	r.ok("alice", "describe", "fix-the-parser", "--set", spec(t, text))

	board := r.ok("alice", "pool", "--all", "--json").stdout
	if !strings.Contains(board, `"described": true`) {
		t.Errorf("the board does not say the task has a description:\n%s", board)
	}
	if strings.Contains(board, text) {
		t.Errorf("the board carries the whole description:\n%s", board)
	}

	info := r.ok("alice", "info", "fix-the-parser", "--json").stdout
	if !strings.Contains(info, text) {
		t.Errorf("info does not carry the description:\n%s", info)
	}
}

// TestARefusalArrivesBeforeTheTyping. Writing is the owner's, and an agent who may
// not must be told before an editor opens rather than after a page of prose.
func TestDescribeNeedsTheSameAuthorityAsScope(t *testing.T) {
	r := describable(t)
	// Pooled, so somebody who is not on it can see it at all: a draft is invisible
	// to a stranger and this is a question about writing, not about seeing.
	r.pool("alice", "fix-the-parser")
	path := spec(t, "what to do")

	if got := r.run("bob", "describe", "fix-the-parser", "--set", path); got.code != fault.CodeDenied {
		t.Errorf("a stranger's write exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	// Reading is not writing: anybody who can see the task can read what it is for.
	if got := r.run("bob", "describe", "fix-the-parser"); got.code != fault.CodeOK {
		t.Errorf("a stranger could not read it: %d\n%s", got.code, got.stderr)
	}
}

// Over the bound it is refused rather than cut, and the refusal gives the arithmetic.
func TestDescribeRefusesAnOversizedFile(t *testing.T) {
	r := describable(t)
	huge := spec(t, strings.Repeat("x", (32<<10)+1))

	got := r.run("alice", "describe", "fix-the-parser", "--set", huge)
	if got.code != fault.CodeUsage {
		t.Errorf("an oversized description exited %d, want %d\n%s", got.code, fault.CodeUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "KiB") {
		t.Errorf("the refusal should give the arithmetic:\n%s", got.stderr)
	}
}

func TestDescribeRefusals(t *testing.T) {
	r := describable(t)
	path := spec(t, "x")

	for _, tc := range []struct {
		what string
		args []string
		code int
	}{
		{"no task", []string{"describe"}, fault.CodeUsage},
		{"two tasks", []string{"describe", "a", "b"}, fault.CodeUsage},
		{"a task that does not exist", []string{"describe", "nonexistent"}, fault.CodeNotFound},
		{"--set with no file", []string{"describe", "fix-the-parser", "--set"}, fault.CodeUsage},
		{"a file that is not there", []string{"describe", "fix-the-parser", "--set", "/nonexistent"}, fault.CodeIO},
		// Naming two would leave the loser silent.
		{"--set and --clear", []string{"describe", "fix-the-parser", "--set", path, "--clear"}, fault.CodeUsage},
		{"--edit and --clear", []string{"describe", "fix-the-parser", "--edit", "--clear"}, fault.CodeUsage},
	} {
		if got := r.run("alice", tc.args...); got.code != tc.code {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, tc.code, got.stderr)
		}
	}
}
