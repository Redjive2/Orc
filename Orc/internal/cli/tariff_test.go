package cli_test

import (
	"strings"
	"testing"
)

// Changing what thinking costs.
//
// A tariff change is felt by every budget at once, because every budget is derived —
// so the thing worth pinning is not the arithmetic (that is model's) but the
// consequence: who it puts over, that they are told before it happens, and that the
// fleet actually charges the new price afterwards.

func TestTheTariffStartsAtTheBuiltInPrices(t *testing.T) {
	r := fullFleet(t)
	got := r.ok("boss", "tariff")
	if !strings.Contains(got.stdout, "opus") || !strings.Contains(got.stdout, "crowd-scale") {
		t.Errorf("the tariff does not list what it prices:\n%s", got.stdout)
	}
}

func TestAPriceCanBeChangedAndIsShown(t *testing.T) {
	r := fullFleet(t)
	got := r.ok("boss", "tariff", "opus", "9")

	if !strings.Contains(got.stdout, "priced") {
		t.Errorf("the change was not confirmed:\n%s", got.stdout)
	}
	// The table printed afterwards must show the new price rather than the one the
	// command started with — the fleet was derived before the write.
	if !strings.Contains(got.stdout, "opus at medium effort costs 18") {
		t.Errorf("the table shows the price it replaced:\n%s", got.stdout)
	}
}

// The consequence, which is the whole reason this is journaled and gated: an actor
// inside its budget can be over it without anybody touching that actor.
func TestAPriceThatPutsSomebodyOverBudgetSaysSo(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "budget", "engineer", "8")
	r.ok("boss", "employ", "ember")

	got := r.run("boss", "tariff", "sonnet", "5")
	if got.code == 0 {
		t.Fatalf("a change that puts an agent over budget went through unasked:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "ember") {
		t.Errorf("the refusal does not name who it affects: %s", got.stderr)
	}
	if !strings.Contains(got.stderr, "--yes") {
		t.Errorf("the refusal does not say how to proceed: %s", got.stderr)
	}
}

// It refuses nothing with --yes: a fleet over its own budget is information, and a
// tariff that could only ever be loosened while agents ran would be one nobody could
// tighten.
func TestWithYesItGoesThroughAndSaysWhoIsOver(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "budget", "engineer", "8")
	r.ok("boss", "employ", "ember")

	got := r.ok("boss", "tariff", "sonnet", "5", "--yes")
	if !strings.Contains(got.stderr, "over budget") {
		t.Errorf("nobody was told who is now over budget:\n%s", got.stderr)
	}
}

// The point of storing it: the fleet charges the new price everywhere afterwards.
func TestTheFleetChargesTheNewPrice(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	before := r.ok("boss", "status")
	r.ok("boss", "tariff", "sonnet", "10", "--yes")
	after := r.ok("boss", "status")

	if before.stdout == after.stdout {
		t.Error("the fleet's loads did not change when the price of a session did")
	}
}

func TestClearingNeedsYesAndReturnsTheBuiltInPrices(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "tariff", "opus", "9")

	if got := r.run("boss", "tariff", "--clear"); got.code == 0 {
		t.Error("clearing every price went through without --yes")
	}
	got := r.ok("boss", "tariff", "--clear", "--yes")
	if !strings.Contains(got.stdout, "opus at medium effort costs 6") {
		t.Errorf("the built-in prices did not come back:\n%s", got.stdout)
	}
}

// Calibration proposes and never applies, and says so when it has nothing to
// propose from — which is every fleet that has not been measured yet.
func TestCalibrationWithNothingMeasuredSaysSo(t *testing.T) {
	r := fullFleet(t)
	got := r.ok("boss", "tariff", "--calibrate")
	if !strings.Contains(got.stdout, "nothing has been measured") {
		t.Errorf("an unmeasured fleet got a proposal from nothing:\n%s", got.stdout)
	}
}

func TestASettingThatIsNotOneIsRefusedWithTheAlternatives(t *testing.T) {
	r := fullFleet(t)
	got := r.run("boss", "tariff", "turbo", "4")
	if got.code == 0 {
		t.Fatal("a setting that does not exist was accepted")
	}
	if !strings.Contains(got.stderr, "crowd-base") {
		t.Errorf("the refusal does not name what a tariff prices: %s", got.stderr)
	}
}
