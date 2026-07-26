package agent

import (
	"context"
	"os"
	"path/filepath"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
)

// MaxCoalescedRounds bounds how many extra passes one nudge will make. A burst
// of commands should collapse into two syncs, not spin forever while more keep
// arriving.
const MaxCoalescedRounds = 1

// Nudge is what Mailman and Macmuffin call after every action.
//
// Four rules keep it from turning a mail tool into a fragile one, and each
// exists because the alternative is a way for those tools to get worse:
//
//   - It never blocks. If a sync is already running, this one leaves a marker
//     and returns; the running sync picks it up.
//   - It never fails its caller with anything but the truth. Mail was already
//     delivered when the nudge fires; the mirror being late is not a reason to
//     tell an agent its message failed. The caller is expected to ignore the
//     error and exit zero.
//   - It coalesces. Twenty commands in a burst produce two syncs, and the last
//     one always reflects the final state.
//   - It is never the only path. A dropped nudge is invisible by design, which
//     is why the timed sync exists.
//
// The returned bool reports whether this call actually synced.
func (a *Agent) Nudge(ctx context.Context) (Report, bool, error) {
	l, got, err := tryLock(a.lockPath())
	if err != nil {
		return Report{}, false, err
	}
	if !got {
		// Someone else is mid-sync. Leave a note so their run goes round once
		// more, and get out of the way.
		return Report{}, false, a.markPending()
	}
	defer func() {
		if err := l.release(); err != nil {
			a.log.Warn("could not release the sync lock", "error", err)
		}
	}()

	// This run supersedes any pending request that arrived before it started.
	if err := a.clearPending(); err != nil {
		return Report{}, false, err
	}

	report, err := a.Sync(ctx)
	if err != nil {
		return report, true, err
	}

	for range MaxCoalescedRounds {
		pending, err := a.pending()
		if err != nil {
			return report, true, err
		}
		if !pending {
			break
		}
		if err := a.clearPending(); err != nil {
			return report, true, err
		}
		again, err := a.Sync(ctx)
		if err != nil {
			return report, true, err
		}
		report = merge(report, again)
	}
	return report, true, nil
}

// merge folds a second round's counts into the first, so one nudge reports one
// total rather than the last pass only.
func merge(a, b Report) Report {
	return Report{
		Machine:   a.Machine,
		Sent:      a.Sent + b.Sent,
		Received:  a.Received + b.Received,
		Applied:   a.Applied + b.Applied,
		Failed:    a.Failed + b.Failed,
		Skipped:   a.Skipped + b.Skipped,
		Truncated: a.Truncated || b.Truncated,
	}
}

func (a *Agent) lockPath() string    { return filepath.Join(a.state, "sync.lock") }
func (a *Agent) pendingPath() string { return filepath.Join(a.state, "pending") }

func (a *Agent) markPending() error {
	return atomic.WriteFile(a.pendingPath(), []byte("1\n"), 0o600)
}

func (a *Agent) clearPending() error { return atomic.Remove(a.pendingPath()) }

func (a *Agent) pending() (bool, error) {
	_, err := os.Stat(a.pendingPath())
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fault.IO{Op: "check", Subject: a.pendingPath(), Err: err}
	}
}
