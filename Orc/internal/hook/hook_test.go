package hook_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/event"
	"orc/orc/internal/hook"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

// rig is a fleet with one identity that holds one write clause and one read clause.
//
// It is built through the store rather than through the CLI, so this package's tests do
// not depend on the CLI compiling — the hook is the boundary and has to be testable on
// its own.
type rig struct {
	t     *testing.T
	store *store.Store
	who   user.Name
	root  string
}

func newRig(t *testing.T) *rig {
	t.Helper()

	root := filepath.Join(t.TempDir(), "fleet")
	s, err := store.Create(root, clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	boss := mustUser(t, "boss")
	if _, err := s.CreateIdentity(boss, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("operator: %v", err)
	}
	if err := s.SetOperator(boss); err != nil {
		t.Fatalf("operator: %v", err)
	}

	ember := mustUser(t, "ember")
	if _, err := s.CreateIdentity(ember, "0000000b-00000002", boss); err != nil {
		t.Fatalf("identity: %v", err)
	}

	patterns, err := model.ParsePatterns([]string{"read(Anno/**)", "write(Anno/internal/**)"})
	if err != nil {
		t.Fatalf("patterns: %v", err)
	}
	if _, err := s.CreatePermission(mustName(t, "edit-anno"), mustAuthority(t, 40), patterns); err != nil {
		t.Fatalf("permission: %v", err)
	}
	if _, err := s.CreateRole(mustName(t, "engineer"), mustAuthority(t, 60), "writes the code"); err != nil {
		t.Fatalf("role: %v", err)
	}
	if _, err := s.ApplyRole(mustName(t, "engineer"), func(model.Role) (model.RoleEvent, error) {
		return model.Permit(boss, epoch, mustName(t, "edit-anno"))
	}); err != nil {
		t.Fatalf("permit: %v", err)
	}
	if _, err := s.ApplyIdentity(ember, func(model.Identity) (model.IdentityEvent, error) {
		return model.AssignRole(boss, epoch, mustName(t, "engineer"))
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	return &rig{t: t, store: s, who: ember, root: root}
}

// workspace is what a clause is relative to, and what a test writes its files under.
func (r *rig) workspace() string { return r.store.WorkspaceDir(r.who) }

// permit gives ember another permission, so a test can say what changes when a
// clause is held rather than only what happens without one.
func (r *rig) permit(name string, clauses ...string) {
	r.t.Helper()
	patterns, err := model.ParsePatterns(clauses)
	if err != nil {
		r.t.Fatalf("patterns: %v", err)
	}
	if _, err := r.store.CreatePermission(mustName(r.t, name), mustAuthority(r.t, 40), patterns); err != nil {
		r.t.Fatalf("permission: %v", err)
	}
	boss := mustUser(r.t, "boss")
	if _, err := r.store.ApplyRole(mustName(r.t, "engineer"), func(model.Role) (model.RoleEvent, error) {
		return model.Permit(boss, epoch, mustName(r.t, name))
	}); err != nil {
		r.t.Fatalf("permit: %v", err)
	}
}

// as builds options for one identity, with an optional extra environment.
func (r *rig) as(name string, extra map[string]string) hook.Options {
	env := map[string]string{"ORC_IDENTITY": name, "ORC_SESSION": "0f9a1a6a-0000-4000-8000-000000000000"}
	for k, v := range extra {
		env[k] = v
	}
	return hook.Options{
		Root:  r.root,
		Clock: clock.NewFake(epoch, time.Second),
		Env: func(key string) (string, bool) {
			v, ok := env[key]
			return v, ok
		},
	}
}

// call fires the hook with a payload, and returns the outcome.
func (r *rig) call(opts hook.Options, p map[string]any) hook.Outcome {
	r.t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		r.t.Fatalf("payload: %v", err)
	}
	return hook.Run(raw, opts)
}

// tool builds a PreToolUse payload for a file tool.
func tool(name, path, cwd string) map[string]any {
	return map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "0f9a1a6a-0000-4000-8000-000000000000",
		"tool_name":       name,
		"cwd":             cwd,
		"tool_input":      map[string]any{"file_path": path},
	}
}

func mustUser(t *testing.T, raw string) user.Name {
	t.Helper()
	n, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("user %q: %v", raw, err)
	}
	return n
}

func mustName(t *testing.T, raw string) model.Name {
	t.Helper()
	n, err := model.ParseName(raw)
	if err != nil {
		t.Fatalf("name %q: %v", raw, err)
	}
	return n
}

func mustAuthority(t *testing.T, n int) model.Authority {
	t.Helper()
	a, err := model.NewAuthority(n)
	if err != nil {
		t.Fatalf("authority %d: %v", n, err)
	}
	return a
}

// TestLiveRung: with the store readable, the hook decides from current permissions.
func TestLiveRung(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	cases := []struct {
		name  string
		tool  string
		path  string
		block bool
	}{
		{"a write inside the clause", "Edit", ws + "/Anno/internal/tree.go", false},
		{"a write outside the clause", "Edit", ws + "/Common/user/user.go", true},
		{"a write the read clause covers but write does not", "Write", ws + "/Anno/main.go", true},
		{"a read inside the clause", "Read", ws + "/Anno/main.go", false},
		{"a read outside the clause", "Read", ws + "/Docs/Vision.md", true},
		{"a read outside the workspace", "Read", "/etc/hosts", true},
		{"a tool orc does not govern", "WebFetch", "", false},
		{"a relative path, resolved against cwd", "Edit", "internal/tree.go", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cwd := ws
			if !filepath.IsAbs(c.path) {
				cwd = ws + "/Anno"
			}
			out := r.call(opts, tool(c.tool, c.path, cwd))
			blocked := out.Code == hook.CodeBlock
			if blocked != c.block {
				t.Errorf("blocked = %v, want %v\n%s", blocked, c.block, out.Stderr)
			}
			if blocked && !strings.Contains(out.Stderr, "orc:") {
				t.Errorf("a refusal must say who refused:\n%s", out.Stderr)
			}
		})
	}
}

// TestSnapshotRung: with the live store unreadable, the hook decides from the snapshot
// written at populate — and says the decision is a stale one.
func TestSnapshotRung(t *testing.T) {
	r := newRig(t)
	ws := r.workspace()

	// The snapshot as `orc employ` would have written it.
	clauses := []authz.Clause{clauseOf(t, "write(Anno/internal/**)")}
	if err := r.store.WriteAuthz(r.who, store.Freeze(r.who, "0f9a1a6a-0000-4000-8000-000000000000",
		clock.Format(epoch), clauses, 0)); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Break the live derivation without touching the snapshot: an identity whose boss
	// does not exist makes the fleet refuse to derive, which is exactly the state the
	// second rung is for.
	if err := os.WriteFile(filepath.Join(r.root, "identities", "boss", "identity.json"),
		[]byte(`{"version":1,"name":"boss","id":"nope","boss":"","created":"2026-07-25T12:00:00.000Z"}`+"\n"),
		0o600); err != nil {
		t.Fatalf("breaking the fleet: %v", err)
	}

	opts := r.as("ember", nil)
	if out := r.call(opts, tool("Edit", ws+"/Anno/internal/tree.go", ws)); out.Code != hook.CodeOK {
		t.Errorf("a write the snapshot permits was blocked:\n%s", out.Stderr)
	}

	out := r.call(opts, tool("Edit", ws+"/Common/user.go", ws))
	if out.Code != hook.CodeBlock {
		t.Fatalf("a write the snapshot does not permit was allowed")
	}
	if !strings.Contains(out.Stderr, "this session started with") {
		t.Errorf("the refusal does not say the decision is the one from populate:\n%s", out.Stderr)
	}
}

// TestBlindRung is the rule that differs from every other hook in this tree: with
// nothing readable, reads pass and writes block.
func TestBlindRung(t *testing.T) {
	r := newRig(t)
	ws := r.workspace()

	// No store at all: the third rung.
	opts := hook.Options{
		Root:  filepath.Join(t.TempDir(), "gone"),
		Clock: clock.NewFake(epoch, time.Second),
		Env: func(key string) (string, bool) {
			v, ok := map[string]string{"ORC_IDENTITY": "ember"}[key]
			return v, ok
		},
	}

	if out := r.call(opts, tool("Read", ws+"/anything.go", ws)); out.Code != hook.CodeOK {
		t.Errorf("a read was blocked on the blind rung:\n%s", out.Stderr)
	}

	out := r.call(opts, tool("Edit", ws+"/anything.go", ws))
	if out.Code != hook.CodeBlock {
		t.Fatalf("a write was allowed with nothing readable")
	}
	for _, want := range []string{"cannot tell", "orc doctor"} {
		if !strings.Contains(out.Stderr, want) {
			t.Errorf("the refusal is missing %q — it is the one that costs an agent that did nothing wrong:\n%s",
				want, out.Stderr)
		}
	}
}

// TestSubagentIsAlwaysBlocked: the denial the load accounting depends on, enforced by
// the hook rather than by a settings rule — because a rule that might be ignored under
// bypassPermissions cannot be the only thing holding it.
func TestSubagentIsAlwaysBlocked(t *testing.T) {
	r := newRig(t)

	for _, opts := range []hook.Options{
		r.as("ember", nil),
		// Even with no store readable at all.
		{Root: filepath.Join(t.TempDir(), "gone"), Clock: clock.NewFake(epoch, time.Second),
			Env: func(k string) (string, bool) {
				return map[string]string{"ORC_IDENTITY": "ember"}[k], k == "ORC_IDENTITY"
			}},
	} {
		out := r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Agent",
			"tool_input":      map[string]any{"description": "go and do a thing"},
		})
		if out.Code != hook.CodeBlock {
			t.Fatalf("the Agent tool was allowed")
		}
		if !strings.Contains(out.Stderr, "orc employ") {
			t.Errorf("the refusal does not name the way to get more hands:\n%s", out.Stderr)
		}
	}
}

// TestKeyringIsAnEscape: reaching for the store is refused whatever permissions say,
// and the message is about containment rather than about permission.
func TestKeyringIsAnEscape(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)

	for _, c := range []struct{ name, tool, path string }{
		{"reading a key", "Read", filepath.Join(r.root, "identities", "boss", "key")},
		{"reading the store root", "Read", r.root},
		{"writing into the store", "Write", filepath.Join(r.root, "identities", "ember", "identity.json")},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := r.call(opts, tool(c.tool, c.path, r.workspace()))
			if out.Code != hook.CodeBlock {
				t.Fatalf("the store was reachable")
			}
			if !strings.Contains(out.Stderr, "plaintext") {
				t.Errorf("the refusal does not say what the directory holds:\n%s", out.Stderr)
			}
		})
	}

	// And through a shell command, which is the shape §7.5 says is only partly
	// covered — the recognised shapes are refused, and the doc says the rest are not.
	out := r.call(opts, map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"cwd":             r.workspace(),
		"tool_input":      map[string]any{"command": "cat $ORC_HOME/identities/boss/key"},
	})
	if out.Code != hook.CodeBlock {
		t.Errorf("a shell command reaching for the keyring was allowed:\n%s", out.Stderr)
	}
}

// TestTheShellIsShutByDefault is the rule `shell(...)` exists for.
//
// It used to be the other way round: every command passed except the two shapes
// the hook could read, and a test here said so — precisely because it was a hole
// somebody would have to change the documentation to close. This is that change.
func TestTheShellIsShutByDefault(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"cwd":             ws,
			"tool_input":      map[string]any{"command": command},
		})
	}

	// ember holds no shell clause, so it has the innocuous set and nothing else.
	if out := bash("echo hello"); out.Code != hook.CodeOK {
		t.Errorf("an innocuous command was blocked:\n%s", out.Stderr)
	}
	for _, command := range []string{
		"ls",
		"sed -i s/a/b/ " + ws + "/Common/user/user.go",
		"curl https://example.com",
		// The toolkit runs without a shell clause, but the parts that do not check
		// their caller still need one.
		"orc bootstrap",
		"orc env ember",
		"mailman admin mail",
	} {
		if out := bash(command); out.Code != hook.CodeBlock {
			t.Errorf("%q ran with no shell permission:\n%s", command, out.Stderr)
		}
	}

	// The toolkit itself runs: an agent that cannot ask what it may do has to guess,
	// and `orc introspect` is the command that answers it.
	for _, command := range []string{
		"orc introspect",
		"muff pool",
		"mailman inbox",
	} {
		if out := bash(command); out.Code != hook.CodeOK {
			t.Errorf("%q was blocked with no shell permission:\n%s", command, out.Stderr)
		}
	}

	// And what the file-reading tools are pointed at is still decided by the
	// clauses, which is what keeps them off the `cat` objection: ember may write
	// Anno/internal/** and nothing else.
	if out := bash("anno write Anno/internal/tree.go"); out.Code != hook.CodeOK {
		t.Errorf("anno write inside the write clause was blocked:\n%s", out.Stderr)
	}
	if out := bash("anno write Communique/internal/web/app.js"); out.Code != hook.CodeBlock {
		t.Errorf("anno write outside every write clause ran:\n%s", out.Stderr)
	}
	if out := bash("anno read " + ws + "/Common/user/user.go"); out.Code != hook.CodeBlock {
		t.Errorf("anno read outside every read clause ran:\n%s", out.Stderr)
	}

	// The refusal names the command rather than the line, and says what to ask for.
	out := bash("ls -la")
	if !strings.Contains(out.Stderr, "you may not run ls") ||
		!strings.Contains(out.Stderr, "shell(ls)") {
		t.Errorf("the refusal should name the command and the clause:\n%s", out.Stderr)
	}
}

// A clause opens exactly what it names, and nothing it does not.
func TestAShellClauseOpensWhatItNames(t *testing.T) {
	r := newRig(t)
	r.permit("run-anno", "shell(anno ls)")
	opts := r.as("ember", nil)
	ws := r.workspace()

	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"cwd":             ws,
			"tool_input":      map[string]any{"command": command},
		})
	}

	if out := bash("anno write Anno/internal/tree.go"); out.Code != hook.CodeOK {
		t.Errorf("an in-scope anno write was blocked:\n%s", out.Stderr)
	}
	if out := bash("ls -la"); out.Code != hook.CodeOK {
		t.Errorf("a named command was blocked:\n%s", out.Stderr)
	}
	if out := bash("rm -rf /"); out.Code != hook.CodeBlock {
		t.Error("a command the clause does not name was allowed")
	}

	// The shell gate says which commands may run; it does not decide what they
	// touch. `anno write` is still checked against the write clauses, and that is
	// what stops this one.
	if out := bash("cd " + ws + " && anno write Common/user/user.go"); out.Code != hook.CodeBlock {
		t.Error("an out-of-scope anno write was allowed")
	}

	// Every command in a line is checked, not only the first.
	if out := bash("ls && rm -rf /"); out.Code != hook.CodeBlock {
		t.Error("only the first command in the line was checked")
	}
}

// A line that hides what it runs needs the one clause that covers everything,
// because every narrower clause would be deciding on a name that says nothing
// about what happens.
func TestAHiddenCommandNeedsEverything(t *testing.T) {
	r := newRig(t)
	r.permit("run-echo", "shell(echo sh)")
	opts := r.as("ember", nil)
	ws := r.workspace()

	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": ws,
			"tool_input": map[string]any{"command": command},
		})
	}

	for _, command := range []string{"echo $(rm -rf /)", "echo `whoami`", "echo ${HOME}"} {
		out := bash(command)
		if out.Code != hook.CodeBlock {
			t.Errorf("%q was allowed by a narrow clause", command)
			continue
		}
		if !strings.Contains(out.Stderr, "hides what it runs") {
			t.Errorf("%q should be refused as unreadable, not as unnamed:\n%s", command, out.Stderr)
		}
	}

	// An interpreter is *not* one of these any more. Its name is knowable, which
	// is the question a clause answers, so `shell(sh)` permits `sh -c` — and what
	// that grants is a shell, which is why the toolkit prices it beside
	// shell-all rather than beside the compilers.
	if out := bash("sh -c rm"); out.Code != hook.CodeOK {
		t.Errorf("shell(sh) names sh, so `sh -c` should run:\n%s", out.Stderr)
	}

	// shell(**) is the one clause that can honestly cover a substitution.
	wide := newRig(t)
	wide.permit("run-anything", "shell(**)")
	if out := wide.call(wide.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": wide.workspace(),
		"tool_input": map[string]any{"command": "echo $(whoami)"},
	}); out.Code != hook.CodeOK {
		t.Errorf("shell(**) did not cover an opaque line:\n%s", out.Stderr)
	}
}

// `except` is the grammar every other kind already has: it takes back out of a
// wide clause, rather than needing a list of everything else.
func TestShellExcept(t *testing.T) {
	r := newRig(t)
	r.permit("almost-everything", "shell(** except rm curl)")
	opts := r.as("ember", nil)

	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": r.workspace(),
			"tool_input": map[string]any{"command": command},
		})
	}

	if out := bash("git status"); out.Code != hook.CodeOK {
		t.Errorf("a command inside ** was blocked:\n%s", out.Stderr)
	}
	for _, command := range []string{"rm -rf /", "curl https://example.com"} {
		if out := bash(command); out.Code != hook.CodeBlock {
			t.Errorf("%q was allowed by a clause that excepts it", command)
		}
	}
	// The exception holds however the command is reached.
	if out := bash("cd /tmp && /bin/rm x"); out.Code != hook.CodeBlock {
		t.Error("an excepted command reached by its full path was allowed")
	}
}

// TestNoIdentityNoOpinion: a hook fired outside an Orc session has nothing to enforce.
func TestNoIdentityNoOpinion(t *testing.T) {
	r := newRig(t)
	opts := hook.Options{Root: r.root, Clock: clock.NewFake(epoch, time.Second),
		Env: func(string) (string, bool) { return "", false }}

	if out := r.call(opts, tool("Edit", "/anywhere/at/all.go", "/tmp")); out.Code != hook.CodeOK {
		t.Errorf("a session orc did not start was policed:\n%s", out.Stderr)
	}
}

// TestOnlyAViolationBlocks is the rule that outranks everything else, because this
// fires on every tool call in somebody's live session.
func TestOnlyAViolationBlocks(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)

	inputs := []string{
		``, `{`, `[]`, `null`, `"a string"`, `{}`,
		`{"hook_event_name":""}`,
		`{"hook_event_name":"Nonsense","tool_name":"Edit"}`,
		`{"hook_event_name":"PreToolUse"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Edit"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":""}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/x"}}`,
		`{"hook_event_name":"Stop"}`,
	}
	for _, in := range inputs {
		out := hook.Run([]byte(in), opts)
		if out.Code != hook.CodeOK {
			t.Errorf("input %q exited %d; only a genuine violation may block\n%s", in, out.Code, out.Stderr)
		}
	}
}

// FuzzRun is the general form of the rule above: no input produces an exit other than
// 0 or 2, and none of them panics.
func FuzzRun(f *testing.F) {
	r := newRig(f2t(f))
	opts := r.as("ember", nil)

	for _, seed := range []string{
		`{}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"x"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Agent"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"anno write x"}}`,
		`{"hook_event_name":"UserPromptSubmit","transcript_path":"/tmp/t.jsonl"}`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := hook.Run([]byte(in), opts)
		if out.Code != hook.CodeOK && out.Code != hook.CodeBlock {
			t.Fatalf("input %q exited %d", in, out.Code)
		}
	})
}

// f2t lets the rig be built from a fuzz target, which needs a *testing.T shape for its
// helpers. The fuzz harness gives an *F; this borrows its failure behaviour.
func f2t(f *testing.F) *testing.T {
	f.Helper()
	var t testing.T
	return &t
}

// TestNeverWritesFleetState is the guarantee, restated precisely because the plan's
// first wording was too broad.
//
// The hook *does* write: it appends to its own session's event feed. What it must never
// touch is fleet state — identities, roles, permissions, journals — because a hook that
// could would put a lock in the path of every edit and turn a journal into a log of
// keystrokes. So the fleet is fingerprinted before and after, and the feed is expected
// to grow.
func TestNeverWritesFleetState(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	before := fingerprint(t, r.root, "session")

	for _, p := range []map[string]any{
		tool("Edit", ws+"/Anno/internal/tree.go", ws),
		tool("Edit", ws+"/Common/user.go", ws),
		tool("Read", ws+"/Anno/main.go", ws),
		{"hook_event_name": "PreToolUse", "tool_name": "Agent"},
		{"hook_event_name": "UserPromptSubmit", "transcript_path": "/tmp/t.jsonl",
			"session_id": "0f9a1a6a-0000-4000-8000-000000000000"},
		{"hook_event_name": "Stop"},
	} {
		r.call(opts, p)
	}

	if after := fingerprint(t, r.root, "session"); after != before {
		t.Errorf("the hook changed fleet state:\nbefore %s\nafter  %s", before, after)
	}

	// And the feed did grow, which is the other half of the claim.
	events, _, err := event.Read(r.store.EventsPath(r.who))
	if err != nil {
		t.Fatalf("reading the feed: %v", err)
	}
	if len(events) != 6 {
		t.Errorf("the feed has %d events, want 6 — one per firing", len(events))
	}
}

// TestFeedRecordsTheDecision: what the view needs is in the feed, including the verdict
// and the reason, and the transcript path on the events that carry it.
func TestFeedRecordsTheDecision(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	r.call(opts, map[string]any{
		"hook_event_name": "UserPromptSubmit", "session_id": "0f9a1a6a-0000-4000-8000-000000000000",
		"transcript_path": "/tmp/transcript.jsonl",
	})
	r.call(opts, tool("Edit", ws+"/Anno/internal/tree.go", ws))
	r.call(opts, tool("Edit", ws+"/Common/user.go", ws))

	events, skipped, err := event.Read(r.store.EventsPath(r.who))
	if err != nil {
		t.Fatalf("reading the feed: %v", err)
	}
	if skipped != 0 {
		t.Errorf("%d bytes of the feed were dropped", skipped)
	}
	if len(events) != 3 {
		t.Fatalf("the feed has %d events, want 3", len(events))
	}

	if events[0].Turn != 1 || events[0].Transcript != "/tmp/transcript.jsonl" {
		t.Errorf("the first turn is not numbered or has no transcript: %+v", events[0])
	}
	if events[1].Verdict != event.VerdictAllow {
		t.Errorf("an allowed edit reads as %q", events[1].Verdict)
	}
	if events[2].Verdict != event.VerdictBlock || events[2].Reason == "" {
		t.Errorf("a blocked edit has no verdict or no reason: %+v", events[2])
	}
	if strings.Contains(events[2].Reason, "\n") {
		t.Errorf("the feed's reason is multi-line, which a table cannot draw: %q", events[2].Reason)
	}
	if events[1].Path == "" {
		t.Errorf("the feed does not say which path was touched")
	}

	// A second turn numbers itself from the feed.
	r.call(opts, map[string]any{
		"hook_event_name": "UserPromptSubmit", "session_id": "0f9a1a6a-0000-4000-8000-000000000000",
	})
	events, _, err = event.Read(r.store.EventsPath(r.who))
	if err != nil {
		t.Fatalf("reading the feed: %v", err)
	}
	if last := events[len(events)-1]; last.Turn != 2 {
		t.Errorf("the second turn is numbered %d, want 2", last.Turn)
	}
}

// TestDeadlineDoesNotStallASession stalls the store *for real* rather than pretending
// to, because the bug this catches is not in the timer.
//
// The store is stalled by making its version file a FIFO with no writer — Macmuffin's
// trick, for the same reason. A hook that only bounded the part of the check that reads
// permissions would hang here forever, in `store.Read`, before any inner timer was ever
// consulted. Which is exactly what the first version of this did.
func TestDeadlineDoesNotStallASession(t *testing.T) {
	r := newRig(t)

	stalled := filepath.Join(t.TempDir(), "stalled")
	if err := os.MkdirAll(stalled, 0o700); err != nil {
		t.Fatalf("making the stalled store: %v", err)
	}
	if err := makeFIFO(filepath.Join(stalled, "version")); err != nil {
		t.Skipf("no fifo on this platform: %v", err)
	}

	opts := hook.Options{
		Root:     stalled,
		Clock:    clock.NewFake(epoch, time.Second),
		Deadline: 150 * time.Millisecond,
		Env: func(key string) (string, bool) {
			v, ok := map[string]string{"ORC_IDENTITY": "ember"}[key]
			return v, ok
		},
	}

	start := time.Now()
	out := r.call(opts, tool("Edit", r.workspace()+"/Anno/internal/tree.go", r.workspace()))
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("the check took %s against a stalled store; the deadline did not bound it", elapsed)
	}
	if out.Code != hook.CodeBlock {
		t.Errorf("a stalled store passed a write; the blind rung blocks writes")
	}
	if !strings.Contains(out.Stderr, "did not answer") {
		t.Errorf("the refusal does not distinguish slow from broken:\n%s", out.Stderr)
	}

	// And a read still passes, because a blocked read discloses nothing and produces a
	// confused agent.
	if out := r.call(opts, tool("Read", r.workspace()+"/Anno/main.go", r.workspace())); out.Code != hook.CodeOK {
		t.Errorf("a stalled store blocked a read:\n%s", out.Stderr)
	}
}

// TestMainWiring: the process-level entry point maps an outcome onto Claude's contract
// and writes the refusal where the agent will see it.
func TestMainWiring(t *testing.T) {
	r := newRig(t)
	ws := r.workspace()

	var stderr bytes.Buffer
	in := strings.NewReader(mustJSON(t, tool("Edit", ws+"/Common/user.go", ws)))
	code := hook.Main(in, &stderr, r.as("ember", nil))

	if code != hook.CodeBlock {
		t.Errorf("Main exited %d, want %d", code, hook.CodeBlock)
	}
	if !strings.Contains(stderr.String(), "you may not write") {
		t.Errorf("the refusal did not reach stderr:\n%s", stderr.String())
	}

	// A broken stream is not a reason to block somebody's tool call.
	if code := hook.Main(badReader{}, &stderr, r.as("ember", nil)); code != hook.CodeOK {
		t.Errorf("an unreadable stdin exited %d, want %d", code, hook.CodeOK)
	}
}

type badReader struct{}

func (badReader) Read([]byte) (int, error) { return 0, fmt.Errorf("no") }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	return string(data)
}

func clauseOf(t *testing.T, raw string) authz.Clause {
	t.Helper()
	p, err := model.ParsePattern(raw)
	if err != nil {
		t.Fatalf("pattern %q: %v", raw, err)
	}
	return authz.Clause{Pattern: p, Asked: p}
}

// fingerprint hashes every file under root, skipping any path containing skip.
func fingerprint(t *testing.T, root, skip string) string {
	t.Helper()

	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.Contains(path, skip) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, path+" "+hex.EncodeToString(sum[:8]))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// Running as somebody else is not what a clause described.
//
// `shell(ls)` named `ls`, not `ls` as root — and `sudo`'s flags take values, so
// reading past them named a person: `sudo -u bob ls` came out as running `bob`,
// which is a refusal nobody could act on.
func TestSudoIsNotACommandNameToMatchOn(t *testing.T) {
	r := newRig(t)
	r.permit("run-ls", "shell(ls)")
	opts := r.as("ember", nil)
	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": r.workspace(),
			"tool_input": map[string]any{"command": command},
		})
	}

	if out := bash("ls -la"); out.Code != hook.CodeOK {
		t.Errorf("the named command was blocked:\n%s", out.Stderr)
	}
	for _, command := range []string{"sudo ls", "sudo -u bob ls", "doas ls"} {
		out := bash(command)
		if out.Code != hook.CodeBlock {
			t.Errorf("%q was covered by shell(ls)", command)
			continue
		}
		if strings.Contains(out.Stderr, "run bob") {
			t.Errorf("%q named a person as the command:\n%s", command, out.Stderr)
		}
	}
}

// TestMailmanNeedsNoPermission is the rule that mail is not a privilege.
//
// An agent is told what to do by mail and reports by mail. If reading it took a
// clause, a newly created identity would be deaf until somebody noticed — and
// the clause would not be narrowing anything, because mailman authenticates
// every command against the caller's own key and shows it its own mailbox and
// no other.
func TestMailmanNeedsNoPermission(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"cwd":             ws,
			"tool_input":      map[string]any{"command": command},
		})
	}

	// ember holds no shell clause at all.
	for _, command := range []string{
		"mailman inbox",
		"mailman send rowan --subject hello",
		"mailman reply 3fa2",
		"mailman check",
		"/usr/local/bin/mailman inbox",
		"cd " + ws + " && mailman inbox",
		"echo checking && mailman inbox",
	} {
		if out := bash(command); out.Code != hook.CodeOK {
			t.Errorf("%q needed a permission:\n%s", command, out.Stderr)
		}
	}
}

// The one part of mailman that does not authenticate is the one part that needs
// a clause. `mailman admin` provisions mailboxes and can name the owner who may
// read the store whole, so an agent that could run it could read the fleet's
// mail.
func TestMailmanAdminStillNeedsAPermission(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	ws := r.workspace()

	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"cwd":             ws,
			"tool_input":      map[string]any{"command": command},
		})
	}

	for _, command := range []string{
		"mailman admin user list",
		"mailman admin owner ember",
		"mailman admin mail --json",
		// The flag-value shape: `--key` takes a separate value, so the subcommand
		// is not in a fixed position and must not be looked for in one.
		"mailman --key x admin user add mole",
		"echo hello && mailman admin mail",
	} {
		if out := bash(command); out.Code != hook.CodeBlock {
			t.Errorf("%q ran with no shell permission:\n%s", command, out.Stderr)
		}
	}

	// The refusal must not contradict itself. Saying "you may not run mailman" to
	// an agent that has used mailman all session reads as a broken gate, and its
	// next move is to try again rather than to ask.
	out := bash("mailman admin user list")
	if strings.Contains(out.Stderr, "you may not run mailman.") {
		t.Errorf("the refusal denies mailman itself, which is allowed:\n%s", out.Stderr)
	}
	for _, want := range []string{"but not mailman admin", "orc new identity", "shell(mailman)"} {
		if !strings.Contains(out.Stderr, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, out.Stderr)
		}
	}

	// A clause naming the command covers the guarded part too — there is no way
	// to ask for `mailman admin` separately, and nothing here invents one.
	wide := newRig(t)
	wide.permit("run-mailman", "shell(mailman)")
	if out := wide.call(wide.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": wide.workspace(),
		"tool_input": map[string]any{"command": "mailman admin user list"},
	}); out.Code != hook.CodeOK {
		t.Errorf("shell(mailman) did not cover mailman admin:\n%s", out.Stderr)
	}
}

// The default set does not depend on the store, so losing the store must not
// take it away. An agent whose permissions cannot be read is exactly the agent
// that needs to say so — by mail.
func TestTheDefaultSetSurvivesABlindRung(t *testing.T) {
	r := newRig(t)
	ws := r.workspace()

	opts := hook.Options{
		Root:  filepath.Join(t.TempDir(), "gone"),
		Clock: clock.NewFake(epoch, time.Second),
		Env: func(key string) (string, bool) {
			v, ok := map[string]string{"ORC_IDENTITY": "ember"}[key]
			return v, ok
		},
	}
	bash := func(command string) hook.Outcome {
		return r.call(opts, map[string]any{
			"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": ws,
			"tool_input": map[string]any{"command": command},
		})
	}

	for _, command := range []string{"echo hello", "mailman inbox", "mailman send rowan"} {
		if out := bash(command); out.Code != hook.CodeOK {
			t.Errorf("%q was blocked with nothing readable, though it needs no clause:\n%s",
				command, out.Stderr)
		}
	}

	// Everything else still stops, including the guarded subcommand and the
	// shapes that hide what they run. A blind rung opens nothing.
	for _, command := range []string{"ls", "mailman admin mail", "sh -c ls", "echo $(rm -rf /)"} {
		if out := bash(command); out.Code != hook.CodeBlock {
			t.Errorf("%q was allowed with nothing readable", command)
		}
	}
}

// TestAnAgentKeepsItsOwnMemory is the promise the provisioned CLAUDE.md makes.
//
// Every agent is told "anything you want to survive this session goes in
// `memory/`, beside this file" — and until this was fixed, that was the one thing
// no session could do. The directory sits *beside* the workspace rather than
// inside it, so the workspace test refused it and no clause could ever have
// helped: every permission an identity holds is workspace-relative.
func TestAnAgentKeepsItsOwnMemory(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	claude := r.store.ClaudeDir(r.who)

	keeps := []string{
		filepath.Join(claude, "memory", "a-fact.md"),
		filepath.Join(claude, "memory", "deep", "nested.md"),
		filepath.Join(claude, "memory"),
		filepath.Join(claude, "CLAUDE.md"),
	}
	// Reads as well as writes: ember holds a read clause, so a read outside the
	// workspace is refused too. Its own memory is not a place it needs one for.
	for _, name := range []string{"Read", "Write", "Edit"} {
		for _, path := range keeps {
			if out := r.call(opts, tool(name, path, r.workspace())); out.Code != hook.CodeOK {
				t.Errorf("%s %s was refused:\n%s", name, path, out.Stderr)
			}
		}
	}
}

// The carve-out is exactly two things, and everything around them still stops.
func TestWhatAnAgentDoesNotKeep(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	claude := r.store.ClaudeDir(r.who)

	for _, c := range []struct{ name, path string }{
		// The hook's own wiring. An agent that could edit this could switch off
		// the thing refusing everything else.
		{"its settings", filepath.Join(claude, "settings.json")},
		// A name that merely starts with the directory's. A prefix comparison
		// without the separator would let this through.
		{"a lookalike beside it", filepath.Join(claude, "memory-notes.md")},
		{"a lookalike directory", filepath.Join(claude, "memoryX", "f.md")},
		// The configuration directory itself, which is not a file it keeps.
		{"the claude directory", claude},
		// Somebody else's, reached through the same shape.
		{"another agent's memory", filepath.Join(r.store.ClaudeDir(mustUser(t, "boss")), "memory", "x.md")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if out := r.call(opts, tool("Write", c.path, r.workspace())); out.Code != hook.CodeBlock {
				t.Errorf("%s was writable", c.path)
			}
		})
	}
}

// The third rung takes memory with it, and that is the honest behaviour to pin.
//
// The directory is found by asking the store where it is — the store being the
// only package that knows the layout — so with no store there is nothing to ask.
// Composing the path in the hook instead would be a second place that had to
// agree about it, which is the disagreement the carve-out exists to end. A store
// that cannot be opened is also one whose memory directory is not there.
func TestMemoryNeedsAStoreToBeFound(t *testing.T) {
	r := newRig(t)
	root := filepath.Join(t.TempDir(), "gone")
	opts := hook.Options{
		Root:  root,
		Clock: clock.NewFake(epoch, time.Second),
		Env: func(key string) (string, bool) {
			v, ok := map[string]string{"ORC_IDENTITY": "ember"}[key]
			return v, ok
		},
	}
	memory := filepath.Join(root, "identities", "ember", "claude", "memory", "note.md")

	if out := r.call(opts, tool("Write", memory, r.workspace())); out.Code != hook.CodeBlock {
		t.Error("a write was allowed with no store to say where memory is")
	}
	// The rung's own rule is unchanged: reads pass, writes block.
	if out := r.call(opts, tool("Read", memory, r.workspace())); out.Code != hook.CodeOK {
		t.Errorf("a read was blocked on the blind rung:\n%s", out.Stderr)
	}
}

// The memory the harness actually points at.
//
// Claude Code keeps per-project state under `projects/<slug>/` in its config
// directory, and its own auto-memory instructions name `projects/<slug>/memory/`
// rather than the `memory/` beside CLAUDE.md. Orc sets CLAUDE_CONFIG_DIR to the
// identity's `claude/` dir, so that path lands inside the store — and it matched
// neither carve-out, so every agent that followed the instructions it was given
// had its memory writes refused as fleet state.
func TestProjectScopedMemoryIsKeptToo(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	claude := r.store.ClaudeDir(r.who)
	slug := "-Users-someone-Dev-Orc"

	for _, path := range []string{
		filepath.Join(claude, "projects", slug, "memory", "a-fact.md"),
		filepath.Join(claude, "projects", slug, "memory", "deep", "nested.md"),
		filepath.Join(claude, "projects", slug, "memory"),
	} {
		for _, name := range []string{"Read", "Write", "Edit"} {
			if out := r.call(opts, tool(name, path, r.workspace())); out.Code != hook.CodeOK {
				t.Errorf("%s %s was refused:\n%s", name, path, out.Stderr)
			}
		}
	}
}

// And only the memory. The rest of a project's tree is Claude Code's own state,
// settings among it — widening the hole to `projects/**` to fix a memory path
// would hand back exactly what `settings.json` is protected for.
func TestTheRestOfAProjectIsNotKept(t *testing.T) {
	r := newRig(t)
	opts := r.as("ember", nil)
	claude := r.store.ClaudeDir(r.who)
	slug := "-Users-someone-Dev-Orc"

	for _, path := range []string{
		filepath.Join(claude, "projects", slug, "settings.json"),
		filepath.Join(claude, "projects", slug, "history.jsonl"),
		filepath.Join(claude, "projects", slug, "memory-notes.md"),
		filepath.Join(claude, "projects", "settings.json"),
		filepath.Join(claude, "projects"),
	} {
		if out := r.call(opts, tool("Write", path, r.workspace())); out.Code != hook.CodeBlock {
			t.Errorf("%s was writable", path)
		}
	}
}

// An interpreter runs when a clause names it, and not otherwise.
//
// This is the rule that replaces "every interpreter is unreadable". That one made
// `shell(python3)` a clause nobody could satisfy — and the toolkit's own
// shell-build named python, python3, sh and bash, every one of which was refused.
// A permission that lies about itself is worse than one that refuses.
func TestAnInterpreterRunsWhenNamed(t *testing.T) {
	named := newRig(t)
	named.permit("run-python", "shell(python3)")
	got := named.call(named.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": named.workspace(),
		"tool_input": map[string]any{"command": `python3 -c "print(1)"`},
	})
	if got.Code != hook.CodeOK {
		t.Errorf("shell(python3) did not permit python3:\n%s", got.Stderr)
	}

	// And an interpreter the clause does not name is still refused — naming one
	// grants that one, not the family.
	if out := named.call(named.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": named.workspace(),
		"tool_input": map[string]any{"command": "sh -c rm"},
	}); out.Code != hook.CodeBlock {
		t.Error("shell(python3) permitted sh as well")
	}

	// With no shell clause at all it is refused like anything else.
	bare := newRig(t)
	if out := bare.call(bare.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": bare.workspace(),
		"tool_input": map[string]any{"command": "python3 script.py"},
	}); out.Code != hook.CodeBlock {
		t.Error("python3 ran with no shell permission at all")
	}
}

// A substitution is still everything-or-nothing, whatever else is named.
//
// The two were folded together and are now apart, so the half that must not have
// moved is worth its own assertion: naming an interpreter does not buy a
// substitution, because the point of that rule is that no name can be attributed
// to one.
func TestNamingAnInterpreterDoesNotBuyASubstitution(t *testing.T) {
	r := newRig(t)
	r.permit("run-sh", "shell(sh echo)")
	out := r.call(r.as("ember", nil), map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": r.workspace(),
		"tool_input": map[string]any{"command": "echo $(rm -rf /)"},
	})
	if out.Code != hook.CodeBlock {
		t.Error("a substitution was allowed by a clause naming an interpreter")
	}
	if !strings.Contains(out.Stderr, "hides what it runs") {
		t.Errorf("it should be refused as unreadable:\n%s", out.Stderr)
	}
}
