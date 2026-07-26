package cli_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/cli"
	"orc/macmuffin/internal/control"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// key is a valid credential. Macmuffin resolves an identity but does not yet
// verify it against a user store — see the note in the plan — so any
// well-formed key is accepted.
const key = "0123456789abcdef0123456789abcdef"

// rig runs commands as chosen agents against one store.
type rig struct {
	t        *testing.T
	root     string
	now      *clock.Fake
	cwd      string
	env      map[string]string
	mail     *mailbox
	control  control.Check
	identity control.Verifier
}

// mailbox stands in for the mailman binary. No test execs anything.
type mailbox struct {
	mu    sync.Mutex
	sent  []string
	fail  error
	execs int
}

func (m *mailbox) run(args []string, stdin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execs++
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, strings.Join(args, " ")+"\n"+stdin)
	return nil
}

func (m *mailbox) delivered() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}

// quiet reports whether nothing was ever handed to the mail binary.
func (m *mailbox) quiet() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.execs == 0
}

func (m *mailbox) attempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.execs
}

func (m *mailbox) breaks(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

type result struct {
	code   int
	stdout string
	stderr string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return &rig{
		t: t, root: t.TempDir(), now: clock.NewFake(epoch, time.Millisecond),
		env: map[string]string{}, mail: &mailbox{},
		// Controls nobody by default, so no test reaches for a real `orc`. A
		// test that wants a yes says so.
		control: func(user.Name) error {
			return control.Refused{Detail: "no fleet was configured for this test"}
		},
		// No authority by default, which is the standalone case and what every
		// test that predates verification assumes. A test about verification
		// answers for itself. Either way nothing execs a real `orc`.
		identity: func(user.Name) error {
			return control.Unverifiable{Reason: "no authority was configured for this test"}
		},
	}
}

// run executes a command as the named agent, or with no identity when who is "".
func (r *rig) run(who string, args ...string) result {
	r.t.Helper()

	env := map[string]string{}
	for k, v := range r.env {
		env[k] = v
	}
	if who != "" {
		env["ORC_USER"] = who
		env["ORC_KEY"] = key
	}

	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(""),
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
	}, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// pool makes a task visible and claimable by everyone.
//
// It reaches through to the store because `scope` is not a command until a
// later milestone, and a task cannot be pushed without one. Everything the CLI
// itself does is still driven through cli.Main.
func (r *rig) pool(author, name string) {
	r.t.Helper()

	s, err := store.Open(r.root+"/store", r.now)
	if err != nil {
		r.t.Fatalf("opening the store: %v", err)
	}
	n, err := task.ParseName(name)
	if err != nil {
		r.t.Fatal(err)
	}
	who, err := user.Parse(author)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := s.Apply(n, func(task.Task) (task.Event, error) {
		return task.Scope(who, s.Now(), []string{"internal/tree/"})
	}); err != nil {
		r.t.Fatalf("scoping %s: %v", name, err)
	}
	r.ok(author, "push", name)
}

// ok runs a command and fails the test if it does not succeed.
func (r *rig) ok(who string, args ...string) result {
	r.t.Helper()
	got := r.run(who, args...)
	if got.code != fault.CodeOK {
		r.t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", args, got.code, got.stdout, got.stderr)
	}
	return got
}

func TestCreate(t *testing.T) {
	r := newRig(t)

	got := r.ok("alice", "create", "fix-the-parser", "4", "3")
	for _, want := range []string{"created draft", "fix-the-parser", "priority 4", "difficulty 3"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("output should mention %q:\n%s", want, got.stdout)
		}
	}
	// A task with no scope can do almost nothing, so the command that unblocks
	// it is printed rather than left to be discovered.
	if !strings.Contains(got.stdout, "muff scope fix-the-parser") {
		t.Errorf("create should say what to do next:\n%s", got.stdout)
	}
}

// TestCreateNormalisesAndSaysSo: the mapping from what was typed to what was
// made is never invisible.
func TestCreateNormalisesAndSaysSo(t *testing.T) {
	r := newRig(t)

	got := r.ok("alice", "create", "Fix The Parser", "4", "3")
	if !strings.Contains(got.stdout, "fix-the-parser") {
		t.Errorf("the task should be normalised:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, `"Fix The Parser" is task fix-the-parser`) {
		t.Errorf("the mapping should be reported:\n%s", got.stderr)
	}
	// And the note is on stderr, so stdout stays pipeable.
	if strings.Contains(got.stdout, "is task") {
		t.Errorf("the note leaked into stdout:\n%s", got.stdout)
	}
}

func TestCreateIsUnique(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")

	got := r.run("bob", "create", "fix-the-parser", "3", "3")
	if got.code != fault.CodeConflict {
		t.Fatalf("re-creating exited %d, want %d", got.code, fault.CodeConflict)
	}
	if !strings.Contains(got.stderr, "alice") {
		t.Errorf("the conflict should name the author:\n%s", got.stderr)
	}
}

func TestPushRequiresAScope(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")

	got := r.run("alice", "push", "fix-the-parser")
	if got.code != fault.CodeConflict {
		t.Fatalf("pushing an unscoped task exited %d, want %d", got.code, fault.CodeConflict)
	}
	if !strings.Contains(got.stderr, "scope") {
		t.Errorf("the refusal should mention the scope:\n%s", got.stderr)
	}
}

// TestADraftIsPrivate: another agent cannot see it, and is told it is missing
// rather than that they may not look.
func TestADraftIsPrivate(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")

	got := r.run("bob", "push", "fix-the-parser")
	if got.code != fault.CodeNotFound {
		t.Fatalf("bob exited %d on alice's draft, want %d", got.code, fault.CodeNotFound)
	}
	if strings.Contains(got.stderr, "may not") || strings.Contains(got.stderr, "denied") {
		t.Errorf("the refusal discloses the draft exists:\n%s", got.stderr)
	}
	// Claiming it is equally invisible.
	if got := r.run("bob", "claim", "fix-the-parser"); got.code != fault.CodeNotFound {
		t.Errorf("claiming a private draft exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

func TestClaim(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	// A scopeless stub can still be claimed, which the reference is explicit
	// about, so the whole flow needs no scope to be tested.
	got := r.ok("alice", "claim", "fix-the-parser")
	if !strings.Contains(got.stdout, "claimed fix-the-parser") {
		t.Errorf("output:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "no scope yet") {
		t.Errorf("claiming an unscoped task should say so:\n%s", got.stdout)
	}

	// Claiming your own task again is a no-op, reported as one rather than as
	// a conflict.
	again := r.ok("alice", "claim", "fix-the-parser")
	if !strings.Contains(again.stdout, "already yours") {
		t.Errorf("re-claiming should be a reported no-op:\n%s", again.stdout)
	}
}

// TestClaimRace is the race as a caller sees it: exactly one agent succeeds,
// and every loser is told who won.
func TestClaimRace(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "contested", "3", "3")
	r.pool("alice", "contested")

	const n = 16
	var wg sync.WaitGroup
	results := make([]result, n)
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = r.run("agent"+string(rune('a'+i)), "claim", "contested")
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, got := range results {
		switch got.code {
		case fault.CodeOK:
			winners++
		case fault.CodeConflict:
			if !strings.Contains(got.stderr, "already claimed by") {
				t.Errorf("loser %d was not told who won:\n%s", i, got.stderr)
			}
		default:
			t.Fatalf("agent %d exited %d:\n%s", i, got.code, got.stderr)
		}
	}
	if winners != 1 {
		t.Fatalf("%d agents claimed the task, want exactly 1", winners)
	}
}

func TestExitCodes(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")

	for _, tc := range []struct {
		name string
		who  string
		args []string
		want int
	}{
		{"success", "alice", []string{"claim", "fix-the-parser"}, fault.CodeOK},
		{"help needs no identity", "", []string{"help"}, fault.CodeOK},
		{"no command", "alice", nil, fault.CodeUsage},
		{"unknown command", "alice", []string{"frobnicate"}, fault.CodeUsage},
		{"too few arguments", "alice", []string{"create", "x"}, fault.CodeUsage},
		{"too many arguments", "alice", []string{"push", "a", "b"}, fault.CodeUsage},
		{"bad score", "alice", []string{"create", "other", "9", "3"}, fault.CodeUsage},
		{"bad name", "alice", []string{"create", "--force", "3", "3"}, fault.CodeUsage},
		{"no identity", "", []string{"push", "fix-the-parser"}, fault.CodeAuth},
		{"missing task", "alice", []string{"push", "nothing"}, fault.CodeNotFound},
		// Refused because the rig's fleet controls nobody: assign now exists,
		// and being unable to direct an agent is a denial rather than a
		// malformed command.
		{"assign without control", "alice", []string{"assign", "bob", "fix-the-parser"}, fault.CodeDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.run(tc.who, tc.args...); got.code != tc.want {
				t.Errorf("%v exited %d, want %d\nstderr: %s", tc.args, got.code, tc.want, got.stderr)
			}
		})
	}
}

// TestAssignWithoutOrcExplainsItself. `assign` is the one command that depends
// on a peer, and the refusal has to name it and say what to do instead — an
// agent that wanted the task is not helped by "orc is not installed".
func TestAssignWithoutOrcExplainsItself(t *testing.T) {
	r := newRig(t)
	r.control = nil // no injected fleet, and no `orc` on PATH under test

	got := r.run("alice", "assign", "bob", "anything")
	if got.code != fault.CodeUnavailable {
		t.Errorf("exited %d, want %d", got.code, fault.CodeUnavailable)
	}
	for _, want := range []string{"orc", "muff claim"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got.stderr)
		}
	}
}

func TestDeniedWhenSomeoneElseOwnsIt(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "claim", "fix-the-parser")

	// Bob still cannot see the draft, so push is not-found rather than denied —
	// privacy outranks permission, and the test says which is which.
	if got := r.run("bob", "push", "fix-the-parser"); got.code != fault.CodeNotFound {
		t.Errorf("bob exited %d on alice's claimed draft, want %d", got.code, fault.CodeNotFound)
	}
}

func TestHelpNeedsNoIdentity(t *testing.T) {
	r := newRig(t)
	for _, arg := range []string{"help", "-h", "--help"} {
		got := r.run("", arg)
		if got.code != fault.CodeOK {
			t.Errorf("%s exited %d", arg, got.code)
		}
		for _, want := range []string{"ORC_USER", "ORC_THEME", "create", "push", "claim"} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("%s should mention %q:\n%s", arg, want, got.stdout)
			}
		}
	}
}

// TestMainSurvivesBrokenStreams: a command with no output streams must exit
// with a code rather than panicking on a nil writer.
func TestMainSurvivesBrokenStreams(t *testing.T) {
	if got := cli.Main(cli.App{}, []string{"push", "x"}); got != fault.CodeInternal {
		t.Errorf("Main without streams exited %d, want %d", got, fault.CodeInternal)
	}
}

// TestOutputIsUncolouredWhenNotATerminal.
func TestOutputIsUncoloured(t *testing.T) {
	r := newRig(t)
	got := r.ok("alice", "create", "fix-the-parser", "3", "3")
	if strings.Contains(got.stdout, "\x1b[") {
		t.Errorf("escape sequences reached a buffer:\n%q", got.stdout)
	}
}

func TestPoolAndInfo(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "4", "3")
	r.ok("alice", "create", "ship-the-docs", "5", "2")

	board := r.ok("alice", "pool")
	for _, want := range []string{"pool · alice", "fix-the-parser", "ship-the-docs", "draft"} {
		if !strings.Contains(board.stdout, want) {
			t.Errorf("the board should show %q:\n%s", want, board.stdout)
		}
	}
	// Priority 5 outranks priority 4, so ship-the-docs is first.
	if i, j := strings.Index(board.stdout, "ship-the-docs"), strings.Index(board.stdout, "fix-the-parser"); i > j {
		t.Errorf("the board should sort by priority:\n%s", board.stdout)
	}

	card := r.ok("alice", "info", "fix-the-parser")
	for _, want := range []string{"fix-the-parser", "P4", "D3", "author", "alice", "scope", "none yet"} {
		if !strings.Contains(card.stdout, want) {
			t.Errorf("the card should show %q:\n%s", want, card.stdout)
		}
	}
}

// TestTheBoardShowsNoOneElsesDrafts.
func TestBoardFiltersDrafts(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "alices-draft", "3", "3")
	r.ok("bob", "create", "bobs-draft", "3", "3")

	got := r.ok("alice", "pool")
	if !strings.Contains(got.stdout, "alices-draft") {
		t.Errorf("alice should see her own draft:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "bobs-draft") {
		t.Errorf("alice should not see bob's draft:\n%s", got.stdout)
	}
	// An empty pool is not an error.
	if got := r.ok("carol", "pool"); !strings.Contains(got.stdout, "no tasks") {
		t.Errorf("carol sees nothing and should be told so:\n%s", got.stdout)
	}
}

func TestInfoRefusesAPrivateDraft(t *testing.T) {
	r := newRig(t)
	r.ok("alice", "create", "fix-the-parser", "3", "3")

	got := r.run("bob", "info", "fix-the-parser")
	if got.code != fault.CodeNotFound {
		t.Errorf("bob exited %d on alice's draft, want %d", got.code, fault.CodeNotFound)
	}
}

// worktree makes a directory that looks like a git working tree.
func (r *rig) worktree(t *testing.T, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	r.cwd = resolved
	return resolved
}

func TestScopeCommand(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree", "internal/render")
	r.ok("alice", "create", "fix-the-parser", "3", "3")

	got := r.ok("alice", "scope", "fix-the-parser", "internal/tree", "cmd/anno/main.go")
	// A bare directory name becomes a directory entry, because a caller writing
	// `internal/tree` means the directory and the slash is a papercut.
	if !strings.Contains(got.stdout, "internal/tree/") {
		t.Errorf("an existing directory should become a prefix entry:\n%s", got.stdout)
	}
	// A file stays exact.
	if !strings.Contains(got.stdout, "cmd/anno/main.go") || strings.Contains(got.stdout, "cmd/anno/main.go/") {
		t.Errorf("a file should stay an exact entry:\n%s", got.stdout)
	}

	// Scope replaces rather than accumulates, so the command always states the
	// whole surface.
	again := r.ok("alice", "scope", "fix-the-parser", "internal/render")
	if strings.Contains(again.stdout, "internal/tree") {
		t.Errorf("scope should replace, not accumulate:\n%s", again.stdout)
	}

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no paths", []string{"scope", "fix-the-parser"}, fault.CodeUsage},
		{"absolute", []string{"scope", "fix-the-parser", "/etc/passwd"}, fault.CodeUsage},
		{"escaping", []string{"scope", "fix-the-parser", "../outside"}, fault.CodeUsage},
		{"recursive glob", []string{"scope", "fix-the-parser", "internal/**/x.go"}, fault.CodeUsage},
		{"missing task", []string{"scope", "nothing", "x"}, fault.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.run("alice", tc.args...); got.code != tc.want {
				t.Errorf("%v exited %d, want %d\n%s", tc.args, got.code, tc.want, got.stderr)
			}
		})
	}
}

func TestWorktreeCommand(t *testing.T) {
	r := newRig(t)
	root := r.worktree(t, "internal/tree")
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "scope", "fix-the-parser", "internal/tree")

	// Binding is owner-only, so an unclaimed task is refused with a redirect.
	denied := r.run("alice", "worktree", "fix-the-parser", root)
	if denied.code != fault.CodeDenied {
		t.Fatalf("binding an unowned task exited %d, want %d", denied.code, fault.CodeDenied)
	}
	if !strings.Contains(denied.stderr, "claim it first") {
		t.Errorf("the refusal should point at claim:\n%s", denied.stderr)
	}

	r.ok("alice", "claim", "fix-the-parser")
	if got := r.ok("alice", "worktree", "fix-the-parser", root); !strings.Contains(got.stdout, root) {
		t.Errorf("output:\n%s", got.stdout)
	}

	// A second task cannot take a worktree an active task already holds: an
	// ambiguous lookup would silently enforce the wrong scope.
	r.ok("alice", "create", "other-task", "3", "3")
	r.ok("alice", "scope", "other-task", "internal/tree")
	r.ok("alice", "claim", "other-task")
	clash := r.run("alice", "worktree", "other-task", root)
	if clash.code != fault.CodeConflict {
		t.Fatalf("re-binding exited %d, want %d", clash.code, fault.CodeConflict)
	}
	if !strings.Contains(clash.stderr, "fix-the-parser") {
		t.Errorf("the conflict should name the holder:\n%s", clash.stderr)
	}

	// A directory that is not a worktree is refused, and so is a subdirectory.
	if got := r.run("alice", "worktree", "fix-the-parser", t.TempDir()); got.code != fault.CodeUsage {
		t.Errorf("binding a non-worktree exited %d, want %d", got.code, fault.CodeUsage)
	}
	if got := r.run("alice", "worktree", "fix-the-parser", filepath.Join(root, "internal")); got.code != fault.CodeUsage {
		t.Errorf("binding a subdirectory exited %d, want %d", got.code, fault.CodeUsage)
	}
}

// TestCheckScopeExitCodes pins the contract Anno calls: 0 in scope, 9 outside,
// 11 for a path that escapes the worktree, and nothing on stdout either way.
func TestCheckScopeExitCodes(t *testing.T) {
	r := newRig(t)
	root := r.worktree(t, "internal/tree", "internal/render")
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "scope", "fix-the-parser", "internal/tree")
	r.ok("alice", "claim", "fix-the-parser")
	r.ok("alice", "worktree", "fix-the-parser", root)

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"in scope", "internal/tree/tree.go", fault.CodeOK},
		{"the scoped directory itself", "internal/tree", fault.CodeOK},
		{"a file that does not exist yet", "internal/tree/new.go", fault.CodeOK},
		{"out of scope", "internal/render/render.go", fault.CodeScope},
		{"outside the worktree", "../elsewhere", fault.CodeEscape},
		{"absolute and outside", "/etc/passwd", fault.CodeEscape},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := r.run("alice", "check-scope", tc.path)
			if got.code != tc.want {
				t.Errorf("check-scope %q exited %d, want %d\n%s", tc.path, got.code, tc.want, got.stderr)
			}
			// Nothing on stdout either way: the status is the answer.
			if got.stdout != "" {
				t.Errorf("check-scope wrote to stdout: %q", got.stdout)
			}
		})
	}

	// Every path must be in scope for the check to pass.
	if got := r.run("alice", "check-scope", "internal/tree/a.go", "internal/render/b.go"); got.code != fault.CodeScope {
		t.Errorf("a mixed batch exited %d, want %d", got.code, fault.CodeScope)
	}
	if got := r.run("alice", "check-scope"); got.code != fault.CodeUsage {
		t.Errorf("check-scope with no paths exited %d, want %d", got.code, fault.CodeUsage)
	}
}

// TestNothingIsEnforcedWithoutATask: an agent that never opted in is never
// blocked.
func TestCheckScopeWithoutATaskInForce(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")

	got := r.run("alice", "check-scope", "anything/at/all.go")
	if got.code != fault.CodeOK {
		t.Errorf("with no task in force, check-scope exited %d, want 0\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "no task is in force") {
		t.Errorf("it should say why nothing was enforced:\n%s", got.stderr)
	}

	// A task with no scope enforces nothing either.
	r.ok("alice", "create", "stub", "3", "3")
	r.env["MUFF_TASK"] = "stub"
	if got := r.run("alice", "check-scope", "anything.go"); got.code != fault.CodeOK {
		t.Errorf("a scopeless task enforced something: exit %d\n%s", got.code, got.stderr)
	}
}

// TestMuffTaskOverridesTheWorktree is §8.1's first rule.
func TestMuffTaskOverridesTheBinding(t *testing.T) {
	r := newRig(t)
	root := r.worktree(t, "internal/tree", "internal/render")
	r.ok("alice", "create", "bound-task", "3", "3")
	r.ok("alice", "scope", "bound-task", "internal/tree")
	r.ok("alice", "claim", "bound-task")
	r.ok("alice", "worktree", "bound-task", root)

	r.ok("alice", "create", "other-task", "3", "3")
	r.ok("alice", "scope", "other-task", "internal/render")
	r.ok("alice", "claim", "other-task")
	// other-task needs no binding: MUFF_TASK names it directly, which is the
	// whole point of the override.

	// Without the variable, the binding decides.
	if got := r.run("alice", "check-scope", "internal/render/x.go"); got.code != fault.CodeScope {
		t.Errorf("the binding should be in force: exit %d", got.code)
	}
	// With it, the named task decides.
	r.env["MUFF_TASK"] = "other-task"
	if got := r.run("alice", "check-scope", "internal/render/x.go"); got.code != fault.CodeOK {
		t.Errorf("MUFF_TASK should override the binding: exit %d\n%s", got.code, got.stderr)
	}
}

// ready makes a scoped, claimed task the milestone-6 commands can act on.
func (r *rig) ready(who, name string, subs ...string) {
	r.t.Helper()
	r.ok(who, "create", name, "3", "3")
	r.ok(who, "scope", name, "internal/tree")
	r.ok(who, "claim", name)
	for _, sub := range subs {
		r.ok(who, "create", name, "--sub", sub)
	}
}

func TestStatusReportsThePreviousValue(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.ok("alice", "status", "fix-the-parser", "2")
	// The previous value is shown beside the new one, so a change is visible.
	if !strings.Contains(got.stdout, "unreported") || !strings.Contains(got.stdout, "slow") {
		t.Errorf("status should show the change:\n%s", got.stdout)
	}

	// The words work as well as the numbers.
	if got := r.ok("alice", "status", "fix-the-parser", "nominal"); !strings.Contains(got.stdout, "nominal") {
		t.Errorf("status by word:\n%s", got.stdout)
	}
	// Setting it to what it already is is a reported no-op, not a journal entry.
	if got := r.ok("alice", "status", "fix-the-parser", "3"); !strings.Contains(got.stdout, "already") {
		t.Errorf("an unchanged status should say so:\n%s", got.stdout)
	}

	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"status", "fix-the-parser", "9"}, fault.CodeUsage},
		{[]string{"status", "fix-the-parser", "unreported"}, fault.CodeUsage},
		{[]string{"status", "fix-the-parser"}, fault.CodeUsage},
		{[]string{"status", "nothing", "3"}, fault.CodeNotFound},
	} {
		if got := r.run("alice", tc.args...); got.code != tc.want {
			t.Errorf("%v exited %d, want %d", tc.args, got.code, tc.want)
		}
	}
}

func TestSubtasks(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "fuzz-it")

	got := r.ok("alice", "create", "fix-the-parser", "--sub", "document-it")
	if !strings.Contains(got.stdout, "0/2") {
		t.Errorf("adding a subtask should show the count:\n%s", got.stdout)
	}

	done := r.ok("alice", "complete", "fix-the-parser", "--sub", "fuzz-it")
	if !strings.Contains(done.stdout, "1/2") {
		t.Errorf("completing a subtask should show the count:\n%s", done.stdout)
	}

	// Finishing the last one says the task is ready to complete.
	last := r.ok("alice", "complete", "fix-the-parser", "--sub", "document-it")
	if !strings.Contains(last.stdout, "muff complete fix-the-parser") {
		t.Errorf("the last subtask should point at completing the task:\n%s", last.stdout)
	}

	// Removing one is not irreversible in the way a task is, so it needs no
	// confirmation.
	if got := r.ok("alice", "delete", "fix-the-parser", "--sub", "fuzz-it"); !strings.Contains(got.stdout, "1/1") {
		t.Errorf("removing a subtask should show the new count:\n%s", got.stdout)
	}

	// An unowned task refuses first on permission, redirecting to claim — the
	// scope gate is the *next* refusal, not this one.
	r.ok("alice", "create", "stub", "3", "3")
	if got := r.run("alice", "create", "stub", "--sub", "x"); got.code != fault.CodeDenied {
		t.Errorf("a subtask on an unowned task exited %d, want %d", got.code, fault.CodeDenied)
	}
	// Once claimed, the scope gate is what refuses it.
	r.ok("alice", "claim", "stub")
	if got := r.run("alice", "create", "stub", "--sub", "x"); got.code != fault.CodeConflict {
		t.Errorf("a subtask on a scopeless task exited %d, want %d", got.code, fault.CodeConflict)
	}
	if got := r.run("alice", "complete", "fix-the-parser", "--sub", "nothing"); got.code != fault.CodeConflict {
		t.Errorf("completing a missing subtask exited %d, want %d", got.code, fault.CodeConflict)
	}
}

// TestCompleteRefusesUnfinishedWorkAndNamesIt.
func TestCompleteRefusesUnfinishedWork(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one", "two")

	got := r.run("alice", "complete", "fix-the-parser")
	if got.code != fault.CodeConflict {
		t.Fatalf("completing with work left exited %d, want %d\n%s", got.code, fault.CodeConflict, got.stderr)
	}
	// The refusal names what is outstanding and how to override, so the caller
	// can act rather than guess.
	for _, want := range []string{"one, two", "--force", "2 unfinished"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got.stderr)
		}
	}
}

// TestForcedCompletionLeavesAMark is milestone 6's named criterion: the point
// of a tracker is that shortcuts stay visible.
func TestForcedCompletionLeavesAMark(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one", "two")
	r.ok("alice", "complete", "fix-the-parser", "--sub", "one")

	got := r.ok("alice", "complete", "fix-the-parser", "--force")
	if !strings.Contains(got.stdout, "skipping 1 subtask") || !strings.Contains(got.stdout, "two") {
		t.Errorf("a forced completion should name what it skipped:\n%s", got.stdout)
	}

	// And the journal records it, where `info` and `verify` can find it.
	data, err := os.ReadFile(filepath.Join(r.root, "store", "tasks", "fix-the-parser", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"forced":true`) {
		t.Errorf("the override left no mark in the journal:\n%s", data)
	}
	if !strings.Contains(string(data), `"skipped":["two"]`) {
		t.Errorf("the journal should name the skipped subtask:\n%s", data)
	}

	// Completing twice is a conflict, not a second completion.
	if got := r.run("alice", "complete", "fix-the-parser"); got.code != fault.CodeConflict {
		t.Errorf("completing twice exited %d, want %d", got.code, fault.CodeConflict)
	}
	// --force applies to a task, not to one subtask.
	if got := r.run("alice", "complete", "fix-the-parser", "--sub", "two", "--force"); got.code != fault.CodeUsage {
		t.Errorf("--force with --sub exited %d, want %d", got.code, fault.CodeUsage)
	}
}

// TestEveryDeleteRefusal is milestone 6's other named criterion.
func TestEveryDeleteRefusal(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one")

	// Without --yes, nothing is deleted — and the task is still there after.
	refused := r.run("alice", "delete", "fix-the-parser")
	if refused.code != fault.CodeUsage {
		t.Fatalf("delete without --yes exited %d, want %d", refused.code, fault.CodeUsage)
	}
	if !strings.Contains(refused.stdout, "1 subtask") {
		t.Errorf("it should say what would be destroyed:\n%s", refused.stdout)
	}
	if got := r.run("alice", "info", "fix-the-parser"); got.code != fault.CodeOK {
		t.Error("the refused delete removed the task anyway")
	}

	// Somebody else's task is refused, and a task nobody can see is missing.
	r.ok("alice", "push", "fix-the-parser")
	if got := r.run("bob", "delete", "fix-the-parser", "--yes"); got.code != fault.CodeDenied {
		t.Errorf("bob deleting alice's task exited %d, want %d", got.code, fault.CodeDenied)
	}
	r.ok("bob", "create", "bobs-draft", "3", "3")
	if got := r.run("alice", "delete", "bobs-draft", "--yes"); got.code != fault.CodeNotFound {
		t.Errorf("deleting an invisible draft exited %d, want %d", got.code, fault.CodeNotFound)
	}
	if got := r.run("alice", "delete", "nothing", "--yes"); got.code != fault.CodeNotFound {
		t.Errorf("deleting a missing task exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

// TestDeleteNamesWhoLosesTheTask: collaborators lose it without warning
// otherwise, and a count does not tell the caller who to mail.
func TestDeleteNamesCollaborators(t *testing.T) {
	r := newRig(t)
	root := r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one")
	r.ok("alice", "worktree", "fix-the-parser", root)

	got := r.run("alice", "delete", "fix-the-parser")
	if !strings.Contains(got.stdout, "worktree binding") {
		t.Errorf("the description should mention the binding:\n%s", got.stdout)
	}

	r.ok("alice", "delete", "fix-the-parser", "--yes")
	// The binding goes with the task: one pointing at a task that is gone would
	// make the hook enforce a scope nobody owns.
	r.ok("alice", "create", "another", "3", "3")
	r.ok("alice", "scope", "another", "internal/tree")
	r.ok("alice", "claim", "another")
	if got := r.run("alice", "worktree", "another", root); got.code != fault.CodeOK {
		t.Errorf("the worktree should be free again, got exit %d\n%s", got.code, got.stderr)
	}
}

// TestDeletionIsRecordedBeforeAnythingIsErased.
func TestDeletionIsRecorded(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one", "two")
	r.ok("alice", "delete", "fix-the-parser", "--yes")

	data, err := os.ReadFile(filepath.Join(r.root, "store", "tombstones.jsonl"))
	if err != nil {
		t.Fatalf("no deletion log: %v", err)
	}
	for _, want := range []string{`"task":"fix-the-parser"`, `"by":"alice"`, `"subtasks":2`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the deletion log should contain %s:\n%s", want, data)
		}
	}
}

func TestInvite(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.ok("alice", "invite", "bob", "fix-the-parser")
	if !strings.Contains(got.stdout, "bob") {
		t.Errorf("invite should name who joined:\n%s", got.stdout)
	}

	// Bob can now see it in his pool and act on it.
	pool := r.ok("bob", "pool")
	if !strings.Contains(pool.stdout, "fix-the-parser") {
		t.Errorf("bob should see the task he was added to:\n%s", pool.stdout)
	}
	if got := r.run("bob", "status", "fix-the-parser", "2"); got.code != fault.CodeOK {
		t.Errorf("a collaborator should be able to report status, got %d\n%s", got.code, got.stderr)
	}

	// And he was told, once, by mail.
	sent := r.mail.delivered()
	if len(sent) != 1 {
		t.Fatalf("%d notices sent, want 1: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "you are on fix-the-parser") || !strings.Contains(sent[0], "bob") {
		t.Errorf("the notice:\n%s", sent[0])
	}
}

func TestInviteRefusals(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")
	r.ok("alice", "invite", "bob", "fix-the-parser")

	// Inviting the same agent twice is a conflict, not a silent success: the
	// second caller is working from a stale picture and should be told.
	if got := r.run("alice", "invite", "bob", "fix-the-parser"); got.code != fault.CodeConflict {
		t.Errorf("a repeat invite exited %d, want %d\n%s", got.code, fault.CodeConflict, got.stderr)
	}
	// A collaborator is not an owner.
	if got := r.run("bob", "invite", "carol", "fix-the-parser"); got.code != fault.CodeDenied {
		t.Errorf("a collaborator inviting exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if got := r.run("alice", "invite", "bob"); got.code != fault.CodeUsage {
		t.Errorf("invite without a name exited %d, want %d", got.code, fault.CodeUsage)
	}
}

func TestKickAndLeave(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")
	r.ok("alice", "invite", "bob", "fix-the-parser")
	r.ok("alice", "invite", "carol", "fix-the-parser")

	r.ok("alice", "kick", "bob", "fix-the-parser")
	if got := r.run("bob", "status", "fix-the-parser", "2"); got.code == fault.CodeOK {
		t.Error("a removed collaborator can still act on the task")
	}
	// Carol removes herself, which needs no owner.
	r.ok("carol", "leave", "fix-the-parser")

	// The owner cannot leave: a task with collaborators and no owner has nobody
	// who can hand it on, so it must be completed or deleted instead. This is a
	// permission rule rather than a state conflict — the policy table refuses it
	// before the event is ever built.
	got := r.run("alice", "leave", "fix-the-parser")
	if got.code != fault.CodeDenied {
		t.Errorf("the owner leaving exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if !strings.Contains(got.stderr, "complete") && !strings.Contains(got.stderr, "delete") {
		t.Errorf("the refusal should say what to do instead:\n%s", got.stderr)
	}

	// Leaving something you are not on is not a silent success either.
	if got := r.run("bob", "leave", "fix-the-parser"); got.code == fault.CodeOK {
		t.Error("leaving a task you are not on succeeded")
	}
}

// TestABrokenMailmanNeverFailsAMembershipChange is milestone 7's criterion. The
// membership change is the fact; the mail is only the announcement.
func TestBrokenMailmanQueuesAndDrains(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	r.mail.breaks(errors.New("mailman is not installed"))
	got := r.ok("alice", "invite", "bob", "fix-the-parser")
	if !strings.Contains(got.stdout, "bob") {
		t.Errorf("the invite still happened, so it should still say so:\n%s", got.stdout)
	}
	// The failure is reported, but on stderr and without an exit code.
	if !strings.Contains(got.stderr, "queued") {
		t.Errorf("a failed notice should say it is queued:\n%s", got.stderr)
	}
	// It really did happen.
	if got := r.run("bob", "status", "fix-the-parser", "2"); got.code != fault.CodeOK {
		t.Errorf("the membership change did not survive the failed notice: %d", got.code)
	}

	// Mailman comes back, and the *next* command delivers what was queued —
	// no daemon, no timer, and not necessarily the process that queued it.
	r.mail.breaks(nil)
	before := len(r.mail.delivered())
	r.ok("alice", "pool")
	sent := r.mail.delivered()
	if len(sent) != before+1 {
		t.Fatalf("the next command delivered %d notices, want 1", len(sent)-before)
	}
	if !strings.Contains(sent[len(sent)-1], "you are on fix-the-parser") {
		t.Errorf("the drained notice:\n%s", sent[len(sent)-1])
	}

	// Once drained, it is gone rather than re-sent on every command.
	attempts := r.mail.attempts()
	r.ok("alice", "pool")
	if r.mail.attempts() != attempts {
		t.Error("a delivered notice was sent again")
	}
}

// TestAStuckNoticeStopsBeingRetried: a queue that retries forever buries the
// problem it is trying to report.
func TestStuckNoticeIsReportedNotRetriedForever(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	r.mail.breaks(errors.New("permanently broken"))
	r.ok("alice", "invite", "bob", "fix-the-parser")

	for range store.MaxAttempts + 3 {
		r.ok("alice", "pool")
	}
	settled := r.mail.attempts()
	r.ok("alice", "pool")
	if r.mail.attempts() != settled {
		t.Errorf("a stuck notice is still being retried after %d attempts", store.MaxAttempts)
	}
	// And every one of those commands still worked.
	if got := r.run("alice", "pool"); got.code != fault.CodeOK {
		t.Errorf("a stuck outbox failed a command: %d\n%s", got.code, got.stderr)
	}
	if !strings.Contains(r.run("alice", "pool").stderr, "gave up") {
		t.Error("a stuck notice should be reported rather than left silent")
	}
}
