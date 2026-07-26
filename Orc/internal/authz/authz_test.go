package authz_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
)

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

// build is a small fleet builder, so a test says what tree it wants rather than how
// to construct one.
type build struct {
	t           *testing.T
	identities  []model.Identity
	roles       []model.Role
	permissions []model.Permission
	n           int
}

func newBuild(t *testing.T) *build { return &build{t: t} }

func (b *build) id(name, boss, role string) *build {
	b.t.Helper()
	b.n++

	who, err := user.Parse(name)
	if err != nil {
		b.t.Fatalf("name %q: %v", name, err)
	}
	var over user.Name
	if boss != "" {
		if over, err = user.Parse(boss); err != nil {
			b.t.Fatalf("boss %q: %v", boss, err)
		}
	}
	i, err := model.NewIdentity(who, fmt.Sprintf("%08x-0000000%x", b.n, b.n), over, epoch)
	if err != nil {
		b.t.Fatalf("identity %q: %v", name, err)
	}
	if role != "" {
		r, err := model.ParseName(role)
		if err != nil {
			b.t.Fatalf("role %q: %v", role, err)
		}
		ev, err := model.AssignRole(who, epoch, r)
		if err != nil {
			b.t.Fatalf("assign %q: %v", role, err)
		}
		if i, err = i.With(ev); err != nil {
			b.t.Fatalf("assign %q: %v", role, err)
		}
	}
	b.identities = append(b.identities, i)
	return b
}

func (b *build) role(name string, authority int, permissions ...string) *build {
	b.t.Helper()

	n, err := model.ParseName(name)
	if err != nil {
		b.t.Fatalf("role %q: %v", name, err)
	}
	level, err := model.NewAuthority(authority)
	if err != nil {
		b.t.Fatalf("authority %d: %v", authority, err)
	}
	r, err := model.NewRole(n, level, "a job", epoch)
	if err != nil {
		b.t.Fatalf("role %q: %v", name, err)
	}
	for _, p := range permissions {
		pn, err := model.ParseName(p)
		if err != nil {
			b.t.Fatalf("permission %q: %v", p, err)
		}
		ev, err := model.Permit(mustUser(b.t, "boss"), epoch, pn)
		if err != nil {
			b.t.Fatalf("permit %q: %v", p, err)
		}
		if r, err = r.With(ev); err != nil {
			b.t.Fatalf("permit %q: %v", p, err)
		}
	}
	b.roles = append(b.roles, r)
	return b
}

func (b *build) perm(name string, floor int, patterns ...string) *build {
	b.t.Helper()

	n, err := model.ParseName(name)
	if err != nil {
		b.t.Fatalf("permission %q: %v", name, err)
	}
	level, err := model.NewAuthority(floor)
	if err != nil {
		b.t.Fatalf("floor %d: %v", floor, err)
	}
	parsed, err := model.ParsePatterns(patterns)
	if err != nil {
		b.t.Fatalf("patterns %v: %v", patterns, err)
	}
	p, err := model.NewPermission(n, level, parsed, epoch)
	if err != nil {
		b.t.Fatalf("permission %q: %v", name, err)
	}
	b.permissions = append(b.permissions, p)
	return b
}

func (b *build) fleet() (authz.Fleet, error) {
	return authz.New(authz.Snapshot{
		Identities:  b.identities,
		Roles:       b.roles,
		Permissions: b.permissions,
		Now:         epoch,
	})
}

func (b *build) must() authz.Fleet {
	b.t.Helper()
	f, err := b.fleet()
	if err != nil {
		b.t.Fatalf("deriving: %v", err)
	}
	return f
}

func mustUser(t *testing.T, name string) user.Name {
	t.Helper()
	n, err := user.Parse(name)
	if err != nil {
		t.Fatalf("name %q: %v", name, err)
	}
	return n
}

// TestStructuralRefusals: a fleet that cannot be derived refuses every command, so
// the derivation refuses to be built. Each of these makes "may they?" unanswerable,
// and answering it from half a tree would be worse than refusing.
func TestStructuralRefusals(t *testing.T) {
	cases := []struct {
		name  string
		build func() *build
		want  string
	}{
		{"no identities", func() *build { return newBuild(t) }, "bootstrap"},
		{"no operator", func() *build {
			return newBuild(t).id("a", "b", "").id("b", "a", "")
		}, "no identity is the operator"},
		{"two operators", func() *build {
			return newBuild(t).id("a", "", "").id("b", "", "")
		}, "one operator"},
		{"missing boss", func() *build {
			return newBuild(t).id("boss", "", "").id("orphan", "ghost", "")
		}, "ghost"},
		{"cycle below the root", func() *build {
			return newBuild(t).id("boss", "", "").id("a", "b", "").id("b", "a", "")
		}, "cycle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.build().fleet()
			if err == nil {
				t.Fatalf("a broken tree derived cleanly")
			}
			if !contains(err.Error(), c.want) {
				t.Errorf("the error does not mention %q: %v", c.want, err)
			}
		})
	}
}

// TestAuthorityIsCapped is the first rule of Auth_Perm_Role.md as arithmetic.
func TestAuthorityIsCapped(t *testing.T) {
	f := newBuild(t).
		role("architect", 80).role("engineer", 40).
		id("boss", "", "").
		id("atlas", "boss", "architect").
		id("ember", "atlas", "engineer").
		id("quill", "ember", "architect").
		must()

	for _, c := range []struct {
		name             string
		effective, asked int
	}{
		{"boss", 100, 100},
		{"atlas", 80, 80},
		{"ember", 40, 40},
		{"quill", 40, 80}, // an architect under an engineer is an engineer's worth
	} {
		effective, asked := f.Authority(mustUser(t, c.name))
		if effective.Int() != c.effective || asked.Int() != c.asked {
			t.Errorf("%s has authority %d (asked %d), want %d (asked %d)",
				c.name, effective.Int(), asked.Int(), c.effective, c.asked)
		}
	}
}

// TestNoRoleNoAuthority: an identity with no role holds nothing at all. It is the
// starting state of every hire, and every permission check has to fail closed on it.
func TestNoRoleNoAuthority(t *testing.T) {
	f := newBuild(t).
		perm("everything", 1, "read(**)").role("open", 50, "everything").
		id("boss", "", "").id("fresh", "boss", "").
		must()

	fresh := mustUser(t, "fresh")
	if effective, _ := f.Authority(fresh); !effective.Zero() {
		t.Errorf("a roleless identity has authority %s", effective)
	}
	if len(f.Clauses(fresh)) != 0 {
		t.Errorf("a roleless identity holds %d clauses", len(f.Clauses(fresh)))
	}
	if f.Allows(fresh, model.KindRead, "Anno/x.go") {
		t.Errorf("a roleless identity may read")
	}
}

// TestFloorFiltersWholePermission: clearing a floor is a condition on the whole
// permission, not on each clause, because a permission that half applied would be a
// different permission from the one somebody audited.
func TestFloorFiltersWholePermission(t *testing.T) {
	f := newBuild(t).
		perm("lead", 60, "read(**)", "write(**)", "spawn(24)").
		role("architect", 80, "lead").role("engineer", 50, "lead").
		id("boss", "", "").
		id("atlas", "boss", "architect").
		id("ember", "boss", "engineer").
		must()

	if got := len(f.Clauses(mustUser(t, "atlas"))); got != 3 {
		t.Errorf("atlas holds %d clauses, want 3", got)
	}
	// The role names `lead`, but 50 is under the floor of 60, so none of it applies.
	if got := len(f.Clauses(mustUser(t, "ember"))); got != 0 {
		t.Errorf("ember holds %d clauses below the floor, want 0", got)
	}
	if _, ok := f.Budget(mustUser(t, "ember")); ok {
		t.Errorf("ember has a spawn budget it cannot reach")
	}
}

// TestIntersectionKeepsBothHalves: a child asking for a wide clause under a boss
// holding two narrow ones keeps both, which a "best match" search would not.
func TestIntersectionKeepsBothHalves(t *testing.T) {
	f := newBuild(t).
		perm("wide", 10, "write(Common/**)").
		perm("narrow", 10, "write(Common/user/**)", "write(Common/clock/**)").
		role("boss-role", 90, "narrow").role("child-role", 50, "wide").
		id("boss", "", "").
		id("mid", "boss", "boss-role").
		id("kid", "mid", "child-role").
		must()

	kid := mustUser(t, "kid")
	var args []string
	for _, c := range f.Clauses(kid) {
		args = append(args, c.Pattern.String())
		if !c.Capped {
			t.Errorf("%s should be marked capped", c.Pattern)
		}
	}
	if len(args) != 2 {
		t.Fatalf("kid holds %v, want both halves of the boss's set", args)
	}
	if !f.Allows(kid, model.KindWrite, "Common/user/user.go") {
		t.Errorf("kid cannot write Common/user, which its boss can")
	}
	if f.Allows(kid, model.KindWrite, "Common/fault/fault.go") {
		t.Errorf("kid can write Common/fault, which its boss cannot")
	}
}

// TestUnprovableIntersectionFailsClosed: a pair of overlapping globs where neither
// provably contains the other loses the child's clause. That is the fail-closed
// direction, and it is the only one that keeps the cap honest.
func TestUnprovableIntersectionFailsClosed(t *testing.T) {
	f := newBuild(t).
		perm("odd-boss", 10, "read(*/internal/**)").
		perm("odd-kid", 10, "read(Anno/*/render/**)").
		role("boss-role", 90, "odd-boss").role("kid-role", 50, "odd-kid").
		id("boss", "", "").
		id("mid", "boss", "boss-role").
		id("kid", "mid", "kid-role").
		must()

	if got := len(f.Clauses(mustUser(t, "kid"))); got != 0 {
		t.Errorf("kid kept %d unprovable clauses, want 0", got)
	}
}

// TestOperatorShortCircuits: a fleet whose owner has not created a single permission
// is still one they can run.
func TestOperatorShortCircuits(t *testing.T) {
	f := newBuild(t).id("boss", "", "").must()
	boss := mustUser(t, "boss")

	if !f.Allows(boss, model.KindWrite, "anything/at/all") {
		t.Errorf("the operator cannot write in an empty fleet")
	}
	if budget, ok := f.Budget(boss); !ok || budget != model.MaxLoad {
		t.Errorf("the operator's budget is %d/%v, want the maximum", budget, ok)
	}
}

// TestControlsIsAncestry: "all agents are able to act on their subagents without
// need for permissions" — and on nobody else, including themselves.
func TestControlsIsAncestry(t *testing.T) {
	f := newBuild(t).
		id("boss", "", "").
		id("atlas", "boss", "").
		id("ember", "atlas", "").
		id("quill", "boss", "").
		must()

	cases := []struct {
		actor, target string
		want          bool
	}{
		{"boss", "atlas", true},
		{"boss", "ember", true}, // transitively
		{"atlas", "ember", true},
		{"ember", "atlas", false},
		{"atlas", "quill", false}, // peers
		{"atlas", "atlas", false}, // not itself
		{"atlas", "ghost", false},
	}
	for _, c := range cases {
		if got := f.Controls(mustUser(t, c.actor), user.Name{}); got {
			t.Errorf("a zero target was controlled")
		}
		target, err := user.Parse(c.target)
		if err != nil {
			t.Fatalf("name: %v", err)
		}
		if got := f.Controls(mustUser(t, c.actor), target); got != c.want {
			t.Errorf("%s controls %s = %v, want %v", c.actor, c.target, got, c.want)
		}
	}
}

// TestWouldCycle: the check that runs before a move is written.
func TestWouldCycle(t *testing.T) {
	f := newBuild(t).
		id("boss", "", "").id("atlas", "boss", "").id("ember", "atlas", "").
		must()

	if !f.WouldCycle(mustUser(t, "atlas"), mustUser(t, "ember")) {
		t.Errorf("moving atlas under its own subordinate is a cycle")
	}
	if !f.WouldCycle(mustUser(t, "atlas"), mustUser(t, "atlas")) {
		t.Errorf("moving atlas under itself is a cycle")
	}
	if f.WouldCycle(mustUser(t, "ember"), mustUser(t, "boss")) {
		t.Errorf("moving ember up is not a cycle")
	}
}

// TestGrantsAreLiveOrNot: a session-scoped grant on an unpopulated identity has
// already lapsed, which is what makes the default honest rather than provisional.
func TestGrantsAreLiveOrNot(t *testing.T) {
	b := newBuild(t).perm("extra", 10, "read(Docs/**)").role("engineer", 50).
		id("boss", "", "").id("ember", "boss", "engineer")

	ember := b.identities[1]
	name, err := model.ParseName("extra")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	timed, err := model.TimedGrant(name, "boss", epoch, 30*time.Minute)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	scoped, err := model.SessionGrant(name, "boss", epoch, "some-session")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	for _, c := range []struct {
		name  string
		grant model.Grant
		at    time.Time
		want  int
	}{
		{"timed, in force", timed, epoch, 1},
		{"timed, lapsed", timed, epoch.Add(time.Hour), 0},
		{"session-scoped, nothing populated", scoped, epoch, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev, err := model.GrantPermission(mustUser(t, "boss"), epoch, c.grant)
			if err != nil {
				t.Fatalf("event: %v", err)
			}
			with, err := ember.With(ev)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			f, err := authz.New(authz.Snapshot{
				Identities:  []model.Identity{b.identities[0], with},
				Roles:       b.roles,
				Permissions: b.permissions,
				Now:         c.at,
			})
			if err != nil {
				t.Fatalf("deriving: %v", err)
			}
			if got := len(f.Clauses(mustUser(t, "ember"))); got != c.want {
				t.Errorf("ember holds %d clauses, want %d", got, c.want)
			}
		})
	}
}

// TestDerivationInvariants is the property test: random trees, random roles, random
// floors, and the four rules that must hold for every identity in every fleet.
//
// This is where the model is proven. The CLI tests assert the commands are wired to
// it; this asserts the arithmetic itself, over shapes nobody would think to write by
// hand.
func TestDerivationInvariants(t *testing.T) {
	const trees = 200

	for seed := range trees {
		rng := rand.New(rand.NewSource(int64(seed)))
		b := randomFleet(t, rng)

		f, err := b.fleet()
		if err != nil {
			t.Fatalf("seed %d: a generated fleet did not derive: %v", seed, err)
		}

		for _, name := range f.Names() {
			i, err := f.Identity(name)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			effective, asked := f.Authority(name)

			// 1. Authority never exceeds the boss's, and never exceeds what the
			//    role asked for.
			if !i.IsOperator() {
				bossLevel, _ := f.Authority(i.Boss())
				if effective.Int() > bossLevel.Int() {
					t.Fatalf("seed %d: %s has authority %d over its boss's %d",
						seed, name, effective.Int(), bossLevel.Int())
				}
			}
			if !asked.Zero() && effective.Int() > asked.Int() {
				t.Fatalf("seed %d: %s has authority %d over its role's %d",
					seed, name, effective.Int(), asked.Int())
			}

			// 2. Every clause is provably inside some clause the boss holds.
			if !i.IsOperator() {
				bossClauses := f.Clauses(i.Boss())
				for _, c := range f.Clauses(name) {
					covered := false
					for _, bc := range bossClauses {
						if bc.Pattern.Contains(c.Pattern) {
							covered = true
							break
						}
					}
					if !covered {
						t.Fatalf("seed %d: %s holds %s, which its boss %s does not",
							seed, name, c.Pattern, i.Boss())
					}
				}
			}

			// 3. No clause comes from a permission whose floor is above the
			//    identity's authority.
			for _, c := range f.Clauses(name) {
				p, ok := f.Permission(c.Permission)
				if !ok {
					t.Fatalf("seed %d: %s holds a clause of a permission that does not exist", seed, name)
				}
				if !effective.AtLeast(p.Floor()) {
					t.Fatalf("seed %d: %s has authority %s and holds %s, floor %s",
						seed, name, effective, c.Permission, p.Floor())
				}
			}

			// 4. The boss chain terminates at the operator, and the identity is in
			//    its own subtree exactly once.
			chain := f.Chain(name)
			if !i.IsOperator() {
				if len(chain) == 0 || chain[len(chain)-1].String() != f.Operator().String() {
					t.Fatalf("seed %d: %s's chain does not end at the operator: %v",
						seed, name, user.Names(chain))
				}
			}
			seen := 0
			for _, below := range f.Subtree(name) {
				if below.String() == name.String() {
					seen++
				}
			}
			if seen != 1 {
				t.Fatalf("seed %d: %s appears %d times in its own subtree", seed, name, seen)
			}
		}
	}
}

// randomFleet builds a tree that is valid by construction — every boss already
// exists when a child is added, so no generated fleet is ever a cycle.
func randomFleet(t *testing.T, rng *rand.Rand) *build {
	t.Helper()
	b := newBuild(t)

	paths := []string{"**", "Anno/**", "Anno/internal/**", "Common/**", "Common/user/**", "Docs/**"}
	kinds := []string{"read", "write"}

	permCount := 1 + rng.Intn(4)
	for p := range permCount {
		var patterns []string
		for range 1 + rng.Intn(3) {
			candidate := fmt.Sprintf("%s(%s)", kinds[rng.Intn(len(kinds))], paths[rng.Intn(len(paths))])
			if !containsString(patterns, candidate) {
				patterns = append(patterns, candidate)
			}
		}
		if rng.Intn(3) == 0 {
			patterns = append(patterns, fmt.Sprintf("spawn(%d)", 1+rng.Intn(40)))
		}
		b.perm(fmt.Sprintf("perm%d", p), 1+rng.Intn(99), patterns...)
	}

	roleCount := 1 + rng.Intn(4)
	for r := range roleCount {
		var perms []string
		for range rng.Intn(permCount + 1) {
			candidate := fmt.Sprintf("perm%d", rng.Intn(permCount))
			if !containsString(perms, candidate) {
				perms = append(perms, candidate)
			}
		}
		b.role(fmt.Sprintf("role%d", r), 1+rng.Intn(99), perms...)
	}

	b.id("boss", "", "")
	names := []string{"boss"}
	for n := range 1 + rng.Intn(12) {
		name := fmt.Sprintf("agent%d", n)
		boss := names[rng.Intn(len(names))]
		role := ""
		if rng.Intn(6) > 0 {
			role = fmt.Sprintf("role%d", rng.Intn(roleCount))
		}
		b.id(name, boss, role)
		names = append(names, name)
	}
	return b
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
