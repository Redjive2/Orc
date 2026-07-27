package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"orc/common/fault"
	"orc/orc/internal/model"
	"orc/orc/internal/render"
	"orc/orc/internal/style"
)

// `orc tariff` — what the fleet charges for thinking.
//
// The weights were constants: opus costs three haikus, max effort costs six lows,
// and a crowd of ten is charged double. Those are judgements about *money*, they
// differ between fleets, and nothing in the code can settle them — so they are the
// fleet's, stored and journaled like a permission.
//
//	orc tariff                          what it is, and what it makes things cost
//	orc tariff <setting> <n> [--yes]    change one
//	orc tariff --calibrate              what measurement suggests instead
//	orc tariff --clear --yes            back to the built-in prices
//
// A change is felt by everything at once, because every budget is derived: raising
// `opus` re-prices every running opus session, and an actor inside its budget can be
// over it without anybody touching that actor. `edit permission` set the precedent —
// say who is affected, then do it — and this follows it.
func (a App) tariff(args []string) error {
	var calibrate, clear, yes bool
	rest, err := flagged(args, options{
		switches: map[string]*bool{"--calibrate": &calibrate, "--clear": &clear, "--yes": &yes},
	})
	if err != nil {
		return err
	}

	s, err := a.begin()
	if err != nil {
		return err
	}

	switch {
	case calibrate:
		if len(rest) > 0 {
			return fault.Usage{Reason: "--calibrate proposes; it takes no setting"}
		}
		return a.calibrate(s)

	case clear:
		if len(rest) > 0 {
			return fault.Usage{Reason: "--clear takes no setting; it removes them all"}
		}
		if err := s.mayRunVerb("tariff"); err != nil {
			return err
		}
		if !yes {
			return fault.Usage{Reason: "--clear returns every price to the built-in one, " +
				"which re-prices every running session; add --yes"}
		}
		if err := s.store.ClearTariff(s.who); err != nil {
			return err
		}
		if err := a.say(fmt.Sprintf("%s   the built-in prices are back",
			a.out.Warn("cleared the tariff"))); err != nil {
			return err
		}
		return a.drawTariff(s)

	case len(rest) == 0:
		return a.drawTariff(s)

	case len(rest) != 2:
		return fault.Usage{Reason: "tariff takes a setting and a number, as in `orc tariff opus 4`"}
	}

	setting := model.Setting(rest[0])
	value, err := strconv.Atoi(rest[1])
	if err != nil {
		return fault.Usage{Reason: fmt.Sprintf("%q is not a number", rest[1])}
	}
	if err := s.mayRunVerb("tariff"); err != nil {
		return err
	}

	// Who this would push over budget, said before it happens.
	//
	// Not a refusal: a fleet that has drifted over its own budget is information,
	// and a tariff that could only ever be loosened while agents were running would
	// be a tariff nobody could tighten.
	over, err := a.wouldExceed(s, setting, value)
	if err != nil {
		return err
	}
	if len(over) > 0 && !yes {
		return fault.Conflict{Path: string(setting), Reason: fmt.Sprintf(
			"this would put %s over budget (%s); add --yes",
			plural2(len(over), "one actor", fmt.Sprintf("%d actors", len(over))),
			strings.Join(over, ", "))}
	}

	if _, err := s.store.SetTariff(s.who, setting, value); err != nil {
		return err
	}
	if err := a.say(fmt.Sprintf("%s %s is now %s",
		a.out.Good("priced"), a.out.Value(string(setting)), a.out.Authority(rest[1]))); err != nil {
		return err
	}
	for _, who := range over {
		a.note("%s is now over budget", who)
	}
	return a.drawTariff(s)
}

// wouldExceed lists the actors whose worklist would cost more than their budget
// under a proposed price.
//
// It derives the whole fleet a second time against the proposed tariff rather than
// estimating, because the count multiplier makes a load non-linear in its parts:
// guessing which actors are affected would be a second, worse copy of the
// arithmetic that already exists.
func (a App) wouldExceed(s caller, setting model.Setting, value int) ([]string, error) {
	proposed, err := s.fleet.Tariff().Set(setting, value)
	if err != nil {
		return nil, err
	}

	var over []string
	for _, who := range s.fleet.Subtree(s.who) {
		budget, held := s.fleet.Budget(who)
		if !held {
			continue
		}
		_, loads := s.fleet.Load(who)
		if len(loads) == 0 {
			continue
		}
		// The loads are what each session costs *now*; re-price them from the
		// sessions themselves rather than scaling, which the multiplier forbids.
		var repriced []int
		for _, name := range s.fleet.Subtree(who) {
			identity, err := s.fleet.Identity(name)
			if err != nil || !identity.Employed() {
				continue
			}
			repriced = append(repriced, proposed.Session(identity.Model(), identity.Effort()))
		}
		if proposed.Total(repriced) > budget {
			over = append(over, who.String())
		}
	}
	return over, nil
}

// drawTariff is the price list, and what it makes a session cost.
func (a App) drawTariff(s caller) error {
	// From the store rather than from `s.fleet`. The fleet was derived when the
	// command began, which is *before* this command changed anything — so printing
	// its copy after a write would show somebody the price they had just replaced
	// and report success in the same breath.
	got := s.store.Tariff()
	rows := make([][]render.Cell, 0, len(model.Settings()))
	for _, setting := range model.Settings() {
		value, ok := got.Value(setting)
		if !ok {
			continue
		}
		rows = append(rows, []render.Cell{
			render.Painted(string(setting), style.Palette.Value),
			render.Painted(fmt.Sprintf("%d", value), style.Palette.Authority),
			render.Text(tariffMeans(got, setting, value)),
		})
	}

	table := render.Table{
		Title: "tariff",
		Note: fmt.Sprintf("a %s session at %s effort costs %d · ten of them cost %s×",
			model.DefaultModel, model.DefaultEffort,
			got.Session(model.DefaultModel, model.DefaultEffort), got.Multiplier(10)),
		Columns: []render.Column{
			{Header: "setting", Align: render.Left, Min: 12},
			{Header: "weight", Align: render.Right},
			{Header: "means", Align: render.Left, Grow: true, Min: 24},
		},
		Rows: rows,
		Footer: []string{
			"a session costs model × effort; a set of n costs ⌈sum × (crowd-base + n) / crowd-scale⌉",
			"changing one re-prices every running session at once",
		},
	}

	out, err := render.DrawTable(table, a.out, a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// tariffMeans says what a number does, because a column of weights explains nothing
// on its own.
func tariffMeans(t model.Tariff, setting model.Setting, value int) string {
	switch setting {
	case "crowd-base":
		return "the count multiplier's offset"
	case "crowd-scale":
		return fmt.Sprintf("a fleet of %d is charged its own sum", value-t.CrowdBase)
	}
	if m, err := model.ParseModel(string(setting)); err == nil {
		return fmt.Sprintf("%s at medium effort costs %d", m, t.Session(m, model.EffortMedium))
	}
	if e, err := model.ParseEffort(string(setting)); err == nil {
		return fmt.Sprintf("sonnet at %s costs %d", e, t.Session(model.ModelSonnet, e))
	}
	return ""
}

// calibrate proposes weights from what the fleet actually spent.
//
// It proposes and never applies. The numbers are a measurement of one fleet over one
// window, and a tool that re-priced a fleet from them would be making the judgement
// this whole feature exists to leave to a person.
func (a App) calibrate(s caller) error {
	got := s.store.Tariff()
	proposals := s.proposals()
	if len(proposals) == 0 {
		return a.say(fmt.Sprintf("%s   nothing has been measured in the last %s; `orc activity` reads it",
			a.out.Muted("no proposal"), round(CalibrationWindow)))
	}

	rows := make([][]render.Cell, 0, len(proposals))
	for _, setting := range model.Settings() {
		p, ok := proposals[setting]
		if !ok {
			continue
		}
		weight, _ := got.Value(setting)
		mark := style.Palette.Muted
		if p.Suggested != weight {
			mark = style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(string(setting), style.Palette.Value),
			render.Text(fmt.Sprintf("%d", weight)),
			render.Text(fmt.Sprintf("%.1f×", p.Measured)),
			render.Painted(fmt.Sprintf("%d", p.Suggested), mark),
		})
	}

	table := render.Table{
		Title: "calibration",
		Note:  fmt.Sprintf("new tokens per turn over the last %s", round(CalibrationWindow)),
		Columns: []render.Column{
			{Header: "setting", Align: render.Left, Min: 12},
			{Header: "weight", Align: render.Right},
			{Header: "measured", Align: render.Right, Min: 10},
			{Header: "suggests", Align: render.Right},
		},
		Rows:  rows,
		Empty: "nothing measured",
		Footer: []string{
			"cache reads are excluded: a tariff that counted them would price context, not work",
			"a combination with no observations proposes nothing rather than a number from none",
			"this proposes; `orc tariff <setting> <n>` decides",
		},
	}

	out, err := render.DrawTable(table, a.out, a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// Proposal is what measurement suggests one setting should weigh.
type Proposal struct {
	// Measured is how many times the cheapest observation this setting cost, so a
	// reader can see the evidence rather than only the conclusion.
	Measured float64
	// Suggested is that, rounded to the integer a tariff takes.
	Suggested int
	// Turns is how much was seen. A suggestion from four turns is a suggestion
	// from four turns, and a screen that hid the count would make it look firmer
	// than it is.
	Turns int
}

// proposals reads the rollup and says what each observed setting would weigh.
//
// One implementation for the table and for the JSON cq mirrors: a browser computing
// its own from the same buckets would be a second opinion about what a fleet should
// charge, and the two would drift the first time either rounded differently.
//
// New tokens only. Cache reads are excluded deliberately — including them would make
// the tariff a measure of context size rather than of work.
func (s caller) proposals() map[model.Setting]Proposal {
	byModel := map[string]*measured{}
	byEffort := map[string]*measured{}

	since := s.store.Now().Add(-CalibrationWindow)
	for _, who := range s.fleet.Subtree(s.who) {
		buckets, err := s.store.Activity(who, since)
		if err != nil {
			continue
		}
		for _, b := range buckets {
			for _, into := range []struct {
				key string
				m   map[string]*measured
			}{{b.Model, byModel}, {b.Effort, byEffort}} {
				if into.key == "" {
					continue
				}
				got, ok := into.m[into.key]
				if !ok {
					got = &measured{}
					into.m[into.key] = got
				}
				got.tokens += b.Tokens.New()
				got.turns += b.Turns
			}
		}
	}

	out := map[model.Setting]Proposal{}
	add(out, byModel, func(name string) bool { _, err := model.ParseModel(name); return err == nil })
	add(out, byEffort, func(name string) bool { _, err := model.ParseEffort(name); return err == nil })
	return out
}

// add normalises one half of the proposal so the cheapest observation weighs 1.
func add(into map[model.Setting]Proposal, seen map[string]*measured, known func(string) bool) {
	var floor float64
	rates := map[string]float64{}
	for name, got := range seen {
		if got.turns == 0 || !known(name) {
			continue
		}
		rate := float64(got.tokens) / float64(got.turns)
		rates[name] = rate
		if floor == 0 || rate < floor {
			floor = rate
		}
	}
	if floor == 0 {
		return
	}
	for name, rate := range rates {
		suggested := int(rate/floor + 0.5)
		if suggested < model.MinWeight {
			suggested = model.MinWeight
		}
		if suggested > model.MaxWeight {
			suggested = model.MaxWeight
		}
		into[model.Setting(name)] = Proposal{
			Measured: rate / floor, Suggested: suggested, Turns: seen[name].turns,
		}
	}
}

// measured is what one model or one effort was observed to cost.
type measured struct {
	tokens int64
	turns  int
}

// CalibrationWindow is how much history a proposal is drawn from. A week, because a
// day is one fleet's Tuesday and a month is mostly work nobody is doing any more.
const CalibrationWindow = 7 * 24 * time.Hour

// calibrationRows renders one half of the proposal, normalised so the cheapest
// observation is 1.
func calibrationRows(t model.Tariff, seen map[string]*measured,
	current func(string) (model.Setting, int)) [][]render.Cell {
	var floor float64
	rates := map[string]float64{}
	for name, got := range seen {
		if got.turns == 0 {
			continue
		}
		rate := float64(got.tokens) / float64(got.turns)
		rates[name] = rate
		if floor == 0 || rate < floor {
			floor = rate
		}
	}

	rows := make([][]render.Cell, 0, len(rates))
	for name, rate := range rates {
		setting, weight := current(name)
		if setting == "" {
			continue
		}
		suggested := int(rate/floor + 0.5)
		if suggested < model.MinWeight {
			suggested = model.MinWeight
		}
		mark := style.Palette.Muted
		if suggested != weight {
			mark = style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(string(setting), style.Palette.Value),
			render.Text(fmt.Sprintf("%d", weight)),
			render.Text(fmt.Sprintf("%.1f×", rate/floor)),
			render.Painted(fmt.Sprintf("%d", suggested), mark),
		})
	}
	return rows
}
