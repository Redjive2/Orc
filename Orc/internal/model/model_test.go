package model_test

import (
	"strings"
	"testing"
	"time"

	"orc/common/user"
	"orc/orc/internal/model"
)

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

func mustName(t *testing.T, raw string) model.Name {
	t.Helper()
	n, err := model.ParseName(raw)
	if err != nil {
		t.Fatalf("name %q: %v", raw, err)
	}
	return n
}

func mustUser(t *testing.T, raw string) user.Name {
	t.Helper()
	n, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("user %q: %v", raw, err)
	}
	return n
}

// TestParseName: what a role or permission may be called, and why the refusals are
// refusals rather than normalisations.
func TestParseName(t *testing.T) {
	if got := mustName(t, "  Engineer  "); got.String() != "engineer" {
		t.Errorf("normalisation gave %q", got)
	}
	for _, bad := range []string{"", "  ", "--force", "role", "identity", "all", "..", "a/b", "a b", "-lead", ".lead"} {
		if _, err := model.ParseName(bad); err == nil {
			t.Errorf("%q was accepted as a name", bad)
		}
	}
	// A name is a path element, so the length bound is enforced rather than
	// truncated: a truncated name is a different name.
	if _, err := model.ParseName(strings.Repeat("a", model.MaxNameLen+1)); err == nil {
		t.Errorf("an over-long name was accepted")
	}
}

// TestGrantsAlwaysExpire: there is no way to construct a grant that never lapses,
// which is what makes the word "temporarily" in the specification true.
func TestGrantsAlwaysExpire(t *testing.T) {
	name := mustName(t, "extra")

	timed, err := model.TimedGrant(name, "boss", epoch, 30*time.Minute)
	if err != nil {
		t.Fatalf("timed: %v", err)
	}
	if !timed.Live(epoch, "") {
		t.Errorf("a fresh timed grant is not live")
	}
	if timed.Live(epoch.Add(31*time.Minute), "") {
		t.Errorf("a timed grant outlived its deadline")
	}
	if got := timed.Lapse(epoch); got != "30m left" {
		t.Errorf("lapse reads %q, want 30m left — rounding must not lose a minute", got)
	}

	scoped, err := model.SessionGrant(name, "boss", epoch, "session-a")
	if err != nil {
		t.Fatalf("scoped: %v", err)
	}
	for _, c := range []struct {
		session string
		want    bool
	}{
		{"session-a", true},
		{"session-b", false}, // a refresh minted a new id
		{"", false},          // depopulated
	} {
		if got := scoped.Live(epoch.Add(time.Hour), c.session); got != c.want {
			t.Errorf("a session grant in session %q is live=%v, want %v", c.session, got, c.want)
		}
	}

	// Neither shape can be built without an expiry, and no shape has both.
	if _, err := model.RestoreGrant(name, "boss", epoch, "", time.Time{}); err == nil {
		t.Errorf("a grant with no expiry was restored")
	}
	if _, err := model.RestoreGrant(name, "boss", epoch, "session-a", epoch.Add(time.Hour)); err == nil {
		t.Errorf("a grant with two expiries was restored")
	}
	if _, err := model.TimedGrant(name, "boss", epoch, 30*24*time.Hour); err == nil {
		t.Errorf("a month-long grant was accepted; that is a role")
	}
}

// TestIdentityFold: the transitions an identity's journal can describe, and the two
// it cannot.
func TestIdentityFold(t *testing.T) {
	boss := mustUser(t, "boss")
	atlas := mustUser(t, "atlas")

	operator, err := model.NewIdentity(boss, "0000000a-00000001", user.Name{}, epoch)
	if err != nil {
		t.Fatalf("operator: %v", err)
	}
	if !operator.IsOperator() {
		t.Fatalf("an identity with no boss is the operator")
	}

	// The operator has no boss and cannot be moved: it is the root of the tree.
	move, err := model.Move(boss, epoch, atlas)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := operator.With(move); err == nil {
		t.Errorf("the operator was moved")
	}

	agent, err := model.NewIdentity(atlas, "0000000b-00000002", boss, epoch)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	if _, err := model.NewIdentity(atlas, "0000000b-00000002", atlas, epoch); err == nil {
		t.Errorf("an identity was made its own boss")
	}

	// A role replaces rather than accumulating: one role means one authority.
	first, err := model.AssignRole(boss, epoch, mustName(t, "engineer"))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	second, err := model.AssignRole(boss, epoch, mustName(t, "architect"))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	agent, err = agent.With(first)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	agent, err = agent.With(second)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := agent.Role().String(); got != "architect" {
		t.Errorf("role is %q after two assignments, want architect", got)
	}

	// A second grant of the same permission replaces the first: two expiries on one
	// permission would leave "when does this lapse?" with two answers.
	short, err := model.TimedGrant(mustName(t, "extra"), "boss", epoch, time.Minute)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	long, err := model.TimedGrant(mustName(t, "extra"), "boss", epoch, time.Hour)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	for _, g := range []model.Grant{short, long} {
		ev, err := model.GrantPermission(boss, epoch, g)
		if err != nil {
			t.Fatalf("event: %v", err)
		}
		if agent, err = agent.With(ev); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	if got := len(agent.Grants()); got != 1 {
		t.Errorf("two grants of one permission left %d grants, want 1", got)
	}
	if got := agent.Grants()[0].Lapse(epoch); got != "1h left" {
		t.Errorf("the later grant did not win: %q", got)
	}

	// Revoking what was never granted is not an error, so `revoke` is safe twice.
	revoke, err := model.RevokePermission(boss, epoch, mustName(t, "never-granted"))
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := agent.With(revoke); err != nil {
		t.Errorf("revoking an ungranted permission failed: %v", err)
	}
}

// TestRoleFold: authority, description, and the permission set, plus the idempotence
// that makes two agents assigning the same permission a race with an agreed outcome.
func TestRoleFold(t *testing.T) {
	boss := mustUser(t, "boss")

	role, err := model.NewRole(mustName(t, "engineer"), mustAuthority(t, 60), "writes the code", epoch)
	if err != nil {
		t.Fatalf("role: %v", err)
	}

	permit, err := model.Permit(boss, epoch, mustName(t, "edit-anno"))
	if err != nil {
		t.Fatalf("permit: %v", err)
	}
	role, err = role.With(permit)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if role, err = role.With(permit); err != nil {
		t.Errorf("permitting twice failed: %v", err)
	}
	if got := len(role.Permissions()); got != 1 {
		t.Errorf("permitting twice left %d permissions, want 1", got)
	}

	unpermit, err := model.Unpermit(boss, epoch, mustName(t, "edit-anno"))
	if err != nil {
		t.Fatalf("unpermit: %v", err)
	}
	if role, err = role.With(unpermit); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := len(role.Permissions()); got != 0 {
		t.Errorf("unpermit left %d permissions, want 0", got)
	}
	if role, err = role.With(unpermit); err != nil {
		t.Errorf("unpermitting twice failed: %v", err)
	}

	// A description is required, and cannot smuggle a newline into a journal line.
	if _, err := model.NewRole(mustName(t, "quiet"), mustAuthority(t, 10), "   ", epoch); err == nil {
		t.Errorf("a role with no description was accepted")
	}
	if _, err := model.NewRole(mustName(t, "sneaky"), mustAuthority(t, 10), "a\nb", epoch); err == nil {
		t.Errorf("a description with a newline was accepted")
	}
}

func mustAuthority(t *testing.T, n int) model.Authority {
	t.Helper()
	a, err := model.NewAuthority(n)
	if err != nil {
		t.Fatalf("authority %d: %v", n, err)
	}
	return a
}

// TestIDShape: an id is also a filename-safe token, and it sorts into creation order.
func TestIDShape(t *testing.T) {
	first, err := model.NewID(epoch, strings.NewReader("abcd"))
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	later, err := model.NewID(epoch.Add(time.Second), strings.NewReader("abcd"))
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	if first >= later {
		t.Errorf("ids do not sort into creation order: %q then %q", first, later)
	}
	for _, bad := range []string{"", "nope", "zz-12345678", "1234-abc", "1234-abcdefgh"} {
		if err := model.CheckID(bad); err == nil {
			t.Errorf("id %q was accepted", bad)
		}
	}
	// A short entropy source is a hard failure rather than a short id.
	if _, err := model.NewID(epoch, strings.NewReader("ab")); err == nil {
		t.Errorf("a truncated entropy read produced an id")
	}
}
