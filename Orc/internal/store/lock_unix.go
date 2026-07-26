//go:build unix

package store

import (
	"os"
	"syscall"
)

// lockFileHandle takes an exclusive advisory lock on an open file.
//
// flock is the right primitive rather than a lock *file* whose existence is the
// lock: a process killed while holding a flock releases it when its descriptors
// close, whereas a stale lock file has to be reaped by guesswork about whether
// its owner is still alive. Orc's writers are short-lived agent processes that
// are killed routinely, so that difference decides whether the fleet deadlocks
// in ordinary use.
func lockFileHandle(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFileHandle releases the lock. Closing the file would release it too;
// this is explicit so the release is visible at the call site and testable.
func unlockFileHandle(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// tryLockFileHandle takes the lock without blocking, reporting whether it got it.
//
// It is what a session supervisor uses to be the only one: waiting would make a
// second supervisor sit there until the first died and then quietly take over,
// which is a much harder thing to reason about than a second one refusing to start.
func tryLockFileHandle(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK {
		return false, nil
	}
	return false, err
}
