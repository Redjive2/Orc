package store_test

import (
	"testing"
	"time"

	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// Watching one agent makes a machine send that session every few seconds, and the
// two things that can go badly wrong here both go wrong quietly.
//
// The first is the merge. A narrow round carries a snapshot with no mail, no tasks
// and no repository; if it were ever stored the ordinary way it would erase a
// whole machine's mirror, every three seconds, to keep one transcript current.
// Nothing on the screen would say so — the mailbox would simply be empty.
//
// The second is the clock. LastSync is what the status bar reads as "how old
// everything you are looking at is". A narrow round that moved it would have the
// site claim a five-minute-old mailbox was three seconds old, which is the exact
// failure the session screen was built to avoid.

// mirrored is a machine with mail, a fleet and one agent's session.
func mirrored(machine protocol.MachineID) protocol.Snapshot {
	snap := snapshot(machine)
	snap.Fleet = &protocol.Fleet{
		Operator: "redjive",
		Identities: []protocol.FleetID{
			{Name: "ember", Authority: 40, Employed: true},
		},
		Sessions: []protocol.FleetSession{
			{Identity: "ember", Live: true, Turn: 3},
		},
	}
	return snap
}

func TestAWatchIsTakenAndReadBack(t *testing.T) {
	s := open(t)
	if err := s.Watch("sandy", "ember", "3s", at); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	got := s.Watching("sandy", at.Add(time.Second))
	if got == nil {
		t.Fatal("a watch just taken is not being watched")
	}
	if got.Identity != "ember" || got.Every != "3s" {
		t.Errorf("watching %+v, want ember every 3s", *got)
	}
}

func TestAWatchLapsesOnItsOwn(t *testing.T) {
	// The whole safety argument. A closed tab, a sleeping phone and a killed
	// browser are the same event from here — nothing arrives — and none of them
	// may leave a machine syncing every three seconds for ever.
	s := open(t)
	if err := s.Watch("sandy", "ember", "3s", at); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got := s.Watching("sandy", at.Add(store.WatchLease+time.Second)); got != nil {
		t.Errorf("a lease nobody renewed is still live: %+v", *got)
	}
}

func TestRenewingIsTheSameCallAsTaking(t *testing.T) {
	s := open(t)
	if err := s.Watch("sandy", "ember", "3s", at); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	later := at.Add(store.WatchLease - time.Second)
	if err := s.Watch("sandy", "ember", "3s", later); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got := s.Watching("sandy", later.Add(store.WatchLease-time.Second)); got == nil {
		t.Error("a renewed lease lapsed at the original deadline")
	}
}

func TestAWatchCannotAskForATighterSpinThanTheFloor(t *testing.T) {
	// The cost lands on a machine nobody is looking at: every narrow round shells
	// out to `orc view`. A client asking for 10ms must not get it.
	s := open(t)
	if err := s.Watch("sandy", "ember", "10ms", at); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	got := s.Watching("sandy", at)
	if got == nil {
		t.Fatal("no watch was taken")
	}
	every, err := time.ParseDuration(got.Every)
	if err != nil {
		t.Fatalf("stored %q, which does not parse: %v", got.Every, err)
	}
	if every < store.WatchFloor {
		t.Errorf("stored %s, which is under the %s floor", every, store.WatchFloor)
	}
}

func TestDroppingAWatchEndsItAtOnce(t *testing.T) {
	s := open(t)
	if err := s.Watch("sandy", "ember", "3s", at); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := s.Unwatch("sandy", "ember"); err != nil {
		t.Fatalf("Unwatch: %v", err)
	}
	if got := s.Watching("sandy", at); got != nil {
		t.Errorf("a dropped watch is still live: %+v", *got)
	}
}

func TestDroppingAWatchNobodyTookIsNotAnError(t *testing.T) {
	// Leaving a pane drops the lease, and a reload can leave two drops for one
	// take. The second must be quiet.
	if err := open(t).Unwatch("sandy", "ember"); err != nil {
		t.Errorf("Unwatch on an unwatched machine: %v", err)
	}
}

func TestDroppingAWatchDoesNotTakeSomebodyElsesWithIt(t *testing.T) {
	// A machine holds one lease and two operators can want it. Without the name,
	// the second one closing their pane would kill the first one's — a transcript
	// that goes cold with nothing on screen to say why.
	s := open(t)
	if err := s.Watch("sandy", "ember", "3s", at); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := s.Unwatch("sandy", "atlas"); err != nil {
		t.Fatalf("Unwatch: %v", err)
	}
	if got := s.Watching("sandy", at); got == nil {
		t.Error("closing a pane on one agent dropped the watch on another")
	}
}

func TestANarrowRoundKeepsEverythingItDidNotCarry(t *testing.T) {
	// The one that would be catastrophic and silent.
	s := open(t)
	if err := s.PutSnapshot(mirrored("sandy"), "cq/test", at); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	later := at.Add(3 * time.Second)
	if _, err := s.PutSession("sandy", protocol.FleetSession{
		Identity: "ember", Live: true, Turn: 4,
	}, later); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	snap, _, err := s.Snapshot("sandy")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Inbox) != 1 {
		t.Errorf("the mailbox has %d messages after a narrow round, want 1", len(snap.Inbox))
	}
	if snap.Fleet == nil || len(snap.Fleet.Identities) != 1 {
		t.Error("the fleet's identities did not survive a narrow round")
	}
	if snap.Fleet == nil || len(snap.Fleet.Sessions) != 1 || snap.Fleet.Sessions[0].Turn != 4 {
		t.Errorf("the session was not replaced: %+v", snap.Fleet)
	}
}

func TestANarrowRoundDoesNotMoveTheMirrorsClock(t *testing.T) {
	// LastSync means "how old everything you are looking at is". A narrow round
	// refreshed one transcript, so moving it would make the site claim a
	// five-minute-old mailbox was seconds old.
	s := open(t)
	if err := s.PutSnapshot(mirrored("sandy"), "cq/test", at); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	later := at.Add(5 * time.Minute)
	if _, err := s.PutSession("sandy", protocol.FleetSession{Identity: "ember", Live: true}, later); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	_, meta, err := s.Snapshot("sandy")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !meta.LastSync.Equal(at) {
		t.Errorf("last sync moved to %s on a narrow round; the mirror is still from %s",
			meta.LastSync, at)
	}
}

func TestAWatchedSessionCarriesItsOwnAge(t *testing.T) {
	// The other half of the same fact: the transcript is genuinely newer than the
	// mirror, and the screen can only say so if it is told.
	s := open(t)
	if err := s.PutSnapshot(mirrored("sandy"), "cq/test", at); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	later := at.Add(5 * time.Minute)
	if _, err := s.PutSession("sandy", protocol.FleetSession{Identity: "ember", Live: true}, later); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	snap, _, err := s.Snapshot("sandy")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got, err := time.Parse(time.RFC3339, snap.Fleet.Sessions[0].At)
	if err != nil {
		t.Fatalf("the session carries %q, which is not a time: %v", snap.Fleet.Sessions[0].At, err)
	}
	if !got.Equal(later) {
		t.Errorf("the session says %s, want %s", got, later)
	}
}

func TestAnAgentThatStoppedComesOffTheScreen(t *testing.T) {
	// Left in place, the last transcript would be a live-looking conversation with
	// a process that is not there — on the one screen built never to do that.
	s := open(t)
	if err := s.PutSnapshot(mirrored("sandy"), "cq/test", at); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	if _, err := s.DropSession("sandy", "ember"); err != nil {
		t.Fatalf("DropSession: %v", err)
	}
	snap, _, err := s.Snapshot("sandy")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Fleet.Sessions) != 0 {
		t.Errorf("a stopped agent still has a session: %+v", snap.Fleet.Sessions)
	}
	if len(snap.Inbox) != 1 {
		t.Error("dropping a session took the mailbox with it")
	}
}

func TestANarrowRoundForAMachineThatHasNeverSyncedInventsNothing(t *testing.T) {
	// A snapshot built from one session would put a machine on the site with no
	// mail and no tasks, which reads exactly like a machine that has lost both.
	s := open(t)
	if _, err := s.PutSession("nowhere", protocol.FleetSession{Identity: "ember"}, at); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if _, _, err := s.Snapshot("nowhere"); err == nil {
		t.Error("a machine that never synced now has a snapshot")
	}
}

func TestARoundThatChangedNothingSaysSo(t *testing.T) {
	// Most narrow rounds find an agent mid-thought and carry back what was already
	// there. Reporting a change would wake every open browser into a full refetch —
	// the mail, the archive, the tasks, the repository — three times a minute to
	// redraw a transcript that did not move.
	s := open(t)
	if err := s.PutSnapshot(mirrored("sandy"), "cq/test", at); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	same := protocol.FleetSession{Identity: "ember", Live: true, Turn: 3}
	if changed, err := s.PutSession("sandy", same, at.Add(3*time.Second)); err != nil {
		t.Fatalf("PutSession: %v", err)
	} else if changed {
		t.Error("an unchanged session was reported as news")
	}
	if changed, err := s.PutSession("sandy", protocol.FleetSession{
		Identity: "ember", Live: true, Turn: 4,
	}, at.Add(6*time.Second)); err != nil {
		t.Fatalf("PutSession: %v", err)
	} else if !changed {
		t.Error("a turn that advanced was not reported as news")
	}
}

func TestAnUnreadableWatchIsNobodyWatching(t *testing.T) {
	// The safe direction: a pane that goes cold and a reader who reloads, rather
	// than a machine spinning for a file nothing can parse.
	s := open(t)
	if got := s.Watching("sandy", at); got != nil {
		t.Errorf("a machine with no watch file is being watched: %+v", *got)
	}
}
