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
		// Every library verb looks before it acts: a write and a delete against
		// the digest they were given, a create against the file already being
		// there, a removal against the directory holding only what the operator
		// was shown. So a second application refuses or finishes rather than
		// repeating, and that makes all of them safe to offer even in doubt —
		// which matters, because "it may or may not have written the file" is
		// precisely when somebody wants to try again and have the machine decide.
		if e.Action.Op.TouchesLibrary() {
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

// Drop removes one action from the queue.
//
// It covers two things somebody means by "get rid of this", and the difference
// is only in what has already happened:
//
//   - **Cancelling.** An action still *waiting* has never left this machine. No
//     agent has seen it, nothing has been attempted, and removing it means it
//     simply never goes. There is nothing to be careful about.
//   - **Forgetting.** A settled action is a record of something that already
//     happened. Removing it discards the record, not the effect.
//
// The one state that refuses is `sent`: collected by a sync and not yet reported
// on, so it may be at the agent this second. Deleting it would leave the agent
// applying something the server has forgotten, and then reporting a result for an
// action that no longer exists.
//
// Dropping what is already gone is quiet. The state asked for is the state in
// place, and two browser tabs showing the same row must not turn the second click
// into an error about something that already happened.
func (s *Store) Drop(id protocol.ActionID) error {
	if err := id.Validate(); err != nil {
		return err
	}

	// Read and remove under one lock, and MarkSent takes the same one. Without
	// that, a cancel could see "waiting", a sync could collect the action, and
	// the removal would land on something the agent had already been handed —
	// which is the exact hazard the `sent` refusal below exists to prevent.
	s.queue.Lock()
	defer s.queue.Unlock()

	entry, err := s.Entry(id)
	if err != nil {
		if errors.Is(err, fault.ErrNotFound) {
			return nil
		}
		return err
	}
	if entry.State == Sent {
		return fault.Conflict{Reason: "this action has already gone to the agent, so it cannot be called back; " +
			"wait for it to report, then decide what to do about the result"}
	}

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
