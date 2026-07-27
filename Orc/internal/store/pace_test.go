package store_test

import (
	"testing"
	"time"

	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

// Pacing is stored so a browser can change it, and layered so an agent that runs
// long builds can be given a longer threshold than one that answers mail.
//
// The chain is the wake message's — the identity's, else its role's, else the
// fleet's, else the built-in — because that chain already exists and an operator
// already knows it. What is worth pinning is the order, and what happens when a
// value is missing, unreadable, or nonsense: a cycle reads this on every pass, and
// none of those may stop it.

func layered(t *testing.T) (*store.Store, user.Name, model.Name) {
	t.Helper()
	s, who := newStore(t)
	role, err := model.ParseName("hand")
	if err != nil {
		t.Fatal(err)
	}
	return s, who, role
}

func setPace(t *testing.T, s *store.Store, kind instruct.Kind, role model.Name, who user.Name, p store.Pace) {
	t.Helper()
	if err := s.SetPace(kind, role, who, p); err != nil {
		t.Fatalf("setting %s: %v", kind, err)
	}
}

func TestTheNearestLayerWins(t *testing.T) {
	s, who, role := layered(t)
	setPace(t, s, instruct.System, model.Name{}, user.Name{}, store.Pace{WakeAfter: "30m"})
	setPace(t, s, instruct.Role, role, user.Name{}, store.Pace{WakeAfter: "20m"})
	setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{WakeAfter: "5m"})

	got := s.Pacing(who, role)
	if got.WakeAfter.Value != "5m" || got.WakeAfter.From != instruct.Identity {
		t.Errorf("the identity's layer did not win: %+v", got.WakeAfter)
	}
}

func TestALayerFallsThroughToTheOneBelow(t *testing.T) {
	s, who, role := layered(t)
	setPace(t, s, instruct.System, model.Name{}, user.Name{}, store.Pace{WakeAfter: "30m", TendWatch: "1m"})
	setPace(t, s, instruct.Role, role, user.Name{}, store.Pace{WakeAfter: "20m"})

	got := s.Pacing(who, role)
	if got.WakeAfter.Value != "20m" || got.WakeAfter.From != instruct.Role {
		t.Errorf("wake after resolved to %+v, want the role's", got.WakeAfter)
	}
	// The role said nothing about tending, so the fleet's stands.
	if got.TendWatch.Value != "1m" || got.TendWatch.From != instruct.System {
		t.Errorf("tend watch resolved to %+v, want the fleet's", got.TendWatch)
	}
}

// Where a value came from is carried, because a screen saying "20m" over a value
// somebody did not set on that agent sends them looking in the wrong place.
func TestWhereASettingCameFromIsCarried(t *testing.T) {
	s, who, role := layered(t)
	setPace(t, s, instruct.System, model.Name{}, user.Name{}, store.Pace{WakeEvery: "10m"})

	got := s.Pacing(who, role)
	if !got.WakeEvery.Set() || got.WakeEvery.From != instruct.System {
		t.Errorf("provenance is %+v", got.WakeEvery)
	}
}

func TestNothingSetIsNothingSet(t *testing.T) {
	s, who, role := layered(t)
	got := s.Pacing(who, role)
	if got.WakeAfter.Set() || got.TendWatch.Set() {
		t.Errorf("an unpaced fleet resolved to %+v", got)
	}
	// And a caller's own default is what it falls back to.
	if d := got.WakeAfter.Duration(9 * time.Minute); d != 9*time.Minute {
		t.Errorf("the fallback was not used: %s", d)
	}
}

// A cycle reads this on every pass. A value that will not parse must leave the
// cycle running at the pace it had, because a fleet that stopped over a typo is the
// worst possible response to one.
func TestNonsenseFallsBackRatherThanStopping(t *testing.T) {
	s, who, role := layered(t)
	setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{WakeAfter: "twenty minutes"})

	got := s.Pacing(who, role)
	if d := got.WakeAfter.Duration(10 * time.Minute); d != 10*time.Minute {
		t.Errorf("a mistyped interval became %s rather than the fallback", d)
	}
}

// Off is a state and not a zero: an agent nobody is waking has to look different
// from one being woken and not answering.
func TestOffIsAState(t *testing.T) {
	s, who, role := layered(t)
	setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{WakeOff: "yes"})
	if !s.Pacing(who, role).WakeOff.Off() {
		t.Error("an agent turned off is not off")
	}

	// And an identity can turn back on what its role turned off, which is why the
	// switch is a word rather than a boolean: "not set" and "set to on" differ.
	setPace(t, s, instruct.Role, role, user.Name{}, store.Pace{WakeOff: "yes"})
	setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{WakeOff: "no"})
	if s.Pacing(who, role).WakeOff.Off() {
		t.Error("an identity could not turn back on what its role turned off")
	}
}

// Anything but a plain yes leaves the cycle running. A fleet that quietly stopped
// because a file said `off ` with a trailing space is a fleet nobody is watching.
func TestAnUnrecognisedSwitchLeavesTheCycleRunning(t *testing.T) {
	s, who, role := layered(t)
	for _, odd := range []string{"true", "off", "1", "  "} {
		setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{WakeOff: odd})
		if s.Pacing(who, role).WakeOff.Off() {
			t.Errorf("%q stopped the cycle", odd)
		}
	}
}

// Clearing a layer removes it rather than leaving an empty one, so `orc pace` does
// not list a layer somebody has cleared.
func TestClearingALayerRemovesIt(t *testing.T) {
	s, who, _ := layered(t)
	setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{WakeAfter: "5m"})
	setPace(t, s, instruct.Identity, model.Name{}, who, store.Pace{})

	if _, set := s.Pace(instruct.Identity, model.Name{}, who); set {
		t.Error("a cleared layer is still there")
	}
}

func TestAnIdentityWithNoRoleStillResolves(t *testing.T) {
	s, who, _ := layered(t)
	setPace(t, s, instruct.System, model.Name{}, user.Name{}, store.Pace{WakeAfter: "30m"})

	got := s.Pacing(who, model.Name{})
	if got.WakeAfter.Value != "30m" {
		t.Errorf("an identity with no role resolved to %+v", got.WakeAfter)
	}
}
