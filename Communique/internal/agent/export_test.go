package agent

import (
	"time"

	"orc/cq/internal/protocol"
)

// MarkReported records that the server took these results, without a sync
// having happened.
//
// It exists to simulate the one case the agent cannot otherwise be driven into:
// a response that reached the agent but whose results never settled on the
// server, so the same action is delivered again. That is the situation the
// journal's idempotence exists for, and it deserves a test.
func MarkReported(state string, at time.Time, ids ...protocol.ActionID) error {
	j, err := openJournal(state)
	if err != nil {
		return err
	}
	return j.append(event{Op: opReported, IDs: ids, At: at})
}

// PruneJournal exposes the journal's tidying step.
func PruneJournal(state string, before time.Time) error {
	j, err := openJournal(state)
	if err != nil {
		return err
	}
	return j.prune(before)
}

// JournalSize reports how many events the journal holds, so a test can see that
// pruning actually removed something.
func JournalSize(state string) (int, error) {
	j, err := openJournal(state)
	if err != nil {
		return 0, err
	}
	s, err := j.replay()
	if err != nil {
		return 0, err
	}
	return len(s.outcomes), nil
}
