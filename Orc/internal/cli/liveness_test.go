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
