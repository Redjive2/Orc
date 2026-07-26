//go:build !unix && !windows

package agent

import (
	"os"

	"orc/cq/internal/fault"
)

// lock is the portable fallback: an exclusively-created file.
//
// Unlike flock this does not release itself when the process dies, so a crash
// mid-sync leaves the lock behind, and `cq sync --force` is the way out. The
// unix and Windows builds each hold a lock the kernel drops for them, so this
// is only reached on a platform that is neither.
type lock struct {
	path string
}

func tryLock(path string) (*lock, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, false, nil
		}
		return nil, false, fault.IO{Op: "open the lock", Subject: path, Err: err}
	}
	if err := f.Close(); err != nil {
		return nil, false, fault.IO{Op: "close the lock", Subject: path, Err: err}
	}
	return &lock{path: path}, true, nil
}

func (l *lock) release() error {
	if l == nil || l.path == "" {
		return nil
	}
	path := l.path
	l.path = ""
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "release the lock", Subject: path, Err: err}
	}
	return nil
}
