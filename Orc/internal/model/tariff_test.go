package model_test

import (
	"testing"

	"orc/orc/internal/model"
)

// What thinking costs, as a value rather than a constant.
//
// The arithmetic has to be reproducible — two machines charging differently for the
// same fleet is how a budget stops meaning anything — so what is pinned here is that
// it stays integer, that the ceiling never rounds in the fleet's favour, and that a
// tariff which has been half-written still prices everything.

func TestTheBuiltInPricesAreWhatTheyWere(t *testing.T) {
	got := model.DefaultTariff()
	if got.Session(model.ModelOpus, model.EffortMax) != 18 {
		t.Errorf("opus at max costs %d, want 18", got.Session(model.ModelOpus, model.EffortMax))
	}
	if got.Session(model.ModelHaiku, model.EffortLow) != 1 {
		t.Errorf("haiku at low costs %d, want 1", got.Session(model.ModelHaiku, model.EffortLow))
	}
}

// The ceiling is by construction, and it never rounds in the fleet's favour: a set
// that costs a fraction over a whole number costs the next one up.
func TestTheCrowdMultiplierRoundsUp(t *testing.T) {
	got := model.DefaultTariff()
	// One session of 4: (4 × 10) / 10 = 4 exactly.
	if n := got.Total([]int{4}); n != 4 {
		t.Errorf("one session of 4 costs %d, want 4", n)
	}
	// Three of 1: (3 × 12) / 10 = 3.6 → 4.
	if n := got.Total([]int{1, 1, 1}); n != 4 {
		t.Errorf("three sessions of 1 cost %d, want 4 (3.6 rounded up)", n)
	}
	if n := got.Total(nil); n != 0 {
		t.Errorf("nothing costs %d", n)
	}
}

func TestAPricedFleetChargesItsOwnPrices(t *testing.T) {
	got, err := model.DefaultTariff().Set("opus", 9)
	if err != nil {
		t.Fatal(err)
	}
	if n := got.Session(model.ModelOpus, model.EffortMedium); n != 18 {
		t.Errorf("opus at medium costs %d under the new price, want 18", n)
	}
	// And nothing else moved.
	if n := got.Session(model.ModelSonnet, model.EffortMedium); n != 4 {
		t.Errorf("sonnet changed to %d when opus was repriced", n)
	}
}

func TestASettingThatIsNotOneIsRefused(t *testing.T) {
	for _, name := range []model.Setting{"turbo", "", "models", "crowd"} {
		if _, err := model.DefaultTariff().Set(name, 2); err == nil {
			t.Errorf("%q was accepted as something a tariff prices", name)
		}
	}
}

// A weight of nothing is a session no budget can refuse, and clamping would hide
// the mistake rather than answer it.
func TestAWeightOutOfRangeIsRefusedRatherThanClamped(t *testing.T) {
	for _, value := range []int{0, -1, 101} {
		if _, err := model.DefaultTariff().Set("opus", value); err == nil {
			t.Errorf("a weight of %d was accepted", value)
		}
	}
}

func TestTheCrowdSettingsHaveTheirOwnRanges(t *testing.T) {
	if _, err := model.DefaultTariff().Set("crowd-scale", 0); err == nil {
		t.Error("a scale of zero was accepted, which divides by nothing")
	}
	if _, err := model.DefaultTariff().Set("crowd-base", 0); err != nil {
		t.Errorf("a base of zero is a fleet charged its own sum and should be allowed: %v", err)
	}
}

// A stored tariff can be half-written: a fleet that priced two models and a build
// that added a third. Filling in beats refusing, because this is on the path of
// every derivation.
func TestAHalfWrittenTariffStillPricesEverything(t *testing.T) {
	partial := model.Tariff{Models: map[model.Model]int{model.ModelOpus: 5}}
	got := partial.WithDefaults()

	if n := got.Weight(model.ModelOpus); n != 5 {
		t.Errorf("the stored weight was lost: %d", n)
	}
	if n := got.Weight(model.ModelHaiku); n != 1 {
		t.Errorf("an unpriced model weighs %d, want the built-in 1", n)
	}
	if n := got.Effort(model.EffortMax); n != 6 {
		t.Errorf("an unpriced effort weighs %d, want the built-in 6", n)
	}
	if got.CrowdScale == 0 {
		t.Error("the crowd scale was left at zero, which divides by nothing")
	}
}

// A scale of zero on disk must not take a fleet down: every derivation calls this.
func TestATariffThatWouldDivideByNothingFallsBack(t *testing.T) {
	broken := model.Tariff{CrowdScale: 0, Models: map[model.Model]int{model.ModelOpus: 3}}
	if n := broken.Total([]int{3}); n <= 0 {
		t.Errorf("a broken tariff priced a session at %d", n)
	}
}

func TestEverySettingCanBeReadAndWritten(t *testing.T) {
	got := model.DefaultTariff()
	for _, setting := range model.Settings() {
		was, ok := got.Value(setting)
		if !ok {
			t.Errorf("%q cannot be read", setting)
			continue
		}
		next := was + 1
		if setting == "crowd-scale" && next > model.MaxCrowdScale {
			continue
		}
		if _, err := got.Set(setting, next); err != nil {
			t.Errorf("%q cannot be set to %d: %v", setting, next, err)
		}
	}
}
