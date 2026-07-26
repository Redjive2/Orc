package cli

import (
	"fmt"
	"strconv"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/render"
	"orc/orc/internal/store"
	"orc/orc/internal/style"
)

// budgetPrefix is the name every budget permission this command manages begins
// with. `spawn-24` holds exactly one clause, `spawn(24)`, and nothing else.
//
// The load is *in the name* because a permission is immutable — see
// store.permissionRecord: nothing in Orc rewrites one, and widening a permission
// means creating another. So a budget cannot be edited in place, and the honest
// way to change one is to swap which permission the role holds. Encoding the load
// in the name makes those permissions reusable across roles and makes
// `orc list permissions` legible: `spawn-24` says what it does.
const budgetPrefix = "spawn-"

// budget is `orc budget`.
//
//	orc budget                  what every identity may employ, and what it is using
//	orc budget <role> <load>    set the load that role may keep employed
//
// The worklist budget was previously reachable only the long way round — create a
// permission whose pattern happens to be `spawn(24)`, then assign it to a role —
// which is two commands, a name nobody needs, and a pattern syntax you have to
// know. It is the one number an operator actually tunes, and it deserved a verb.
//
// This changes nothing about the model. A budget is still a `spawn(n)` clause on a
// permission held by a role; this is a shorthand that manages one permission per
// load, and `orc list permissions` shows exactly what it did.
func (a App) budget(args []string) error {
	var asJSON bool
	rest, err := flagged(args, options{switches: map[string]*bool{"--json": &asJSON}})
	if err != nil {
		return err
	}

	s, err := a.begin()
	if err != nil {
		return err
	}

	switch len(rest) {
	case 0:
		return a.budgetReport(s, asJSON)
	case 2:
		if asJSON {
			return fault.Usage{Reason: "--json reads the budgets; setting one prints what changed"}
		}
		return a.setBudget(s, rest[0], rest[1])
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"budget takes a role and a load to set one, or nothing to see them all, got %d argument%s",
			len(rest), plural(len(rest)))}
	}
}

// budgetReport is the table: who may employ how much, and how much of it is spent.
//
// Per identity rather than per role, because the budget an identity actually has
// is the derived one — its role's, capped by its boss chain — and the number on
// the role is not what refuses an `orc employ`.
func (a App) budgetReport(s caller, asJSON bool) error {
	visible := s.fleet.Subtree(s.who)

	type line struct {
		Identity string `json:"identity"`
		Role     string `json:"role,omitempty"`
		Budget   int    `json:"budget"`
		HasA     bool   `json:"has_budget"`
		Spent    int    `json:"spent"`
		Employed int    `json:"employed"`
	}

	lines := make([]line, 0, len(visible))
	for _, name := range visible {
		i, err := s.fleet.Identity(name)
		if err != nil {
			return err
		}
		load, employed := s.fleet.Load(name)
		amount, has := s.fleet.Budget(name)
		lines = append(lines, line{
			Identity: name.String(),
			Role:     i.Role().String(),
			Budget:   amount,
			HasA:     has,
			Spent:    load,
			Employed: len(employed),
		})
	}

	if asJSON {
		return a.emitJSON(lines)
	}

	rows := make([][]render.Cell, 0, len(lines))
	overspent := false
	for _, l := range lines {
		// No budget and a budget of zero both refuse every employ, and they are
		// different mistakes: one wants a bigger number, the other wants the
		// permission. The table says which.
		allowed := render.Painted("none", style.Palette.Muted)
		if l.HasA {
			allowed = render.Painted(strconv.Itoa(l.Budget), style.Palette.Authority)
		}

		spent := render.Text(strconv.Itoa(l.Spent))
		if l.HasA && l.Spent > l.Budget {
			// Reachable without anybody doing anything wrong: a boss chain that
			// narrows, or a role whose budget was lowered under a running fleet.
			// Nothing is stopped — the sessions are already running — so this is a
			// mark rather than a refusal.
			spent = render.Painted(strconv.Itoa(l.Spent)+render.GlyphCapped, style.Palette.Capped)
			overspent = true
		}

		rows = append(rows, []render.Cell{
			render.Painted(l.Identity, style.Palette.Identity),
			render.Painted(orText(l.Role, render.GlyphNone), style.Palette.Role),
			allowed,
			spent,
			render.Text(strconv.Itoa(l.Employed)),
		})
	}

	var footer []string
	if overspent {
		footer = append(footer, render.GlyphCapped+
			" already spending more than the budget allows; nothing new can be employed until it drops")
	}
	footer = append(footer, "`orc budget <role> <load>` sets one · a session costs model × effort")

	return a.drawList(render.Table{
		Title: "budget",
		Note:  "what each identity may keep on the worklist, and what it is keeping",
		Columns: []render.Column{
			{Header: "identity", Align: render.Left, Grow: true, Min: 12},
			{Header: "role", Align: render.Left, Min: 8},
			{Header: "budget", Align: render.Right, Min: 6},
			{Header: "spent", Align: render.Right, Min: 6},
			{Header: "sessions", Align: render.Right},
		},
		Rows:   rows,
		Empty:  "nobody below you",
		Footer: footer,
	})
}

// setBudget puts a role on a given spawn load.
//
// Handing out authority is the operator's business — the same rule `orc assign
// authority` follows — because a budget is authority over machine time, and an
// agent that could raise its own would have no budget at all.
func (a App) setBudget(s caller, roleName, loadText string) error {
	if err := s.mustBeOperator("set a worklist budget"); err != nil {
		return err
	}

	name, err := model.ParseName(roleName)
	if err != nil {
		return err
	}
	role, ok := s.fleet.Role(name)
	if !ok {
		return fault.NotFound{Target: "role " + name.String()}
	}

	load, err := strconv.Atoi(strings.TrimSpace(loadText))
	if err != nil {
		return fault.Usage{Reason: fmt.Sprintf("a budget is a whole number of load units, not %q", loadText)}
	}
	// Validated by building the pattern rather than by re-checking the range here:
	// model.ParsePattern owns what a spawn clause may say, and a second copy of its
	// bounds is a second copy to keep in step.
	clause, err := model.ParsePattern(fmt.Sprintf("spawn(%d)", load))
	if err != nil {
		return err
	}

	// A budget the role already has is not an error and not a write. Saying so
	// plainly keeps `orc budget engineer 24` safe in a setup script.
	if current, has := roleBudget(s, role); has && current == load && role.Holds(budgetName(load)) {
		return a.say(fmt.Sprintf("%s already employs up to %s",
			a.out.Role(role.Name().String()), a.out.Authority(strconv.Itoa(load))))
	}

	// A spawn clause the role got some other way is left alone and reported. The
	// derivation takes the *largest* spawn clause, so quietly adding a second one
	// would produce a role whose budget is not the number just typed.
	if other := foreignBudget(s, role); other != "" {
		return fault.Conflict{Reason: fmt.Sprintf(
			"%s already gets a budget from %s, which is not one `orc budget` manages.\n"+
				"  the derivation takes the largest spawn clause, so setting one here would not decide the answer.\n"+
				"  `orc remove permission %s --from %s` first, or edit that permission's role instead",
			role.Name(), other, other, role.Name())}
	}

	// Ensure the permission exists. Reused across roles when it does: `spawn-24`
	// means the same thing everywhere, which is the point of naming it after its
	// load.
	permission := budgetName(load)
	if existing, ok := s.fleet.Permission(permission); ok {
		if err := checkBudgetPermission(existing, load); err != nil {
			return err
		}
	} else {
		// The lowest floor there is: a budget says how much may be employed, not
		// who is senior enough to be told. The role's own authority is what gates
		// this, and it is checked where roles are assigned.
		floor, err := model.NewAuthority(model.MinAuthority)
		if err != nil {
			return err
		}
		if _, err := s.store.CreatePermission(permission, floor, []model.Pattern{clause}); err != nil {
			return err
		}
	}

	// Off with the old, then on with the new. This order is deliberate: two writes
	// cannot be one, and a crash between them must not leave a role holding more
	// than was asked for. Landing on "no budget" refuses work; landing on the old
	// higher number would be a policy hole that nothing reports.
	previous, hadPrevious := "", false
	for _, held := range role.Permissions() {
		if !strings.HasPrefix(held.String(), budgetPrefix) {
			continue
		}
		if _, err := s.store.ApplyRole(role.Name(), unpermitBy(s, held)); err != nil {
			return err
		}
		previous, hadPrevious = held.String(), true
	}
	if _, err := s.store.ApplyRole(role.Name(), permitBy(s, permission)); err != nil {
		return err
	}

	held := s.fleet.UsesRole(role.Name())
	said := fmt.Sprintf("%s may now keep %s employed",
		a.out.Role(role.Name().String()), a.out.Authority(strconv.Itoa(load)))
	if hadPrevious {
		said += fmt.Sprintf("   %s", a.out.Muted("was "+previous))
	}
	if err := a.say(said); err != nil {
		return err
	}
	if len(held) == 0 {
		// Not a warning: creating the budget before the agent that will use it is a
		// perfectly ordinary order to do things in. But a number that changed
		// nothing visible should say so, or it reads as having failed.
		return a.say(fmt.Sprintf("  %s", a.out.Muted("nobody holds "+role.Name().String()+" yet, so nothing changed today")))
	}
	return a.say(fmt.Sprintf("  %s", a.out.Muted(fmt.Sprintf(
		"%s hold%s it; every one of them is still capped by its own boss chain",
		strings.Join(user.Names(held), ", "), plural2(len(held), "s", "")))))
}

// budgetName is the permission that carries one load.
func budgetName(load int) model.Name {
	name, err := model.ParseName(fmt.Sprintf("%s%d", budgetPrefix, load))
	if err != nil {
		// Unreachable: the prefix is a constant and the load is an integer, so the
		// name is always well formed. It is checked rather than assumed because a
		// name that failed to parse here would produce a permission called "".
		return model.Name{}
	}
	return name
}

// checkBudgetPermission refuses a `spawn-<n>` that is not what its name claims.
//
// Somebody may have made one by hand with `orc new permission spawn-24 50 read(**)`,
// and assigning that to a role because its name looked right would hand out a read
// clause nobody asked for.
func checkBudgetPermission(p model.Permission, load int) error {
	patterns := p.Patterns()
	got, has := p.Load()
	if len(patterns) == 1 && has && got == load {
		return nil
	}
	return fault.Conflict{Reason: fmt.Sprintf(
		"a permission called %s already exists and is not a plain budget (%s); "+
			"rename or remove it, or set the budget the long way with `orc new permission`",
		p.Name(), strings.Join(model.PatternStrings(patterns), " "))}
}

// foreignBudget names a spawn clause the role holds that this command does not
// manage, or "" when there is none.
func foreignBudget(s caller, r model.Role) string {
	for _, held := range r.Permissions() {
		if strings.HasPrefix(held.String(), budgetPrefix) {
			continue
		}
		p, ok := s.fleet.Permission(held)
		if !ok {
			continue
		}
		if _, has := p.Load(); has {
			return held.String()
		}
	}
	return ""
}

func permitBy(s caller, permission model.Name) store.DecideRole {
	return func(current model.Role) (model.RoleEvent, error) {
		if current.Holds(permission) {
			return model.RoleEvent{}, nil
		}
		return model.Permit(s.who, s.fleet.Now(), permission)
	}
}

func unpermitBy(s caller, permission model.Name) store.DecideRole {
	return func(current model.Role) (model.RoleEvent, error) {
		if !current.Holds(permission) {
			return model.RoleEvent{}, nil
		}
		return model.Unpermit(s.who, s.fleet.Now(), permission)
	}
}
