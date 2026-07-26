//go:build !unix

package store

import "os"

// On platforms without flock, Mailman degrades to no cross-process locking
// rather than to a lock file that would be worse than none.
//
// A lock file whose existence is the lock has to be reaped when its owner dies,
// and reaping it wrongly corrupts exactly the data the lock was protecting.
// Within one process the mutex in Store still serialises writers, and the
// append-only journal design means the damage a lost race can do is bounded to
// a duplicated puid rather than a lost message. `mailman verify` reports that
// if it happens.
//
// Orc targets unix, so this path is a compile-time courtesy, not a supported
// configuration.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }

func tryLockFile(*os.File) (bool, error) { return true, nil }
