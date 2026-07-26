package cli

import (
	"fmt"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/provision"
)

// newThing dispatches `orc new`.
func (a App) newThing(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "new takes identity, role, or permission"}
	}
	switch args[0] {
	case "identity":
		return a.newIdentity(args[1:])
	case "role":
		return a.newRole(args[1:])
	case "permission":
		return a.newPermission(args[1:])
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"orc cannot create a %q; try identity, role, or permission", args[0])}
	}
}

// newIdentity hires an agent, under the caller.
//
// Reference.md's `new identity <name>` takes no parent, and Auth_Perm_Role.md says
// every agent "is a subagent of either the user or another subagent", so the caller
// is the boss. There is deliberately no authority check: a new identity has no
// role, so it holds nothing, and an unemployed identity costs nothing to have. What
// costs something is employing it, and that is what `spawn` is for.
func (a App) newIdentity(args []string) error {
	if err := exactly(args, 1, "new identity takes one name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("new"); err != nil {
		return err
	}

	name, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if s.fleet.Has(name) {
		return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf("an identity called %s already exists", name)}
	}

	p, err := provision.New(s.store, a.Provision)
	if err != nil {
		return err
	}
	made, err := p.WithEntropy(a.Entropy).Identity(name, s.who)
	if err != nil {
		return err
	}

	if err := a.say(fmt.Sprintf("%s %s   under %s, with a mailbox and a workspace",
		a.out.Good("hired"), a.out.Identity(made.Name().String()), a.out.Identity(s.who.String()))); err != nil {
		return err
	}
	// An identity with no role holds nothing at all, which is a state somebody
	// will otherwise report as a bug. Naming the next command is the fix.
	return a.say(fmt.Sprintf("       it has no role yet, so it may do nothing: %s",
		a.out.Command(fmt.Sprintf("orc assign role %s <role>", made.Name()))))
}

// newRole creates a job.
func (a App) newRole(args []string) error {
	if len(args) < 3 {
		return fault.Usage{Reason: "new role takes a name, an authority level, and a description"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("new"); err != nil {
		return err
	}

	name, err := model.ParseName(args[0])
	if err != nil {
		return err
	}
	authority, err := model.ParseAuthority(args[1])
	if err != nil {
		return err
	}
	// Handing out authority is the one thing that needs the caller to have it.
	if err := s.atLeast(authority, "role "+name.String()); err != nil {
		return err
	}

	// The description is the rest of the line, so it does not have to be quoted.
	// A role's description is prose, and prose that must be quoted is prose that
	// gets left out.
	made, err := s.store.CreateRole(name, authority, strings.Join(args[2:], " "))
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s role %s   authority %s · %s",
		a.out.Good("created"), a.out.Role(made.Name().String()),
		a.out.Authority(made.Authority().String()), a.out.Muted(made.Description())))
}

// newPermission creates a named set of clauses with a floor.
func (a App) newPermission(args []string) error {
	if len(args) < 3 {
		return fault.Usage{Reason: "new permission takes a name, a minimum authority, " +
			"and at least one pattern, as in `orc new permission edit-anno 40 read(Anno/**) write(Anno/internal/**)`"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("new"); err != nil {
		return err
	}

	name, err := model.ParseName(args[0])
	if err != nil {
		return err
	}
	floor, err := model.ParseAuthority(args[1])
	if err != nil {
		return err
	}
	// The floor is an authority level, and creating a permission nobody at the
	// caller's level could ever hold is a way to make policy for people above
	// them. So the same rule as a role's authority applies.
	if err := s.atLeast(floor, "permission "+name.String()); err != nil {
		return err
	}
	patterns, err := model.ParsePatterns(args[2:])
	if err != nil {
		return err
	}

	made, err := s.store.CreatePermission(name, floor, patterns)
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s permission %s   floor %s · %s",
		a.out.Good("created"), a.out.Permission(made.Name().String()),
		a.out.Authority(made.Floor().String()),
		a.out.Path(strings.Join(model.PatternStrings(made.Patterns()), " "))))
}

// assign dispatches `orc assign`.
func (a App) assign(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "assign takes role, authority, or permission"}
	}
	switch args[0] {
	case "role":
		return a.assignRole(args[1:])
	case "authority":
		return a.assignAuthority(args[1:])
	case "permission":
		return a.assignPermission(args[1:])
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"orc cannot assign a %q; try role, authority, or permission", args[0])}
	}
}

// assignRole gives an identity a job, replacing whatever it had.
//
// One role per identity is the decision in Plan.md §2.3: authority lives on the
// role, so one role means an identity's authority is a number rather than a
// maximum over a set. The replacement is reported, because silently dropping the
// old role would silently drop every permission that came with it.
func (a App) assignRole(args []string) error {
	if err := exactly(args, 2, "assign role takes an identity and a role"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("assign"); err != nil {
		return err
	}

	who, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if err := s.controls(who, "assign a role to"); err != nil {
		return err
	}
	name, err := model.ParseName(args[1])
	if err != nil {
		return err
	}
	role, ok := s.fleet.Role(name)
	if !ok {
		return fault.NotFound{Target: "role " + name.String()}
	}
	if err := s.atLeast(role.Authority(), "role "+name.String()); err != nil {
		return err
	}

	before, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	if _, err := s.store.ApplyIdentity(who, func(current model.Identity) (model.IdentityEvent, error) {
		if current.Role().Equal(name) {
			return model.IdentityEvent{}, nil
		}
		return model.AssignRole(s.who, s.store.Now(), name)
	}); err != nil {
		return err
	}

	if before.Role().Equal(name) {
		return a.say(fmt.Sprintf("%s already has role %s", a.out.Identity(who.String()), a.out.Role(name.String())))
	}
	if !before.Role().Zero() {
		a.note("%s no longer has role %s, and no longer holds what it granted", who, before.Role())
	}
	return a.sayEffect(s, who, fmt.Sprintf("%s now has role %s",
		a.out.Identity(who.String()), a.out.Role(name.String())))
}

// assignAuthority changes what a role asks for.
//
// It refuses when the role is held by anybody the caller does not control. A role
// is shared, so raising its authority raises every holder — and a rule that only
// checked the caller's own level would let a mid-level agent promote a peer by
// editing the job they happen to share.
func (a App) assignAuthority(args []string) error {
	if err := exactly(args, 2, "assign authority takes a role and a level"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("assign"); err != nil {
		return err
	}

	name, err := model.ParseName(args[0])
	if err != nil {
		return err
	}
	if _, ok := s.fleet.Role(name); !ok {
		return fault.NotFound{Target: "role " + name.String()}
	}
	authority, err := model.ParseAuthority(args[1])
	if err != nil {
		return err
	}
	if err := s.atLeast(authority, "role "+name.String()); err != nil {
		return err
	}
	if err := s.controlsHolders(name, "change the authority of"); err != nil {
		return err
	}

	updated, err := s.store.ApplyRole(name, func(current model.Role) (model.RoleEvent, error) {
		if current.Authority().Int() == authority.Int() {
			return model.RoleEvent{}, nil
		}
		return model.SetAuthority(s.who, s.store.Now(), authority)
	})
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("role %s asks for authority %s",
		a.out.Role(updated.Name().String()), a.out.Authority(updated.Authority().String())))
}

// assignPermission adds a permission to a role.
func (a App) assignPermission(args []string) error {
	if err := exactly(args, 2, "assign permission takes a role and a permission"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("assign"); err != nil {
		return err
	}

	roleName, err := model.ParseName(args[0])
	if err != nil {
		return err
	}
	role, ok := s.fleet.Role(roleName)
	if !ok {
		return fault.NotFound{Target: "role " + roleName.String()}
	}
	permName, err := model.ParseName(args[1])
	if err != nil {
		return err
	}
	perm, ok := s.fleet.Permission(permName)
	if !ok {
		return fault.NotFound{Target: "permission " + permName.String()}
	}
	if err := s.controlsHolders(roleName, "add a permission to"); err != nil {
		return err
	}

	// The floor is checked here as well as in the derivation, so that assigning a
	// permission a role can never hold is a refusal with a reason rather than a
	// clause that quietly never applies.
	if !role.Authority().AtLeast(perm.Floor()) {
		return fault.Denied{Actor: s.who.String(), Action: "add", Target: permName.String(),
			Reason: fmt.Sprintf("%s needs authority %s and role %s has %s",
				permName, perm.Floor(), roleName, role.Authority())}
	}
	if err := s.atLeast(perm.Floor(), "permission "+permName.String()); err != nil {
		return err
	}

	updated, err := s.store.ApplyRole(roleName, func(current model.Role) (model.RoleEvent, error) {
		if current.Holds(permName) {
			return model.RoleEvent{}, nil
		}
		return model.Permit(s.who, s.store.Now(), permName)
	})
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("role %s holds %s",
		a.out.Role(updated.Name().String()),
		a.out.Permission(strings.Join(model.Names(updated.Permissions()), " "))))
}

// controlsHolders refuses a change to a role held by somebody outside the caller's
// subtree.
func (s caller) controlsHolders(role model.Name, action string) error {
	for _, holder := range s.fleet.UsesRole(role) {
		if holder.String() == s.who.String() {
			// Changing a role you hold yourself is allowed: the authority check has
			// already refused anything above the caller's own level, and an agent
			// narrowing its own job is not an escalation.
			continue
		}
		if !s.fleet.Controls(s.who, holder) {
			return fault.Denied{Actor: s.who.String(), Action: action, Target: "role " + role.String(),
				Reason: fmt.Sprintf("%s holds it, and is not below %s", holder, s.who)}
		}
	}
	return nil
}

// remove dispatches `orc remove`.
func (a App) remove(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "remove takes identity, role, or permission"}
	}
	switch args[0] {
	case "identity":
		return a.removeIdentity(args[1:])
	case "role":
		return a.removeRole(args[1:])
	case "permission":
		return a.removePermission(args[1:])
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"orc cannot remove a %q; try identity, role, or permission", args[0])}
	}
}

// removeIdentity deletes an identity whole: its record, its credential, its
// mailbox account, and its workspace.
//
// This is the one destructive command in milestone 1, and the workspace is why it
// asks. A workspace may hold work nobody else has a copy of, so the command prints
// what will go and requires --yes when stdin is not a terminal — which, for an
// agent, is always.
func (a App) removeIdentity(args []string) error {
	var yes bool
	rest, err := flagged(args, options{switches: map[string]*bool{"--yes": &yes}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "remove identity takes one name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("remove"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	if err := s.controls(who, "remove"); err != nil {
		return err
	}
	if children := s.fleet.Children(who); len(children) > 0 {
		return fault.Conflict{Path: who.String(), Reason: fmt.Sprintf(
			"%s is the boss of %s; move them first with `orc move <identity> <boss>`",
			who, strings.Join(user.Names(children), ", "))}
	}
	if !yes {
		return fault.Usage{Reason: fmt.Sprintf(
			"removing %s deletes its workspace at %s, its memories, and its key.\n"+
				"  its mail stays in mailman. pass --yes to go ahead",
			who, s.store.WorkspaceDir(who))}
	}

	// The mailbox goes first. If it fails, the identity is still whole and the
	// caller can try again; the other order would leave a mailbox nothing owns,
	// which is the shape that makes re-creating the name impossible.
	p, err := provision.New(s.store, a.Provision)
	if err != nil {
		return err
	}
	if err := p.RemoveMailbox(who); err != nil {
		return err
	}
	if err := s.store.DeleteIdentity(who); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s %s   its mail is untouched",
		a.out.Warn("removed"), a.out.Identity(who.String())))
}

// removeRole deletes a role, refusing while anybody holds it.
func (a App) removeRole(args []string) error {
	if err := exactly(args, 1, "remove role takes one name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("remove"); err != nil {
		return err
	}

	name, err := model.ParseName(args[0])
	if err != nil {
		return err
	}
	if _, ok := s.fleet.Role(name); !ok {
		return fault.NotFound{Target: "role " + name.String()}
	}
	if holders := s.fleet.UsesRole(name); len(holders) > 0 {
		return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf(
			"role %s is held by %s; give them another role first",
			name, strings.Join(user.Names(holders), ", "))}
	}
	if err := s.store.DeleteRole(name); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s role %s", a.out.Warn("removed"), a.out.Role(name.String())))
}

// removePermission deletes a permission, or takes it off one role.
//
// `--from <role>` is the additive half: Reference.md has no verb for taking a
// permission back off a role, which leaves narrowing a job impossible without
// deleting the permission for everybody. The flag reads as the sentence it is —
// remove permission X from role Y — rather than adding a verb.
func (a App) removePermission(args []string) error {
	var from string
	rest, err := flagged(args, options{values: map[string]*string{"--from": &from}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "remove permission takes one name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("remove"); err != nil {
		return err
	}

	name, err := model.ParseName(rest[0])
	if err != nil {
		return err
	}
	if _, ok := s.fleet.Permission(name); !ok {
		return fault.NotFound{Target: "permission " + name.String()}
	}

	if strings.TrimSpace(from) != "" {
		roleName, err := model.ParseName(from)
		if err != nil {
			return err
		}
		if _, ok := s.fleet.Role(roleName); !ok {
			return fault.NotFound{Target: "role " + roleName.String()}
		}
		if err := s.controlsHolders(roleName, "take a permission from"); err != nil {
			return err
		}
		if _, err := s.store.ApplyRole(roleName, func(current model.Role) (model.RoleEvent, error) {
			if !current.Holds(name) {
				return model.RoleEvent{}, nil
			}
			return model.Unpermit(s.who, s.store.Now(), name)
		}); err != nil {
			return err
		}
		return a.say(fmt.Sprintf("role %s no longer holds %s",
			a.out.Role(roleName.String()), a.out.Permission(name.String())))
	}

	roles, granted := s.fleet.UsesPermission(name)
	if len(roles) > 0 || len(granted) > 0 {
		var in []string
		if len(roles) > 0 {
			in = append(in, "roles "+strings.Join(model.Names(roles), ", "))
		}
		if len(granted) > 0 {
			in = append(in, "granted to "+strings.Join(user.Names(granted), ", "))
		}
		return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf(
			"permission %s is in use (%s); `orc remove permission %s --from <role>` narrows one role instead",
			name, strings.Join(in, "; "), name)}
	}
	if err := s.store.DeletePermission(name); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s permission %s", a.out.Warn("removed"), a.out.Permission(name.String())))
}

// grant hands a permission directly to an identity, temporarily.
func (a App) grant(args []string) error {
	if len(args) == 0 || args[0] != "permission" {
		return fault.Usage{Reason: "grant takes permission, as in `orc grant permission <identity> <permission>`"}
	}
	var until string
	rest, err := flagged(args[1:], options{values: map[string]*string{"--until": &until}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 2, "grant permission takes an identity and a permission"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("grant"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	if err := s.controls(who, "grant a permission to"); err != nil {
		return err
	}
	name, err := model.ParseName(rest[1])
	if err != nil {
		return err
	}
	perm, ok := s.fleet.Permission(name)
	if !ok {
		return fault.NotFound{Target: "permission " + name.String()}
	}
	// An actor cannot hand on what it does not hold. Checked as a whole rather
	// than clause by clause: a partial hand-on would create a permission by that
	// name meaning something narrower than the one everybody else audited.
	if !s.fleet.Holds(s.who, name) {
		return fault.Denied{Actor: s.who.String(), Action: "grant", Target: name.String(),
			Reason: "it does not hold that permission itself"}
	}

	// A grant of what the role already gives permanently changes nothing, and
	// saying so is better than letting somebody conclude the grant did not work.
	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	if role, ok := s.fleet.Role(target.Role()); ok && role.Holds(name) {
		a.note("%s's role %s already grants %s permanently; this adds nothing but an expiry",
			who, role.Name(), name)
	}

	g, fellBack, err := s.buildGrant(who, name, until)
	if err != nil {
		return err
	}
	if _, err := s.store.ApplyIdentity(who, func(model.Identity) (model.IdentityEvent, error) {
		return model.GrantPermission(s.who, s.store.Now(), g)
	}); err != nil {
		return err
	}

	// What was granted and when it lapses are the same sentence, because a grant
	// whose expiry is not visible at the moment it is made is a grant nobody
	// remembers making.
	head := fmt.Sprintf("%s %s to %s   %s",
		a.out.Good("granted"), a.out.Permission(name.String()),
		a.out.Identity(who.String()), a.out.Muted(g.Lapse(s.store.Now())))
	if fellBack {
		a.note("%s has no session to scope the grant to, so it lapses in %s instead; --until sets another deadline",
			who, model.UnpopulatedGrant)
	}
	if !perm.Floor().Zero() {
		effective, _ := s.fleet.Authority(who)
		if !effective.AtLeast(perm.Floor()) {
			a.note("%s has authority %s and %s needs %s, so the grant will not take effect until that changes",
				who, effective, name, perm.Floor())
		}
	}
	return a.say(head)
}

// buildGrant decides a grant's expiry, and reports whether the default had to fall
// back to a clock.
//
// The default is the session, per Plan.md §2.5. With nothing populated — which is
// every identity in milestone 1 — there is no session to scope to, so it becomes a
// timed grant with the one-hour default and the caller is told. That is the honest
// reading of "temporarily": a grant tied to a session that does not exist has
// already lapsed, and silently making one would be a permission that never works.
//
// An explicit `--until` is never a fallback, and must not be reported as one: the
// caller asked for a clock and got one.
func (s caller) buildGrant(who user.Name, permission model.Name, until string) (grant model.Grant, fellBack bool, err error) {
	now := s.store.Now()

	if strings.TrimSpace(until) != "" {
		span, err := clock.ParseSpan(until)
		if err != nil {
			return model.Grant{}, false, fault.Usage{Reason: fmt.Sprintf("--until %q: %s", until, err)}
		}
		g, err := model.TimedGrant(permission, s.who.String(), now, span)
		return g, false, err
	}

	sessions, err := s.store.Sessions()
	if err != nil {
		return model.Grant{}, false, err
	}
	if id := sessions[who.String()]; id != "" {
		g, err := model.SessionGrant(permission, s.who.String(), now, id)
		return g, false, err
	}
	g, err := model.TimedGrant(permission, s.who.String(), now, model.UnpopulatedGrant)
	return g, true, err
}

// revoke ends a grant early.
//
// It needs no --yes and no confirmation: taking a permission away is never the
// dangerous direction, and revoking what was never granted is not an error, so the
// command is safe to run twice.
func (a App) revoke(args []string) error {
	if len(args) == 0 || args[0] != "permission" {
		return fault.Usage{Reason: "revoke takes permission, as in `orc revoke permission <identity> <permission>`"}
	}
	if err := exactly(args[1:], 2, "revoke permission takes an identity and a permission"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("revoke"); err != nil {
		return err
	}

	who, err := user.Parse(args[1])
	if err != nil {
		return err
	}
	if err := s.controls(who, "revoke a permission from"); err != nil {
		return err
	}
	name, err := model.ParseName(args[2])
	if err != nil {
		return err
	}

	before, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	held := false
	for _, g := range before.Grants() {
		if g.Permission().Equal(name) {
			held = true
		}
	}
	if _, err := s.store.ApplyIdentity(who, func(model.Identity) (model.IdentityEvent, error) {
		return model.RevokePermission(s.who, s.store.Now(), name)
	}); err != nil {
		return err
	}

	if !held {
		return a.say(fmt.Sprintf("%s had no grant of %s", a.out.Identity(who.String()), a.out.Permission(name.String())))
	}
	return a.say(fmt.Sprintf("%s the grant of %s to %s",
		a.out.Warn("revoked"), a.out.Permission(name.String()), a.out.Identity(who.String())))
}

// move re-parents an identity.
//
// Two refusals matter here, and they are different. A cycle is refused *before* it
// is written, because a store that will not derive is one no command can run and
// "you have broken the fleet" is a much worse message than "that would put atlas
// under its own subordinate". And a new boss outside the caller's own reach is
// refused because moving somebody under a stranger is a way to hand an agent to
// somebody the caller has no authority over.
func (a App) move(args []string) error {
	if err := exactly(args, 2, "move takes an identity and a boss"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("move"); err != nil {
		return err
	}

	who, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if err := s.controls(who, "move"); err != nil {
		return err
	}
	boss, err := user.Parse(args[1])
	if err != nil {
		return err
	}
	if !s.fleet.Has(boss) {
		return fault.NotFound{Target: boss.String()}
	}
	if boss.String() != s.who.String() && !s.fleet.Controls(s.who, boss) {
		return fault.Denied{Actor: s.who.String(), Action: "move " + who.String() + " under", Target: boss.String(),
			Reason: "the new boss has to be the caller or somebody below it"}
	}
	if s.fleet.WouldCycle(who, boss) {
		return fault.Conflict{Path: who.String(), Reason: fmt.Sprintf(
			"%s is at or below %s, so that would make a cycle", boss, who)}
	}

	before, _ := s.fleet.Authority(who)
	if _, err := s.store.ApplyIdentity(who, func(current model.Identity) (model.IdentityEvent, error) {
		if current.Boss().String() == boss.String() {
			return model.IdentityEvent{}, nil
		}
		return model.Move(s.who, s.store.Now(), boss)
	}); err != nil {
		return err
	}

	// The whole subtree may have just been re-capped, so the effect is derived
	// again and reported rather than assumed. This is the visible half of "nothing
	// effective is stored".
	after, err := s.store.Fleet()
	if err != nil {
		return err
	}
	now, _ := after.Authority(who)
	line := fmt.Sprintf("%s %s under %s", a.out.Good("moved"),
		a.out.Identity(who.String()), a.out.Identity(boss.String()))
	if before.Int() != now.Int() {
		line += fmt.Sprintf("   authority %s → %s",
			a.out.Muted(before.String()), a.out.Capped(now.String()))
	}
	if err := a.say(line); err != nil {
		return err
	}
	return a.sayCapped(after, who)
}

// sayEffect prints a line and then what the change did to the identity's derived
// standing, which is the question the caller actually asked.
func (a App) sayEffect(s caller, who user.Name, line string) error {
	if err := a.say(line); err != nil {
		return err
	}
	after, err := s.store.Fleet()
	if err != nil {
		return err
	}
	effective, asked := after.Authority(who)
	clauses := after.Clauses(who)
	if err := a.say(fmt.Sprintf("       authority %s · %d permission clause%s",
		a.out.Authority(effective.String()), len(clauses), plural(len(clauses)))); err != nil {
		return err
	}
	if !asked.Zero() && effective.Int() != asked.Int() {
		a.note("its role asks for %s; its boss chain caps it at %s", asked, effective)
	}
	return nil
}

// sayCapped reports every identity in a subtree whose authority is now lower than
// its role asks for. A move that quietly demoted four agents is a move whose
// consequence should not have to be discovered one card at a time.
func (a App) sayCapped(fleet fleetReader, root user.Name) error {
	for _, name := range fleet.Subtree(root) {
		effective, asked := fleet.Authority(name)
		if asked.Zero() || effective.Int() == asked.Int() {
			continue
		}
		a.note("%s asks for authority %s and is capped at %s", name, asked, effective)
	}
	return nil
}

// fleetReader is the slice of authz.Fleet these reports need. It is an interface
// only so that sayCapped can be called with a freshly derived fleet or with a
// session's, without either having to be converted.
type fleetReader interface {
	Subtree(user.Name) []user.Name
	Authority(user.Name) (model.Authority, model.Authority)
}

// edit dispatches `orc edit`.
func (a App) edit(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "edit takes permission"}
	}
	switch args[0] {
	case "permission":
		return a.editPermission(args[1:])
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"orc cannot edit a %q; only a permission can be edited in place.\n"+
				"  a role's authority is `orc assign authority`, and its permission set is\n"+
				"  `orc assign permission` and `orc remove permission --from <role>`", args[0])}
	}
}

// editPermission changes a permission's floor and clauses in place.
//
// Plan.md §13 decided a permission was immutable, on the reasoning that nothing
// mutated one — so widening meant creating another under a new name, which shows
// up in every card that lists it. That is right for widening and wrong for
// correcting: an operator who typed `read(Ano/**)` wants that permission fixed,
// not a second one beside it with the misspelling preserved forever. The name is
// what roles hold, so it is the one thing an edit cannot change.
//
// Everything a permission guards changes the instant this returns, for every role
// that holds it and every identity under them. That is the point, and it is why
// the command says who is affected before it does it.
func (a App) editPermission(args []string) error {
	var floorFlag string
	rest, err := flagged(args, options{values: map[string]*string{"--floor": &floorFlag}})
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fault.Usage{Reason: "edit permission takes a name, then the clauses that replace the old ones, " +
			"as in `orc edit permission edit-anno read(Anno/**) write(Anno/internal/**)`.\n" +
			"  `--floor <n>` changes the floor; giving no clauses keeps the ones it has"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	// Editing is what `new` and `remove` are: the same verb class, so the same
	// gate. An identity that may not make policy may not rewrite it either.
	if err := s.mayRunVerb("new"); err != nil {
		return err
	}

	name, err := model.ParseName(rest[0])
	if err != nil {
		return err
	}
	current, ok := s.fleet.Permission(name)
	if !ok {
		return fault.NotFound{Target: "permission " + name.String()}
	}

	floor := current.Floor()
	if strings.TrimSpace(floorFlag) != "" {
		if floor, err = model.ParseAuthority(floorFlag); err != nil {
			return err
		}
		// The same rule as creating one: a floor above the caller's own authority
		// is policy for people above them.
		if err := s.atLeast(floor, "permission "+name.String()); err != nil {
			return err
		}
	}

	patterns := current.Patterns()
	if len(rest) > 1 {
		if patterns, err = model.ParsePatterns(rest[1:]); err != nil {
			return err
		}
	}

	// Raising a floor can leave a role holding a permission it is too junior to
	// use. Orc tolerates that — `verify` reports it — but doing it silently from
	// one command would be a permission that stops working for reasons nobody
	// was told about.
	var stranded []string
	holders, granted := s.fleet.UsesPermission(name)
	for _, roleName := range holders {
		role, exists := s.fleet.Role(roleName)
		if !exists {
			continue
		}
		if !role.Authority().AtLeast(floor) {
			stranded = append(stranded, fmt.Sprintf("%s (authority %s)", roleName, role.Authority()))
		}
	}
	if len(stranded) > 0 {
		return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf(
			"floor %s is above %s, which hold%s this permission and would keep it without being able to use it.\n"+
				"  lower the floor, or take it off them first with `orc remove permission %s --from <role>`",
			floor, strings.Join(stranded, ", "), plural2(len(stranded), "s", ""), name)}
	}

	amended, err := s.store.ApplyPermission(name, func(now model.Permission) (model.PermissionEvent, error) {
		if now.Floor() == floor && equalPatterns(now.Patterns(), patterns) {
			// Nothing to do, and saying so is better than appending an event
			// that changes nothing to a journal somebody will read later.
			return model.PermissionEvent{}, nil
		}
		return model.Amend(s.who, s.store.Now(), floor, patterns)
	})
	if err != nil {
		return err
	}

	if amended.Floor() == current.Floor() && equalPatterns(amended.Patterns(), current.Patterns()) {
		return a.say(fmt.Sprintf("permission %s is already that", a.out.Permission(name.String())))
	}
	if err := a.say(fmt.Sprintf("%s permission %s   floor %s · %s",
		a.out.Good("edited"), a.out.Permission(name.String()),
		a.out.Authority(amended.Floor().String()),
		a.out.Path(strings.Join(model.PatternStrings(amended.Patterns()), " ")))); err != nil {
		return err
	}

	// Who this just changed. An edit with no holders is a quiet one; an edit
	// that widened what six agents may write should say six.
	var affected []string
	if len(holders) > 0 {
		affected = append(affected, fmt.Sprintf("roles %s", strings.Join(model.Names(holders), ", ")))
	}
	if len(granted) > 0 {
		affected = append(affected, fmt.Sprintf("granted to %s", strings.Join(user.Names(granted), ", ")))
	}
	if len(affected) == 0 {
		return a.say("  " + a.out.Muted("nothing holds it, so nothing changed but the permission"))
	}
	return a.say("  " + a.out.Warn("in force now for ") + a.out.Muted(strings.Join(affected, "; ")))
}

// equalPatterns reports whether two clause sets are the same set.
//
// Both sides are sorted by the model, so this is a comparison rather than a
// search: it exists to tell an edit that changed nothing from one that did.
func equalPatterns(a, b []model.Pattern) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].String() != b[i].String() {
			return false
		}
	}
	return true
}
