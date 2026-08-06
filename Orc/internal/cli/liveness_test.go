package cli_test

import (
	"strings"
	"testing"

	"orc/orc/internal/store"
)

// Coming back from a stop.
//
// The case these are about is the one a fleet actually meets: an agent stops because
// something outside it went wrong for a while — a usage limit reached mid-turn, a
// network that came and went, a machine that slept — and by the time anything
// reconciles, that is over. What matters is what it comes back *as*.

// kill leaves the identity employed with no live session, which is what the store
// looks like after a supervisor has given up and removed its state.
func kill(t *testing.T, r *rig, who string) {
	t.Helper()
	s := mustStore(t, r)
	name := mustName(t, who)
	if err := s.RemoveSession(name); err != nil {
		t.Fatal(err)
	}
	delete(r.populated, who)
	r.populates = nil
}

// end records how the previous session finished, as the supervisor does on its way
// out.
func end(t *testing.T, r *rig, who string, got store.Ended) {
	t.Helper()
	if err := mustStore(t, r).RecordEnded(mustName(t, who), got); err != nil {
		t.Fatal(err)
	}
}

// TestTendResumesRatherThanReplaces.
//
// An agent does not usually stop because its conversation is broken. It stops because
// something outside it went wrong for a while — a usage limit reached mid-turn, a
// network that came and went — and by the time `tend` runs, that is over. Starting a
// fresh session then throws away whatever the agent was part-way through and hands
// back something that has never heard of it, which is what "it did not come back
// properly" means in practice.
func TestTendResumesTheSessionThatEnded(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	// The supervisor went away, leaving a record of what it was.
	end(t, r, "ember", store.Ended{Session: "aaaaaaaa-1111-2222-3333-444444444444"})
	kill(t, r, "ember")

	got := r.ok("boss", "tend")
	if !strings.Contains(got.stdout, "resumed") {
		t.Errorf("tend did not resume the previous session:\n%s", got.stdout)
	}
	// And it resumed *that* session rather than minting one.
	if !strings.Contains(strings.Join(r.populates, " "), "resume=true") {
		t.Errorf("the session was not resumed: %v", r.populates)
	}
}

// With nothing remembered there is nothing to continue, so a fresh one is right.
func TestTendStartsFreshWhenNothingIsRemembered(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	kill(t, r, "ember")

	got := r.ok("boss", "tend")
	if !strings.Contains(got.stdout, "populated") {
		t.Errorf("tend did not start a session:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "resumed") {
		t.Errorf("tend resumed a session it knows nothing about:\n%s", got.stdout)
	}
}

// TestARefreshIsNotUndoneByABackstop. `orc refresh` is somebody saying the old
// conversation is over; resuming it afterwards would be Orc overruling them.
func TestRefreshForgetsTheEnding(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	end(t, r, "ember", store.Ended{Session: "aaaaaaaa-1111-2222-3333-444444444444", MidTurn: true})

	r.ok("boss", "refresh", "ember")
	kill(t, r, "ember")

	if got := r.ok("boss", "tend"); strings.Contains(got.stdout, "resumed") {
		t.Errorf("a refreshed identity was resumed into its old conversation:\n%s", got.stdout)
	}
}

// And firing, for the same reason.
func TestFiringForgetsTheEnding(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	end(t, r, "ember", store.Ended{Session: "aaaaaaaa-1111-2222-3333-444444444444"})

	r.ok("boss", "fire", "ember", "--yes")
	r.ok("boss", "employ", "ember")
	kill(t, r, "ember")

	if got := r.ok("boss", "tend"); strings.Contains(got.stdout, "resumed") {
		t.Errorf("a fired identity was resumed into its old conversation:\n%s", got.stdout)
	}
}

// The card says what became of the previous session, because "employed, not running"
// reads the same whether it ended an hour ago mid-turn or was never started — and the
// difference decides what the next tend does.
func TestTheCardSaysHowTheLastSessionEnded(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	end(t, r, "ember", store.Ended{
		Session: "aaaaaaaa-1111-2222-3333-444444444444", Why: "signal: killed", MidTurn: true, Restarts: 5,
	})
	kill(t, r, "ember")

	got := r.ok("boss", "status", "ember")
	if !strings.Contains(got.stdout, "last session") {
		t.Fatalf("the card does not say what became of the last session:\n%s", got.stdout)
	}
	for _, want := range []string{"part-way through a turn", "signal: killed"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the card does not say %q:\n%s", want, got.stdout)
		}
	}
}

// Tending turned off for one agent is honoured, not merely recorded.
//
// `orc pace tend <agent> --off` writes at the identity layer and resolves through
// the same three layers everything else does. The backstop read it at the *fleet*
// layer only, so the setting confirmed, drew itself as in force in `orc pace` and
// in cq's browser, and did nothing: the very next pass repopulated the agent. A
// control that reports success and has no effect is worse than one that refuses —
// the operator stops looking at it.
func TestTendingOffForOneAgentIsHonoured(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	r.ok("boss", "pace", "tend", "ember", "--off")

	// It is employed and its session has gone. Ordinarily this is exactly what the
	// backstop exists to fix.
	kill(t, r, "ember")
	r.populates = nil

	got := r.ok("boss", "tend")
	if len(r.populates) != 0 {
		t.Errorf("tending is off for ember and it was started anyway: %v", r.populates)
	}
	if !strings.Contains(got.stdout+got.stderr, "tending is off") {
		t.Errorf("the pass did not say why it left ember down:\n%s\n%s", got.stdout, got.stderr)
	}
}

// And the fleet keeps being tended around it: a pause is one agent's, not a hole
// in the backstop.
func TestTendingOffForOneAgentDoesNotStopTheRest(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	r.ok("boss", "employ", "quill")
	r.ok("boss", "pace", "tend", "ember", "--off")

	kill(t, r, "ember")
	kill(t, r, "quill")
	r.populates = nil

	r.ok("boss", "tend")
	joined := strings.Join(r.populates, " ")
	if strings.Contains(joined, "ember") {
		t.Errorf("ember was started with its tending off: %v", r.populates)
	}
	if !strings.Contains(joined, "quill") {
		t.Errorf("quill was not started, though only ember was paused: %v", r.populates)
	}
}

// Turning waking off does not leave an agent down.
//
// The pause is about *poking*: an agent nobody is nudging can still be employed
// with no session, and starting it is the cycle's other job. The pause used to be
// consulted before that check, so `wake --tend` — the form that runs on a machine
// where the wake cron is the only thing there — skipped the agent entirely and
// left it stopped with nothing else running to notice.
func TestWakeTendStartsAnAgentWhoseWakingIsOff(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	r.ok("boss", "pace", "wake", "ember", "--off")

	kill(t, r, "ember")
	r.populates = nil

	r.ok("boss", "wake", "--tend")
	if !strings.Contains(strings.Join(r.populates, " "), "ember") {
		t.Errorf("waking is off for ember, but it was employed and down and was not started: %v",
			r.populates)
	}
}

// And it is still not poked — the pause is honoured for the thing it is about.
func TestWakeDoesNotPokeAnAgentWhoseWakingIsOff(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	r.ok("boss", "pace", "wake", "ember", "--off")

	got := r.ok("boss", "wake", "--after", "1m", "--dry-run")
	if strings.Contains(got.stdout, "would wake") && strings.Contains(got.stdout, "ember") {
		t.Errorf("ember's waking is off and it was still woken:\n%s", got.stdout)
	}
}

// A paused agent is in the summary rather than missing from it.
//
// The count was kept and never passed to the report, so a fleet of eight with
// three paused said "all working — 5 sessions" and the three nobody was waking
// were nowhere in the line. That is the decision the pause is supposed to make
// visible: an agent nobody is waking looks exactly like a healthy one otherwise.
func TestWakeSaysHowManyAreNotBeingWoken(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	r.ok("boss", "pace", "wake", "ember", "--off")

	got := r.ok("boss", "wake")
	if !strings.Contains(got.stdout, "not being woken") {
		t.Errorf("the pass did not say that anything was paused:\n%s", got.stdout)
	}
}
