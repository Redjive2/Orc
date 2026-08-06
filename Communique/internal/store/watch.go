package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"

	"orc/cq/internal/protocol"
)

// Watching one agent, so its session comes back every few seconds instead of
// every few minutes.
//
// The whole of cq is built on the server never reaching an agent machine, which
// means a browser cannot ask a session for its latest state — it can only ask the
// server to *want* it, and wait for the machine to come round. What is here is
// that wanting, written down.
//
// **It is a lease and not a switch, and that is the entire safety argument.** A
// closed tab, a phone that went to sleep, a browser killed mid-pane and a laptop
// carried out of range are all the same event from the server's side: nothing
// arrives. A switch would leave the machine syncing every three seconds for ever
// after any one of them. A lease that has to be renewed decays back to the
// ordinary cadence on its own, and the worst a lost browser can cost is one
// lease's worth of fast rounds.

// WatchLease is how long a taken watch lasts before it has to be renewed.
//
// Long enough that a renewal can be missed — a slow request, a phone that paused a
// timer for a moment — without the pane going cold in front of somebody who is
// still looking at it. Short enough that a browser which vanished stops costing
// anything within a minute.
const WatchLease = 45 * time.Second

// WatchFloor is the fastest a watch may ask a machine to come back.
//
// A floor rather than a warning, because the cost lands on a machine nobody is
// looking at: the agent shells out to `orc view` on every narrow round, and a
// browser that asked for 100ms would put a fleet's machine into a spin on behalf
// of one pane. Two seconds is faster than anybody can read and slower than
// anything here can hurt.
const WatchFloor = 2 * time.Second

// watched is one machine's lease, as it is stored.
type watched struct {
	Identity string    `json:"identity"`
	Every    string    `json:"every"`
	Until    time.Time `json:"until"`
}

func (s *Store) watchPath(machine protocol.MachineID) string {
	return filepath.Join(s.path("machines", string(machine)), "watch.json")
}

// Watch takes or renews the lease on one agent's session.
//
// Renewing is the same call as taking, deliberately: a browser that has been open
// for an hour and one that just opened want exactly the same thing, and a protocol
// where the second renewal is a different verb from the first is a protocol with a
// state the client has to track and can get wrong.
//
// Two operators on one agent is not a conflict — it is the same lease, pushed out
// twice. Two operators on *different* agents of one machine is the case worth
// naming: the second replaces the first, because a machine sends one session per
// narrow round and choosing to send both would double a cost that exists to be
// small. The one that lost is not left guessing: its session's timestamp stops
// advancing, and the screen turns that into a warning naming this as one of the
// two reasons — see staleness in session.js.
func (s *Store) Watch(machine protocol.MachineID, identity, every string, at time.Time) error {
	if err := machine.Validate(); err != nil {
		return err
	}
	want, err := time.ParseDuration(strings.TrimSpace(every))
	if err != nil {
		return fault.Field("Watch", "every", "%q is not a duration", every)
	}
	if want < WatchFloor {
		want = WatchFloor
	}
	// Checked as the thing that will be sent, after the floor rather than before.
	// What goes on the wire is what has to be sound, and validating the request and
	// then storing something else is how the two drift apart.
	lease := protocol.Watching{Identity: identity, Every: want.String()}
	if err := lease.Validate(); err != nil {
		return err
	}
	if at.IsZero() {
		return fault.Internal{Where: "store.Watch", Detail: "zero timestamp"}
	}

	dir := s.path("machines", string(machine))
	if err := atomic.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	return atomic.WriteJSON(s.watchPath(machine), watched{
		Identity: lease.Identity, Every: lease.Every, Until: at.Add(WatchLease),
	}, fileMode)
}

// Unwatch drops the lease, if it is the one the caller thinks it is.
//
// Leaving a pane says so rather than waiting to be timed out. The lease alone
// would be correct — it lapses either way — but a machine that keeps syncing every
// three seconds for the better part of a minute after somebody navigated away is
// work nobody is watching, and the browser knows the moment it happens.
//
// **Named, because a machine holds one lease and two people can want it.** A
// second operator who opened another agent and closed it again would otherwise
// drop the first one's watch: their pane would go cold with nothing on screen to
// say why, and the fix — reload — is not one anybody would guess at. Dropping a
// lease somebody else now holds is a no-op, and the loser of the race finds out
// the way they were always going to, from the transcript that stops moving.
func (s *Store) Unwatch(machine protocol.MachineID, identity string) error {
	if err := machine.Validate(); err != nil {
		return err
	}
	var got watched
	if err := atomic.ReadJSON(s.watchPath(machine), &got); err != nil {
		// Nothing there, or nothing readable. Either way there is no lease to drop
		// and saying so would make leaving a pane an error somebody has to read.
		return nil
	}
	if got.Identity != identity {
		return nil
	}
	return atomic.Remove(s.watchPath(machine))
}

// Watching is the live lease on a machine, or nil.
//
// Expiry is decided here on every read rather than by anything sweeping the store.
// A lapsed lease is simply one whose deadline has passed, so a server that was
// down for an hour comes back with nothing being watched — which is the truth,
// since no browser has said otherwise since.
//
// Unreadable reads as nobody watching. That is the safe direction: the cost of
// getting it wrong this way is a pane that goes cold and a reader who reloads, and
// the cost of the other way is a machine spinning for a file nobody can parse.
func (s *Store) Watching(machine protocol.MachineID, now time.Time) *protocol.Watching {
	if machine.Validate() != nil {
		return nil
	}
	var got watched
	if err := atomic.ReadJSON(s.watchPath(machine), &got); err != nil {
		return nil
	}
	if got.Until.IsZero() || !now.Before(got.Until) {
		return nil
	}
	want := protocol.Watching{Identity: got.Identity, Every: got.Every}
	if want.Validate() != nil {
		return nil
	}
	return &want
}

// DropSession removes one agent's session from the stored snapshot.
//
// What a narrow round does when it finds nothing running. Without it the last
// transcript an agent produced would stay on the screen after the agent died,
// under a timestamp that keeps advancing — a pane showing a live conversation
// with a process that is not there. The screen already has words for an agent
// with no session; this is what puts it in that state.
func (s *Store) DropSession(machine protocol.MachineID, identity string) (bool, error) {
	if err := machine.Validate(); err != nil {
		return false, err
	}
	dir := s.path("machines", string(machine))
	var snap protocol.Snapshot
	if err := atomic.ReadJSON(filepath.Join(dir, "snapshot.json"), &snap); err != nil {
		if fault.Classify(err) == fault.CodeNotFound {
			return false, nil
		}
		return false, err
	}
	if snap.Fleet == nil {
		return false, nil
	}
	kept := snap.Fleet.Sessions[:0]
	for _, got := range snap.Fleet.Sessions {
		if got.Identity != identity {
			kept = append(kept, got)
		}
	}
	if len(kept) == len(snap.Fleet.Sessions) {
		return false, nil
	}
	snap.Fleet.Sessions = kept
	return true, atomic.WriteJSON(filepath.Join(dir, "snapshot.json"), snap, fileMode)
}

// PutSession merges one agent's session into the stored snapshot.
//
// This is what a narrow round writes, and what it does *not* write is the point.
// A narrow snapshot carries no mail, no tasks and no repository, so storing it the
// ordinary way would destroy a whole machine's mirror every three seconds to keep
// one transcript current. Only the session moves.
//
// **The metadata is deliberately left alone.** `meta.json` holds LastSync, which
// the status bar reads as "how old everything you are looking at is". A narrow
// round refreshed one agent's transcript and nothing else, so moving that clock
// would have the site claim a five-minute-old mailbox was three seconds old. The
// session carries its own timestamp instead, and the screen shows the two ages
// separately because they are two different facts.
//
// A machine with no stored snapshot is not an error and not a write: there is
// nothing to merge into, and the first full round is moments away. Inventing a
// snapshot from a session would put a machine on the site with no mail and no
// tasks, which reads exactly like a machine that has lost both.
func (s *Store) PutSession(machine protocol.MachineID, got protocol.FleetSession, at time.Time) (bool, error) {
	if err := machine.Validate(); err != nil {
		return false, err
	}
	if err := got.Validate(); err != nil {
		return false, err
	}
	if at.IsZero() {
		return false, fault.Internal{Where: "store.PutSession", Detail: "zero timestamp"}
	}

	dir := s.path("machines", string(machine))
	var snap protocol.Snapshot
	if err := atomic.ReadJSON(filepath.Join(dir, "snapshot.json"), &snap); err != nil {
		if fault.Classify(err) == fault.CodeNotFound {
			return false, nil
		}
		return false, err
	}
	// A machine that has synced but runs no agents. Nothing to merge into, and
	// adding a fleet here would invent one from a single session.
	if snap.Fleet == nil {
		return false, nil
	}

	got.At = at.UTC().Format(time.RFC3339)
	replaced := false
	for i := range snap.Fleet.Sessions {
		if snap.Fleet.Sessions[i].Identity == got.Identity {
			// Most narrow rounds find an agent mid-thought and carry back exactly
			// what was there before. Saying so — rather than writing and announcing
			// a change — is what keeps a watched pane from waking every browser on
			// the site into a full refetch three times a minute for nothing.
			//
			// Compared with the timestamp set aside, because that is the one field
			// that always differs and never means anything changed.
			was := snap.Fleet.Sessions[i]
			was.At = got.At
			if reflect.DeepEqual(was, got) {
				return false, nil
			}
			snap.Fleet.Sessions[i] = got
			replaced = true
			break
		}
	}
	if !replaced {
		// An agent employed since the last full round. Its session is real and
		// worth showing; the identity list beside it will catch up on the next
		// full one, and the screen already says what an agent it cannot find looks
		// like.
		snap.Fleet.Sessions = append(snap.Fleet.Sessions, got)
	}

	return true, atomic.WriteJSON(filepath.Join(dir, "snapshot.json"), snap, fileMode)
}
