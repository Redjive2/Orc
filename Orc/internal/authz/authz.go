// Package authz answers one question: may this identity do that?
//
// It is the only package that computes an *effective* authority or permission,
// and it is pure — no filesystem, no clock beyond the instant it is handed, no
// process. Everything it knows arrives in New as a snapshot, which is what makes
// the rules in Docs/Orc/Auth_Perm_Role.md testable as arithmetic rather than as
// a store.
//
// The rules, from that document:
//
//	authority(operator) = 100
//	authority(i)        = min(authority(role(i)), authority(boss(i)))
//	perms(operator)     = every permission
//	perms(i)            = (perms(role(i)) ∪ grants(i)) ∩ perms(boss(i))
//	                      filtered to those whose floor ≤ authority(i)
//
// Nothing derived is ever stored. "A subagent can only have as high of a
// permission as their boss" is a live constraint, so `orc move` re-caps a whole
// subtree by appending one line to one journal, and a demotion cannot leave a
// stale cached permission behind anywhere. The cost is that every command
// derives the fleet on startup; the fleet is small, the derivation is linear in
// identities times clauses, and correctness here is worth more than the
// microseconds.
package authz

import (
	"fmt"
	"slices"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
)

// Source says where a clause came from, so a card can show provenance rather
// than a bare list an operator has to reverse-engineer.
type Source uint8

// The provenances.
const (
	FromRole Source = iota + 1
	FromGrant
	FromOperator
)

// String names the source as a card shows it.
func (s Source) String() string {
	switch s {
	case FromRole:
		return "from role"
	case FromGrant:
		return "granted"
	case FromOperator:
		return "operator"
	default:
		return "unknown"
	}
}

// Clause is one effective permission clause: what is allowed, which permission
// allowed it, where that came from, and whether the boss chain narrowed it.
type Clause struct {
	Pattern    model.Pattern
	Permission model.Name
	Source     Source
	// Capped reports that the boss chain narrowed this clause from what the
	// role or grant asked for. Asked is what it asked for; a card shows both,
	// because an agent told it may write Common/user/** while its role says
	// Common/** will otherwise file a bug.
	Capped bool
	Asked  model.Pattern
	// Grant is the grant behind the clause, when Source is FromGrant.
	Grant model.Grant
}

// Compare orders clauses for a stable card.
func (c Clause) Compare(other Clause) int {
	if n := c.Pattern.Compare(other.Pattern); n != 0 {
		return n
	}
	return c.Permission.Compare(other.Permission)
}

// derived is what New computes once per identity.
type derived struct {
	authority model.Authority
	asked     model.Authority // what the role asked for, before the cap
	clauses   []Clause
	depth     int
}

// Fleet is a derived snapshot of the whole tree. The zero value is not usable;
// build one with New.
type Fleet struct {
	identities  map[string]model.Identity
	roles       map[string]model.Role
	permissions map[string]model.Permission
	sessions    map[string]string
	now         time.Time

	tariff   model.Tariff
	operator user.Name
	order    []string // every identity, breadth-first from the operator
	children map[string][]string
	derived  map[string]derived
	problems []string
}

// Snapshot is everything the derivation needs, as the store hands it over.
//
// Sessions maps an identity name to its current session id, and is how a
// session-scoped grant knows whether it has lapsed. It is empty for the whole of
// milestone 1, where nothing is populated — which is correct rather than
// provisional: with no session, a session-scoped grant has already lapsed.
type Snapshot struct {
	Identities  []model.Identity
	Roles       []model.Role
	Permissions []model.Permission
	Sessions    map[string]string
	Now         time.Time
	// Tariff is what this fleet charges for thinking. The zero value is the
	// built-in price list, which is what a fleet that has never set one pays.
	//
	// It travels in the snapshot rather than being read here for the reason
	// nothing else in this package reads anything: the derivation is pure, and a
	// budget computed against whichever tariff happened to be on disk at the
	// moment of the call is one nobody can reproduce.
	Tariff model.Tariff
}

// New derives the fleet.
//
// Three structural failures are refused outright rather than worked around,
// because every one of them makes "may they?" unanswerable: no operator, a boss
// who does not exist, and a cycle. A fleet that cannot be derived is a fleet
// where no command should run — including, deliberately, a read-only one, since
// an answer derived from half a tree is worse than a refusal.
//
// Two softer problems are tolerated and reported through Problems: an identity
// whose role was removed under it, and a role naming a permission that no longer
// exists. Both fail closed — they grant nothing — and both are things `orc
// verify` should name rather than things that should stop the fleet.
func New(snap Snapshot) (Fleet, error) {
	f := Fleet{
		identities:  make(map[string]model.Identity, len(snap.Identities)),
		roles:       make(map[string]model.Role, len(snap.Roles)),
		permissions: make(map[string]model.Permission, len(snap.Permissions)),
		sessions:    make(map[string]string, len(snap.Sessions)),
		now:         clock.Normalise(snap.Now),
		children:    make(map[string][]string, len(snap.Identities)),
		derived:     make(map[string]derived, len(snap.Identities)),
		tariff:      snap.Tariff.WithDefaults(),
	}
	if f.now.IsZero() {
		return Fleet{}, fault.Internal{Where: "authz.New", Detail: "no instant given"}
	}

	for _, r := range snap.Roles {
		if r.Zero() {
			return Fleet{}, fault.Internal{Where: "authz.New", Detail: "unconstructed role"}
		}
		f.roles[r.Name().String()] = r
	}
	for _, p := range snap.Permissions {
		if p.Zero() {
			return Fleet{}, fault.Internal{Where: "authz.New", Detail: "unconstructed permission"}
		}
		f.permissions[p.Name().String()] = p
	}
	for name, session := range snap.Sessions {
		f.sessions[name] = session
	}

	for _, i := range snap.Identities {
		if i.Zero() {
			return Fleet{}, fault.Internal{Where: "authz.New", Detail: "unconstructed identity"}
		}
		key := i.Name().String()
		if _, clash := f.identities[key]; clash {
			return Fleet{}, fault.Conflict{Path: key, Reason: "two identities have the same name"}
		}
		f.identities[key] = i
		if i.IsOperator() {
			if !f.operator.Zero() {
				return Fleet{}, fault.Conflict{Path: key, Reason: fmt.Sprintf(
					"%s and %s both have no boss; there is one operator, and the tree has one root",
					f.operator, i.Name())}
			}
			f.operator = i.Name()
		}
	}
	if len(f.identities) == 0 {
		return Fleet{}, fault.NotFound{Target: "any identity; run `orc bootstrap` first"}
	}
	if f.operator.Zero() {
		return Fleet{}, fault.Conflict{Path: "the fleet",
			Reason: "no identity is the operator; every boss chain has to end somewhere"}
	}

	// Every boss must exist. Checked before the walk, so a missing one is
	// reported as itself rather than as an unreachable identity.
	for key, i := range f.identities {
		if i.IsOperator() {
			continue
		}
		boss := i.Boss().String()
		if _, ok := f.identities[boss]; !ok {
			return Fleet{}, fault.NotFound{Target: fmt.Sprintf(
				"%s, the boss of %s", boss, key)}
		}
		f.children[boss] = append(f.children[boss], key)
	}
	for boss := range f.children {
		slices.Sort(f.children[boss])
	}

	if err := f.walk(); err != nil {
		return Fleet{}, err
	}
	return f, nil
}

// walk visits every identity breadth-first from the operator, deriving each one
// from its boss — which the order guarantees is already derived.
//
// Reaching fewer identities than exist is how a cycle shows up: a cycle is
// unreachable from the root by construction, so the count is the detection. It
// is done this way rather than with a per-identity chain walk because it is one
// pass rather than one per identity, and because the error can then name every
// identity involved instead of the first one noticed.
func (f *Fleet) walk() error {
	f.deriveOperator()
	f.order = []string{f.operator.String()}

	for head := 0; head < len(f.order); head++ {
		parent := f.order[head]
		for _, child := range f.children[parent] {
			f.derive(child, parent)
			f.order = append(f.order, child)
		}
	}

	if len(f.order) != len(f.identities) {
		var stranded []string
		for key := range f.identities {
			if !slices.Contains(f.order, key) {
				stranded = append(stranded, key)
			}
		}
		slices.Sort(stranded)
		return fault.Conflict{Path: "the fleet", Reason: fmt.Sprintf(
			"%d identities are in a boss cycle or unreachable from %s: %v",
			len(stranded), f.operator, stranded)}
	}
	return nil
}

// deriveOperator gives the root every permission there is.
//
// It is written as an enumeration of the actual permissions rather than as a
// wildcard, so a card for the operator shows what exists rather than a claim of
// omnipotence, and so `orc status` on the operator is a useful inventory.
// Allows short-circuits for the operator anyway, which is what makes a fleet
// with no permissions at all still fully usable by its owner.
func (f *Fleet) deriveOperator() {
	key := f.operator.String()
	clauses := make([]Clause, 0, len(f.permissions)*2)
	for _, name := range f.permissionNames() {
		p := f.permissions[name]
		for _, pattern := range p.Patterns() {
			clauses = append(clauses, Clause{
				Pattern: pattern, Asked: pattern, Permission: p.Name(), Source: FromOperator,
			})
		}
	}
	slices.SortFunc(clauses, Clause.Compare)
	f.derived[key] = derived{
		authority: model.OperatorAuthority(),
		asked:     model.OperatorAuthority(),
		clauses:   clauses,
	}
}

// derive computes one identity from its already-derived boss.
func (f *Fleet) derive(key, bossKey string) {
	i := f.identities[key]
	boss := f.derived[bossKey]

	// Authority: the lower of what the role asks and what the boss has. No role
	// means no authority at all, which is the right starting state — hired, with
	// no job yet — and it makes every permission check below fail closed.
	var asked model.Authority
	role, hasRole := f.roles[i.Role().String()]
	switch {
	case i.Role().Zero():
		// Nothing to report: an identity with no role is ordinary.
	case !hasRole:
		f.problems = append(f.problems, fmt.Sprintf(
			"%s holds role %s, which does not exist; it has no authority until one is assigned",
			key, i.Role()))
	default:
		asked = role.Authority()
	}
	authority := model.Min(asked, boss.authority)

	// The clauses this identity asks for: its role's permissions, then its live
	// grants. A grant is listed after the role so that when both name the same
	// permission the grant's provenance is the one a card shows — a grant is the
	// deliberate act, and it is the one with an expiry worth seeing.
	var wanted []Clause
	if hasRole {
		for _, permName := range role.Permissions() {
			p, ok := f.permissions[permName.String()]
			if !ok {
				f.problems = append(f.problems, fmt.Sprintf(
					"role %s grants permission %s, which does not exist", role.Name(), permName))
				continue
			}
			wanted = append(wanted, f.clausesOf(p, authority, FromRole, model.Grant{})...)
		}
	}
	for _, g := range i.LiveGrants(f.now, f.sessions[key]) {
		p, ok := f.permissions[g.Permission().String()]
		if !ok {
			f.problems = append(f.problems, fmt.Sprintf(
				"%s was granted permission %s, which does not exist", key, g.Permission()))
			continue
		}
		wanted = append(wanted, f.clausesOf(p, authority, FromGrant, g)...)
	}

	f.derived[key] = derived{
		authority: authority,
		asked:     asked,
		clauses:   intersect(wanted, boss.clauses),
		depth:     boss.depth + 1,
	}
}

// clausesOf expands a permission into clauses, dropping the whole permission if
// the holder does not clear its floor.
//
// The floor is checked per permission rather than per clause because that is
// what Auth_Perm_Role.md describes — "only those at or above its permission
// level can have that permission" — and because a permission that half applied
// would be a different permission from the one somebody audited.
func (f *Fleet) clausesOf(p model.Permission, holder model.Authority, src Source, g model.Grant) []Clause {
	if !holder.AtLeast(p.Floor()) {
		return nil
	}
	out := make([]Clause, 0, 4)
	for _, pattern := range p.Patterns() {
		out = append(out, Clause{Pattern: pattern, Asked: pattern, Permission: p.Name(), Source: src, Grant: g})
	}
	return out
}

// intersect caps what an identity asks for by what its boss holds.
//
// Every (asked, boss) pair of the same kind is narrowed; each survivor is a
// clause the boss provably permits (see model.Narrow). Pairing every combination
// rather than looking for one "best" boss clause is what makes the result a real
// intersection of two unions: a child asking write(Common/**) under a boss
// holding write(Common/user/**) and write(Common/clock/**) keeps both halves,
// and a "best match" search would have kept one and silently lost the other.
//
// Anything that cannot be proven inside the boss's set is dropped. That is the
// fail-closed direction, and it is the only one that keeps the cap honest.
func intersect(wanted, bossClauses []Clause) []Clause {
	var out []Clause
	for _, w := range wanted {
		for _, b := range bossClauses {
			if b.Pattern.Kind() != w.Pattern.Kind() {
				continue
			}
			narrowed, ok := model.Narrow(w.Pattern, b.Pattern)
			if !ok {
				continue
			}
			out = append(out, Clause{
				Pattern:    narrowed,
				Asked:      w.Asked,
				Permission: w.Permission,
				Source:     w.Source,
				Capped:     !narrowed.Equal(w.Asked),
				Grant:      w.Grant,
			})
		}
	}
	return dedupe(out)
}

// dedupe drops exact repeats and clauses another clause already covers.
//
// Narrowing a union against a union produces overlaps — write(Common/user/**) and
// write(Common/**) can both survive when the boss holds the wider one — and a card
// listing both would suggest a distinction that does not exist.
//
// Provenance decides which survivor is kept, and the rule is worth stating because
// the obvious one is wrong. Two clauses of the *same permission* absorb each other
// regardless of source, preferring the role's: a grant of something a role already
// gives permanently adds nothing, and listing it twice makes a card look like the
// identity has two of something. But a **wider** granted clause is never absorbed
// by a narrower role clause, because that is a permission that is about to lapse
// and the expiry is the whole point of showing it.
func dedupe(in []Clause) []Clause {
	out := make([]Clause, 0, len(in))
	for _, c := range in {
		covered := false
		for j, kept := range out {
			if !kept.Permission.Equal(c.Permission) {
				continue
			}
			if kept.Pattern.Equal(c.Pattern) {
				// Same clause twice. The role's copy wins, being the one that does
				// not lapse.
				if c.Source == FromRole && kept.Source == FromGrant {
					out[j] = c
				}
				covered = true
				break
			}
			if kept.Source == c.Source {
				if kept.Pattern.Contains(c.Pattern) {
					covered = true
					break
				}
				if c.Pattern.Contains(kept.Pattern) {
					out[j] = c
					covered = true
					break
				}
			}
		}
		if !covered {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, Clause.Compare)
	return out
}

func (f Fleet) permissionNames() []string {
	names := make([]string, 0, len(f.permissions))
	for name := range f.permissions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Problems lists the soft inconsistencies New tolerated, in the order it found
// them. `orc verify` prints these; nothing else consults them.
func (f Fleet) Problems() []string { return slices.Clone(f.problems) }

// Operator returns the root of the tree.
func (f Fleet) Operator() user.Name { return f.operator }

// Now returns the instant the fleet was derived at, so a caller rendering a
// grant's expiry uses the same one the derivation did.
func (f Fleet) Now() time.Time { return f.now }

// Names lists every identity, breadth-first from the operator — which is the
// order `orc status` draws the tree in.
func (f Fleet) Names() []user.Name {
	out := make([]user.Name, 0, len(f.order))
	for _, key := range f.order {
		out = append(out, f.identities[key].Name())
	}
	return out
}

// Identity returns one identity's record.
func (f Fleet) Identity(name user.Name) (model.Identity, error) {
	i, ok := f.identities[name.String()]
	if !ok {
		return model.Identity{}, fault.NotFound{Target: name.String(), Near: f.near(name)}
	}
	return i, nil
}

// Has reports whether an identity exists.
func (f Fleet) Has(name user.Name) bool {
	_, ok := f.identities[name.String()]
	return ok
}

// near lists identity names close to a missed one, so a typo is one paste from
// fixed. It is a prefix match rather than an edit distance: cheap, predictable,
// and enough for a fleet of tens.
func (f Fleet) near(name user.Name) []string {
	var out []string
	for _, key := range f.order {
		if len(key) >= 2 && len(name.String()) >= 2 && key[:2] == name.String()[:2] {
			out = append(out, key)
		}
	}
	return out
}

// Authority returns an identity's effective authority, and what its role asked
// for. The two differ exactly when the boss chain capped it, and every screen
// that shows one shows the other.
func (f Fleet) Authority(name user.Name) (effective, asked model.Authority) {
	d, ok := f.derived[name.String()]
	if !ok {
		return model.Authority{}, model.Authority{}
	}
	return d.authority, d.asked
}

// Depth returns how far below the operator an identity sits. The operator is 0.
func (f Fleet) Depth(name user.Name) int { return f.derived[name.String()].depth }

// Session returns the session id an identity is currently running, or empty when it
// is not populated.
//
// It exists because *every* reader of a grant needs the same answer the derivation
// used. A screen that asked whether a grant was live while passing no session would
// report a session-scoped grant as lapsed the moment it was made — which is exactly
// the bug the CLI tests found before this method existed.
func (f Fleet) Session(name user.Name) string { return f.sessions[name.String()] }

// Clauses returns an identity's effective permission clauses.
func (f Fleet) Clauses(name user.Name) []Clause {
	return slices.Clone(f.derived[name.String()].clauses)
}

// Children returns an identity's direct subordinates, in name order.
func (f Fleet) Children(name user.Name) []user.Name {
	keys := f.children[name.String()]
	out := make([]user.Name, 0, len(keys))
	for _, key := range keys {
		out = append(out, f.identities[key].Name())
	}
	return out
}

// Subtree returns an identity and everyone below it, depth-first: each identity
// immediately followed by its own branch, siblings in name order.
//
// Depth-first because the order *is* the tree wherever it is drawn. `orc status`
// and `orc list` indent each row by its depth, and breadth-first order — every
// depth-1 identity, then every depth-2 identity — puts a grandchild under the last
// of its parent's siblings rather than under its parent. The rows were right and
// the shape was a lie: it said nib worked for ember when nib works for atlas, which
// is the one thing a fleet listing exists to show.
//
// Nothing else depends on the order. Every other caller asks this for the *set* —
// who is visible, who to reconcile, whose instructions to gather — and reads it to
// the end.
func (f Fleet) Subtree(name user.Name) []user.Name {
	if !f.Has(name) {
		return nil
	}
	var out []user.Name
	var walk func(user.Name)
	walk = func(at user.Name) {
		out = append(out, f.identities[at.String()].Name())
		for _, child := range f.Children(at) {
			walk(child)
		}
	}
	walk(name)
	return out
}

// Chain returns the boss chain from an identity up to the operator, nearest
// first. It is what a card shows as `boss  atlas → redjive2`.
func (f Fleet) Chain(name user.Name) []user.Name {
	var out []user.Name
	seen := map[string]bool{name.String(): true}
	current, ok := f.identities[name.String()]
	for ok && !current.IsOperator() {
		boss := current.Boss()
		if seen[boss.String()] {
			// Unreachable: New refuses a cycle. Stopping rather than looping is
			// still the right response to the impossible.
			break
		}
		seen[boss.String()] = true
		out = append(out, boss)
		current, ok = f.identities[boss.String()]
	}
	return out
}

// Controls reports whether actor is strictly above target in the tree.
//
// This is the whole of "all agents are able to move, fire, employ, and otherwise
// act on their subagents without need for permissions": ancestry is the
// permission. It is also what `orc check-control` exits 0 or 8 on, and therefore
// what `muff assign` asks before it assigns work.
//
// An identity does not control itself. Self-directed commands are permitted
// separately, where each one can say whether it makes sense.
func (f Fleet) Controls(actor, target user.Name) bool {
	if actor.Zero() || target.Zero() || actor.String() == target.String() {
		return false
	}
	if !f.Has(actor) || !f.Has(target) {
		return false
	}
	for _, boss := range f.Chain(target) {
		if boss.String() == actor.String() {
			return true
		}
	}
	return false
}

// WouldCycle reports whether making boss the boss of name would create a cycle —
// which it would exactly when boss is name or sits below it.
//
// It is checked before a move is written rather than discovered by New
// afterwards, because a store that will not derive is one no command can run,
// and "you have broken the fleet" is a much worse message than "that would put
// atlas under its own subordinate".
func (f Fleet) WouldCycle(name, boss user.Name) bool {
	if name.String() == boss.String() {
		return true
	}
	for _, below := range f.Subtree(name) {
		if below.String() == boss.String() {
			return true
		}
	}
	return false
}

// Allows reports whether an identity may act on a target of a given kind: a path
// for read and write, a verb for orc.
//
// The operator short-circuits. Its authority is not derived from a permission
// set, so a fleet whose owner has not yet created a single permission is still
// one they can run — and the enumeration in deriveOperator stays a display of
// what exists rather than the source of the answer.
func (f Fleet) Allows(name user.Name, kind model.Kind, target string) bool {
	if !f.Has(name) {
		return false
	}
	if name.String() == f.operator.String() {
		return true
	}
	for _, c := range f.derived[name.String()].clauses {
		if c.Pattern.Kind() == kind && c.Pattern.Matches(target) {
			return true
		}
	}
	return false
}

// Budget returns the spawn load an identity may keep employed, and reports
// whether it holds a spawn permission at all.
//
// The distinction matters: a budget of zero and no budget both refuse every
// employ, but they are different mistakes with different fixes — one needs a
// bigger number, the other needs the permission.
func (f Fleet) Budget(name user.Name) (int, bool) {
	if name.String() == f.operator.String() {
		return model.MaxLoad, true
	}
	best, found := 0, false
	for _, c := range f.Clauses(name) {
		if c.Pattern.Kind() != model.KindSpawn {
			continue
		}
		found = true
		if c.Pattern.Load() > best {
			best = c.Pattern.Load()
		}
	}
	return best, found
}

// Employed lists the identities on the worklist below an actor, itself included
// when it is employed. It is the set the budget is measured over.
//
// Transitive rather than direct, because employing through a subordinate would
// otherwise be a way around a budget: an actor with spawn(8) could employ one agent
// and have *that* agent employ ten more, and nothing would have been exceeded at
// any single step.
func (f Fleet) Employed(actor user.Name) []user.Name {
	var out []user.Name
	for _, name := range f.Subtree(actor) {
		if f.identities[name.String()].Employed() {
			out = append(out, name)
		}
	}
	return out
}

// Load returns what an actor's subtree currently costs, and the loads it is the
// total of.
//
// The two are returned together because every screen that shows the total also
// wants the count — the multiplier is a function of it, and a total without a count
// cannot be explained to somebody who has just been refused.
func (f Fleet) Load(actor user.Name) (total int, loads []int) {
	for _, name := range f.Employed(actor) {
		loads = append(loads, f.identities[name.String()].Load())
	}
	return f.tariff.Total(loads), loads
}

// Afford reports what employing one more session would cost an actor, and whether
// its budget covers it.
//
// It is a *hypothetical*, computed by adding the load to the set and totalling
// again, rather than by adding a number to a total. That is the whole reason it
// exists as a function: the count multiplier means the marginal cost of a session
// is not its own load, so a caller that added and compared would refuse the wrong
// employments and permit others it should not.
//
// The extra return is what `orc employ` prints when the count is what pushed an
// actor over: "load 21 → 26 of 24: the count multiplier rose from 1.3 to 1.4" is a
// refusal somebody can act on, and a bare "over budget" is not.
func (f Fleet) Afford(actor user.Name, load int) (before, after, budget int, ok bool) {
	before, loads := f.Load(actor)
	after = f.tariff.Total(append(append([]int{}, loads...), load))

	budget, held := f.Budget(actor)
	if !held {
		return before, after, 0, false
	}
	return before, after, budget, after <= budget
}

// Multiplier renders the count multiplier for a set of the given size, so a caller
// explaining a refusal does not compute it a second way.
//
// A method rather than a function, now that the multiplier is the fleet's: a
// package-level one would render a number this fleet does not charge.
func (f Fleet) Multiplier(count int) string { return f.tariff.Multiplier(count) }

// Tariff is what this fleet charges, filled in with the built-in prices wherever it
// said nothing.
func (f Fleet) Tariff() model.Tariff { return f.tariff }

// Price is what one session costs this fleet.
//
// Every screen that shows a load must come through here rather than through
// `model.Identity.Load()`, which prices at the *built-in* rate: an identity does not
// know what fleet it is in, and a column showing 4 for a session this fleet charges
// 20 for would be the tariff appearing to do nothing.
func (f Fleet) Price(m model.Model, e model.Effort) int { return f.tariff.Session(m, e) }

// LoadOf is what one identity's session costs this fleet, or zero when it is not
// employed.
func (f Fleet) LoadOf(name user.Name) int {
	got, ok := f.identities[name.String()]
	if !ok || !got.Employed() {
		return 0
	}
	return f.Price(got.Model(), got.Effort())
}

// Holds reports whether an identity holds every clause of a permission.
//
// It is what `orc grant` asks: a grant hands a permission on, and an actor
// cannot hand on what it does not hold. Asking about the permission as a whole
// rather than clause by clause is deliberate — a partial hand-on would create a
// permission by that name which means something narrower than the one everybody
// else audited.
func (f Fleet) Holds(name user.Name, permission model.Name) bool {
	if !f.Has(name) {
		return false
	}
	if name.String() == f.operator.String() {
		_, ok := f.permissions[permission.String()]
		return ok
	}
	p, ok := f.permissions[permission.String()]
	if !ok {
		return false
	}
	// The floor first, and not only as a shortcut. Containment is by clause, which
	// is right for paths — an identity that may write everything may hand on a
	// permission to write one directory — but a clause is a spelling, and a wide
	// enough spelling reaches anything of its kind: `tool(**)` covers
	// `tool(upgrade)` however low the floor of the permission it was written into.
	// A floor is the one part of a permission that is not a pattern, so it is the
	// one thing a pattern cannot argue its way past.
	if effective, _ := f.Authority(name); !effective.AtLeast(p.Floor()) {
		return false
	}
	mine := f.derived[name.String()].clauses
	for _, want := range p.Patterns() {
		covered := false
		for _, c := range mine {
			if c.Pattern.Contains(want) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// Role returns a role's record.
func (f Fleet) Role(name model.Name) (model.Role, bool) {
	r, ok := f.roles[name.String()]
	return r, ok
}

// Permission returns a permission's record.
func (f Fleet) Permission(name model.Name) (model.Permission, bool) {
	p, ok := f.permissions[name.String()]
	return p, ok
}

// Roles lists every role in name order.
func (f Fleet) Roles() []model.Role {
	out := make([]model.Role, 0, len(f.roles))
	for _, r := range f.roles {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b model.Role) int { return a.Name().Compare(b.Name()) })
	return out
}

// Permissions lists every permission in name order.
func (f Fleet) Permissions() []model.Permission {
	out := make([]model.Permission, 0, len(f.permissions))
	for _, p := range f.permissions {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b model.Permission) int { return a.Name().Compare(b.Name()) })
	return out
}

// UsesRole lists the identities holding a role, which is what makes `orc remove
// role` refusable with a reason rather than a bare no.
func (f Fleet) UsesRole(name model.Name) []user.Name {
	var out []user.Name
	for _, key := range f.order {
		if f.identities[key].Role().Equal(name) {
			out = append(out, f.identities[key].Name())
		}
	}
	return out
}

// UsesPermission lists the roles holding a permission, and the identities holding
// it as a live grant.
func (f Fleet) UsesPermission(name model.Name) (roles []model.Name, granted []user.Name) {
	for _, r := range f.Roles() {
		if r.Holds(name) {
			roles = append(roles, r.Name())
		}
	}
	for _, key := range f.order {
		i := f.identities[key]
		for _, g := range i.LiveGrants(f.now, f.sessions[key]) {
			if g.Permission().Equal(name) {
				granted = append(granted, i.Name())
				break
			}
		}
	}
	return roles, granted
}
