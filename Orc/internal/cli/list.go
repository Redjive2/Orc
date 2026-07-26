package cli

import (
	"fmt"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/guess"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/render"
	"orc/orc/internal/style"
)

// The rosters.
//
//	orc list identities    who is in the fleet
//	orc list roles         what jobs exist, and who holds them
//	orc list permissions   what named clause sets exist, and who has them
//	orc list grants        every live grant, and when it lapses
//
// Additive to Reference.md, and the gap they fill is real: `orc status` shows the
// identity tree and nothing else, so the only way to see the roles was to read a
// card for somebody who happened to hold one, and the only way to see a permission
// nobody held was to fail to create it again.
//
// Each is a *roster* rather than a card: one line per thing, the couple of facts
// that distinguish them, and a count. `orc status <identity>` is still where the
// detail is.
//
// Everything here is filtered by what the caller may see. The rule is the one
// `status` already uses — an identity outside your own branch is not yours to
// read — and it extends to roles and permissions by their holders: a role nobody
// you can see holds is not a role you are shown. The operator sees all of it,
// which is that same rule from the top rather than an exception to it.
func (a App) list(args []string) error {
	var asJSON bool
	rest, err := flagged(args, options{switches: map[string]*bool{"--json": &asJSON}})
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fault.Usage{Reason: "list takes " + strings.Join(listKinds(), ", ")}
	}
	if len(rest) > 1 {
		return fault.Usage{Reason: fmt.Sprintf("list takes one of %s, got %d arguments",
			strings.Join(listKinds(), ", "), len(rest))}
	}

	s, err := a.begin()
	if err != nil {
		return err
	}

	switch normaliseListKind(rest[0]) {
	case "identities":
		return a.listIdentities(s, asJSON)
	case "roles":
		return a.listRoles(s, asJSON)
	case "permissions":
		return a.listPermissions(s, asJSON)
	case "grants":
		return a.listGrants(s, asJSON)
	default:
		// The singular and the plural both work, so a caller who typed neither has
		// typed something else entirely — and gets the same treatment an unknown
		// verb gets, for the same reason.
		if near := guess.Nearest(rest[0], listAliases()); near != "" {
			return fault.Usage{Reason: fmt.Sprintf(
				"orc list has nothing called %q — did you mean `orc list %s`?", rest[0], near)}
		}
		return fault.Usage{Reason: fmt.Sprintf("orc list has nothing called %q; it takes %s",
			rest[0], strings.Join(listKinds(), ", "))}
	}
}

func listKinds() []string {
	return []string{"identities", "roles", "permissions", "grants"}
}

// listAliases is every word `orc list` answers to, for the suggestion. The
// singulars are here because typing one is not a mistake worth a refusal.
func listAliases() []string {
	return []string{"identities", "identity", "roles", "role",
		"permissions", "permission", "perms", "perm", "grants", "grant"}
}

func normaliseListKind(word string) string {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "identities", "identity":
		return "identities"
	case "roles", "role":
		return "roles"
	case "permissions", "permission", "perms", "perm":
		return "permissions"
	case "grants", "grant":
		return "grants"
	}
	return ""
}

// listIdentities is the flat roster: every identity, with its boss rather than as
// a tree.
//
// Flat on purpose, and not a second copy of `orc status`. The tree answers "who
// works for whom"; this answers "who is there", which is the question you have
// when a name in a task or a mailbox is one you do not recognise — and a tree is
// the worst shape for looking a name up in.
func (a App) listIdentities(s caller, asJSON bool) error {
	visible := s.fleet.Subtree(s.who)

	if asJSON {
		out := make([]jsonIdentity, 0, len(visible))
		for _, name := range visible {
			shape, err := s.identityJSON(name)
			if err != nil {
				return err
			}
			out = append(out, shape)
		}
		return a.emitJSON(out)
	}

	rows := make([][]render.Cell, 0, len(visible))
	for _, name := range visible {
		i, err := s.fleet.Identity(name)
		if err != nil {
			return err
		}
		effective, asked := s.fleet.Authority(name)

		level := effective.String()
		paintLevel := style.Palette.Authority
		if !asked.Zero() && asked.Int() != effective.Int() {
			level = effective.String() + "/" + asked.String() + render.GlyphCapped
			paintLevel = style.Palette.Capped
		}

		role := orText(i.Role().String(), render.GlyphNone)
		if i.IsOperator() {
			role = "operator"
		}
		boss := orText(i.Boss().String(), render.GlyphNone)

		paintName := style.Palette.Identity
		if i.IsOperator() {
			paintName = style.Palette.Operator
		}

		state := render.Text(render.GlyphNone)
		if i.Employed() {
			state = render.Painted("employed", style.Palette.Good)
		}

		rows = append(rows, []render.Cell{
			render.Painted(name.String(), paintName),
			render.Painted(level, paintLevel),
			render.Painted(role, style.Palette.Role),
			render.Painted(boss, style.Palette.Muted),
			state,
			render.Text(clock.Format(i.Created())),
		})
	}

	return a.drawList(render.Table{
		Title: "identities",
		Note:  fmt.Sprintf("%d identit%s", len(rows), plural2(len(rows), "y", "ies")),
		Columns: []render.Column{
			{Header: "name", Align: render.Left, Grow: true, Min: 12},
			{Header: "authority", Align: render.Right, Min: 9},
			{Header: "role", Align: render.Left, Min: 8},
			{Header: "boss", Align: render.Left, Min: 8},
			{Header: "worklist", Align: render.Left, Min: 8},
			{Header: "created", Align: render.Left, Min: 20},
		},
		Rows:  rows,
		Empty: "nobody below you",
	})
}

// listRoles is every role, with what it asks for and who holds it.
func (a App) listRoles(s caller, asJSON bool) error {
	visible := s.fleet.Subtree(s.who)
	roles := s.fleet.Roles()

	if asJSON {
		out := make([]jsonRole, 0, len(roles))
		for _, r := range roles {
			held := heldBy(s, r.Name(), visible)
			if len(held) == 0 && !s.isOperator() {
				continue
			}
			out = append(out, jsonRole{
				Name:        r.Name().String(),
				Authority:   r.Authority().Int(),
				Description: r.Description(),
				Permissions: model.Names(r.Permissions()),
				Created:     clock.Format(r.Created()),
				HeldBy:      user.Names(held),
			})
		}
		return a.emitJSON(out)
	}

	rows := make([][]render.Cell, 0, len(roles))
	for _, r := range roles {
		held := heldBy(s, r.Name(), visible)
		if len(held) == 0 && !s.isOperator() {
			// A role nobody in your branch holds is not yours to read about. The
			// operator's branch is the whole fleet, so this never hides anything
			// from them.
			continue
		}

		budget := render.Text(render.GlyphNone)
		if load, ok := roleBudget(s, r); ok {
			budget = render.Painted(fmt.Sprintf("%d", load), style.Palette.Authority)
		}

		rows = append(rows, []render.Cell{
			render.Painted(r.Name().String(), style.Palette.Role),
			render.Painted(r.Authority().String(), style.Palette.Authority),
			render.Text(fmt.Sprintf("%d", len(r.Permissions()))),
			budget,
			render.Painted(namesOrNone(held), style.Palette.Identity),
			render.Painted(r.Description(), style.Palette.Muted),
		})
	}

	return a.drawList(render.Table{
		Title: "roles",
		Note:  fmt.Sprintf("%d role%s · %s", len(rows), plural(len(rows)), "`orc new role` makes one"),
		Columns: []render.Column{
			{Header: "role", Align: render.Left, Min: 10},
			{Header: "authority", Align: render.Right, Min: 9},
			{Header: "perms", Align: render.Right},
			{Header: "budget", Align: render.Right},
			{Header: "held by", Align: render.Left, Min: 12},
			{Header: "what it is", Align: render.Left, Grow: true, Min: 16},
		},
		Rows:  rows,
		Empty: "no roles anybody below you holds",
	})
}

// listPermissions is every permission, with its floor, its clauses, and where it
// is in use.
//
// The "in use" column is the one that earns this command. A permission held by
// nothing is a permission somebody made and then took a different route, and
// there is currently no other way to notice one.
func (a App) listPermissions(s caller, asJSON bool) error {
	visible := s.fleet.Subtree(s.who)
	permissions := s.fleet.Permissions()

	if asJSON {
		out := make([]jsonPermission, 0, len(permissions))
		for _, p := range permissions {
			out = append(out, jsonPermission{
				Name:     p.Name().String(),
				Floor:    p.Floor().Int(),
				Patterns: model.PatternStrings(p.Patterns()),
				Created:  clock.Format(p.Created()),
			})
		}
		return a.emitJSON(out)
	}

	rows := make([][]render.Cell, 0, len(permissions))
	for _, p := range permissions {
		roles, granted := s.fleet.UsesPermission(p.Name())
		granted = onlyVisible(granted, visible)

		var where []string
		for _, r := range roles {
			where = append(where, r.String())
		}
		for _, g := range granted {
			where = append(where, g.String()+" (granted)")
		}
		use := render.Painted(strings.Join(where, ", "), style.Palette.Role)
		if len(where) == 0 {
			use = render.Painted("nothing", style.Palette.Warn)
		}

		rows = append(rows, []render.Cell{
			render.Painted(p.Name().String(), style.Palette.Permission),
			render.Painted(p.Floor().String(), style.Palette.Authority),
			render.Painted(strings.Join(model.PatternStrings(p.Patterns()), " "), style.Palette.Value),
			use,
		})
	}

	var notes []string
	for _, row := range rows {
		if row[3].Text == "nothing" {
			notes = append(notes, "a permission held by nothing does nothing; "+
				"`orc assign permission <role> <name>` puts it to work")
			break
		}
	}

	return a.drawList(render.Table{
		Title: "permissions",
		Note:  fmt.Sprintf("%d permission%s", len(rows), plural(len(rows))),
		Columns: []render.Column{
			{Header: "permission", Align: render.Left, Min: 12},
			{Header: "floor", Align: render.Right},
			{Header: "clauses", Align: render.Left, Grow: true, Min: 20},
			{Header: "held by", Align: render.Left, Min: 14},
		},
		Rows:   rows,
		Empty:  "no permissions yet",
		Footer: notes,
	})
}

// listGrants is every grant in the caller's branch, live or lapsed.
//
// Lapsed ones are shown rather than filtered out, because "I granted that and it
// stopped working" is the question this answers, and a row that has vanished
// answers nothing.
func (a App) listGrants(s caller, asJSON bool) error {
	type row struct {
		who   user.Name
		grant model.Grant
	}
	var all []row
	for _, name := range s.fleet.Subtree(s.who) {
		i, err := s.fleet.Identity(name)
		if err != nil {
			return err
		}
		for _, g := range i.Grants() {
			all = append(all, row{who: name, grant: g})
		}
	}

	if asJSON {
		type jsonHeldGrant struct {
			Identity string `json:"identity"`
			jsonGrant
		}
		out := make([]jsonHeldGrant, 0, len(all))
		for _, r := range all {
			out = append(out, jsonHeldGrant{
				Identity:  r.who.String(),
				jsonGrant: s.grantJSON(r.who, r.grant),
			})
		}
		return a.emitJSON(out)
	}

	rows := make([][]render.Cell, 0, len(all))
	live := 0
	for _, r := range all {
		lapse := r.grant.Lapse(s.fleet.Now())
		alive := r.grant.Live(s.fleet.Now(), s.fleet.Session(r.who))
		state := render.Painted("lapsed", style.Palette.Muted)
		if alive {
			live++
			state = render.Painted("live", style.Palette.Good)
		}

		rows = append(rows, []render.Cell{
			render.Painted(r.who.String(), style.Palette.Identity),
			render.Painted(r.grant.Permission().String(), style.Palette.Granted),
			state,
			render.Painted(orText(lapse, render.GlyphNone), style.Palette.Muted),
			render.Painted(orText(r.grant.By(), render.GlyphNone), style.Palette.Muted),
			render.Text(clock.Format(r.grant.Granted())),
		})
	}

	return a.drawList(render.Table{
		Title: "grants",
		Note:  fmt.Sprintf("%d grant%s · %d live", len(rows), plural(len(rows)), live),
		Columns: []render.Column{
			{Header: "identity", Align: render.Left, Min: 12},
			{Header: "permission", Align: render.Left, Min: 12},
			{Header: "state", Align: render.Left, Min: 6},
			{Header: "lapses", Align: render.Left, Grow: true, Min: 14},
			{Header: "granted by", Align: render.Left, Min: 10},
			{Header: "granted", Align: render.Left, Min: 20},
		},
		Rows:  rows,
		Empty: "no grants below you",
	})
}

// drawList renders one roster at the caller's width.
func (a App) drawList(t render.Table) error {
	text, err := render.DrawTable(t, a.out, a.width())
	if err != nil {
		return err
	}
	return a.say(text)
}

// heldBy is the identities in a set that hold a role.
func heldBy(s caller, role model.Name, visible []user.Name) []user.Name {
	return onlyVisible(s.fleet.UsesRole(role), visible)
}

// onlyVisible intersects a list of identities with what the caller may see.
func onlyVisible(names, visible []user.Name) []user.Name {
	out := make([]user.Name, 0, len(names))
	for _, n := range names {
		for _, v := range visible {
			if n.String() == v.String() {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// orText is the first of two strings that is not empty. render has its own copy
// and keeps it unexported, and one three-line helper is cheaper than widening
// that package's surface for a default.
func orText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func namesOrNone(names []user.Name) string {
	if len(names) == 0 {
		return render.GlyphNone
	}
	return strings.Join(user.Names(names), ", ")
}

// roleBudget is the spawn load a role's own permissions come to, which is what an
// identity holding it can employ before its boss chain has a say.
func roleBudget(s caller, r model.Role) (int, bool) {
	best, found := 0, false
	for _, name := range r.Permissions() {
		p, ok := s.fleet.Permission(name)
		if !ok {
			continue
		}
		if load, has := p.Load(); has {
			found = true
			if load > best {
				best = load
			}
		}
	}
	return best, found
}

func (s caller) isOperator() bool { return s.who.String() == s.fleet.Operator().String() }
