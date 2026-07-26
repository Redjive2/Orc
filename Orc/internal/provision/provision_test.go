package provision_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/provision"
	"orc/orc/internal/store"
)

// Provisioning is four things that can each fail, and the interesting question
// is always what is left behind when one of them does. A half-made identity —
// a name with no key, or a record with no mailbox — is worse than no identity at
// all, so most of what is worth testing here is the rollback.
//
// Nothing in this file runs Mailman. The Run seam is injected, which is what
// keeps a test from creating a mailbox in the developer's own store: the kind of
// accident a fleet tool gets to have exactly once.

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

// calls records what provisioning asked another tool to do, and can be made to
// refuse any of it.
type calls struct {
	ran   [][]string
	stdin []string
	fail  map[string]error
	// during runs while provisioning is part-way through. It is the only seam
	// that fires after the identity exists and before the steps that follow, so
	// it is how a test reaches the failures those steps can have.
	during func()
}

func (c *calls) run(args []string, stdin string) error {
	c.ran = append(c.ran, args)
	c.stdin = append(c.stdin, stdin)
	if c.during != nil {
		c.during()
	}
	if err, ok := c.fail[strings.Join(args, " ")]; ok {
		return err
	}
	return nil
}

func (c *calls) sawAdd() bool {
	for _, args := range c.ran {
		if len(args) > 2 && args[1] == "user" && args[2] == "add" {
			return true
		}
	}
	return false
}

func fresh(t *testing.T) (*store.Store, *calls, provision.Provisioner) {
	t.Helper()
	s, err := store.Create(filepath.Join(t.TempDir(), "fleet"), clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("creating a store: %v", err)
	}
	c := &calls{fail: map[string]error{}}
	p, err := provision.New(s, c.run)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, c, p
}

func mustUser(t *testing.T, raw string) user.Name {
	t.Helper()
	n, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("user %q: %v", raw, err)
	}
	return n
}

// TestAnIdentityIsMadeEverywhere walks the whole of a success: the record, the
// credential, the mailbox, and the configuration directory.
func TestAnIdentityIsMadeEverywhere(t *testing.T) {
	s, c, p := fresh(t)
	name := mustUser(t, "scribe")

	made, err := p.Identity(name, user.Name{})
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if made.Name().String() != "scribe" {
		t.Errorf("identity = %+v", made)
	}

	if _, err := s.Identity(name); err != nil {
		t.Errorf("the record was not written: %v", err)
	}
	key, err := s.Key(name)
	if err != nil || key == "" {
		t.Errorf("credential = %q, %v", key, err)
	}

	// The mailbox is made through Mailman's own command, never by writing its
	// records: a store's owner is the only thing that should decide what a valid
	// record in it looks like.
	if len(c.ran) != 1 {
		t.Fatalf("ran %v, want one mailman command", c.ran)
	}
	want := []string{"admin", "user", "add", "scribe", "--key", "-"}
	if strings.Join(c.ran[0], " ") != strings.Join(want, " ") {
		t.Errorf("ran %v, want %v", c.ran[0], want)
	}
	// The key travels on stdin. In argv it would be visible in `ps` to every
	// process on the machine, which is the whole reason `--key -` exists.
	if c.stdin[0] != key {
		t.Errorf("the mailbox was made with a different key than the store holds")
	}

	body, err := os.ReadFile(filepath.Join(s.ClaudeDir(name), "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(body), "scribe") {
		t.Errorf("the instructions do not name the identity:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(s.ClaudeDir(name), "memory")); err != nil {
		t.Errorf("the memory directory was not made: %v", err)
	}
}

// TestSettingsAreNeverAnEmptyPlaceholder guards the rule claude.go states.
//
// A settings file that exists and permits everything is a claim that the
// permission model is in force when it is not. The durable invariant is
// therefore not "the file is absent" — provisioning will write a compiled one —
// but "it is never a stub that grants everything". This holds before that lands
// and after it.
func TestSettingsAreNeverAnEmptyPlaceholder(t *testing.T) {
	s, _, p := fresh(t)
	name := mustUser(t, "scribe")
	if _, err := p.Identity(name, user.Name{}); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(s.ClaudeDir(name), "settings.json"))
	if os.IsNotExist(err) {
		return // nothing compiles it yet, which is the honest state
	}
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 || strings.TrimSpace(string(body)) == "{}" {
		t.Errorf("settings.json is an empty placeholder, which claims an enforcement that is not there:\n%s", body)
	}
}

// TestAFailedMailboxLeavesNoHalfMadeIdentity is the rollback that matters most.
//
// The mailbox is the one step performed by another tool, so it is the one most
// likely to refuse — and an identity with a name and a key but no mailbox cannot
// do the one thing every agent in this tree does.
func TestAFailedMailboxLeavesNoHalfMadeIdentity(t *testing.T) {
	s, c, p := fresh(t)
	name := mustUser(t, "scribe")
	c.fail["admin user add scribe --key -"] = errors.New("mailman: name is taken")

	_, err := p.Identity(name, user.Name{})
	if err == nil {
		t.Fatal("Identity should have failed")
	}
	if !strings.Contains(err.Error(), "mailbox") {
		t.Errorf("the failure should name what went wrong: %v", err)
	}

	// Nothing is left: no record, no credential, no directory. The name is free.
	if _, err := s.Identity(name); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("the record survived a failed provisioning: %v", err)
	}
	if _, err := os.Stat(s.IdentityDir(name)); !os.IsNotExist(err) {
		t.Errorf("the identity directory survived: %v", err)
	}

	// And the name really is reusable, which is the point of rolling back.
	c.fail = map[string]error{}
	if _, err := p.Identity(name, user.Name{}); err != nil {
		t.Errorf("the name was not free after the rollback: %v", err)
	}
}

// TestAFailedClaudeConfigIsReportedButNotRolledBack is the deliberate exception.
//
// An identity that exists everywhere except in its own configuration is a working
// identity — the next populate remakes the directory — whereas one whose mailbox
// creation half-succeeded is not. The failure is still returned, so it is not a
// silence.
func TestAFailedClaudeConfigIsReportedButNotRolledBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this depends on")
	}
	s, c, p := fresh(t)
	name := mustUser(t, "scribe")

	// The mailbox succeeds and then the configuration directory becomes
	// unwritable, which is the one ordering that reaches this branch.
	c.during = func() {
		dir := s.ClaudeDir(name)
		if _, err := os.Stat(dir); err == nil {
			_ = os.Chmod(dir, 0o500)
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		}
	}

	made, err := p.Identity(name, user.Name{})
	if err == nil {
		t.Fatal("a configuration that could not be written should be reported")
	}
	// The identity is returned alongside the failure, because it exists.
	if made.Name().String() != "scribe" {
		t.Errorf("identity = %+v, want the one that was made", made)
	}
	if _, err := s.Identity(name); err != nil {
		t.Errorf("the identity was rolled back when it should not have been: %v", err)
	}
	if key, err := s.Key(name); err != nil || key == "" {
		t.Errorf("the credential was rolled back too: %q, %v", key, err)
	}
}

// TestARollbackThatFailsReportsBothProblems: hiding the second would leave an
// operator wondering why the name is taken, with nothing to act on.
func TestARollbackThatFailsReportsBothProblems(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission that makes the rollback fail")
	}
	s, c, p := fresh(t)
	name := mustUser(t, "scribe")
	c.fail["admin user add scribe --key -"] = errors.New("mailman: refused")

	// The identity exists by the time the mailbox is asked for, so sealing its
	// parent here is what makes the *rollback* fail rather than the creation.
	c.during = func() {
		parent := filepath.Dir(s.IdentityDir(name))
		_ = os.Chmod(parent, 0o500)
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	}

	_, err := p.Identity(name, user.Name{})
	if err == nil {
		t.Fatal("Identity should have failed")
	}
	// Both: what happened, and that the cleanup did not — with the command that
	// clears what is left behind.
	for _, want := range []string{"refused", "roll back", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q: %v", want, err)
		}
	}
}

// TestRemovingAMailboxThatIsGoneIsNotAFailure: `orc remove identity` is the
// caller, and a removal that cannot be retried because half of it already
// succeeded is worse than one that tolerates the half.
func TestRemovingAMailboxThatIsGoneIsNotAFailure(t *testing.T) {
	_, c, p := fresh(t)
	name := mustUser(t, "scribe")

	for _, missing := range []string{
		`mailman: nothing matches "scribe"`,
		"mailman: user not found",
	} {
		c.fail["admin user remove scribe"] = errors.New(missing)
		if err := p.RemoveMailbox(name); err != nil {
			t.Errorf("%q should be tolerated: %v", missing, err)
		}
	}

	// Anything else is a real failure and is reported.
	c.fail["admin user remove scribe"] = errors.New("mailman: the store is locked")
	if err := p.RemoveMailbox(name); err == nil {
		t.Error("a real failure should be reported")
	}
}

// A provisioner that was not built with New has no store, and every entry point
// says so rather than dereferencing nothing.
func TestAnUnbuiltProvisionerRefusesRatherThanPanics(t *testing.T) {
	var p provision.Provisioner

	if _, err := p.Identity(mustUser(t, "scribe"), user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Identity: %v, want an internal fault", err)
	}
	if err := p.RemoveMailbox(mustUser(t, "scribe")); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("RemoveMailbox: %v, want an internal fault", err)
	}
}

func TestNamelessCallsAreRefused(t *testing.T) {
	_, _, p := fresh(t)

	if _, err := p.Identity(user.Name{}, user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Identity: %v, want an internal fault", err)
	}
	if err := p.RemoveMailbox(user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("RemoveMailbox: %v, want an internal fault", err)
	}
}

func TestNewRefusesAStoreItDoesNotHave(t *testing.T) {
	if _, err := provision.New(nil, func([]string, string) error { return nil }); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("New with no store: %v, want an internal fault", err)
	}
}

// A nil runner is not an omission: it means the real Mailman, which is what
// every caller outside a test wants. Nothing here runs it — the point is only
// that New accepts the default rather than refusing it.
func TestNoRunnerMeansTheRealMailman(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "fleet"), clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provision.New(s, nil); err != nil {
		t.Errorf("New with no runner should default to the real binary: %v", err)
	}
}
