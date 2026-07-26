package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/cli"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

// rig runs the CLI against a temporary fleet, with a recorded mailman instead of a
// real one.
//
// Nothing in this file executes another binary. That is not only for speed: a test
// that shelled out to the real `mailman` would provision mailboxes in whoever's
// store the developer happens to have, which is the kind of accident a fleet tool
// gets to have exactly once.
type rig struct {
	t        *testing.T
	root     string
	now      *clock.Fake
	keys     map[string]string
	mailman  []string
	mailbox  map[string]bool
	failNext bool

	// The worklist half. Recorded rather than spawned: the supervisor is tested
	// against a real pty in internal/session, and a CLI test that started one per
	// employment would be testing the same thing again, slower.
	populated map[string]string
	populates []string
	failStart bool
}

type result struct {
	code   int
	stdout string
	stderr string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return &rig{
		t:         t,
		root:      t.TempDir() + "/fleet",
		now:       clock.NewFake(epoch, time.Second),
		keys:      map[string]string{},
		mailbox:   map[string]bool{},
		populated: map[string]string{},
	}
}

// provision records what Orc asked another tool to do, and refuses a duplicate
// mailbox exactly as Mailman does.
func (r *rig) provision(args []string, stdin string) error {
	r.t.Helper()
	r.mailman = append(r.mailman, strings.Join(args, " "))
	if r.failNext {
		r.failNext = false
		return fmt.Errorf("mailman: refused on purpose")
	}
	if len(args) >= 4 && args[2] == "add" {
		if r.mailbox[args[3]] {
			return fmt.Errorf("mailman: mailbox %s already exists", args[3])
		}
		// The key travels on stdin, which is the whole point of --key -.
		if strings.TrimSpace(stdin) == "" {
			return fmt.Errorf("mailman: no key on stdin")
		}
		r.mailbox[args[3]] = true
	}
	if len(args) >= 4 && args[2] == "remove" {
		if !r.mailbox[args[3]] {
			return fmt.Errorf("mailman: nothing matches %q", args[3])
		}
		delete(r.mailbox, args[3])
	}
	return nil
}

func (r *rig) populate(s *store.Store, name user.Name, id string, m model.Model, e model.Effort, resume bool) error {
	r.t.Helper()
	r.populates = append(r.populates, fmt.Sprintf("%s %s/%s resume=%v", name, m, e, resume))
	if r.failStart {
		return fmt.Errorf("the session would not start")
	}
	r.populated[name.String()] = id
	return s.WriteSession(name, store.SessionState{
		ID: id, Supervisor: os.Getpid(), Child: os.Getpid(),
		Model: m.String(), Effort: e.String(), Started: clock.Format(epoch),
	})
}

func (r *rig) depopulate(s *store.Store, name user.Name) error {
	r.t.Helper()
	delete(r.populated, name.String())
	return s.RemoveSession(name)
}

func (r *rig) run(who string, args ...string) result {
	r.t.Helper()

	env := map[string]string{}
	if who != "" {
		env["ORC_USER"] = who
		if key, ok := r.keys[who]; ok {
			env["ORC_KEY"] = key
		}
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
		Root:       r.root,
		Clock:      r.now,
		Width:      100,
		User:       "operator",
		Provision:  r.provision,
		Populate:   r.populate,
		Depopulate: r.depopulate,
	}, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// runEnv runs a command with an environment given exactly, rather than derived from
// an identity. It exists for the owner fallback, whose whole subject is what happens
// when ORC_USER and ORC_KEY are absent, half-present, or wrong.
func (r *rig) runEnv(env map[string]string, args ...string) result {
	r.t.Helper()

	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Env: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
		Root:       r.root,
		Clock:      r.now,
		Width:      100,
		User:       "operator",
		Provision:  r.provision,
		Populate:   r.populate,
		Depopulate: r.depopulate,
	}, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (r *rig) ok(who string, args ...string) result {
	r.t.Helper()
	got := r.run(who, args...)
	if got.code != fault.CodeOK {
		r.t.Fatalf("orc %s exited %d\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
	}
	return got
}

// bootstrap makes the fleet and remembers the operator's key, which is the only
// place in the tests a key is read out of output.
func (r *rig) bootstrap(name string) {
	r.t.Helper()
	got := r.ok("", "bootstrap", "--as", name)
	r.keys[name] = keyFrom(r.t, got.stdout)
}

// hire creates an identity and remembers its key, read from the store through the
// one command that discloses one.
func (r *rig) hire(boss, name string) {
	r.t.Helper()
	r.ok(boss, "new", "identity", name)
	got := r.ok(boss, "env", name)
	r.keys[name] = keyFrom(r.t, got.stdout)
}

func keyFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if key, ok := strings.CutPrefix(line, "export ORC_KEY="); ok {
			return key
		}
	}
	t.Fatalf("no key in:\n%s", out)
	return ""
}

// A whole fleet, as the tests below need it: an operator, two roles, two
// permissions, and three agents in a chain.
func fullFleet(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.bootstrap("boss")

	r.ok("boss", "new", "permission", "edit-anno", "40", "read(Anno/**)", "write(Anno/internal/**)")
	r.ok("boss", "new", "permission", "lead", "60", "read(**)", "write(**)", "spawn(24)")
	r.ok("boss", "new", "role", "architect", "80", "leads", "the", "design")
	r.ok("boss", "new", "role", "engineer", "60", "writes", "the", "code")
	r.ok("boss", "assign", "permission", "architect", "lead")
	r.ok("boss", "assign", "permission", "engineer", "edit-anno")

	r.hire("boss", "atlas")
	r.ok("boss", "assign", "role", "atlas", "architect")
	r.hire("boss", "ember")
	r.ok("boss", "assign", "role", "ember", "engineer")
	r.hire("boss", "quill")
	r.ok("boss", "assign", "role", "quill", "engineer")
	return r
}

// TestBootstrap: the fleet comes into being, says how to use itself, and cannot be
// bootstrapped over.
func TestBootstrap(t *testing.T) {
	r := newRig(t)
	got := r.ok("", "bootstrap", "--as", "boss")

	for _, want := range []string{"bootstrapped", "export ORC_USER=boss", "export ORC_KEY="} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("bootstrap did not mention %q:\n%s", want, got.stdout)
		}
	}
	if len(r.mailman) != 1 || !strings.HasPrefix(r.mailman[0], "admin user add boss --key -") {
		t.Errorf("the operator's mailbox was not provisioned through mailman: %v", r.mailman)
	}

	// Twice is safe, and does not mint a second operator.
	again := r.ok("", "bootstrap", "--as", "boss")
	if !strings.Contains(again.stdout, "already bootstrapped") {
		t.Errorf("a second bootstrap should report, not re-create:\n%s", again.stdout)
	}
	other := r.ok("", "bootstrap", "--as", "someone-else")
	if !strings.Contains(other.stderr, "cannot be replaced") {
		t.Errorf("bootstrapping under another name should say the operator is fixed:\n%s", other.stderr)
	}
}

// TestNoFleet: every command but bootstrap and help refuses a store that does not
// exist, and says which command makes one. A tool that conjured a fleet out of a
// mistyped ORC_HOME would be one nobody could trust with a path.
func TestNoFleet(t *testing.T) {
	r := newRig(t)
	// A whole credential, so the refusal is about the missing fleet rather than
	// about the caller. The two are different messages and a caller with a
	// mistyped ORC_HOME needs the second one.
	r.keys["boss"] = "0123456789abcdef0123456789abcdefAAA0"

	for _, args := range [][]string{{"status"}, {"introspect"}, {"verify"}, {"new", "identity", "x"}} {
		got := r.run("boss", args...)
		if got.code != fault.CodeNotFound {
			t.Errorf("orc %s against no fleet exited %d, want %d\n%s",
				strings.Join(args, " "), got.code, fault.CodeNotFound, got.stderr)
		}
		if !strings.Contains(got.stderr, "bootstrap") {
			t.Errorf("orc %s did not say how to make a fleet:\n%s", strings.Join(args, " "), got.stderr)
		}
	}
	if got := r.run("", "help"); got.code != fault.CodeOK {
		t.Errorf("help needs no fleet and no identity, exited %d", got.code)
	}
}

// TestAuthentication: Orc issued the credential, so Orc checks it. This is the one
// thing that makes check-control worth anything to the tools that ask it.
func TestAuthentication(t *testing.T) {
	r := fullFleet(t)

	// An *empty* environment is no longer a refusal: on a store this unix user owns,
	// orc reads the operator's credential from the keyring (see the owner fallback in
	// begin, and TestOwnerFallback*). What follows is what that fallback is careful
	// not to cover — a credential that was presented and is wrong.
	if got := r.run("", "status"); got.code != fault.CodeOK {
		t.Errorf("no credential exited %d; the owner fallback should have applied\n%s", got.code, got.stderr)
	}

	// A real name with a wrong key must fail, and must not say which half was
	// wrong: that distinction is an oracle over the roster.
	r.keys["atlas"] = "0123456789abcdef0123456789abcdefWRONG"
	got := r.run("atlas", "status")
	if got.code != fault.CodeAuth {
		t.Errorf("a wrong key exited %d, want %d", got.code, fault.CodeAuth)
	}
	if strings.Contains(strings.ToLower(got.stderr), "no such") {
		t.Errorf("the refusal distinguishes a bad name from a bad key:\n%s", got.stderr)
	}

	// A name the fleet has never heard of fails the same way.
	r.keys["nobody"] = "0123456789abcdef0123456789abcdefXYZ0"
	if got := r.run("nobody", "status"); got.code != fault.CodeAuth {
		t.Errorf("an unknown identity exited %d, want %d", got.code, fault.CodeAuth)
	}
}

// TestDerivation: the rules from Auth_Perm_Role.md, seen from the CLI. The
// arithmetic is proven in internal/authz; this is the assertion that the commands
// are wired to it.
func TestDerivation(t *testing.T) {
	r := fullFleet(t)

	// A role's authority is a request. Under the operator it is granted whole.
	if got := r.ok("atlas", "introspect", "--only", "authority"); strings.TrimSpace(got.stdout) != "80" {
		t.Errorf("atlas under the operator has authority %q, want 80", strings.TrimSpace(got.stdout))
	}

	// Moved under an authority-60 engineer, the architect is capped at 60 — and
	// loses `lead` entirely, because its floor is 60 and the cap is exactly 60.
	r.ok("boss", "move", "atlas", "ember")
	if got := r.ok("atlas", "introspect", "--only", "authority"); strings.TrimSpace(got.stdout) != "60" {
		t.Errorf("atlas under ember has authority %q, want 60", strings.TrimSpace(got.stdout))
	}

	// Its clauses are now the intersection with ember's, which holds only edit-anno
	// — so an architect under an engineer can do what the engineer can and no more.
	got := r.ok("atlas", "introspect", "--only", "permissions")
	if strings.Contains(got.stdout, "spawn") {
		t.Errorf("atlas kept a spawn clause its boss does not hold: %q", got.stdout)
	}

	// And the card says why rather than leaving it to be discovered.
	card := r.ok("boss", "status", "atlas")
	if !strings.Contains(card.stdout, "caps it") {
		t.Errorf("the card does not explain the cap:\n%s", card.stdout)
	}
}

// TestMoveRefusals: a cycle is refused before it is written, because a store that
// will not derive is one no command can run.
func TestMoveRefusals(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "move", "ember", "atlas")

	if got := r.run("boss", "move", "atlas", "ember"); got.code != fault.CodeConflict {
		t.Errorf("a cycle exited %d, want %d\n%s", got.code, fault.CodeConflict, got.stderr)
	}
	if got := r.run("boss", "move", "atlas", "atlas"); got.code != fault.CodeConflict {
		t.Errorf("self-parenting exited %d, want %d", got.code, fault.CodeConflict)
	}

	// The fleet still derives afterwards, which is the property the refusal exists
	// to protect.
	r.ok("boss", "status")

	// An agent may not move somebody under a stranger.
	if got := r.run("ember", "move", "atlas", "quill"); got.code == fault.CodeOK {
		t.Errorf("ember moved atlas under an identity it does not control")
	}
}

// TestSubtreePrivacy: an identity sees itself and everyone below it, and anybody
// else is reported as not found rather than as forbidden — saying "you may not"
// would confirm the roster.
func TestSubtreePrivacy(t *testing.T) {
	r := fullFleet(t)

	fleet := r.ok("ember", "status")
	if strings.Contains(fleet.stdout, "atlas") || strings.Contains(fleet.stdout, "quill") {
		t.Errorf("ember can see its peers:\n%s", fleet.stdout)
	}
	if !strings.Contains(fleet.stdout, "ember") {
		t.Errorf("ember cannot see itself:\n%s", fleet.stdout)
	}

	if got := r.run("ember", "status", "quill"); got.code != fault.CodeNotFound {
		t.Errorf("reading a peer's card exited %d, want %d", got.code, fault.CodeNotFound)
	}
	if got := r.run("ember", "grant", "permission", "quill", "edit-anno"); got.code != fault.CodeNotFound {
		t.Errorf("granting to a peer exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

// TestCheckControl is the contract `muff assign` calls: 0, 8, or 2, and nothing on
// stdout to parse.
func TestCheckControl(t *testing.T) {
	r := fullFleet(t)

	if got := r.run("boss", "check-control", "atlas"); got.code != fault.CodeOK || got.stdout != "" {
		t.Errorf("boss over atlas: exit %d, stdout %q; want 0 and nothing", got.code, got.stdout)
	}
	if got := r.run("atlas", "check-control", "boss"); got.code != fault.CodeDenied {
		t.Errorf("atlas over boss exited %d, want %d", got.code, fault.CodeDenied)
	}
	if got := r.run("atlas", "check-control", "atlas"); got.code != fault.CodeDenied {
		t.Errorf("atlas over itself exited %d, want %d", got.code, fault.CodeDenied)
	}
	if got := r.run("boss", "check-control", "ghost"); got.code != fault.CodeNotFound {
		t.Errorf("a missing agent exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

// TestGrants: every grant expires, revoke ends one early, and revoking twice is
// safe.
func TestGrants(t *testing.T) {
	r := fullFleet(t)

	// Nothing is populated, so the session-scoped default falls back to a clock and
	// says so. A grant tied to a session that does not exist would already have
	// lapsed, which is a permission that never works.
	got := r.ok("boss", "grant", "permission", "atlas", "edit-anno")
	if !strings.Contains(got.stderr, "no session") {
		t.Errorf("the fallback to a clock was not reported:\n%s", got.stderr)
	}
	if !strings.Contains(got.stdout, "1h left") {
		t.Errorf("a fallen-back grant should last an hour:\n%s", got.stdout)
	}

	if got := r.ok("boss", "introspect", "--only", "grants"); strings.TrimSpace(got.stdout) != "" {
		t.Errorf("the operator has grants it was never given: %q", got.stdout)
	}
	if got := r.ok("atlas", "introspect", "--only", "grants"); strings.TrimSpace(got.stdout) != "edit-anno" {
		t.Errorf("atlas's live grants are %q, want edit-anno", strings.TrimSpace(got.stdout))
	}

	// --until is not a fallback and must not be reported as one.
	timed := r.ok("boss", "grant", "permission", "ember", "edit-anno", "--until", "30m")
	if strings.Contains(timed.stderr, "no session") {
		t.Errorf("--until was reported as a fallback:\n%s", timed.stderr)
	}
	if !strings.Contains(timed.stdout, "30m left") {
		t.Errorf("a 30m grant should read as 30m, not less:\n%s", timed.stdout)
	}

	r.ok("boss", "revoke", "permission", "atlas", "edit-anno")
	if got := r.ok("atlas", "introspect", "--only", "grants"); strings.TrimSpace(got.stdout) != "" {
		t.Errorf("the grant survived revocation: %q", got.stdout)
	}
	// Twice is safe: the caller's intent is satisfied either way.
	r.ok("boss", "revoke", "permission", "atlas", "edit-anno")

	// An actor cannot hand on what it does not hold — checked against somebody it
	// *does* control, so the refusal is about the permission rather than about the
	// tree. Both refusals matter and they are different codes.
	r.ok("boss", "move", "quill", "ember")
	if got := r.run("ember", "grant", "permission", "quill", "lead"); got.code != fault.CodeDenied {
		t.Errorf("granting a permission the actor lacks exited %d, want %d\n%s",
			got.code, fault.CodeDenied, got.stderr)
	}
	if got := r.run("ember", "grant", "permission", "atlas", "edit-anno"); got.code != fault.CodeNotFound {
		t.Errorf("granting to a peer exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

// TestGrantExpiry: a grant is not in force after its deadline, and nothing has to
// run to expire it — the fold answers a question about now.
func TestGrantExpiry(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "grant", "permission", "atlas", "edit-anno", "--until", "30m")

	if got := r.ok("atlas", "introspect", "--only", "grants"); !strings.Contains(got.stdout, "edit-anno") {
		t.Fatalf("the grant is not live: %q", got.stdout)
	}
	r.now = clock.NewFake(epoch.Add(time.Hour), time.Second)
	if got := r.ok("atlas", "introspect", "--only", "grants"); strings.TrimSpace(got.stdout) != "" {
		t.Errorf("the grant outlived its deadline: %q", got.stdout)
	}
}

// TestRemoveIdentity: the destructive command asks first, refuses while somebody
// reports to it, and retires the mailbox when it goes.
func TestRemoveIdentity(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "move", "ember", "atlas")

	if got := r.run("boss", "remove", "identity", "atlas", "--yes"); got.code != fault.CodeConflict {
		t.Errorf("removing a boss exited %d, want %d\n%s", got.code, fault.CodeConflict, got.stderr)
	}
	if got := r.run("boss", "remove", "identity", "ember"); got.code != fault.CodeUsage {
		t.Errorf("removing without --yes exited %d, want %d", got.code, fault.CodeUsage)
	} else if !strings.Contains(got.stderr, "workspace") {
		t.Errorf("the confirmation does not say the workspace goes:\n%s", got.stderr)
	}

	r.ok("boss", "remove", "identity", "ember", "--yes")
	if r.mailbox["ember"] {
		t.Errorf("the mailbox outlived the identity")
	}
	if got := r.run("boss", "status", "ember"); got.code != fault.CodeNotFound {
		t.Errorf("the identity survived removal: exit %d", got.code)
	}
	// The name is free again, which is what retiring the mailbox is for.
	r.hire("boss", "ember")
}

// TestRemovePolicyRefusals: a role in use and a permission in use both refuse, and
// both name the way forward.
func TestRemovePolicyRefusals(t *testing.T) {
	r := fullFleet(t)

	if got := r.run("boss", "remove", "role", "engineer"); got.code != fault.CodeConflict {
		t.Errorf("removing a held role exited %d, want %d", got.code, fault.CodeConflict)
	} else if !strings.Contains(got.stderr, "ember") {
		t.Errorf("the refusal does not name who holds it:\n%s", got.stderr)
	}

	if got := r.run("boss", "remove", "permission", "edit-anno"); got.code != fault.CodeConflict {
		t.Errorf("removing a used permission exited %d, want %d", got.code, fault.CodeConflict)
	} else if !strings.Contains(got.stderr, "--from") {
		t.Errorf("the refusal does not offer --from:\n%s", got.stderr)
	}

	// --from narrows one role instead of deleting for everybody.
	r.ok("boss", "remove", "permission", "edit-anno", "--from", "engineer")
	if got := r.ok("ember", "introspect", "--only", "permissions"); strings.TrimSpace(got.stdout) != "" {
		t.Errorf("engineer kept the permission: %q", got.stdout)
	}
	r.ok("boss", "remove", "permission", "edit-anno")
}

// TestRollback: a failure in another tool must not leave a half-made identity, and
// the name must be free afterwards.
func TestRollback(t *testing.T) {
	r := fullFleet(t)

	r.failNext = true
	if got := r.run("boss", "new", "identity", "doomed"); got.code == fault.CodeOK {
		t.Fatalf("a refused mailbox should fail the whole creation")
	}
	if got := r.run("boss", "status", "doomed"); got.code != fault.CodeNotFound {
		t.Errorf("a half-made identity survived: exit %d\n%s", got.code, got.stdout)
	}
	// The name is free, which is the property the rollback exists for.
	r.hire("boss", "doomed")
}

// TestKeyHygiene is the assertion rather than the review item: a key reaches a
// session's environment and nothing else.
//
// `orc bootstrap` and `orc env` are the two deliberate exceptions, and both say so
// when they run. Every other command is checked against every key in the fleet, on
// both streams.
func TestKeyHygiene(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "grant", "permission", "atlas", "edit-anno")

	commands := [][]string{
		{"status"}, {"status", "atlas"}, {"status", "--json"}, {"status", "atlas", "--json"},
		{"introspect"}, {"introspect", "--json"}, {"introspect", "--only", "permissions"},
		{"verify"}, {"help"},
		{"new", "identity", "fresh"},
		{"assign", "role", "fresh", "engineer"},
		{"grant", "permission", "fresh", "edit-anno"},
		{"revoke", "permission", "fresh", "edit-anno"},
		{"move", "fresh", "atlas"},
		{"check-control", "atlas"},
		{"remove", "identity", "fresh", "--yes"},
	}
	for _, args := range commands {
		got := r.run("boss", args...)
		for name, key := range r.keys {
			if strings.Contains(got.stdout, key) || strings.Contains(got.stderr, key) {
				t.Errorf("orc %s disclosed %s's key", strings.Join(args, " "), name)
			}
		}
	}

	// The exceptions disclose, and say that they are doing it.
	got := r.ok("boss", "env", "atlas")
	if !strings.Contains(got.stdout, r.keys["atlas"]) {
		t.Errorf("env should print the key it is for:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "credential") {
		t.Errorf("env should say it printed a credential:\n%s", got.stderr)
	}
	if got := r.run("ember", "env", "atlas"); got.code == fault.CodeOK {
		t.Errorf("ember read a peer's credential")
	}
}

// TestNotBuiltYet: the verbs Reference.md lists and this build does not have say
// what they are waiting on. A specified verb answering "unknown command" reads as a
// broken build rather than an unfinished one.
func TestNotBuiltYet(t *testing.T) {
	r := fullFleet(t)
	for _, verb := range []string{"doctor"} {
		got := r.run("boss", verb, "atlas")
		if got.code != fault.CodeUsage {
			t.Errorf("orc %s exited %d, want %d", verb, got.code, fault.CodeUsage)
		}
		if strings.Contains(got.stderr, "unknown command") {
			t.Errorf("orc %s reads as unrecognised rather than unbuilt:\n%s", verb, got.stderr)
		}
	}
	if got := r.run("boss", "sprint"); !strings.Contains(got.stderr, "unknown command") {
		t.Errorf("a genuinely unknown command should say so:\n%s", got.stderr)
	}
}

// TestColourIsALayer: every screen, stripped of its escape sequences, is exactly
// the plain rendering. A pipe through grep must lose nothing but the pleasure.
func TestColourIsALayer(t *testing.T) {
	r := fullFleet(t)

	for _, args := range [][]string{{"status"}, {"status", "atlas"}, {"help"}} {
		plain := r.ok("boss", args...)
		coloured := r.ok("boss", append(append([]string{}, args...), "--color")...)
		if stripped := strip(coloured.stdout); stripped != plain.stdout {
			t.Errorf("orc %s differs once colour is stripped:\nplain:\n%s\nstripped:\n%s",
				strings.Join(args, " "), plain.stdout, stripped)
		}
	}
}

func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestJSONHasNoKeys pins the shape a mirror will read, and the rule that governs
// it: no field for a credential, and populated present-and-false rather than
// missing.
func TestJSONHasNoKeys(t *testing.T) {
	r := fullFleet(t)
	got := r.ok("boss", "status", "--json")

	for _, want := range []string{`"operator": "boss"`, `"identities"`, `"clauses"`, `"populated": false`} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the JSON shape is missing %s:\n%s", want, got.stdout)
		}
	}
	for _, forbidden := range []string{`"key"`, `"secret"`, `ORC_KEY`} {
		if strings.Contains(got.stdout, forbidden) {
			t.Errorf("the JSON shape mentions %s", forbidden)
		}
	}
}

// TestUnknownFieldNamesTheAlternatives: a script reading one field must never get an
// empty line it can mistake for an answer.
func TestUnknownFieldNamesTheAlternatives(t *testing.T) {
	r := fullFleet(t)

	got := r.run("atlas", "introspect", "--only", "salary")
	if got.code != fault.CodeUsage {
		t.Errorf("an unknown field exited %d, want %d", got.code, fault.CodeUsage)
	}
	if !strings.Contains(got.stderr, "authority") {
		t.Errorf("the error does not name the valid fields:\n%s", got.stderr)
	}

	// A field that belongs to liveness is named as such rather than reported as a
	// typo, because a script written against the Reference will ask for it.
	// A field with a legitimate negative answers empty rather than failing: "am I
	// populated?" is a question a caller tests for empty, not an error.
	got = r.run("atlas", "introspect", "--only", "session")
	if got.code != fault.CodeOK || strings.TrimSpace(got.stdout) != "" {
		t.Errorf("an unpopulated session should be empty: exit %d, %q", got.code, got.stdout)
	}
}

// TestBrokenStreams: a command with no output streams exits with a code rather than
// panicking on a nil writer.
func TestBrokenStreams(t *testing.T) {
	if got := cli.Main(cli.App{}, []string{"status"}); got != fault.CodeInternal {
		t.Errorf("no streams exited %d, want %d", got, fault.CodeInternal)
	}
}

// TestEmployAndFire: the worklist round trip, and the two states that make
// employment and population different questions.
func TestEmployAndFire(t *testing.T) {
	r := fullFleet(t)

	// A roleless identity is refused: a session with no permissions could do
	// nothing, and starting one to discover that wastes a minute and some money.
	r.ok("boss", "new", "identity", "fresh")
	if got := r.run("boss", "employ", "fresh"); got.code != fault.CodeConflict {
		t.Errorf("employing a roleless identity exited %d, want %d", got.code, fault.CodeConflict)
	}

	got := r.ok("boss", "employ", "ember")
	if !strings.Contains(got.stdout, "sonnet/med") {
		t.Errorf("employ did not default to sonnet/medium:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "populated") {
		t.Errorf("employ did not populate:\n%s", got.stdout)
	}
	if r.populated["ember"] == "" {
		t.Errorf("no session was started")
	}
	if got := r.ok("ember", "introspect", "--only", "employed"); strings.TrimSpace(got.stdout) != "yes" {
		t.Errorf("employed reads %q, want yes", got.stdout)
	}
	if got := r.ok("ember", "introspect", "--only", "load"); strings.TrimSpace(got.stdout) != "4" {
		t.Errorf("load reads %q, want 4 for sonnet/medium", got.stdout)
	}

	// Employing twice is a no-op somebody may be running from a script.
	if got := r.ok("boss", "employ", "ember"); !strings.Contains(got.stdout, "already employed") {
		t.Errorf("employing twice should say so:\n%s", got.stdout)
	}

	// Firing a live session needs --yes, because it ends a turn.
	if got := r.run("boss", "fire", "ember"); got.code != fault.CodeUsage {
		t.Errorf("firing a live session without --yes exited %d, want %d", got.code, fault.CodeUsage)
	}
	r.ok("boss", "fire", "ember", "--yes")
	if r.populated["ember"] != "" {
		t.Errorf("the session outlived the firing")
	}
	if got := r.ok("ember", "introspect", "--only", "employed"); strings.TrimSpace(got.stdout) != "no" {
		t.Errorf("employed reads %q after firing, want no", got.stdout)
	}
}

// TestEmployRemembersTheLoad: a fire and a re-employ must not quietly downgrade an
// agent somebody deliberately put on opus.
func TestEmployRemembersTheLoad(t *testing.T) {
	r := fullFleet(t)

	r.ok("boss", "employ", "atlas", "--model", "opus", "--effort", "high")
	if got := r.ok("atlas", "introspect", "--only", "load"); strings.TrimSpace(got.stdout) != "9" {
		t.Errorf("opus/high is load %q, want 9", got.stdout)
	}
	r.ok("boss", "fire", "atlas", "--yes")

	got := r.ok("boss", "employ", "atlas")
	if !strings.Contains(got.stdout, "opus/high") {
		t.Errorf("re-employing reset the load:\n%s", got.stdout)
	}
}

// TestBudgetIsSuperlinear is the arithmetic from Auth_Perm_Role.md, at the CLI: the
// count is a third input, so the tenth agent costs more than the first.
func TestBudgetIsSuperlinear(t *testing.T) {
	r := fullFleet(t)

	// atlas holds spawn(24) through `lead`, and everything below it counts against
	// that budget — so employing through a subordinate is not a way round.
	r.ok("boss", "move", "ember", "atlas")
	r.ok("boss", "move", "quill", "atlas")
	r.ok("boss", "new", "identity", "flint")
	r.ok("boss", "assign", "role", "flint", "engineer")
	r.ok("boss", "move", "flint", "atlas")

	// Four sonnet/medium sessions: the sum is 16 and the total is 21, not 16.
	for _, who := range []string{"atlas", "ember", "quill", "flint"} {
		r.ok("boss", "employ", who)
	}
	got := r.ok("atlas", "status")
	if !strings.Contains(got.stdout, "load 21 of 24") {
		t.Errorf("four sonnet/medium sessions should total 21 of 24:\n%s", got.stdout)
	}

	// A fifth is refused, and the refusal says the count is what did it — otherwise
	// a load-4 session pushing a 21 over 24 reads as arithmetic nobody can follow.
	r.ok("boss", "new", "identity", "brand")
	r.ok("boss", "assign", "role", "brand", "engineer")
	r.ok("boss", "move", "brand", "atlas")

	refused := r.run("atlas", "employ", "brand")
	if refused.code != fault.CodeDenied {
		t.Fatalf("the fifth session exited %d, want %d\n%s", refused.code, fault.CodeDenied, refused.stderr)
	}
	if !strings.Contains(refused.stderr, "of 24") {
		t.Errorf("the refusal does not show the budget:\n%s", refused.stderr)
	}
	if !strings.Contains(refused.stderr, "multiplier") {
		t.Errorf("the refusal does not say the count multiplier rose:\n%s", refused.stderr)
	}
}

// TestNoSpawnNoEmploy: the two failures a budget can have are different mistakes
// with different fixes, so they are different messages.
func TestNoSpawnNoEmploy(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "move", "quill", "ember")

	// ember's role holds edit-anno, which carries no spawn clause at all.
	got := r.run("ember", "employ", "quill")
	if got.code != fault.CodeDenied {
		t.Fatalf("employing with no spawn permission exited %d, want %d", got.code, fault.CodeDenied)
	}
	if !strings.Contains(got.stderr, "no spawn permission") {
		t.Errorf("the refusal does not name the missing permission:\n%s", got.stderr)
	}
}

// TestTendReconciles: the two disagreements between the worklist and reality, and
// the rule that a session is never killed for being over budget.
func TestTendReconciles(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	// A supervisor that died: the state file goes, the employment stays.
	if err := r.depopulate(mustStore(t, r), mustName(t, "ember")); err != nil {
		t.Fatalf("simulating a crash: %v", err)
	}
	if got := r.ok("boss", "status"); !strings.Contains(got.stdout, "employed, not running") {
		t.Errorf("a crashed session should show as employed and not running:\n%s", got.stdout)
	}

	got := r.ok("boss", "tend")
	if !strings.Contains(got.stdout, "populated ember") {
		t.Errorf("tend did not restart the crashed session:\n%s", got.stdout)
	}

	// Nothing left to do, said plainly.
	if got := r.ok("boss", "tend"); !strings.Contains(got.stdout, "nothing to do") {
		t.Errorf("a reconciled fleet should say so:\n%s", got.stdout)
	}
}

// TestRefreshMintsANewSession is the other half of the distinction: a refresh is a
// new conversation, and a crash restart is not.
func TestRefreshMintsANewSession(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	first := r.populated["ember"]

	got := r.ok("boss", "refresh", "ember")
	if !strings.Contains(got.stdout, "fresh context") {
		t.Errorf("refresh did not say it was fresh:\n%s", got.stdout)
	}
	if second := r.populated["ember"]; second == first || second == "" {
		t.Errorf("refresh did not mint a new session id (%q then %q)", first, second)
	}
	// Never with --resume: that flag is what a crash restart uses, and using it here
	// would make a refresh a continuation.
	for _, p := range r.populates {
		if strings.Contains(p, "resume=true") {
			t.Errorf("a CLI populate asked to resume: %v", r.populates)
		}
	}

	// A refresh of something not employed is a conflict with the way forward in it.
	r.ok("boss", "fire", "ember", "--yes")
	if got := r.run("boss", "refresh", "ember"); got.code != fault.CodeConflict {
		t.Errorf("refreshing an unemployed identity exited %d, want %d", got.code, fault.CodeConflict)
	}
}

// TestPokeAndAttachNeedASession: both say what to do when there is nothing running,
// and they say different things depending on why.
func TestPokeAndAttachNeedASession(t *testing.T) {
	r := fullFleet(t)

	got := r.run("boss", "poke", "ember")
	if got.code != fault.CodeUnavailable {
		t.Errorf("poking an unpopulated identity exited %d, want %d", got.code, fault.CodeUnavailable)
	}
	if !strings.Contains(got.stderr, "orc employ ember") {
		t.Errorf("the refusal does not say how to start one:\n%s", got.stderr)
	}

	// The clean view is not built; --direct is, and the refusal names it.
	r.ok("boss", "employ", "ember")
	plain := r.run("boss", "attach", "ember")
	if plain.code != fault.CodeUsage || !strings.Contains(plain.stderr, "--direct") {
		t.Errorf("attach without --direct should point at --direct: exit %d\n%s", plain.code, plain.stderr)
	}
	// And --direct refuses a buffer rather than pretending to hand over a terminal.
	if got := r.run("boss", "attach", "ember", "--direct"); got.code != fault.CodeUsage {
		t.Errorf("attach --direct without a terminal exited %d, want %d", got.code, fault.CodeUsage)
	}
}

// TestSessionScopedGrantLapsesOnRefresh: the default grant is tied to the session,
// so a refresh is a clean slate in permissions as well as in context.
func TestSessionScopedGrantLapsesOnRefresh(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	// With a session to scope to, the default is session-scoped rather than timed.
	got := r.ok("boss", "grant", "permission", "ember", "lead")
	if strings.Contains(got.stderr, "no session") {
		t.Errorf("a populated identity should get a session-scoped grant:\n%s", got.stderr)
	}
	if !strings.Contains(got.stdout, "at session end") {
		t.Errorf("the grant does not say it lapses with the session:\n%s", got.stdout)
	}
	if got := r.ok("ember", "introspect", "--only", "grants"); strings.TrimSpace(got.stdout) != "lead" {
		t.Errorf("the grant is not live: %q", got.stdout)
	}

	refreshed := r.ok("boss", "refresh", "ember")
	if !strings.Contains(refreshed.stderr, "lapsed") {
		t.Errorf("the refresh did not report the lapsed grant:\n%s", refreshed.stderr)
	}
	if got := r.ok("ember", "introspect", "--only", "grants"); strings.TrimSpace(got.stdout) != "" {
		t.Errorf("a session-scoped grant survived a refresh: %q", got.stdout)
	}
}

func mustStore(t *testing.T, r *rig) *store.Store {
	t.Helper()
	s, err := store.Open(r.root, r.now)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return s
}

func mustName(t *testing.T, raw string) user.Name {
	t.Helper()
	n, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("name %q: %v", raw, err)
	}
	return n
}
