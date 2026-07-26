//go:build !unix

package store

import "os"

// Without flock there is no advisory locking, so the store falls back to the
// process mutex alone. That is honest rather than silently unsafe: Orc runs on
// the machine the agents run on, every one of those is a unix, and a platform
// that got here would be told by `orc doctor` that concurrent writers are
// unguarded rather than being allowed to believe otherwise.
func lockFileHandle(*os.File) error { return nil }

func unlockFileHandle(*os.File) error { return nil }

// Without flock, a second supervisor cannot be refused, so this reports that it
// took the lock and `orc doctor` is what says the guarantee is absent.
func tryLockFileHandle(*os.File) (bool, error) { return true, nil }
