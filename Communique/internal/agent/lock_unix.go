//go:build unix

package agent

import (
	"os"
	"syscall"

	"orc/cq/internal/fault"
)

// lock is an advisory lock held for the duration of a sync.
//
// flock is used rather than a lock file with a pid in it, because the kernel
// releases it when the process dies. That removes stale-lock handling
// entirely — and a stale lock on a sync would mean the mirror silently stopped
// updating until someone noticed and deleted a file.
type lock struct {
	file *os.File
}

// tryLock takes the lock without waiting. It reports whether it was acquired;
// not acquiring it is an ordinary outcome, not a failure.
func tryLock(path string) (*lock, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fault.IO{Op: "open the lock", Subject: path, Err: err}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, fault.IO{Op: "lock", Subject: path, Err: err}
	}
	return &lock{file: f}, true, nil
}

// release drops the lock. The file is left in place: creating and deleting it
// would race with another process opening it.
func (l *lock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fault.IO{Op: "unlock", Subject: "the sync lock", Err: err}
	}
	if closeErr != nil {
		return fault.IO{Op: "close the lock", Subject: "the sync lock", Err: closeErr}
	}
	return nil
}
