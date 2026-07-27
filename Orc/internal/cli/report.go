package cli

import (
	"fmt"
	"strings"

	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
	"orc/orc/internal/render"
	"orc/orc/internal/store"
	"orc/orc/internal/style"
	"orc/orc/internal/view"
)

// status shows one identity's card, or the whole fleet.
//
// The bare form is additive — Reference.md has `status <identity>` only — and it is
// the screen an operator opens first: a fleet with no fleet-wide view is a fleet
// read one agent at a time.
func (a App) status(args []string) error {
	var asJSON bool
	rest, err := flagged(args, options{switches: map[string]*bool{"--json": &asJSON}})
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return fault.Usage{Reason: "status takes one identity, or none for the whole fleet"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		if asJSON {
			return a.emitFleetJSON(s)
		}
		return a.drawFleet(s)
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	// Reading a card about somebody outside your own branch is reading the roster,
	// so it follows the same rule as acting on them: self and subordinates only,
	// and anybody else is reported as not found rather than as forbidden.
	if who.String() != s.who.String() && !s.fleet.Controls(s.who, who) {
		return fault.NotFound{Target: who.String()}
	}
	if asJSON {
		return a.emitIdentityJSON(s, who)
	}
	return a.drawCard(s, who)
}

// drawFleet draws the tree.
func (a App) drawFleet(s caller) error {
	// The visible fleet is the caller's own branch. The operator sees everything,
	// which is the same rule seen from the top rather than an exception to it.
	visible := s.fleet.Subtree(s.who)

	rows := make([][]render.Cell, 0, len(visible))
	for _, name := range visible {
		i, err := s.fleet.Identity(name)
		if err != nil {
			return err
		}
		effective, asked := s.fleet.Authority(name)
		depth := s.fleet.Depth(name) - s.fleet.Depth(s.who)

		level := effective.String()
		paintLevel := func(p style.Palette, text string) string { return p.Authority(text) }
		if !asked.Zero() && asked.Int() != effective.Int() {
			level = effective.String() + "/" + asked.String() + render.GlyphCapped
			paintLevel = func(p style.Palette, text string) string { return p.Capped(text) }
		}

		role := i.Role().String()
		if i.Role().Zero() {
			role = render.GlyphNone
		}
		if i.IsOperator() {
			role = "operator"
		}

		clauses := len(s.fleet.Clauses(name))

		paintName := func(p style.Palette, text string) string { return p.Identity(text) }
		if i.IsOperator() {
			paintName = func(p style.Palette, text string) string { return p.Operator(text) }
		}

		// The load column shows what the worklist is spending on this identity, and
		// the session column shows whether that spending is actually happening. They
		// are separate because the interesting states are the ones where they
		// disagree: employed and dead is what `orc tend` fixes, and running while not
		// employed is what it tidies away.
		loadCell := render.Text(render.GlyphNone)
		if i.Employed() {
			loadCell = render.Painted(fmt.Sprintf("%d", s.fleet.LoadOf(name)),
				func(p style.Palette, text string) string { return p.Authority(text) })
		}

		rows = append(rows, []render.Cell{
			render.Painted(render.Indent(depth)+name.String(), paintName),
			render.Painted(level, paintLevel),
			render.Painted(role, func(p style.Palette, text string) string { return p.Role(text) }),
			render.Text(fmt.Sprintf("%d", clauses)),
			a.loadShape(i),
			loadCell,
			a.sessionCell(s, name, i),
		})
	}

	total, loads := s.fleet.Load(s.who)
	note := fmt.Sprintf("%d identit%s · %d employed · load %d",
		len(visible), plural2(len(visible), "y", "ies"), len(loads), total)
	if budget, held := s.fleet.Budget(s.who); held {
		note = fmt.Sprintf("%d identit%s · %d employed · load %d of %d",
			len(visible), plural2(len(visible), "y", "ies"), len(loads), total, budget)
	}

	table := render.Table{
		Title: "orc",
		Note:  note,
		Columns: []render.Column{
			{Header: "identity", Align: render.Left, Grow: true, Min: 12},
			{Header: "authority", Align: render.Right, Min: 9},
			{Header: "role", Align: render.Left, Min: 8},
			{Header: "clauses", Align: render.Right},
			{Header: "runs", Align: render.Left, Min: 10},
			{Header: "load", Align: render.Right},
			{Header: "session", Align: render.Left, Min: 12},
		},
		Rows:  rows,
		Empty: "nobody below you",
	}

	// Two footnotes, and both are honesty rather than decoration: one explains the
	// only column that can show two numbers, and the other says what this build
	// does not do, so a fleet with no sessions does not read as a fleet that is
	// broken.
	var notes []string
	for _, row := range rows {
		if strings.Contains(row[1].Text, render.GlyphCapped) {
			notes = append(notes, render.GlyphCapped+" effective/asked: the boss chain caps it")
			break
		}
	}
	table.Footer = notes

	out, err := render.DrawTable(table, a.out, a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// loadShape renders what an identity is employed to run: model and effort, or a
// dash when it is not on the worklist.
func (a App) loadShape(i model.Identity) render.Cell {
	if !i.Employed() {
		return render.Text(render.GlyphNone)
	}
	return render.Painted(i.Model().String()+"/"+i.Effort().Short(),
		func(p style.Palette, text string) string { return p.Value(text) })
}

// sessionCell renders whether an identity is actually running, which is the column
// the whole of milestone 2 exists to be able to fill in.
//
// The four states are worth naming because three of them are the ones somebody is
// looking for: running, idle-but-up, employed-and-dead (what `tend` fixes), and
// running-while-not-employed (what it tidies away).
func (a App) sessionCell(s caller, name user.Name, i model.Identity) render.Cell {
	state, live, err := s.store.Session(name)
	switch {
	case err != nil:
		return render.Painted("unreadable", func(p style.Palette, t string) string { return p.Dead(t) })

	case live && i.Employed():
		// A limit first, because it is the state this column got wrong. A limited
		// session is live by every measure the store has — the child is up, the
		// socket answers, the id is right — and it will never do anything again
		// until somebody speaks to it. Drawing it as `● 68ab61e4` said "working" to
		// an operator scanning a fleet, which is how seven agents sat stopped for
		// half a day under a status screen that looked perfect.
		if limit, hit := a.limitOf(s, name); hit {
			return render.Painted(render.GlyphDead+" "+limitWord(limit, s.store.Now()),
				func(p style.Palette, t string) string { return p.Warn(t) })
		}
		text := render.GlyphLive + " " + short(state.ID)
		if state.Restarts > 0 {
			text += fmt.Sprintf(" ×%d", state.Restarts)
		}
		return render.Painted(text, func(p style.Palette, t string) string { return p.Live(t) })

	case live && !i.Employed():
		return render.Painted(render.GlyphLive+" not employed",
			func(p style.Palette, t string) string { return p.Warn(t) })

	case !live && i.Employed():
		return render.Painted(render.GlyphDead+" employed, not running",
			func(p style.Palette, t string) string { return p.Dead(t) })

	default:
		return render.Painted(render.GlyphIdle+" idle", func(p style.Palette, t string) string { return p.Idle(t) })
	}
}

// drawCard draws one identity, with its derivation shown rather than asserted.
func (a App) drawCard(s caller, who user.Name) error {
	i, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	effective, asked := s.fleet.Authority(who)

	// Who it is, and where it sits.
	head := render.Section{Fields: []render.Field{}}
	role := i.Role().String()
	if i.Role().Zero() {
		role = "none yet"
	}
	roleNote := ""
	if r, ok := s.fleet.Role(i.Role()); ok {
		roleNote = r.Description()
	}
	head.Fields = append(head.Fields, render.Field{
		Label: "role", Value: role, Note: roleNote,
		Paint: func(p style.Palette, text string) string { return p.Role(text) },
	})

	authorityNote := ""
	paintAuthority := func(p style.Palette, text string) string { return p.Authority(text) }
	switch {
	case i.IsOperator():
		authorityNote = "the operator, and the root of the tree"
	case asked.Zero():
		authorityNote = "no role, so no authority"
	case effective.Int() != asked.Int():
		authorityNote = fmt.Sprintf("its role asks for %s; the boss chain caps it", asked)
		paintAuthority = func(p style.Palette, text string) string { return p.Capped(text) }
	}
	head.Fields = append(head.Fields, render.Field{
		Label: "authority", Value: effective.String(), Note: authorityNote, Paint: paintAuthority,
	})

	chain := s.fleet.Chain(who)
	boss := "—"
	if len(chain) > 0 {
		boss = strings.Join(user.Names(chain), " → ")
	}
	head.Fields = append(head.Fields, render.Field{Label: "boss", Value: boss,
		Paint: func(p style.Palette, text string) string { return p.Identity(text) }})

	if children := s.fleet.Children(who); len(children) > 0 {
		head.Fields = append(head.Fields, render.Field{
			Label: "subordinates", Value: strings.Join(user.Names(children), " "),
			Note:  fmt.Sprintf("%d", len(children)),
			Paint: func(p style.Palette, text string) string { return p.Identity(text) },
		})
	}
	head.Fields = append(head.Fields,
		render.Field{Label: "mailbox", Value: who.String(),
			Note:  "in mailman, with the key orc holds",
			Paint: func(p style.Palette, text string) string { return p.Identity(text) }},
		render.Field{Label: "workspace", Value: s.store.WorkspaceDir(who),
			Paint: func(p style.Palette, text string) string { return p.Path(text) }},
	)

	// What it may do, and where each clause came from.
	perms := render.Section{Title: "permissions", Empty: "none — it may do nothing"}
	for _, c := range s.fleet.Clauses(who) {
		note := c.Source.String()
		if c.Source == authz.FromGrant {
			note = "granted, " + c.Grant.Lapse(s.fleet.Now())
		}
		if c.Capped {
			note += ", capped from " + c.Asked.String()
		}
		paint := func(p style.Palette, text string) string { return p.Path(text) }
		if c.Source == authz.FromGrant {
			paint = func(p style.Palette, text string) string { return p.Granted(text) }
		}
		perms.Fields = append(perms.Fields, render.Field{
			Label: c.Pattern.Kind().String(), Value: c.Pattern.Arg(), Note: note, Paint: paint,
		})
	}

	// A permission the identity cannot reach is worth showing: "why does my role
	// not work" is otherwise unanswerable from any screen.
	if r, ok := s.fleet.Role(i.Role()); ok {
		for _, name := range r.Permissions() {
			p, ok := s.fleet.Permission(name)
			if !ok {
				perms.Fields = append(perms.Fields, render.Field{
					Label: "missing", Value: name.String(), Note: "the role names it, but it does not exist",
					Paint: func(p style.Palette, text string) string { return p.Warn(text) },
				})
				continue
			}
			if !effective.AtLeast(p.Floor()) {
				perms.Fields = append(perms.Fields, render.Field{
					Label: "blocked", Value: name.String(),
					Note:  fmt.Sprintf("floor %s > %s", p.Floor(), effective),
					Paint: func(p style.Palette, text string) string { return p.Warn(text) },
				})
			}
		}
	}

	live := a.sessionSection(s, who, i)

	card := render.Card{
		Title:    who.String(),
		Note:     "created " + clock.Show(i.Created()),
		Sections: []render.Section{head, perms, live},
	}
	if budget, ok := s.fleet.Budget(who); ok {
		total, loads := s.fleet.Load(who)
		card.Footer = fmt.Sprintf("spawn budget %d; it is employing %d session%s at a total of %d",
			budget, len(loads), plural(len(loads)), total)
	}

	out, err := render.DrawCard(card, a.out, a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// sessionSection is the card's account of what is running.
// instructedField says what standing instructions this session started with.
//
// Three states, and they are not the same thing:
//
//   - it failed to compose — the loudest, because something is set and is not
//     reaching the agent;
//   - none were composed — either nothing is set, or a session old enough to
//     predate the record, which reads as "cannot say" rather than as "none";
//   - some were, with how many bytes, so an operator can tell a prompt that
//     arrived from one that arrived empty.
func instructedField(state store.SessionState) render.Field {
	switch {
	case state.InstructError != "":
		return render.Field{
			Label: "instructed", Value: "no — " + state.InstructError,
			Paint: func(p style.Palette, t string) string { return p.Warn(t) },
		}
	case state.Instructed > 0:
		return render.Field{
			Label: "instructed", Value: fmt.Sprintf("%d bytes, at the last start", state.Instructed),
			Paint: func(p style.Palette, t string) string { return p.Value(t) },
		}
	default:
		return render.Field{
			Label: "instructed", Value: "nothing was set for it",
			Paint: func(p style.Palette, t string) string { return p.Muted(t) },
		}
	}
}

// endedNote says what became of the previous session, on a card that has no live one.
//
// Without it, an identity that is employed with nothing running says only that — and
// "employed, not running" is the same sentence whether the session ended an hour ago
// mid-turn or was never started. The difference decides whether the next `orc tend`
// resumes a conversation or begins one.
func endedNote(ended store.Ended) render.Field {
	what := "waiting"
	if ended.MidTurn {
		what = "part-way through a turn"
	}
	value := fmt.Sprintf("%s, %s", short(ended.Session), what)
	if ended.Why != "" {
		value += " — " + ended.Why
	}
	if ended.Restarts > 0 {
		value += fmt.Sprintf(" (after %d restart%s)", ended.Restarts, plural(ended.Restarts))
	}
	return render.Field{
		Label: "last session", Value: value,
		Paint: func(p style.Palette, t string) string { return p.Muted(t) },
	}
}

func (a App) sessionSection(s caller, who user.Name, i model.Identity) render.Section {
	out := render.Section{Title: "session"}

	if !i.Employed() {
		out.Empty = "not employed — `orc employ " + who.String() + "` puts it on the worklist"
	} else {
		out.Empty = "employed but not running — `orc tend` starts it"
	}

	state, live, err := s.store.Session(who)
	if err != nil {
		out.Fields = append(out.Fields, render.Field{
			Label: "state", Value: "unreadable", Note: err.Error(),
			Paint: func(p style.Palette, t string) string { return p.Dead(t) },
		})
		return out
	}
	if !live {
		if i.Employed() {
			out.Fields = append(out.Fields, render.Field{
				Label: "worklist", Value: i.Model().String() + "/" + i.Effort().Short(),
				Note:  fmt.Sprintf("employed at load %d, not running", s.fleet.LoadOf(who)),
				Paint: func(p style.Palette, t string) string { return p.Dead(t) },
			})
		}
		// What became of the one before, when there is one to say. It decides what
		// the next `orc tend` does — resume a conversation, or begin one.
		if ended, ok := s.store.LastEnded(who); ok {
			out.Fields = append(out.Fields, endedNote(ended))
		}
		return out
	}

	out.Fields = append(out.Fields,
		render.Field{Label: "id", Value: state.ID,
			Paint: func(p style.Palette, t string) string { return p.Live(t) }},
		render.Field{Label: "running", Value: state.Model + "/" + state.Effort,
			Note:  fmt.Sprintf("load %d", s.fleet.LoadOf(who)),
			Paint: func(p style.Palette, t string) string { return p.Value(t) }},
	)
	if started, err := state.StartedAt(); err == nil {
		note := ""
		if state.Restarts > 0 {
			// A restart count is the one number on this card that means something is
			// wrong, so it says how many and the log says why.
			note = fmt.Sprintf("%d restart%s — %s", state.Restarts, plural(state.Restarts),
				s.store.SessionLogPath(who))
		}
		out.Fields = append(out.Fields, render.Field{
			Label: "started", Value: clock.Show(started), Note: note,
		})
	}
	// What this session was actually started with, which is the only place that
	// question is answered. An agent not following an instruction looks exactly like
	// an agent choosing not to, so "it was never sent" has to be readable rather
	// than inferred.
	out.Fields = append(out.Fields, instructedField(state))
	out.Fields = append(out.Fields, render.Field{
		Label: "pids", Value: fmt.Sprintf("supervisor %d · claude %d", state.Supervisor, state.Child),
		Paint: func(p style.Palette, t string) string { return p.Muted(t) },
	})
	if state.LastExit != "" {
		out.Fields = append(out.Fields, render.Field{
			Label: "last exit", Value: state.LastExit,
			Paint: func(p style.Palette, t string) string { return p.Warn(t) },
		})
	}
	return out
}

// introspect answers "who am I?" from inside a leaf session.
//
// The default is a card. `--only <field>` prints one raw value with no formatting
// and no colour, which is the machine half of the same command: it is what a hook,
// a script, or another tool reads, and adding a heading to it would break every one
// of them.
func (a App) introspect(args []string) error {
	var only string
	var asJSON bool
	rest, err := flagged(args, options{
		values:   map[string]*string{"--only": &only},
		switches: map[string]*bool{"--json": &asJSON},
	})
	if err != nil {
		return err
	}
	if err := exactly(rest, 0, "introspect takes no arguments"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	if strings.TrimSpace(only) != "" {
		value, err := s.field(only)
		if err != nil {
			return err
		}
		// No trailing prose, no colour, and no note on stderr: a caller reading one
		// field is reading it into a variable.
		return a.say(value)
	}
	if asJSON {
		return a.emitIdentityJSON(s, s.who)
	}
	return a.drawCard(s, s.who)
}

// fields lists what --only accepts, in the order help shows them.
//
// Every one of them exists in this build, which is now the whole set the Reference
// names: a script that asked for a field and got an empty line would take it for an
// answer, so an unknown one is an error naming the alternatives.
func fields() []string {
	return []string{"identity", "role", "authority", "asked", "permissions",
		"grants", "boss", "chain", "subordinates", "workspace", "mailbox", "operator",
		"employed", "model", "effort", "load", "session"}
}

// field resolves one --only value.
func (s caller) field(name string) (string, error) {
	i, err := s.fleet.Identity(s.who)
	if err != nil {
		return "", err
	}
	effective, asked := s.fleet.Authority(s.who)

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "identity":
		return s.who.String(), nil
	case "role":
		return i.Role().String(), nil
	case "authority":
		return effective.String(), nil
	case "asked":
		return asked.String(), nil
	case "permissions":
		var out []string
		for _, c := range s.fleet.Clauses(s.who) {
			out = append(out, c.Pattern.String())
		}
		return strings.Join(out, " "), nil
	case "grants":
		var out []string
		for _, g := range i.LiveGrants(s.fleet.Now(), s.fleet.Session(s.who)) {
			out = append(out, g.Permission().String())
		}
		return strings.Join(out, " "), nil
	case "boss":
		return i.Boss().String(), nil
	case "chain":
		return strings.Join(user.Names(s.fleet.Chain(s.who)), " "), nil
	case "subordinates":
		return strings.Join(user.Names(s.fleet.Children(s.who)), " "), nil
	case "workspace":
		return s.store.WorkspaceDir(s.who), nil
	case "mailbox":
		return s.who.String(), nil
	case "operator":
		return s.fleet.Operator().String(), nil

	// The worklist half. `employed` is a yes/no in the form a shell tests, and the
	// load is a number rather than a sentence, because both of these are read into
	// variables rather than shown to anybody.
	case "employed":
		if i.Employed() {
			return "yes", nil
		}
		return "no", nil
	case "model":
		if !i.Model().Valid() {
			return "", nil
		}
		return i.Model().String(), nil
	case "effort":
		if !i.Effort().Valid() {
			return "", nil
		}
		return i.Effort().String(), nil
	case "load":
		// The fleet's price, not the built-in one: `orc introspect --only load` is
		// read into a shell variable and compared against a budget this fleet set.
		return fmt.Sprintf("%d", s.fleet.LoadOf(s.who)), nil

	// The session id, or nothing when there is no session. Nothing is the right
	// answer here rather than an error: "am I populated?" is a question with a
	// legitimate negative, and a caller asking it is testing for empty.
	case "session":
		state, live, err := s.store.Session(s.who)
		if err != nil || !live {
			return "", nil
		}
		return state.ID, nil

	default:
		return "", fault.Usage{Reason: fmt.Sprintf(
			"unknown field %q; try one of: %s", name, strings.Join(fields(), " "))}
	}
}

// checkControl is the contract `muff assign` calls.
//
// Exit 0 if the caller controls the agent, 8 if not, 2 if the agent does not
// exist. It prints nothing on success — a tool asking a yes/no question wants a
// status, not output to parse — and the reason on failure, which is what an agent
// reading stderr needs to know what to do instead.
//
// This is the one place in Orc where fail-open is not the convention. Anything
// other than a definite exit 8 must not be read as a yes, which is why an
// unreachable store or a broken fleet exits with its own code rather than with 0.
func (a App) checkControl(args []string) error {
	if err := exactly(args, 1, "check-control takes one agent"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	target, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if !s.fleet.Has(target) {
		return fault.NotFound{Target: target.String()}
	}
	if !s.fleet.Controls(s.who, target) {
		return fault.Denied{Actor: s.who.String(), Action: "direct", Target: target.String(),
			Reason: fmt.Sprintf("%s is not below %s in the tree", target, s.who)}
	}
	return nil
}

// checkPermission is `orc check-permission <name>`: exit 0 if the caller holds it,
// 8 if not.
//
// The sibling of `check-control`, and it exists for the same reason: another tool
// needs to ask Orc a yes-or-no question about authority and must not answer it
// itself. `muff assign` asks about ancestry; `cq upgrade` asks about a permission.
// Both get a number rather than a screen, so a shell can branch on it.
//
// A permission the fleet does not have is `2`, not `8` — the same rule the rest of
// Orc follows. "You may not hold what does not exist" would be a refusal about
// policy, and this is a fact about the store.
func (a App) checkPermission(args []string) error {
	if err := exactly(args, 1, "check-permission takes one permission"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	want, err := model.ParseName(args[0])
	if err != nil {
		return err
	}
	if _, ok := s.fleet.Permission(want); !ok {
		return fault.NotFound{Target: "permission " + want.String()}
	}
	if !s.fleet.Holds(s.who, want) {
		return fault.Denied{Actor: s.who.String(), Action: "use", Target: want.String(),
			Reason: "it is not in this identity's effective permissions"}
	}
	return nil
}

// env prints the export block for a manual shell.
//
// It is the only command besides `orc bootstrap` that discloses a key, and it says
// so on stderr every time. The caller must control the identity — handing out
// somebody else's credential is handing out their identity — and the note is not
// suppressible, because a command that quietly prints a secret is one that ends up
// in a script whose output is logged.
func (a App) env(args []string) error {
	if err := exactly(args, 1, "env takes one identity"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	who, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if who.String() != s.who.String() {
		if err := s.controls(who, "read the credential of"); err != nil {
			return err
		}
	}
	key, err := s.store.Key(who)
	if err != nil {
		return err
	}

	a.note("this prints %s's key; it is a credential, so do not log the output", who)
	return a.say(strings.Join([]string{
		fmt.Sprintf("export ORC_USER=%s", who),
		fmt.Sprintf("export ORC_KEY=%s", key),
		fmt.Sprintf("export ORC_HOME=%s", s.store.Root()),
		fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s", s.store.ClaudeDir(who)),
	}, "\n"))
}

// plural2 picks between two suffixes, for words that do not simply take an s.
func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// limitOf reports whether a session is sitting at a usage limit.
//
// It is a read of the transcript and nothing else, so a status screen cannot be
// held up by it: an unreadable or absent transcript is simply not a limit, which is
// the same answer this gives for the overwhelming majority of sessions.
func (a App) limitOf(s caller, who user.Name) (view.Limit, bool) {
	feed, err := view.Load(s.store.EventsPath(who), who)
	if err != nil {
		return view.Limit{}, false
	}
	return view.ReadLimit(feed.Transcript)
}

// limitWord is the column's version of the limit: short enough for a table, and
// still the difference between "wait" and "do something".
func limitWord(l view.Limit, now time.Time) string {
	switch {
	case !l.Known():
		return "at a limit"
	case l.Over(now):
		return "limit lifted"
	default:
		return "limit · " + l.Reset.Local().Format("15:04")
	}
}
