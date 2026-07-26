//go:build unix

package store

import (
	"io/fs"
	"syscall"
)

// ownerUID reports which unix user owns a file.
//
// It is what makes the CLI's owner fallback conditional rather than assumed: the
// argument for reading the operator's key out of the keyring is that the caller
// could read it anyway, and that argument only holds if the caller is the user the
// directory belongs to.
func ownerUID(info fs.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
