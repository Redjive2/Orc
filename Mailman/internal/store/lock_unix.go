//go:build unix

package store

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on an open file.
//
// flock is the right primitive here rather than a lock *file* whose existence
// is the lock: a process killed while holding a flock releases it when its
// descriptors close, whereas a stale lock file has to be reaped by guesswork
// about whether its owner is still alive. Mailman's writers are short-lived
// agent processes that are killed routinely, so that difference decides whether
// the store deadlocks in ordinary use.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the lock. Closing the file would release it too; this is
// explicit so the release is visible at the call site and testable.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// tryLockFile takes the lock without blocking, reporting whether it got it.
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK {
		return false, nil
	}
	return false, err
}
