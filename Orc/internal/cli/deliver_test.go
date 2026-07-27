package cli

import (
	"errors"
	"testing"

	"orc/common/fault"
	"orc/common/user"
)

// named is this file's own, because these tests are inside the package — the
// external suite's helper is not reachable from here.
func named(t *testing.T, raw string) user.Name {
	t.Helper()
	got, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("name %q: %v", raw, err)
	}
	return got
}

// Retrying the transient and refusing the decided.
//
// The whole value of `keepTrying` is in which errors it retries. Retrying too
// little leaves a poke lost to a millisecond of a restarting supervisor; retrying
// too much turns "you may not do that" into a command that hangs and then says the
// same thing anyway.

func TestATransientFailureIsTriedAgain(t *testing.T) {
	calls := 0
	tried, err := keepTrying(func() error {
		calls++
		if calls < 3 {
			return fault.Unavailable{Peer: "ember", Err: errors.New("no socket yet")}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a session that answered on the third try was given up on: %v", err)
	}
	if tried != 3 {
		t.Errorf("it took %d attempts, want 3", tried)
	}
}

// A refusal is an answer. Trying it again is how a tool turns a clear no into a
// wait followed by the same no.
func TestADecisionIsNotRetried(t *testing.T) {
	for _, decided := range []error{
		fault.Denied{Actor: "ember", Action: "poke", Target: "atlas"},
		fault.NotFound{Target: "atlas"},
		fault.Usage{Reason: "that message cannot be typed"},
		fault.Conflict{Path: "ember", Reason: "already has a supervisor"},
	} {
		calls := 0
		tried, err := keepTrying(func() error {
			calls++
			return decided
		})
		if !errors.Is(err, decided) && err.Error() != decided.Error() {
			t.Errorf("the error changed on the way back: %v", err)
		}
		if calls != 1 || tried != 1 {
			t.Errorf("%T was tried %d times; a decision is answered once", decided, calls)
		}
	}
}

func TestItGivesUpRatherThanHanging(t *testing.T) {
	calls := 0
	tried, err := keepTrying(func() error {
		calls++
		return fault.Unavailable{Peer: "ember", Err: errors.New("still nothing")}
	})
	if err == nil {
		t.Fatal("a session that never answered was reported as reached")
	}
	if calls != DeliverTries || tried != DeliverTries {
		t.Errorf("it tried %d times, want %d; a cycle cannot hang on one agent", calls, DeliverTries)
	}
}

func TestSuccessCostsOneAttempt(t *testing.T) {
	tried, err := keepTrying(func() error { return nil })
	if err != nil || tried != 1 {
		t.Errorf("the ordinary case took %d attempts (%v)", tried, err)
	}
}

// The message says what happened, so a fleet that is limping does not read like one
// that is fine.
func TestAFailureSaysHowHardItTried(t *testing.T) {
	base := fault.Unavailable{Peer: "ember", Err: errors.New("no socket")}
	got := unreached(named(t, "ember"), 4, base)
	if got.Error() == base.Error() {
		t.Error("four failed attempts read exactly like one")
	}
	// One attempt is the ordinary failure and is left alone.
	if unreached(named(t, "ember"), 1, base).Error() != base.Error() {
		t.Error("a single failure was dressed up")
	}
}
