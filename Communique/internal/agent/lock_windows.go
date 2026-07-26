package agent

import (
	"errors"
	"syscall"

	"orc/cq/internal/fault"
)

// lock is an advisory lock held for the duration of a sync.
//
// Windows has no flock, but it has the property that actually matters: a handle
// opened with no sharing is exclusive, and the kernel closes it when the process
// dies. That is what removes stale-lock handling entirely, exactly as flock does
// on unix — and a stale lock on a sync would mean the mirror quietly stopped
// updating until somebody noticed a file and deleted it.
//
// The portable fallback in lock_other.go cannot promise that, which is why
// Windows does not use it.
type lock struct {
	handle syscall.Handle
}

// What Windows returns when another process holds the file open exclusively.
// The standard library does not name it.
const errorSharingViolation = syscall.Errno(32)

// tryLock takes the lock without waiting. It reports whether it was acquired;
// not acquiring it is an ordinary outcome, not a failure.
func tryLock(path string) (*lock, bool, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, fault.IO{Op: "open the lock", Subject: path, Err: err}
	}
	handle, err := syscall.CreateFile(name,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // no sharing — this is the whole of the lock
		nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, false, nil
		}
		return nil, false, fault.IO{Op: "lock", Subject: path, Err: err}
	}
	return &lock{handle: handle}, true, nil
}

// release drops the lock. The file is left in place: creating and deleting it
// would race with another process opening it.
//
// A zero handle is the released state. CreateFile never hands out zero for a
// file, so it is free to mean "nothing held here".
func (l *lock) release() error {
	if l == nil || l.handle == 0 {
		return nil
	}
	handle := l.handle
	l.handle = 0
	if err := syscall.CloseHandle(handle); err != nil {
		return fault.IO{Op: "release the lock", Subject: "the sync lock", Err: err}
	}
	return nil
}
