package neuter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/fault"
)

// Orc's layout, from Orc/internal/store/store.go and session.go.
const (
	orcIdentitiesDir = "identities"
	orcSessionDir    = "session"
	orcSessionFile   = "session.json"
	orcSocketFile    = "session.sock"
	orcSessionLog    = "log.jsonl"
)

// orcState takes away a probe's claim to be running anything.
//
// Orc's session state is the one part of its store that is not a journal, and
// its own comment says why: employment is a decision and belongs to history,
// while having a live session is a fact about a process. `session.json` names
// two pids and a socket — and in a probe, all three are lies. The pids belong to
// whatever the real machine is running now, or to nothing; the socket cannot
// be connected to; and `orc status` reading them would report a fleet at work
// inside a museum.
//
// So the claim goes and the history stays. `log.jsonl` is what the session did,
// which is exactly the thing a probe is for looking at.
func orcState(s Spec, r *Report) error {
	dir := filepath.Join(s.OrcDir, orcIdentitiesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "list", Path: dir, Err: err}
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := cutSession(s, r, e.Name()); err != nil {
			return err
		}
	}

	if r.Sessions > 0 || r.Sockets > 0 {
		r.add(ActDrop, "orc sessions", fmt.Sprintf(
			"%d session claim(s) and %d socket(s) removed; the pids in them belong to the real machine, "+
				"and nothing in a probe is running", r.Sessions, r.Sockets))
	}
	return nil
}

func cutSession(s Spec, r *Report, identity string) error {
	dir := filepath.Join(s.OrcDir, orcIdentitiesDir, identity, orcSessionDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // an identity that has never been employed
		}
		return fault.IO{Op: "list", Path: dir, Err: err}
	}

	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)

		switch {
		case name == orcSessionFile:
			if err := os.Remove(path); err != nil {
				return fault.IO{Op: "remove", Path: path, Err: err}
			}
			r.Sessions++

		case name == orcSocketFile || strings.HasSuffix(name, ".sock"):
			// A socket is not a regular file, so the copy dropped it already and
			// recorded that. This is the case where one arrived anyway — a
			// regular file left by something else, or a copy that ran on a
			// filesystem that made one. Either way a probe must not hold a path
			// that looks connectable.
			if err := os.RemoveAll(path); err != nil {
				return fault.IO{Op: "remove", Path: path, Err: err}
			}
			r.Sockets++

		case name == orcSessionLog:
			// Kept. What a session did is history, and history is the thing a
			// probe exists to read.

		default:
			// A lock, or something a newer Orc writes. Locks are advisory and
			// held by open file descriptors, so a stale one means nothing; and
			// removing a file this build does not recognise would be guessing at
			// another tool's state.
		}
	}
	return nil
}
