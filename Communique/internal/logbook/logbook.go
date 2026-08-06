// Package logbook keeps what the fleet's cycles have been saying.
//
// Until this existed, none of it was kept. `watch.Spawn` sends a detached
// watcher's streams to the null device — for a good reason, that a watcher
// writing to its parent's terminal would corrupt whatever that parent was
// printing — and the consequence was that the three loops holding a fleet
// together ran with nobody able to see a word of it. A sync that had been
// failing for a day looked exactly like a sync that had never started.
//
// So each cycle writes here as well, and the tail rides to the server with the
// rest of the mirror. Three properties matter and each is deliberate:
//
//   - **Bounded, always.** This is an unattended file on somebody's machine. A log
//     that grows without limit is not a diagnostic, it is a disk that fills up in
//     a month nobody was watching.
//   - **Append-only within a round.** Two processes can hold the same cycle open
//     across a restart, and an O_APPEND write is the one thing that stays sane
//     when they overlap.
//   - **Plain text, no escapes.** The logger is slog's text handler, which paints
//     nothing. A browser rendering these does its own colouring from the level,
//     rather than decoding terminal escapes it would have to sanitise first.
package logbook

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"orc/cq/internal/fault"
)

// Kind is one of the cycles that keeps a fleet alive.
type Kind string

const (
	Sync Kind = "sync"
	Wake Kind = "wake"
	Tend Kind = "tend"
)

// Kinds is every cycle, in the order a reader wants them: the mirror first,
// because a fleet whose sync has stopped has no other symptom worth reading.
var Kinds = []Kind{Sync, Wake, Tend}

// Valid reports whether a name is one of the cycles.
//
// A closed set, because these become file names and a path assembled from
// whatever a caller passed is how a log directory grows a `../`.
func Valid(k Kind) bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// MaxBytes is how large one cycle's log may grow before the oldest half goes.
//
// Half rather than all of it: truncating to nothing means the moment a log fills
// is the moment its evidence disappears, which is reliably the moment somebody
// needed it. Losing the older half keeps whatever was happening recently.
const MaxBytes = 256 << 10

// MaxTail bounds how many lines are read back.
//
// The tail rides in every snapshot, so this is a number paid on the network on a
// cadence rather than once. Enough to see a loop's last few rounds and what went
// wrong in them; not enough to be a reader for the whole file, which is what the
// file on the machine is for.
const MaxTail = 60

// Dir is where a machine's logs live.
func Dir(home string) string { return filepath.Join(home, "logs") }

// Path is one cycle's log.
func Path(home string, k Kind) string {
	return filepath.Join(Dir(home), string(k)+".log")
}

// Open returns a writer for one cycle, making the directory if it is missing.
//
// The caller closes it. It is opened append-only so that a watcher restarted into
// a new build continues the same log rather than truncating what the old one
// wrote — the restart is usually the thing being investigated.
func Open(home string, k Kind) (io.WriteCloser, error) {
	if !Valid(k) {
		return nil, fault.Internal{Where: "logbook.Open", Detail: fmt.Sprintf("unknown cycle %q", k)}
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		return nil, fault.IO{Op: "mkdir", Subject: Dir(home), Err: err}
	}
	path := Path(home, k)
	if err := trim(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fault.IO{Op: "open", Subject: path, Err: err}
	}
	return f, nil
}

// trim drops the older half of a log that has grown past the bound.
//
// Done when the file is opened rather than on every write: a cycle opens its log
// once per run, and a size check on each line would be a stat per log line for a
// bound that is reached once a month.
//
// A failure here is not a failure to log. The worst case is a file larger than
// intended, and refusing to start a watcher over it would trade the mirror for
// tidiness.
func trim(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= MaxBytes {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(info.Size()-MaxBytes/2, io.SeekStart); err != nil {
		return nil
	}
	kept, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	// The seek landed mid-line; that fragment is not a log line and writing it
	// back would put a half-sentence at the top of the file.
	if cut := strings.IndexByte(string(kept), '\n'); cut >= 0 {
		kept = kept[cut+1:]
	}
	// Written through a temporary and renamed, so a reader never sees the file
	// empty. A tail that came back blank would read as a cycle that has said
	// nothing, which is the one thing this must not be mistaken for.
	tmp := path + ".trimming"
	if err := os.WriteFile(tmp, kept, 0o600); err != nil {
		return nil
	}
	_ = os.Rename(tmp, path)
	return nil
}

// Line is one entry, with the level pulled out.
//
// The level is separated because the browser colours by it, and a browser that
// had to find it in the text would be a second parser of a format this one
// already knows. Empty when the line does not carry one — output from a child
// process, a panic, anything that is not slog's.
type Line struct {
	Level string `json:"level,omitempty"`
	Text  string `json:"text"`
}

// Tail reads the last lines of one cycle's log.
//
// An absent file is no lines and no error: a cycle that has never run has nothing
// to say, and that is a state to draw rather than a failure to report.
func Tail(home string, k Kind, n int) ([]Line, error) {
	if !Valid(k) {
		return nil, fault.Internal{Where: "logbook.Tail", Detail: fmt.Sprintf("unknown cycle %q", k)}
	}
	if n <= 0 || n > MaxTail {
		n = MaxTail
	}
	f, err := os.Open(Path(home, k))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "open", Subject: Path(home, k), Err: err}
	}
	defer func() { _ = f.Close() }()

	// Only the tail is read, and the whole bound is small, so the simple thing —
	// scan forward keeping a ring — costs less than seeking backwards costs to get
	// right.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 8<<10), 256<<10)
	ring := make([]Line, 0, n)
	for scanner.Scan() {
		text := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, Line{Level: levelOf(text), Text: text})
	}
	// A line longer than the buffer, or a read that failed part way. What was read
	// is still what the cycle said, and reporting nothing because the last line was
	// too long would lose the whole log to one bad entry.
	return ring, nil
}

// levelOf finds slog's level in a line, or empty.
//
// A scan for the token rather than a parse of the whole format: the text handler
// writes `time=… level=INFO msg=…`, and everything downstream of this only needs
// to know which of four words it was. Anything else — a child process's own
// output, a panic — has no level, which the screen draws as a plain line rather
// than guessing one.
func levelOf(line string) string {
	const key = "level="
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	if cut := strings.IndexAny(rest, " \t"); cut >= 0 {
		rest = rest[:cut]
	}
	switch rest {
	case "DEBUG", "INFO", "WARN", "ERROR":
		return rest
	}
	return ""
}
