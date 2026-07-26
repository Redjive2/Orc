package store

import (
	"errors"
	"os"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// Acting on an action that did not work: trying it again, or letting it go.
//
// Both exist because the alternative was a dead end. An action the agent refused
// used to sit in the queue marked "failed" for ever, counted in the status bar,
// with nothing anywhere that could clear it or make it happen.

// Retry queues a fresh copy of an action that did not work.
//
// A *fresh* copy, with a new identifier, and that is the whole design. The agent
// keeps a journal of every action id it has touched and skips the ones it
// recognises — that is what makes delivery exactly-once in effect — so putting
// the same id back in the queue would produce an action that is collected, sent,
// and then deliberately ignored. It would look like a retry and never be one.
//
// The old entry is left exactly as it was. It is the record that this was tried
// and refused, and overwriting history to make the queue tidier would lose the
// only evidence of what happened.
func (s *Store) Retry(id protocol.ActionID, at time.Time) (protocol.Action, error) {
	if err := id.Validate(); err != nil {
		return protocol.Action{}, err
	}
	entry, err := s.Entry(id)
	if err != nil {
		return protocol.Action{}, err
	}

	if err := retryable(entry); err != nil {
		return protocol.Action{}, err
	}
	return s.Enqueue(entry.Action.Machine, entry.Action.Op, entry.Action.Args, at)
}

// retryable says whether an entry may be tried again, and why not when it may
// not.
//
// The interesting case is an action in doubt. It was started and its end was
// never recorded, so it may already have happened — and whether that matters
// depends entirely on the verb. Marking mail read twice is marking it read;
// sending twice is a second message to a real person who will read it twice.
//
// So idempotent operations may be retried and the two that are not may not. The
// refusal says what to check, because the operator can settle the question in
// one command and cq cannot settle it at all.
func retryable(e Entry) error {
	switch {
	case e.State == Failed:
		return nil

	case e.State == InDoubt:
		if e.Action.Op.Idempotent() {
			return nil
		}
		// A library verb carries the digest of what it expected to find, so a
		// second application refuses rather than repeating. That makes it safe to
		// offer even in doubt — which matters, because "it may or may not have
		// written the file" is precisely when somebody wants to try again and
		// have the machine decide.
		if e.Action.Op.TouchesLibrary() && e.Action.Args.Base != "" {
			return nil
		}
		return fault.Conflict{Reason: "this " + string(e.Action.Op) +
			" was interrupted and may already have been delivered, so cq will not repeat it.\n" +
			"  check your sent mail; if it never arrived, write it again"}

	case e.State.Pending():
		return fault.Conflict{Reason: "this action has not been tried yet"}

	default: // Done
		return fault.Conflict{Reason: "this action already worked"}
	}
}

// Drop removes one settled action from the queue.
//
// Only a settled one. An action still waiting to be collected, or collected and
// not yet reported on, may be in flight at the agent this second; deleting it
// here would leave the agent applying something the server has forgotten, and
// then reporting a result for an action that no longer exists.
//
// Dropping what is already gone is quiet. The state asked for is the state in
// place, and two browser tabs showing the same failed action must not turn the
// second click into an error about something that already happened.
func (s *Store) Drop(id protocol.ActionID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	entry, err := s.Entry(id)
	if err != nil {
		if errors.Is(err, fault.ErrNotFound) {
			return nil
		}
		return err
	}
	if !entry.State.Settled() {
		return fault.Conflict{Reason: "this action is still in flight; it can be dropped once the agent has reported on it"}
	}

	// The same lock Enqueue takes, so a drop cannot race a sequence number being
	// chosen and land on a file another action is about to create.
	s.seq.Lock()
	defer s.seq.Unlock()

	path := s.queuePath(entry.Action.Seq)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// A missing file is the end state that was asked for: somebody else
		// dropped it first.
		return fault.IO{Op: "remove", Subject: "queue entry", Err: err}
	}
	return nil
}

// Entry finds one queued action by id.
func (s *Store) Entry(id protocol.ActionID) (Entry, error) {
	entries, err := s.entries()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.Action.ID == id {
			return e, nil
		}
	}
	return Entry{}, fault.NotFound{What: "action", Name: string(id)}
}
