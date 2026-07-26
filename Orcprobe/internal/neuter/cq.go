package neuter

import (
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/fault"
)

// Communiqué's local state, from its plan §5.1: a version marker, an
// append-only record of applied actions, and a cursor holding the last
// successful sync watermark.
const (
	cursorFile  = "cursor.json"
	pendingFile = "pending"
	appliedFile = "applied.jsonl"
)

// communique resets the probe's sync state to "never synced".
//
// The distinction that matters here is between history and liveness.
// `applied.jsonl` is history: it says which of the user's actions have already
// been carried out, and losing it would make a probe misrepresent what happened.
// The cursor is liveness: it is a claim about a conversation with a server this
// probe will never speak to, and a sync that somehow ran while holding it would
// ask that server to carry on from a watermark taken in another world.
//
// The cursor is the fourth of the four independent stops in front of the
// network (plan §4.4), and the only one that lives in the copied state rather
// than in orcprobe's own behaviour.
func communique(s Spec, rep *Report) error {
	for _, name := range []string{cursorFile, pendingFile} {
		path := filepath.Join(s.CQDir, name)
		err := os.Remove(path)
		if err == nil {
			rep.add(ActDrop, "cq "+name, "this probe has never synced and never will")
			continue
		}
		if !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: path, Err: err}
		}
	}

	// Anything that looks like a credential is removed whatever it is called.
	// The documented layout holds none, but this is the store that talks to the
	// network, and a token found in a probe is a token that could leave it.
	entries, err := os.ReadDir(s.CQDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "list", Path: s.CQDir, Err: err}
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || !looksLikeSecret(name) {
			continue
		}
		path := filepath.Join(s.CQDir, e.Name())
		if err := os.RemoveAll(path); err != nil {
			return fault.IO{Op: "remove", Path: path, Err: err}
		}
		rep.add(ActDrop, "cq "+e.Name(), "looks like a credential; a probe authenticates to nothing")
	}
	return nil
}

func looksLikeSecret(name string) bool {
	for _, mark := range []string{"token", "secret", "password", "operator", "credential", "key"} {
		if strings.Contains(name, mark) {
			return true
		}
	}
	return false
}
