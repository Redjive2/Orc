package model

import (
	"fmt"
	"strings"

	"orc/common/fault"
)

// Load is how much thinking a session costs, and it is the thing `spawn(<n>)`
// budgets. Auth_Perm_Role.md calls it "a function of model, effort, and # of
// models active", and all three inputs are real:
//
//	session(model, effort) = weight(model) × weight(effort)      1 … 18
//	total(S)               = ⌈ Σ session(s) × (9 + |S|) / 10 ⌉
//
// S is the set the budget is measured over — everything an actor employs,
// transitively — and the same set feeds both the sum and the count, so a deep
// subtree is not charged twice for its depth.
//
// The count multiplier is what makes a fleet charged for being a fleet: the tenth
// agent costs more than the first, so a budget discourages sprawl without anybody
// writing a rule about sprawl, and swapping two haikus for one sonnet is cheaper
// at equal weight — which is the trade a budget should encourage.
//
// All of it is integer arithmetic. A budget that depended on floating point would
// be a budget that rounded differently on two machines, and the ceiling means it
// never rounds in the fleet's favour.

// Model is which Claude a session runs.
type Model uint8

// The models, lightest first. The weights are the plan's and are the one part of
// this file somebody may want to tune: they say how much more an opus session
// costs than a haiku one, which is a judgement about money and not about code.
const (
	ModelUnset Model = iota
	ModelHaiku
	ModelSonnet
	ModelOpus
)

// DefaultModel and DefaultEffort are what `orc employ` uses when it is not told.
// Sonnet at medium effort is the middle of the range in both directions, so a
// fleet nobody has tuned is a fleet of reasonable agents rather than a fleet of
// cheap or expensive ones.
const (
	DefaultModel  = ModelSonnet
	DefaultEffort = EffortMedium
)

// String returns the name Claude's own --model flag takes.
func (m Model) String() string {
	switch m {
	case ModelHaiku:
		return "haiku"
	case ModelSonnet:
		return "sonnet"
	case ModelOpus:
		return "opus"
	default:
		return "unset"
	}
}

// Weight is the model's contribution at the built-in prices. A fleet with its own
// tariff asks that instead — see Tariff.Weight.
func (m Model) Weight() int { return DefaultTariff().Weight(m) }

// Valid reports whether the model is one this build knows.
func (m Model) Valid() bool { return m >= ModelHaiku && m <= ModelOpus }

// Models lists them, for help and for tests that must be total.
func Models() []Model { return []Model{ModelHaiku, ModelSonnet, ModelOpus} }

// ParseModel reads a model name.
//
// Only the aliases are accepted, not a full model id like `claude-opus-5`. A
// budget has to know what a session costs, and a full id names a specific build
// whose weight this table cannot know — so an unrecognised name is a refusal
// naming the three, rather than a session admitted at a cost of zero.
func ParseModel(raw string) (Model, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "haiku":
		return ModelHaiku, nil
	case "sonnet":
		return ModelSonnet, nil
	case "opus":
		return ModelOpus, nil
	default:
		return ModelUnset, fault.Usage{Reason: fmt.Sprintf(
			"unknown model %q; orc budgets by weight, so it takes haiku, sonnet, or opus", raw)}
	}
}

// Effort is how hard a session thinks.
type Effort uint8

// The effort levels, matching Claude's own --effort flag.
const (
	EffortUnset Effort = iota
	EffortLow
	EffortMedium
	EffortHigh
	EffortXHigh
	EffortMax
)

// String returns the name Claude's own --effort flag takes.
func (e Effort) String() string {
	switch e {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	default:
		return "unset"
	}
}

// Short returns the name a narrow column shows.
func (e Effort) Short() string {
	if e == EffortMedium {
		return "med"
	}
	return e.String()
}

// Weight is the effort's contribution at the built-in prices. Max is 6 rather than
// 5 because the step from xhigh to max is the largest one in practice, and a budget
// should feel it — a fleet that disagrees says so in its tariff.
func (e Effort) Weight() int { return DefaultTariff().Effort(e) }

// Valid reports whether the effort is one this build knows.
func (e Effort) Valid() bool { return e >= EffortLow && e <= EffortMax }

// Efforts lists them, for help and for tests that must be total.
func Efforts() []Effort {
	return []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
}

// ParseEffort reads an effort level.
func ParseEffort(raw string) (Effort, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low":
		return EffortLow, nil
	case "medium", "med":
		return EffortMedium, nil
	case "high":
		return EffortHigh, nil
	case "xhigh":
		return EffortXHigh, nil
	case "max":
		return EffortMax, nil
	default:
		return EffortUnset, fault.Usage{Reason: fmt.Sprintf(
			"unknown effort %q; try low, medium, high, xhigh, or max", raw)}
	}
}

// SessionLoad is what one session costs at the built-in prices.
//
// Kept for the callers that genuinely have no fleet to ask — a session state read
// off disk with nothing else loaded — and *not* the way a budget is computed. A
// fleet with its own tariff answers through authz, which carries one; see
// Tariff.Session.
func SessionLoad(m Model, e Effort) int { return DefaultTariff().Session(m, e) }

// TotalLoad is what a set of sessions costs together, with the count multiplier.
//
// The ceiling is by construction rather than by rounding a float: `(sum × (9 + n)
// + 9) / 10` in integer division is exactly ⌈sum × (9 + n) / 10⌉ for the
// non-negative values this can be given.
// TotalLoad is Tariff.Total at the built-in prices, for the same callers.
func TotalLoad(loads []int) int { return DefaultTariff().Total(loads) }

// Multiplier renders the count multiplier a total was computed with, for the line
// `orc employ` prints when a decision costs more than the agent being employed.
//
// It is text rather than a number because it is only ever shown: a caller doing
// arithmetic should call TotalLoad, and a second float in the codebase would be a
// second thing that could round differently.
func Multiplier(count int) string { return DefaultTariff().Multiplier(count) }
