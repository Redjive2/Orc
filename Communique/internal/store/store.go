// Package store holds the server's state on disk.
//
// Two shapes live here, and they have opposite rules. A **snapshot** is replaced
// wholesale on every sync, so it needs no merge and a half-written one is
// impossible. The **queue** is one file per action, so an append needs no lock,
// a collection is a read, and a completion is a rewrite of one small file.
//
// Everything is written through package atomic, so a reader — `cq serve` on one
// side, `cq admin` on the other — sees a whole file or the previous one.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// Version is the on-disk format version. An unknown one is a hard, clear error
// rather than a hopeful read.
const Version = 1

// File modes. The store holds everyone's mail and a password digest, so it is
// private to its owner.
const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// State is where a queued action has got to.
type State string

const (
	// Queued means the action is waiting for a sync to collect it.
	Queued State = "queued"
	// Sent means a sync took it and has not yet reported a result. It is
	// re-delivered on the next sync: delivery is at-least-once on the wire, and
	// the agent makes it exactly-once in effect by action id.
	Sent State = "sent"
	// Done means the agent applied it.
	Done State = "done"
	// Failed means the agent refused it, and Error says why. Nothing happened,
	// so it is safe to try again.
	Failed State = "failed"
	// InDoubt means the agent began applying it and never recorded the end. It
	// may or may not have happened.
	//
	// Kept apart from Failed because the two demand opposite responses: a
	// refusal can simply be retried, and this cannot be retried blindly — the
	// message may already be in somebody's inbox.
	InDoubt State = "in_doubt"
)

// Valid reports whether s is one of the defined states.
func (s State) Valid() bool {
	switch s {
	case Queued, Sent, Done, Failed, InDoubt:
		return true
	default:
		return false
	}
}

// Pending reports whether an action in this state should still be delivered.
func (s State) Pending() bool { return s == Queued || s == Sent }

// Settled reports whether an action has reached its end, one way or another.
func (s State) Settled() bool { return s == Done || s == Failed || s == InDoubt }

// Unresolved reports whether the action ended without being applied — refused,
// or in doubt. These are the states that carry a reason, and the only ones the
// operator can still act on.
func (s State) Unresolved() bool { return s == Failed || s == InDoubt }

// Entry is one queued action and what has become of it.
type Entry struct {
	Action protocol.Action `json:"action"`
	State  State           `json:"state"`
	// Both times are pointers because `omitempty` does nothing for a
	// time.Time: a struct is never "empty" to encoding/json, so a queued action
	// went out over the wire claiming it had been sent and completed in the
	// year 1. Absent is absent.
	SentAt    *time.Time `json:"sent_at,omitempty"`
	Completed *time.Time `json:"completed,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Unresolved reports whether this entry's action ended without being applied.
func (e Entry) Unresolved() bool { return e.State.Unresolved() }

// Validate checks the entry is internally consistent, so a corrupt file is
// reported rather than shown to the user as a half-sensible row.
func (e Entry) Validate() error {
	if err := e.Action.Validate(); err != nil {
		return err
	}
	if !e.State.Valid() {
		return fault.Field("Entry", "state", "unknown state %q", string(e.State))
	}
	if e.Unresolved() && e.Error == "" {
		return fault.Field("Entry", "error", "a %s action carries no reason", e.State)
	}
	if !e.Unresolved() && e.Error != "" {
		return fault.Field("Entry", "error", "a %s action carries an error message", e.State)
	}
	if (e.State == Done || e.Unresolved()) && orZero(e.Completed).IsZero() {
		return fault.Field("Entry", "completed", "a %s action carries no completion time", e.State)
	}
	return nil
}

// normalised turns an explicit zero time into an absent one.
//
// Files written before these fields were optional hold `"sent_at":"0001-01-01…"`.
// Tolerating that in the logic is not enough: the entry is re-encoded and served
// to the browser, so a stale zero would keep telling the reader that an action
// waiting to go out was sent two thousand years ago.
func (e Entry) normalised() Entry {
	if orZero(e.SentAt).IsZero() {
		e.SentAt = nil
	}
	if orZero(e.Completed).IsZero() {
		e.Completed = nil
	}
	return e
}

// orZero reads an optional time, treating absent and zero alike: a file written
// before these fields were optional holds an explicit zero, and it means the
// same thing as nothing at all.
func orZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Meta is what the server knows about a machine besides its snapshot.
type Meta struct {
	Machine  protocol.MachineID `json:"machine"`
	LastSync time.Time          `json:"last_sync"`
	Agent    string             `json:"agent,omitempty"`
	Protocol int                `json:"protocol"`
}

// Store is the server's state directory. It is safe for concurrent use.
type Store struct {
	root string

	// queue guards the queue directory: allocating a sequence number, handing
	// actions to a sync, and removing one. Two goroutines enqueuing at once would
	// otherwise pick the same number, and — the reason this grew past allocation —
	// a cancel could read an action as "waiting" while a sync was collecting it,
	// then delete something the agent had already been handed.
	//
	// The O_EXCL create below is the backstop for two *processes*, which the
	// design does not expect but does not silently corrupt either.
	queue sync.Mutex
}

// Open prepares a store at root, creating it if needed.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, fault.Usage{Reason: "empty store path"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fault.IO{Op: "resolve", Subject: root, Err: err}
	}

	s := &Store{root: abs}
	for _, dir := range []string{"", "machines", "queue", "tokens", "sessions"} {
		if err := atomic.MkdirAll(filepath.Join(abs, dir), dirMode); err != nil {
			return nil, err
		}
	}
	if err := s.checkVersion(); err != nil {
		return nil, err
	}
	return s, nil
}

// Root returns the store's directory.
func (s *Store) Root() string { return s.root }

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.root}, parts...)...)
}

// checkVersion writes the version on a fresh store and refuses an unknown one.
func (s *Store) checkVersion() error {
	path := s.path("version")
	data, err := atomic.ReadFile(path)
	switch {
	case err == nil:
		got := strings.TrimSpace(string(data))
		n, convErr := strconv.Atoi(got)
		if convErr != nil {
			return fault.Parse{Where: path, Reason: fmt.Sprintf("version %q is not a number", got)}
		}
		if n != Version {
			return fault.Parse{Where: path,
				Reason: fmt.Sprintf("store is format version %d; this build reads version %d", n, Version)}
		}
		return nil
	case fault.Classify(err) == fault.CodeNotFound:
		return atomic.WriteFile(path, []byte(strconv.Itoa(Version)+"\n"), fileMode)
	default:
		return err
	}
}

// --- snapshots -----------------------------------------------------------

// PutSnapshot replaces a machine's snapshot and metadata.
//
// The snapshot is validated before anything is written: a store that accepted
// what the wire refuses would be a second, laxer definition of the format.
func (s *Store) PutSnapshot(snap protocol.Snapshot, agent string, at time.Time) error {
	if err := snap.Validate(); err != nil {
		return err
	}
	if at.IsZero() {
		return fault.Internal{Where: "store.PutSnapshot", Detail: "zero timestamp"}
	}

	dir := s.path("machines", string(snap.Machine))
	if err := atomic.MkdirAll(dir, dirMode); err != nil {
		return err
	}

	// The snapshot lands first. If the process dies between the two writes, the
	// metadata is merely stale — the wrong way round would advertise a sync that
	// left no data.
	if err := atomic.WriteJSON(filepath.Join(dir, "snapshot.json"), snap, fileMode); err != nil {
		return err
	}
	meta := Meta{Machine: snap.Machine, LastSync: at, Agent: agent, Protocol: protocol.Version}
	return atomic.WriteJSON(filepath.Join(dir, "meta.json"), meta, fileMode)
}

// Snapshot returns a machine's last snapshot and metadata.
func (s *Store) Snapshot(machine protocol.MachineID) (protocol.Snapshot, Meta, error) {
	if err := machine.Validate(); err != nil {
		return protocol.Snapshot{}, Meta{}, err
	}
	dir := s.path("machines", string(machine))

	var snap protocol.Snapshot
	if err := atomic.ReadJSON(filepath.Join(dir, "snapshot.json"), &snap); err != nil {
		if fault.Classify(err) == fault.CodeNotFound {
			return protocol.Snapshot{}, Meta{}, fault.NotFound{What: "machine", Name: string(machine)}
		}
		return protocol.Snapshot{}, Meta{}, err
	}
	if err := snap.Validate(); err != nil {
		return protocol.Snapshot{}, Meta{}, err
	}

	var meta Meta
	if err := atomic.ReadJSON(filepath.Join(dir, "meta.json"), &meta); err != nil {
		if fault.Classify(err) != fault.CodeNotFound {
			return protocol.Snapshot{}, Meta{}, err
		}
		// A snapshot with no metadata is the crash window in PutSnapshot. The
		// data is sound; only the sync time is unknown.
		meta = Meta{Machine: machine, Protocol: protocol.Version}
	}
	return snap, meta, nil
}

// Machines lists every machine that has ever synced, in a stable order.
func (s *Store) Machines() ([]protocol.MachineID, error) {
	entries, err := os.ReadDir(s.path("machines"))
	if err != nil {
		return nil, fault.IO{Op: "list", Subject: s.path("machines"), Err: err}
	}
	var out []protocol.MachineID
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := protocol.MachineID(e.Name())
		// A directory whose name is not a valid machine id was not written by
		// cq; skipping it beats failing every listing because something else
		// left a file in the store.
		if id.Validate() != nil {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// --- the queue -----------------------------------------------------------

// Enqueue records an action for a machine and returns it with its identity.
func (s *Store) Enqueue(machine protocol.MachineID, op protocol.Op, args protocol.Args, at time.Time) (protocol.Action, error) {
	if at.IsZero() {
		return protocol.Action{}, fault.Internal{Where: "store.Enqueue", Detail: "zero timestamp"}
	}

	s.queue.Lock()
	defer s.queue.Unlock()

	entries, err := s.entries()
	if err != nil {
		return protocol.Action{}, err
	}
	next := uint64(1)
	for _, e := range entries {
		if e.Action.Seq >= next {
			next = e.Action.Seq + 1
		}
	}

	id, err := newActionID()
	if err != nil {
		return protocol.Action{}, err
	}
	action := protocol.Action{ID: id, Seq: next, Machine: machine, Op: op, Args: args, Queued: at}
	if err := action.Validate(); err != nil {
		return protocol.Action{}, err
	}

	entry := Entry{Action: action, State: Queued}
	// O_EXCL, so two processes racing for the same sequence produce a conflict
	// rather than one silently overwriting the other's action.
	if err := atomic.CreateJSON(s.queuePath(next), entry, fileMode); err != nil {
		return protocol.Action{}, err
	}
	return action, nil
}

func (s *Store) queuePath(seq uint64) string {
	return s.path("queue", fmt.Sprintf("%020d.json", seq))
}

// entries reads the whole queue, in sequence order.
func (s *Store) entries() ([]Entry, error) {
	dir := s.path("queue")
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, fault.IO{Op: "list", Subject: dir, Err: err}
	}

	var out []Entry
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".json") {
			continue
		}
		var e Entry
		if err := atomic.ReadJSON(filepath.Join(dir, n.Name()), &e); err != nil {
			return nil, err
		}
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", n.Name(), err)
		}
		out = append(out, e.normalised())
	}
	slices.SortFunc(out, func(a, b Entry) int {
		switch {
		case a.Action.Seq < b.Action.Seq:
			return -1
		case a.Action.Seq > b.Action.Seq:
			return 1
		default:
			return 0
		}
	})
	return out, nil
}

// Queue returns every entry, newest last. It is what the UI reads to show what
// is waiting, what is in flight, and what failed.
func (s *Store) Queue() ([]Entry, error) { return s.entries() }

// Pending returns the actions a machine should apply, in sequence order.
//
// Actions already sent but not yet reported are included: the agent skips ones
// it has applied, so re-delivery costs nothing and a sync lost in transit does
// not strand the user's reply forever.
func (s *Store) Pending(machine protocol.MachineID) ([]protocol.Action, error) {
	if err := machine.Validate(); err != nil {
		return nil, err
	}
	entries, err := s.entries()
	if err != nil {
		return nil, err
	}
	var out []protocol.Action
	for _, e := range entries {
		if e.Action.Machine == machine && e.State.Pending() {
			out = append(out, e.Action)
		}
	}
	return out, nil
}

// MarkSent records that a sync has taken these actions.
func (s *Store) MarkSent(ids []protocol.ActionID, at time.Time) error {
	if at.IsZero() {
		return fault.Internal{Where: "store.MarkSent", Detail: "zero timestamp"}
	}
	// Under the queue lock, so that collecting an action and cancelling one
	// cannot interleave. A cancel that read "waiting" a moment before this ran
	// would otherwise delete an action the agent is being handed.
	s.queue.Lock()
	defer s.queue.Unlock()

	return s.update(ids, func(e *Entry) bool {
		// A completed action is not walked back to sent by a re-delivery.
		if e.State != Queued {
			return false
		}
		e.State = Sent
		e.SentAt = &at
		return true
	})
}

// Complete records what the agent made of each action.
//
// A result for an action the store does not have is not an error: the queue may
// have been pruned since, and refusing the whole sync over a stale result would
// strand every other action in the batch.
func (s *Store) Complete(results []protocol.Result) error {
	for _, r := range results {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	byID := make(map[protocol.ActionID]protocol.Result, len(results))
	ids := make([]protocol.ActionID, 0, len(results))
	for _, r := range results {
		byID[r.ActionID] = r
		ids = append(ids, r.ActionID)
	}
	return s.update(ids, func(e *Entry) bool {
		r, ok := byID[e.Action.ID]
		if !ok {
			return false
		}
		if e.State.Settled() {
			return false // already settled; a repeated report changes nothing
		}
		e.Completed = &r.At
		switch {
		case r.OK:
			e.State, e.Error = Done, ""
		case r.InDoubt:
			e.State, e.Error = InDoubt, r.Error
		default:
			e.State, e.Error = Failed, r.Error
		}
		return true
	})
}

// update applies a change to the named entries and writes back the ones that
// actually changed. The mutator reports whether it did anything, because an
// Entry holds slices and so cannot be compared for equality.
func (s *Store) update(ids []protocol.ActionID, apply func(*Entry) bool) error {
	if len(ids) == 0 {
		return nil
	}
	wanted := make(map[protocol.ActionID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}

	entries, err := s.entries()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, ok := wanted[e.Action.ID]; !ok {
			continue
		}
		if !apply(&e) {
			continue
		}
		if err := e.Validate(); err != nil {
			return err
		}
		if err := atomic.WriteJSON(s.queuePath(e.Action.Seq), e, fileMode); err != nil {
			return err
		}
	}
	return nil
}

// Prune deletes settled entries completed before the given time, and reports how
// many went. Entries still queued or in flight are never pruned, whatever their
// age: an action that has not been applied is still the user's words.
func (s *Store) Prune(before time.Time) (int, error) {
	entries, err := s.entries()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		done := orZero(e.Completed)
		if e.State.Pending() || done.IsZero() || !done.Before(before) {
			continue
		}
		if err := atomic.Remove(s.queuePath(e.Action.Seq)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// newActionID mints a 32-character hex identifier from the system's randomness.
func newActionID() (protocol.ActionID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.IO{Op: "read", Subject: "system randomness", Err: err}
	}
	return protocol.ActionID(hex.EncodeToString(b[:])), nil
}
