package cli_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/control"
)

// fleet stands in for Orc: it answers who controls whom, and records what it
// was asked. Nothing in this suite execs anything.
type fleet struct {
	mu       sync.Mutex
	controls map[string]bool
	asked    []string
	fail     error
}

func newFleet(controlled ...string) *fleet {
	f := &fleet{controls: map[string]bool{}}
	for _, who := range controlled {
		f.controls[who] = true
	}
	return f
}

func (f *fleet) check(who user.Name) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, who.String())

	if f.fail != nil {
		return f.fail
	}
	if !f.controls[who.String()] {
		return control.Refused{Agent: who, Detail: "alice may not direct " + who.String() +
			": " + who.String() + " is not below alice in the tree"}
	}
	return nil
}

func (f *fleet) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func TestAssign(t *testing.T) {
	r := newRig(t)
	r.control = newFleet("bob").check
	r.worktree(t, "internal/tree")
	r.ok("alice", "create", "fix-the-parser", "4", "3")
	r.ok("alice", "scope", "fix-the-parser", "internal/tree")
	r.ok("alice", "push", "fix-the-parser")

	got := r.ok("alice", "assign", "bob", "fix-the-parser")
	if !strings.Contains(got.stdout, "bob") || !strings.Contains(got.stdout, "fix-the-parser") {
		t.Errorf("assign should say what went to whom:\n%s", got.stdout)
	}

	// Bob owns it, and can act as an owner does.
	if board := r.ok("bob", "pool").stdout; !strings.Contains(board, "bob") {
		t.Errorf("bob should own it:\n%s", board)
	}
	if got := r.run("bob", "invite", "carol", "fix-the-parser"); got.code != fault.CodeOK {
		t.Errorf("the new owner could not invite: %d\n%s", got.code, got.stderr)
	}

	// And he was told, as its owner rather than as a collaborator.
	sent := r.mail.delivered()
	if len(sent) == 0 {
		t.Fatal("no notice was sent")
	}
	if !strings.Contains(sent[0], "you own fix-the-parser") || !strings.Contains(sent[0], "You own it") {
		t.Errorf("the notice should say they own it:\n%s", sent[0])
	}
}

// TestAssignAsksOrcFirst — and asks it before touching the store, so a refusal
// for a reason Macmuffin cannot see costs nothing.
func TestAssignAsksOrcBeforeWriting(t *testing.T) {
	r := newRig(t)
	f := newFleet() // controls nobody
	r.control = f.check
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	before := fingerprint(t, r.storePath())
	got := r.run("alice", "assign", "bob", "fix-the-parser")

	if got.code != fault.CodeDenied {
		t.Fatalf("assign exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if asked := f.questions(); len(asked) != 1 || asked[0] != "bob" {
		t.Errorf("orc was asked %v, want just bob", asked)
	}
	if after := fingerprint(t, r.storePath()); after != before {
		t.Error("a refused assign still wrote to the store")
	}
	// Orc's reason survives to the caller.
	if !strings.Contains(got.stderr, "not below alice in the tree") {
		t.Errorf("orc's reason was lost:\n%s", got.stderr)
	}
	if !r.mail.quiet() {
		t.Error("a refused assign still sent mail")
	}
}

// TestAssignFailsClosed. Orc unreachable is not permission.
func TestAssignFailsClosed(t *testing.T) {
	r := newRig(t)
	f := newFleet("bob")
	f.fail = fault.Unavailable{Peer: "orc", Err: errors.New("it is not installed")}
	r.control = f.check
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.run("alice", "assign", "bob", "fix-the-parser")
	if got.code != fault.CodeUnavailable {
		t.Errorf("with orc down, assign exited %d, want %d\n%s", got.code, fault.CodeUnavailable, got.stderr)
	}
	if strings.Contains(r.ok("alice", "pool").stdout, "bob") {
		t.Error("the task was assigned anyway")
	}
}

func TestAssignRefusals(t *testing.T) {
	r := newRig(t)
	r.control = newFleet("bob", "carol").check
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser") // alice claims it herself

	// Already owned — the same conflict `claim` gives, naming the holder.
	got := r.run("alice", "assign", "bob", "fix-the-parser")
	if got.code != fault.CodeConflict {
		t.Errorf("assigning an owned task exited %d, want %d\n%s", got.code, fault.CodeConflict, got.stderr)
	}
	if !strings.Contains(got.stderr, "alice") {
		t.Errorf("the conflict should name the holder:\n%s", got.stderr)
	}

	// Assigning to yourself is `claim`, and says so rather than reporting that
	// you do not control yourself, which is true but unhelpful.
	self := r.run("alice", "assign", "alice", "fix-the-parser")
	if self.code != fault.CodeUsage {
		t.Errorf("self-assign exited %d, want %d", self.code, fault.CodeUsage)
	}
	if !strings.Contains(self.stderr, "muff claim") {
		t.Errorf("self-assign should redirect to claim:\n%s", self.stderr)
	}

	for _, tc := range []struct {
		what string
		args []string
	}{
		{"no arguments", []string{"assign"}},
		{"only an agent", []string{"assign", "bob"}},
		{"too many", []string{"assign", "bob", "fix-the-parser", "extra"}},
		{"a bad agent name", []string{"assign", "not a name", "fix-the-parser"}},
	} {
		if got := r.run("alice", tc.args...); got.code != fault.CodeUsage {
			t.Errorf("%s exited %d, want %d", tc.what, got.code, fault.CodeUsage)
		}
	}
}

// A task the caller cannot see is missing, not forbidden — assign must not
// become a way to probe for other agents' drafts.
func TestAssignCannotProbeDrafts(t *testing.T) {
	r := newRig(t)
	r.control = newFleet("carol").check
	r.ok("bob", "create", "bobs-secret", "3", "3")

	got := r.run("alice", "assign", "carol", "bobs-secret")
	if got.code != fault.CodeNotFound {
		t.Errorf("assigning another agent's draft exited %d, want %d\n%s",
			got.code, fault.CodeNotFound, got.stderr)
	}
}

// --- identity ------------------------------------------------------------

// TestABadCredentialStopsEverything. Every permission decision rests on the
// caller being who they claim, so a definite no from the authority must refuse
// before a command runs at all — not just the commands that seem sensitive.
func TestUnverifiedIdentityRefusesEveryCommand(t *testing.T) {
	r := newRig(t)
	r.identity = func(user.Name) error {
		return fault.Auth{Reason: "orc does not accept this credential for alice"}
	}

	for _, args := range [][]string{
		{"pool"},
		{"info", "anything"},
		{"create", "a-task", "3", "3"},
		{"verify"},
		{"check-scope", "x"},
	} {
		got := r.run("alice", args...)
		if got.code != fault.CodeAuth {
			t.Errorf("%v exited %d, want %d\n%s", args, got.code, fault.CodeAuth, got.stderr)
		}
	}

	// `help` still answers: an agent with a broken credential needs to be able
	// to find out what the credential is meant to be.
	if got := r.run("alice", "help"); got.code != fault.CodeOK {
		t.Errorf("help exited %d with a bad credential", got.code)
	}
}

// A key that belongs to somebody else is refused with both names, because the
// fix depends on which of the two is wrong.
func TestIdentityMismatchIsRefused(t *testing.T) {
	r := newRig(t)
	r.identity = func(claimed user.Name) error {
		return fault.Auth{Reason: "ORC_USER says " + claimed.String() + ", but that key belongs to bob"}
	}

	got := r.run("alice", "pool")
	if got.code != fault.CodeAuth {
		t.Fatalf("exited %d, want %d", got.code, fault.CodeAuth)
	}
	for _, want := range []string{"alice", "bob"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal should name %q:\n%s", want, got.stderr)
		}
	}
}

// TestNoAuthorityDoesNotStopWork. muff predates Orc and has to keep working
// beside it; a machine with no fleet is not a machine with a liar on it.
func TestUnverifiableIdentityProceeds(t *testing.T) {
	r := newRig(t)
	r.identity = func(user.Name) error {
		return control.Unverifiable{Reason: "orc is not installed"}
	}

	if got := r.run("alice", "create", "a-task", "3", "3"); got.code != fault.CodeOK {
		t.Fatalf("an unverifiable identity blocked a command: %d\n%s", got.code, got.stderr)
	}

	// But `verify` says so, since every permission rests on the unchecked claim.
	report := r.ok("alice", "verify")
	if !strings.Contains(report.stdout, "nobody confirmed you are alice") {
		t.Errorf("verify should mention the unchecked identity:\n%s", report.stdout)
	}
	// And it is a note, not damage: a store on a fleetless machine is healthy,
	// and a check nobody can keep green is one people learn to ignore.
	if report.code != fault.CodeOK {
		t.Errorf("verify exited %d over an unverified identity", report.code)
	}
}

// When an authority does confirm the caller, verify stops mentioning it.
func TestVerifiedIdentityIsNotReported(t *testing.T) {
	r := newRig(t)
	r.identity = func(user.Name) error { return nil }

	got := r.ok("alice", "verify")
	if strings.Contains(got.stdout, "nobody confirmed") {
		t.Errorf("a confirmed identity should not be remarked on:\n%s", got.stdout)
	}
}
