package settings

import (
	"path/filepath"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
)

// What this machine has been told from the website.
//
// Everything else in the agent's home is a record of what happened —
// `applied.jsonl` is what was done, `cursor.json` is how far it got. This is the
// one file that is an *instruction*: settings an operator chose from the browser,
// which the next round of `cq sync` reads and obeys.
//
// It exists because the alternative does not work. A watcher's settings arrive as
// environment variables and command-line flags, both of which are fixed at the
// moment it launches — `watch.Spawn` hands the child `os.Environ()`, so a variable
// exported in a shell an hour later reaches nothing. A machine mirroring the wrong
// directory could only be corrected by finding the terminal it was started from,
// which on a headless server is the whole problem the website exists to solve.
//
// So: a small file, read between rounds. It holds only what somebody may change
// from a browser, and it is not a cache of anything — a value absent here means
// "nobody has said", not "not known yet", and the flag or the environment answers
// instead.
type Settings struct {
	// Library is the repository this machine mirrors for reading, and the only
	// directory the library verbs may write. Empty means nobody has chosen one
	// from the website and $CQ_LIBRARY still decides.
	Library string `json:"library,omitempty"`
}

// settingsName is the file, beside the journal and the cursor.
const settingsName = "settings.json"

// Path is where a home keeps them, exported so a refusal can name the file
// rather than describing it.
func Path(home string) string { return filepath.Join(home, settingsName) }

// Read returns what this machine has been told, or the zero value.
//
// A home with no settings file is the ordinary case — every machine starts that
// way and most stay that way — so it is not an error. Anything else is: a file
// that exists and cannot be parsed means somebody's choice is on disk and not
// being honoured, and quietly falling back to the environment would mirror the
// wrong directory while showing no sign of it.
func Read(home string) (Settings, error) {
	var s Settings
	err := atomic.ReadJSON(Path(home), &s)
	if err != nil && fault.Classify(err) == fault.CodeNotFound {
		return Settings{}, nil
	}
	return s, err
}

// Write records what this machine has been told.
//
// 0600, like the cursor: it names a path on this machine, and the home may sit in
// a directory other accounts can list.
func Write(home string, s Settings) error {
	return atomic.WriteJSON(Path(home), s, 0o600)
}
