package cli

import (
	"fmt"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/render"
	"orc/orc/internal/store"
	"orc/orc/internal/style"
)

// `orc pace` — how often a fleet is kept moving.
//
// The three cycles have always had settings and never had a place to keep them:
// `orc wake --after`, `orc wake --every`, `orc tend --watch` are flags, read once
// when a process starts. That is why nothing could change them but the person who
// started the process, and why a browser could not offer them at all.
//
// The values are stored per layer and resolved the way a wake message is — the
// identity's, else its role's, else the fleet's, else the built-in — and every
// cycle re-reads at the top of each pass. A flag still wins for the process it was
// given to: somebody debugging with `--after 1m` is making a decision about this
// run, and a stored value silently overriding it would be the tool arguing with the
// operator.
//
//	orc pace                            every setting, and where each came from
//	orc pace wake --after 20m           the fleet's
//	orc pace wake ember --after 5m      one agent's
//	orc pace tend --watch 30s
//	orc pace wake ember --off           stop waking this one
//	orc pace wake ember --clear         fall back to the layer above
//
// `pace` rather than `set`, because `orc set` would be a verb whose meaning
// depended entirely on its object.
func (a App) pace(args []string) error {
	var after, every, watch string
	var off, on, clear bool
	rest, err := flagged(args, options{
		values: map[string]*string{"--after": &after, "--every": &every, "--watch": &watch},
		switches: map[string]*bool{
			"--off": &off, "--on": &on, "--clear": &clear,
		},
	})
	if err != nil {
		return err
	}
	if off && on {
		return fault.Usage{Reason: "--off and --on are opposites; give one"}
	}

	s, err := a.begin()
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		if after != "" || every != "" || watch != "" || off || on || clear {
			return fault.Usage{Reason: "pace needs a cycle to change: `orc pace wake --after 20m`"}
		}
		return a.drawPacing(s)
	}

	cycle := strings.ToLower(strings.TrimSpace(rest[0]))
	if cycle != "wake" && cycle != "tend" {
		return fault.Usage{Reason: fmt.Sprintf(
			"%q is not a cycle; it is `wake` or `tend`. sync is the mirror's and is set in cq", rest[0])}
	}

	// Who it is for: the fleet by default, one agent or one role when named.
	kind, role, who := instruct.System, model.Name{}, user.Name{}
	switch len(rest) {
	case 1:
	case 2:
		got, err := a.paceTarget(s, cycle, rest[1])
		if err != nil {
			return err
		}
		kind, role, who = got.kind, got.role, got.who
	default:
		return fault.Usage{Reason: "pace takes a cycle and at most one identity or role"}
	}

	if err := s.mayRunVerb("pace"); err != nil {
		return err
	}

	current, _ := s.store.Pace(kind, role, who)
	changed := current
	touched := false

	if clear {
		changed, touched = store.Pace{}, true
	}
	for _, set := range []struct {
		flag  string
		value *string
		cycle string
		into  *string
	}{
		{"--after", &after, "wake", &changed.WakeAfter},
		{"--every", &every, "wake", &changed.WakeEvery},
		{"--watch", &watch, "tend", &changed.TendWatch},
	} {
		if strings.TrimSpace(*set.value) == "" {
			continue
		}
		if set.cycle != cycle {
			return fault.Usage{Reason: fmt.Sprintf("%s is %s's, not %s's", set.flag, set.cycle, cycle)}
		}
		got, err := time.ParseDuration(*set.value)
		if err != nil || got <= 0 {
			return fault.Usage{Reason: fmt.Sprintf(
				"%s takes a duration with something in it, like 20m — not %q", set.flag, *set.value)}
		}
		if floor := paceFloor(set.flag); got < floor {
			return fault.Usage{Reason: fmt.Sprintf(
				"%s %s is under %s, which is tight enough to be a busy-wait rather than a cycle",
				set.flag, *set.value, floor)}
		}
		*set.into, touched = got.String(), true
	}

	if off || on {
		value := "yes"
		if on {
			value = "no"
		}
		if cycle == "wake" {
			changed.WakeOff = value
		} else {
			changed.TendOff = value
		}
		touched = true
	}

	if !touched {
		// Reading one layer rather than changing it. Its own answer rather than a
		// usage error: `orc pace wake ember` is a reasonable question.
		return a.drawOneLayer(s, cycle, kind, role, who)
	}

	if err := s.store.SetPace(kind, role, who, changed); err != nil {
		return err
	}
	if err := a.say(fmt.Sprintf("%s %s for %s",
		a.out.Good("paced"), a.out.Value(cycle), a.out.Identity(layerName(kind, role, who)))); err != nil {
		return err
	}
	// What it now resolves to, because a layer is not an answer: an identity's
	// setting sits under its role's and the fleet's, and somebody who has just set
	// one wants to know what an agent will actually do.
	return a.drawPacing(s)
}

// paceFloor is the shortest interval each flag will take. They are the cycles' own
// floors, so a stored value cannot ask for something a flag would have refused.
func paceFloor(flag string) time.Duration {
	switch flag {
	case "--after":
		return MinQuiet
	default:
		return MinWatch
	}
}

// target is who a layer belongs to.
type paceLayer struct {
	kind instruct.Kind
	role model.Name
	who  user.Name
}

// paceTarget reads a name as an identity, else as a role.
//
// An identity first, because that is what somebody pacing one agent types, and a
// name that is both is theirs — an identity is the more specific thing and the more
// specific layer wins everywhere else in this feature too.
func (a App) paceTarget(s caller, cycle, raw string) (paceLayer, error) {
	if who, err := user.Parse(raw); err == nil {
		if _, err := s.fleet.Identity(who); err == nil {
			if who.String() != s.who.String() {
				if err := s.controls(who, "pace"); err != nil {
					return paceLayer{}, err
				}
			}
			return paceLayer{kind: instruct.Identity, who: who}, nil
		}
	}
	role, err := model.ParseName(raw)
	if err != nil {
		return paceLayer{}, err
	}
	if _, ok := s.fleet.Role(role); !ok {
		return paceLayer{}, fault.NotFound{Target: raw}
	}
	return paceLayer{kind: instruct.Role, role: role}, nil
}

func layerName(kind instruct.Kind, role model.Name, who user.Name) string {
	switch kind {
	case instruct.Role:
		return "the role " + role.String()
	case instruct.Identity:
		return who.String()
	default:
		return "the fleet"
	}
}

// drawPacing is the table: what every agent's cycles will actually do.
func (a App) drawPacing(s caller) error {
	rows := make([][]render.Cell, 0)
	for _, who := range s.fleet.Subtree(s.who) {
		identity, err := s.fleet.Identity(who)
		if err != nil {
			continue
		}
		if identity.IsOperator() {
			// The operator runs no session, so nothing wakes or tends it.
			continue
		}
		got := s.store.Pacing(who, identity.Role())
		rows = append(rows, []render.Cell{
			render.Painted(who.String(), style.Palette.Identity),
			a.paceCell(got.WakeAfter, DefaultQuiet, got.WakeOff),
			a.paceCell(got.WakeEvery, 0, got.WakeOff),
			a.paceCell(got.TendWatch, 0, got.TendOff),
		})
	}

	fleet := s.store.FleetPacing()
	table := render.Table{
		Title: "pace",
		Note: fmt.Sprintf("the fleet: wake after %s, every %s · tend every %s",
			shown(fleet.WakeAfter, DefaultQuiet), shown(fleet.WakeEvery, 0), shown(fleet.TendWatch, 0)),
		Columns: []render.Column{
			{Header: "identity", Align: render.Left, Grow: true, Min: 12},
			{Header: "wake after", Align: render.Right, Min: 12},
			{Header: "wake every", Align: render.Right, Min: 12},
			{Header: "tend every", Align: render.Right, Min: 12},
		},
		Rows:  rows,
		Empty: "nobody below you",
		Footer: []string{
			"r came from the role, s from the fleet; unmarked is the agent's own",
			"a cycle with no interval runs when somebody runs it; `orc wake --every` and " +
				"`orc tend --watch` are the loops",
			"`--after` given on the line wins for that run; a stored interval reaches a " +
				"running loop, which is the only way to change one",
		},
	}

	out, err := render.DrawTable(table, a.out, a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// paceCell renders one resolved setting, marked where it came from.
func (a App) paceCell(got store.Setting, fallback time.Duration, off store.Setting) render.Cell {
	if off.Off() {
		return render.Painted("off", style.Palette.Warn)
	}
	text := shown(got, fallback)
	if !got.Set() {
		return render.Painted(text, style.Palette.Muted)
	}
	// Marked when it comes from a layer that is not this identity's own, so a value
	// somebody is surprised by leads them to the layer that set it.
	if got.From != instruct.Identity {
		text += " " + string(got.From)[:1]
	}
	return render.Painted(text, style.Palette.Value)
}

// shown renders a setting, or what a cycle would do without one.
func shown(got store.Setting, fallback time.Duration) string {
	if got.Set() {
		return got.Value
	}
	if fallback > 0 {
		return round(fallback)
	}
	return render.GlyphNone
}

// drawOneLayer answers `orc pace wake ember` — what this layer says, as against
// what the identity resolves to.
func (a App) drawOneLayer(s caller, cycle string, kind instruct.Kind, role model.Name, who user.Name) error {
	got, set := s.store.Pace(kind, role, who)
	name := layerName(kind, role, who)
	if !set {
		return a.say(fmt.Sprintf("%s sets nothing for %s; it inherits",
			a.out.Identity(name), a.out.Value(cycle)))
	}

	var parts []string
	if cycle == "wake" {
		if got.WakeAfter != "" {
			parts = append(parts, "after "+got.WakeAfter)
		}
		if got.WakeEvery != "" {
			parts = append(parts, "every "+got.WakeEvery)
		}
		if strings.EqualFold(got.WakeOff, "yes") {
			parts = append(parts, "off")
		}
	} else {
		if got.TendWatch != "" {
			parts = append(parts, "every "+got.TendWatch)
		}
		if strings.EqualFold(got.TendOff, "yes") {
			parts = append(parts, "off")
		}
	}
	if len(parts) == 0 {
		return a.say(fmt.Sprintf("%s sets nothing for %s; it inherits",
			a.out.Identity(name), a.out.Value(cycle)))
	}
	return a.say(fmt.Sprintf("%s   %s %s", a.out.Identity(name), a.out.Value(cycle),
		a.out.Muted(strings.Join(parts, " · "))))
}
