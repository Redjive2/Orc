package agent_test

import (
	"strings"
	"testing"

	"orc/cq/internal/protocol"
	"orc/cq/internal/settings"
)

// A pace set in the browser used to be one queued action and nothing else. If it
// failed, or the queue was cleared, or the machine was rebuilt from a checkout, the
// setting was simply gone — and neither end knew, because nothing ever compared what
// the fleet was doing against what anybody had asked for.
//
// So the server says what it intends on every response and the agent closes the gap.
// These pin the four things that makes true: it corrects drift, it stays quiet when
// there is none, it has no opinion where the server has none, and it never touches a
// layer that is not the fleet's.

// paced puts a machine's intended fleet pace on the server and gives the fake
// machine the pace orc currently resolves, then syncs once.
func paced(t *testing.T, want protocol.DesiredPace, got protocol.FleetPace) []protocol.Args {
	t.Helper()
	r := newRig(t)
	if err := r.state.SetFleetPace(r.src.snapshot.Machine, want); err != nil {
		t.Fatal(err)
	}
	r.src.snapshot.Fleet = &protocol.Fleet{Pace: got}
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r.src.paceCalls()
}

func TestASyncPutsADriftedFleetPaceBack(t *testing.T) {
	calls := paced(t,
		protocol.DesiredPace{WakeAfter: "20m", WakeEvery: "5m", TendWatch: "1m"},
		protocol.FleetPace{WakeAfter: "1h0m0s", WakeEvery: "5m", TendWatch: "1m"})

	if len(calls) != 1 {
		t.Fatalf("ran %d pace commands, want 1: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.Cycle != "wake" || got.After != "20m" {
		t.Errorf("the correction was %+v, want wake --after 20m", got)
	}
	// Only what had drifted. Re-asserting the two that already agreed would make
	// every sync a write, and `orc pace` is a command that logs.
	if got.Every != "" || got.Watch != "" {
		t.Errorf("it re-set values that already agreed: %+v", got)
	}
}

func TestASyncLeavesAFleetThatAlreadyAgrees(t *testing.T) {
	calls := paced(t,
		protocol.DesiredPace{WakeAfter: "20m", TendWatch: "1m"},
		protocol.FleetPace{WakeAfter: "20m", TendWatch: "1m"})
	if len(calls) != 0 {
		t.Errorf("it ran %d pace commands against a fleet that already matched: %+v",
			len(calls), calls)
	}
}

// An absent field is no opinion, not "clear it". A server nobody has asked about
// tend must not turn tend off, and one that has never been asked at all must not
// touch anything.
func TestASilentServerChangesNothing(t *testing.T) {
	if calls := paced(t, protocol.DesiredPace{},
		protocol.FleetPace{WakeAfter: "20m", TendWatch: "1m"}); len(calls) != 0 {
		t.Errorf("a server with no opinion changed the pace: %+v", calls)
	}
	// And an opinion about one cycle says nothing about the other.
	calls := paced(t, protocol.DesiredPace{WakeAfter: "20m"},
		protocol.FleetPace{WakeAfter: "1h0m0s", TendWatch: "1m", TendOff: true})
	for _, c := range calls {
		if c.Cycle == "tend" {
			t.Errorf("an opinion about wake reached tend: %+v", c)
		}
	}
}

// Off and on are three-state on the wire for this reason: a cycle nobody has
// expressed a view about and one somebody deliberately left running are different,
// and only the second should be turned back on.
func TestAStoppedCycleIsPutBackOnlyWhenTheServerSaidSo(t *testing.T) {
	calls := paced(t, protocol.DesiredPace{TendOff: "no"}, protocol.FleetPace{TendOff: true})
	if len(calls) != 1 || !calls[0].PaceOn || calls[0].Cycle != "tend" {
		t.Fatalf("a cycle the server wants running was not started: %+v", calls)
	}

	if calls := paced(t, protocol.DesiredPace{TendOff: "yes"},
		protocol.FleetPace{TendOff: true}); len(calls) != 0 {
		t.Errorf("a cycle already stopped was stopped again: %+v", calls)
	}
	if calls := paced(t, protocol.DesiredPace{TendOff: "yes"},
		protocol.FleetPace{}); len(calls) != 1 || !calls[0].PaceOff {
		t.Errorf("a cycle the server wants stopped was left running: %+v", calls)
	}
}

// A machine that runs no agents has no fleet to pace, and a server that somehow
// holds an intention for it must not make cq shell out to a tool that is not there.
func TestAMachineWithNoFleetIsNotPaced(t *testing.T) {
	r := newRig(t)
	if err := r.state.SetFleetPace(r.src.snapshot.Machine,
		protocol.DesiredPace{WakeAfter: "20m"}); err != nil {
		t.Fatal(err)
	}
	r.src.snapshot.Fleet = nil
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls := r.src.paceCalls(); len(calls) != 0 {
		t.Errorf("a machine with no fleet was paced anyway: %+v", calls)
	}
}

// The correction is reported, never silent: it may be undoing something somebody
// typed on this machine, and learning that from a fleet's behaviour days later is
// the worst way to learn it.
func TestAPaceThatWasPutBackIsReported(t *testing.T) {
	r := newRig(t)
	if err := r.state.SetFleetPace(r.src.snapshot.Machine,
		protocol.DesiredPace{WakeEvery: "5m"}); err != nil {
		t.Fatal(err)
	}
	r.src.snapshot.Fleet = &protocol.Fleet{Pace: protocol.FleetPace{WakeEvery: "30m"}}

	report, err := r.agent.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Paced) != 1 {
		t.Fatalf("the report said nothing about the correction: %+v", report.Paced)
	}
	for _, want := range []string{"wake", "5m"} {
		if !strings.Contains(report.Paced[0], want) {
			t.Errorf("the report does not say %q: %q", want, report.Paced[0])
		}
	}
}

// The sync interval only ever lived in a running watcher's head. It arrives on a
// response and was applied by resetting a ticker, so a one-shot `cq sync` read it
// and dropped it, and a watcher restarted by a service manager went back to the
// number on its command line — fixed at install time — and stayed there.
func TestTheServersSyncPaceIsRememberedAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	r := newRigAt(t, t.TempDir(), dir)
	if err := r.state.SetSyncPace("30m"); err != nil {
		t.Fatal(err)
	}

	report, err := r.agent.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Pace != "30m" {
		t.Fatalf("the response did not carry the pace: %q", report.Pace)
	}

	// The agent hands it to the caller; the CLI is what writes it down, and
	// settings.Write/Read is the file both ends of that agree on.
	if err := settings.Write(dir, settings.Settings{Pace: report.Pace}); err != nil {
		t.Fatal(err)
	}
	got, err := settings.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pace != "30m" {
		t.Errorf("the pace did not survive being written down: %+v", got)
	}
}
