package model

import (
	"fmt"
	"strings"

	"orc/common/fault"
)

// What thinking costs, as a value a fleet can change.
//
// The weights were `const` — opus is 3, max effort is 6, the crowd multiplier is
// (9+n)/10 — and they are the one part of the load model that is a *judgement about
// money* rather than a fact about code. A fleet running haiku for everything and a
// fleet running opus at max effort do not agree about what a budget of 24 means, and
// neither of them is wrong.
//
// So the numbers become a record, and everything that computes a load is handed
// one. That is a wider change than storing them in a global would have been, and it
// is the reason for it: a load computed against whichever tariff happened to be
// loaded is a load nobody can reproduce, and two processes disagreeing about what a
// session costs is how a budget stops meaning anything.
//
// All integers, because the load model is integer arithmetic — see TotalLoad. A
// tariff with a float in it would be a fleet whose budget rounded differently on two
// machines.

// Tariff is the price list.
type Tariff struct {
	// Models is the weight of each model, keyed by the word a budget is written
	// in. A model this build does not know weighs nothing, which is deliberate:
	// see Weight.
	Models map[Model]int
	// Efforts is the weight of each effort level.
	Efforts map[Effort]int
	// CrowdBase and CrowdScale are the count multiplier: a set of n sessions is
	// charged (base + n) / scale of its own sum, so the tenth agent costs more
	// than the first and a fleet is charged for being a fleet.
	CrowdBase  int
	CrowdScale int
}

// The bounds. A weight of zero is a session that costs nothing, which would make a
// budget unenforceable against it; the ceiling is high enough for any judgement
// somebody actually holds and low enough that a typo is caught.
const (
	MinWeight = 1
	MaxWeight = 100
	// MinCrowdScale is 1, which is a fleet charged its own sum with no discount.
	// A scale of zero would divide by nothing.
	MinCrowdScale = 1
	MaxCrowdScale = 1000
	MaxCrowdBase  = 1000
)

// DefaultTariff is what this build shipped with, and what a fleet that has never
// set one is charged at. The values are the plan's.
func DefaultTariff() Tariff {
	return Tariff{
		Models:  map[Model]int{ModelHaiku: 1, ModelSonnet: 2, ModelOpus: 3},
		Efforts: map[Effort]int{EffortLow: 1, EffortMedium: 2, EffortHigh: 3, EffortXHigh: 4, EffortMax: 6},
		// (9 + n) / 10: one session is charged its own weight, ten are charged
		// double.
		CrowdBase: 9, CrowdScale: 10,
	}
}

// Weight is what a model costs under this tariff.
//
// A model the tariff does not price weighs nothing, and that is the same answer the
// old constant gave for an unset model. It matters because `ParseModel` refuses an
// unknown name at the door: a session can only be running something priced, so a
// zero here means "no model", not "a free model".
func (t Tariff) Weight(m Model) int { return t.Models[m] }

// Effort is what an effort level costs.
func (t Tariff) Effort(e Effort) int { return t.Efforts[e] }

// Session is what one session costs: the two weights multiplied.
func (t Tariff) Session(m Model, e Effort) int { return t.Weight(m) * t.Effort(e) }

// Total is what a set of sessions costs together, with the crowd multiplier.
//
// The ceiling is by construction rather than by rounding a float:
// `(sum × (base + n) + scale - 1) / scale` in integer division is exactly
// ⌈sum × (base + n) / scale⌉ for the non-negative values this can be given.
func (t Tariff) Total(loads []int) int {
	sum := 0
	for _, l := range loads {
		if l > 0 {
			sum += l
		}
	}
	if sum == 0 {
		return 0
	}
	scale := t.CrowdScale
	if scale < MinCrowdScale {
		// A tariff that would divide by nothing is a tariff nobody can have
		// written on purpose. Falling back beats refusing: this is called on every
		// derivation, and a fleet that could not answer "what may this agent do"
		// because a settings file was wrong would be worse than one charged at the
		// built-in rate.
		scale = DefaultTariff().CrowdScale
	}
	return (sum*(t.CrowdBase+len(loads)) + scale - 1) / scale
}

// Multiplier renders the count multiplier a total was computed with, for the line
// `orc employ` prints when a decision costs more than the agent being employed.
func (t Tariff) Multiplier(count int) string {
	scale := t.CrowdScale
	if scale < MinCrowdScale {
		scale = DefaultTariff().CrowdScale
	}
	if count <= 0 {
		count = 0
	}
	whole, part := (t.CrowdBase+count)/scale, (t.CrowdBase+count)%scale
	if part == 0 {
		return fmt.Sprintf("%d", whole)
	}
	return fmt.Sprintf("%d.%d", whole, (part*10)/scale)
}

// Zero reports whether the tariff is unset, which is what a fleet that has never
// priced anything has.
func (t Tariff) Zero() bool { return len(t.Models) == 0 && len(t.Efforts) == 0 && t.CrowdScale == 0 }

// WithDefaults fills in whatever a stored tariff left out.
//
// A tariff missing a model is a fleet that priced two of three and a build that
// added a fourth — both real, and neither a reason to charge a session nothing.
// Filling in beats refusing for the reason Total's fallback does: this is on the
// path of every derivation.
func (t Tariff) WithDefaults() Tariff {
	got := DefaultTariff()
	if t.CrowdBase > 0 {
		got.CrowdBase = t.CrowdBase
	}
	if t.CrowdScale >= MinCrowdScale {
		got.CrowdScale = t.CrowdScale
	}
	for m, w := range t.Models {
		if w >= MinWeight {
			got.Models[m] = w
		}
	}
	for e, w := range t.Efforts {
		if w >= MinWeight {
			got.Efforts[e] = w
		}
	}
	return got
}

// Setting names one thing a tariff prices, as `orc tariff` and cq spell it.
type Setting string

// Settings lists every one, so a test can be total and a screen cannot invent one.
func Settings() []Setting {
	out := make([]Setting, 0, len(Models())+len(Efforts())+2)
	for _, m := range Models() {
		out = append(out, Setting(m.String()))
	}
	for _, e := range Efforts() {
		out = append(out, Setting(e.String()))
	}
	return append(out, "crowd-base", "crowd-scale")
}

// Set changes one setting, returning the tariff that results.
//
// It refuses rather than clamping: a caller asking for a weight of 900 has made a
// mistake, and quietly charging them 100 would hide it.
func (t Tariff) Set(name Setting, value int) (Tariff, error) {
	got := t.WithDefaults()
	key := Setting(strings.ToLower(strings.TrimSpace(string(name))))

	switch key {
	case "crowd-base":
		if value < 0 || value > MaxCrowdBase {
			return Tariff{}, fault.Usage{Reason: fmt.Sprintf(
				"crowd-base runs 0 to %d, not %d", MaxCrowdBase, value)}
		}
		got.CrowdBase = value
		return got, nil
	case "crowd-scale":
		if value < MinCrowdScale || value > MaxCrowdScale {
			return Tariff{}, fault.Usage{Reason: fmt.Sprintf(
				"crowd-scale runs %d to %d, not %d", MinCrowdScale, MaxCrowdScale, value)}
		}
		got.CrowdScale = value
		return got, nil
	}

	if value < MinWeight || value > MaxWeight {
		return Tariff{}, fault.Usage{Reason: fmt.Sprintf(
			"a weight runs %d to %d, not %d; a weight of nothing is a session no budget can refuse",
			MinWeight, MaxWeight, value)}
	}
	if m, err := ParseModel(string(key)); err == nil {
		got.Models[m] = value
		return got, nil
	}
	if e, err := ParseEffort(string(key)); err == nil {
		got.Efforts[e] = value
		return got, nil
	}
	return Tariff{}, fault.Usage{Reason: fmt.Sprintf(
		"%q is not something a tariff prices; it is a model, an effort, crowd-base, or crowd-scale", name)}
}

// Value reads one setting, for a screen that lists them all.
func (t Tariff) Value(name Setting) (int, bool) {
	got := t.WithDefaults()
	switch Setting(strings.ToLower(strings.TrimSpace(string(name)))) {
	case "crowd-base":
		return got.CrowdBase, true
	case "crowd-scale":
		return got.CrowdScale, true
	}
	if m, err := ParseModel(string(name)); err == nil {
		return got.Models[m], true
	}
	if e, err := ParseEffort(string(name)); err == nil {
		return got.Efforts[e], true
	}
	return 0, false
}
