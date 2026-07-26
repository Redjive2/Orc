package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// A workspace was a derived path until now, so most of what is worth testing is that
// the default still works exactly as it did, and that the exception is refused
// everywhere it would be dangerous.

func TestWorkspaceReportsTheDerivedPath(t *testing.T) {
	r := fullFleet(t)

	got := r.ok("boss", "workspace", "ember")
	if !strings.Contains(got.stdout, "ember") || !strings.Contains(got.stdout, "works in") {
		t.Errorf("`orc workspace ember` does not say where it works:\n%s", got.stdout)
	}
	// The distinction is worth drawing: one of these is a fact about the layout,
	// the other is a decision somebody made.
	if !strings.Contains(got.stdout, "orc's own") {
		t.Errorf("an identity that has never been moved should say the path is orc's:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, filepath.Join("identities", "ember", "workspace")) {
		t.Errorf("the derived path is not what was reported:\n%s", got.stdout)
	}
}

// TestAdoptingAnExistingDirectory — the form that is built.
func TestWorkspaceAdopt(t *testing.T) {
	r := fullFleet(t)
	tree := filepath.Join(t.TempDir(), "parser")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}

	got := r.ok("boss", "workspace", "ember", tree, "--adopt")
	if !strings.Contains(got.stdout, "moved") || !strings.Contains(got.stdout, tree) {
		t.Errorf("the move was not reported:\n%s", got.stdout)
	}
	// It says what has not happened yet, since the identity is not employed.
	if !strings.Contains(got.stdout, "not employed") {
		t.Errorf("it should say when the change takes effect:\n%s", got.stdout)
	}

	// And it stuck: read back through a fresh command, which is what proves the
	// event replays.
	after := r.ok("boss", "workspace", "ember")
	if !strings.Contains(after.stdout, tree) {
		t.Errorf("the move did not survive a reload:\n%s", after.stdout)
	}
	if strings.Contains(after.stdout, "orc's own") {
		t.Error("a chosen path should not be reported as the derived one")
	}
}

// TestEverythingElseSeesIt. The workspace is asked for in eight places, including
// the supervisor's working directory and the hook's path resolution — one of them
// missing the exception would be an agent working in one directory while its
// permissions were checked against another.
func TestWorkspaceReachesTheOtherScreens(t *testing.T) {
	r := fullFleet(t)
	tree := filepath.Join(t.TempDir(), "parser")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", tree, "--adopt")

	// The card truncates a long path to fit its frame, and `env` does not carry a
	// workspace at all, so the JSON is what can be asserted on exactly. The card is
	// checked for the part of the path that survives truncation.
	if got := r.ok("boss", "status", "ember", "--json"); !strings.Contains(got.stdout, tree) {
		t.Errorf("status --json does not show the moved workspace:\n%s", got.stdout)
	}
	if got := r.ok("boss", "status", "ember"); !strings.Contains(got.stdout, tree[:30]) {
		t.Errorf("the card does not show the moved workspace:\n%s", got.stdout)
	}
}

// The session takes it when it starts, since that is when a working directory is
// fixed.
func TestWorkspaceIsWhereTheSessionStarts(t *testing.T) {
	r := fullFleet(t)
	tree := filepath.Join(t.TempDir(), "parser")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}

	r.ok("boss", "workspace", "ember", tree, "--adopt")
	r.ok("boss", "employ", "ember")

	// A running session keeps the directory it started in, so the change is
	// deferred and said rather than forced.
	got := r.ok("boss", "workspace", "ember", t.TempDir(), "--adopt")
	if !strings.Contains(got.stdout, "still working in") {
		t.Errorf("it should say the session has not moved:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "--now") || !strings.Contains(got.stdout, "refresh") {
		t.Errorf("it should name both ways to make it take effect:\n%s", got.stdout)
	}
	// And it says why that matters here specifically.
	if !strings.Contains(got.stdout, "permissions are compiled") {
		t.Errorf("it should say why a stale cwd matters:\n%s", got.stdout)
	}
}

func TestWorkspaceNowRestarts(t *testing.T) {
	r := fullFleet(t)
	first := filepath.Join(t.TempDir(), "one")
	second := filepath.Join(t.TempDir(), "two")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r.ok("boss", "workspace", "ember", first, "--adopt")
	r.ok("boss", "employ", "ember")
	before := len(r.populates)

	got := r.ok("boss", "workspace", "ember", second, "--adopt", "--now")
	if len(r.populates) != before+1 {
		t.Fatalf("--now did not replace the session: %v", r.populates)
	}
	if !strings.Contains(got.stdout, "fresh context") {
		t.Errorf("--now should say what it cost:\n%s", got.stdout)
	}
}

// TestTheDangerousPathsAreRefused. Each of these would be quiet damage.
func TestWorkspaceRefusals(t *testing.T) {
	r := fullFleet(t)

	existing := filepath.Join(t.TempDir(), "exists")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	// A tree quill has adopted, with something inside it.
	quills := filepath.Join(t.TempDir(), "quills-tree")
	if err := os.MkdirAll(filepath.Join(quills, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "quill", quills, "--adopt")

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		what string
		args []string
		code int
		says string
	}{
		{
			what: "a relative path", code: fault.CodeUsage, says: "absolute",
			args: []string{"workspace", "ember", "trees/parser", "--adopt"},
		},
		{
			what: "inside the fleet's own store", code: fault.CodeDenied, says: "key",
			args: []string{"workspace", "ember", r.root, "--adopt"},
		},
		// Inside somebody else's *adopted* tree. A path inside their default
		// workspace is inside the store too, and that rule fires first — rightly,
		// since it is the more dangerous of the two.
		{
			what: "inside another agent's workspace", code: fault.CodeConflict, says: "overlap",
			args: []string{"workspace", "ember", filepath.Join(quills, "sub"), "--adopt"},
		},
		{
			what: "a file", code: fault.CodeConflict, says: "not a directory",
			args: []string{"workspace", "ember", file, "--adopt"},
		},
		{
			what: "adopting what is not there", code: fault.CodeNotFound, says: "nothing to adopt",
			args: []string{"workspace", "ember", filepath.Join(t.TempDir(), "absent"), "--adopt"},
		},
		{
			what: "an existing directory without --adopt", code: fault.CodeConflict, says: "--adopt",
			args: []string{"workspace", "ember", existing},
		},
		{
			what: "no identity", code: fault.CodeUsage, says: "workspace takes",
			args: []string{"workspace"},
		},
		{
			what: "nobody by that name", code: fault.CodeNotFound, says: "nobody",
			args: []string{"workspace", "nobody", existing, "--adopt"},
		},
	} {
		got := r.run("boss", tc.args...)
		if got.code != tc.code {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, tc.code, got.stderr)
			continue
		}
		if !strings.Contains(got.stderr, tc.says) {
			t.Errorf("%s should say %q:\n%s", tc.what, tc.says, got.stderr)
		}
	}
}

// TestRelocateMovesTheFiles — the form where the workspace comes with the identity.
func TestWorkspaceRelocate(t *testing.T) {
	r := fullFleet(t)

	// Something in the workspace to move. An identity that has never been employed
	// has no directory on disk, so the test makes the one orc would have.
	was := filepath.Join(r.root, "identities", "ember", "workspace")
	if err := os.MkdirAll(filepath.Join(was, "internal", "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"README.md":                  "the parser\n",
		"internal/tree/tree.go":      "package tree\n",
		"internal/tree/tree_test.go": "package tree\n",
	} {
		if err := os.WriteFile(filepath.Join(was, filepath.FromSlash(path)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	to := filepath.Join(t.TempDir(), "moved")
	got := r.ok("boss", "workspace", "ember", to)

	if !strings.Contains(got.stdout, "moved") || !strings.Contains(got.stdout, "3 files copied") {
		t.Errorf("the relocation did not report what it copied:\n%s", got.stdout)
	}

	// Every file arrived, with its contents.
	for path, want := range map[string]string{
		"README.md":             "the parser\n",
		"internal/tree/tree.go": "package tree\n",
	} {
		body, err := os.ReadFile(filepath.Join(to, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s did not arrive: %v", path, err)
			continue
		}
		if string(body) != want {
			t.Errorf("%s arrived as %q", path, body)
		}
	}

	// The identity points at the new one.
	if after := r.ok("boss", "workspace", "ember"); !strings.Contains(after.stdout, to) {
		t.Errorf("the identity was not moved:\n%s", after.stdout)
	}
}

// TestTheOldDirectoryIsLeftAlone. Orc does not delete an agent's work as a side
// effect of a settings change.
func TestWorkspaceRelocateKeepsTheOriginal(t *testing.T) {
	r := fullFleet(t)

	was := filepath.Join(r.root, "identities", "ember", "workspace")
	if err := os.MkdirAll(was, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(was, "work.txt"), []byte("hours of it\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := r.ok("boss", "workspace", "ember", filepath.Join(t.TempDir(), "moved"))

	if _, err := os.Stat(filepath.Join(was, "work.txt")); err != nil {
		t.Errorf("the original was removed: %v", err)
	}
	if !strings.Contains(got.stdout, "untouched") {
		t.Errorf("it should say the old directory is still there:\n%s", got.stdout)
	}
}

// A workspace with nothing in it — an identity nobody has employed — relocates to an
// empty directory rather than failing.
func TestWorkspaceRelocateWithNothingThere(t *testing.T) {
	r := fullFleet(t)
	to := filepath.Join(t.TempDir(), "fresh")

	if got := r.ok("boss", "workspace", "ember", to); !strings.Contains(got.stdout, "moved") {
		t.Errorf("relocating an empty workspace failed:\n%s", got.stdout)
	}
	if info, err := os.Stat(to); err != nil || !info.IsDir() {
		t.Errorf("the new workspace was not made: %v", err)
	}
}

// Copying a tree into itself is a loop that fills a disk.
func TestWorkspaceRelocateIntoItself(t *testing.T) {
	r := fullFleet(t)
	was := filepath.Join(r.root, "identities", "ember", "workspace")
	if err := os.MkdirAll(was, 0o755); err != nil {
		t.Fatal(err)
	}

	got := r.run("boss", "workspace", "ember", filepath.Join(was, "inside"))
	if got.code == fault.CodeOK {
		t.Error("a workspace was copied into itself")
	}
	if !strings.Contains(got.stderr, "loop") && !strings.Contains(got.stderr, "store") {
		t.Errorf("the refusal should say why:\n%s", got.stderr)
	}
}

// Symlinks are recreated rather than followed: a link to something large would be
// copied twice, and a link to itself would not terminate.
func TestWorkspaceRelocateKeepsSymlinks(t *testing.T) {
	r := fullFleet(t)
	was := filepath.Join(r.root, "identities", "ember", "workspace")
	if err := os.MkdirAll(was, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(was, "real.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(was, "link.txt")); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}

	to := filepath.Join(t.TempDir(), "moved")
	r.ok("boss", "workspace", "ember", to)

	info, err := os.Lstat(filepath.Join(to, "link.txt"))
	if err != nil {
		t.Fatalf("the link did not arrive: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link arrived as a copy of what it pointed at")
	}
}

// Where somebody else's agent works is the boss's call, and an agent moving its own
// would be stepping outside the directory its permissions were compiled against.
func TestWorkspaceNeedsControl(t *testing.T) {
	r := fullFleet(t)
	tree := filepath.Join(t.TempDir(), "parser")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := r.run("quill", "workspace", "ember", tree, "--adopt"); got.code == fault.CodeOK {
		t.Error("a peer moved somebody else's workspace")
	}
	if got := r.run("ember", "workspace", "ember", tree, "--adopt"); got.code == fault.CodeOK {
		t.Error("an agent moved its own workspace")
	}
	// Reading is not directing.
	if got := r.run("quill", "workspace", "ember"); got.code != fault.CodeOK {
		t.Errorf("reading where an agent works exited %d\n%s", got.code, got.stderr)
	}
}

// A script that sets it every pass should be a no-op on the passes where nothing
// changed, not a failure.
func TestWorkspaceUnchangedIsNotAnError(t *testing.T) {
	r := fullFleet(t)
	tree := filepath.Join(t.TempDir(), "parser")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", tree, "--adopt")

	got := r.ok("boss", "workspace", "ember", tree, "--adopt")
	if !strings.Contains(got.stdout, "already works in") {
		t.Errorf("an unchanged move should say so:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "moved") {
		t.Errorf("nothing changed, so nothing should be reported as moved:\n%s", got.stdout)
	}
}

// TestDriftIsReported. An agent writing outside the directory its permissions were
// compiled against is the state nobody would think to look for, so `orc workspace`
// says it without being asked.
func TestWorkspaceReportsDrift(t *testing.T) {
	r := fullFleet(t)
	first := filepath.Join(t.TempDir(), "one")
	second := filepath.Join(t.TempDir(), "two")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r.ok("boss", "workspace", "ember", first, "--adopt")
	r.ok("boss", "employ", "ember")
	// Moved while it runs: the session keeps the directory it started in.
	r.ok("boss", "workspace", "ember", second, "--adopt")

	got := r.ok("boss", "workspace", "ember")
	if !strings.Contains(got.stdout, "running session is working in") {
		t.Errorf("the drift is not reported:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, first) {
		t.Errorf("it does not say where the session actually is:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "refresh") {
		t.Errorf("it does not say how to fix it:\n%s", got.stdout)
	}

	// And once the session is replaced, there is nothing to report.
	r.ok("boss", "refresh", "ember")
	if after := r.ok("boss", "workspace", "ember"); strings.Contains(after.stdout, "running session is working in") {
		t.Errorf("a refreshed session still reads as drifted:\n%s", after.stdout)
	}
}

// A session recorded before the workspace was written down says nothing: "cannot
// say" is not a disagreement.
func TestWorkspaceSilentWhenTheSessionPredatesTheField(t *testing.T) {
	r := fullFleet(t)
	tree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", tree, "--adopt")
	r.ok("boss", "employ", "ember")

	// Strip the field, as a session written by an older orc would have.
	path := filepath.Join(r.root, "identities", "ember", "session", "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	delete(state, "workspace")
	out, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := r.ok("boss", "workspace", "ember"); strings.Contains(got.stdout, "running session is working in") {
		t.Errorf("a session with no recorded workspace was reported as drifted:\n%s", got.stdout)
	}
}

// withMuff puts a stand-in `muff` on PATH that records how it was called and answers
// with the given output and exit code. Orc's side of §2.4.3 is *that it asks* and
// what it does with the answer; what a rebind actually does is Macmuffin's to test.
func withMuff(t *testing.T, stdout string, code int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "called")
	// printf rather than cat: PATH is replaced wholesale here, so the script may use
	// shell builtins and nothing else.
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %s\nprintf '%%s\\n' %s\nexit %d\n",
		quoted(log), quoted(stdout), code)
	if err := os.WriteFile(filepath.Join(dir, "muff"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return log
}

// quoted is a shell single-quoted word.
func quoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func called(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return ""
	}
	return string(data)
}

// TestARelocateFollowsTheWorktreeBindings — §2.4.3. A relocate that orphans a
// binding turns the scope hook off without saying so, which is the worst way for an
// enforcement mechanism to fail.
func TestWorkspaceRebindsWorktrees(t *testing.T) {
	log := withMuff(t, "fix-the-parser is now bound to /new/tree", 0)
	r := fullFleet(t)
	from := filepath.Join(t.TempDir(), "from")
	to := filepath.Join(t.TempDir(), "to")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", from, "--adopt")

	got := r.ok("boss", "workspace", "ember", to)

	// It asked, with both directories.
	asked := called(t, log)
	if !strings.Contains(asked, "rebind") || !strings.Contains(asked, from) || !strings.Contains(asked, to) {
		t.Errorf("muff was not asked to follow the move: %q", asked)
	}
	// And relayed what came back, rather than swallowing it.
	if !strings.Contains(got.stdout, "fix-the-parser is now bound to") {
		t.Errorf("the rebind was not reported:\n%s", got.stdout)
	}
}

// A binding that could not follow is a task with no scope enforcement anywhere. The
// words come back verbatim, because `muff rebind` names the command that restores
// each one and paraphrasing would lose it.
func TestWorkspaceReportsBindingsThatDidNotFollow(t *testing.T) {
	withMuff(t, "  fix-the-parser  not a worktree\n    muff worktree fix-the-parser /new/tree", 6)
	r := fullFleet(t)
	from := filepath.Join(t.TempDir(), "from")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", from, "--adopt")

	// The move still stands: the files are copied and the identity is written, so
	// failing here would report a move that happened as one that did not.
	got := r.ok("boss", "workspace", "ember", filepath.Join(t.TempDir(), "to"))
	if !strings.Contains(got.stdout, "did not follow") {
		t.Errorf("a stranded binding was not reported:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "muff worktree fix-the-parser") {
		t.Errorf("the restoring command was not relayed:\n%s", got.stdout)
	}
}

// A fleet with no Macmuffin installed has no bindings to strand, and says nothing
// about a tool it does not have.
func TestWorkspaceIsQuietWithoutMacmuffin(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := fullFleet(t)
	from := filepath.Join(t.TempDir(), "from")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", from, "--adopt")

	got := r.ok("boss", "workspace", "ember", filepath.Join(t.TempDir(), "to"))
	if strings.Contains(got.stdout, "binding") {
		t.Errorf("it talked about a tool that is not installed:\n%s", got.stdout)
	}
}

// Nothing bound under the old directory is the common case, and not worth a line.
func TestWorkspaceSaysNothingWhenNothingWasBound(t *testing.T) {
	withMuff(t, "no task is bound to a worktree under /old", 0)
	r := fullFleet(t)
	from := filepath.Join(t.TempDir(), "from")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ok("boss", "workspace", "ember", from, "--adopt")

	got := r.ok("boss", "workspace", "ember", filepath.Join(t.TempDir(), "to"))
	if strings.Contains(got.stdout, "binding") {
		t.Errorf("it reported an absence:\n%s", got.stdout)
	}
}
