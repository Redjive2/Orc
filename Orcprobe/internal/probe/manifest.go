package probe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
)

// Acts a manifest records. The vocabulary is small on purpose: a manifest is
// read by someone asking "is this probe a fair picture of the world, and what
// did orcprobe change on the way in?", and every line answers that question.
const (
	// ActCopy is state that came across.
	ActCopy = "copy"
	// ActSkip is a source that was not there to copy.
	ActSkip = "skip"
	// ActDrop is something deliberately left behind: a symlink out of the tree,
	// a git remote, a worktree link.
	ActDrop = "drop"
	// ActMint is a credential replaced with a probe-local one.
	ActMint = "mint"
	// ActStamp is a directory marked as part of the probe.
	ActStamp = "stamp"
	// ActDefer is a guarantee this build does not yet make. It is a manifest
	// entry rather than a comment in a plan because a probe must be able to say,
	// on its own, which of the tool's promises were true when it was made.
	ActDefer = "defer"
	// ActNote is anything else worth reading back.
	ActNote = "note"
)

// Entry is one manifest line.
type Entry struct {
	At     string `json:"at"`
	Act    string `json:"act"`
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
}

// Manifest appends to a probe's record of how it was made. It is append-only
// and flushed per line, so a creation that dies halfway still says how far it
// got — which is what makes an unfinished probe diagnosable rather than just
// broken.
type Manifest struct {
	path  string
	clock clock.Clock
}

// OpenManifest binds a manifest to a probe directory.
func OpenManifest(dir string, c clock.Clock) *Manifest {
	return &Manifest{path: dir + string(os.PathSeparator) + ManifestFile, clock: c}
}

// Add appends one entry.
func (m *Manifest) Add(act, what, detail string) error {
	if m == nil {
		return fault.Internal{Where: "probe.Manifest.Add", Detail: "no manifest"}
	}
	entry := Entry{At: clock.Format(m.clock.Now()), Act: act, What: what, Detail: detail}
	line, err := json.Marshal(entry)
	if err != nil {
		return fault.Internal{Where: "probe.Manifest.Add", Detail: err.Error()}
	}
	// A newline inside a value would split one entry into two lines, one of
	// which would not parse. Marshal escapes them, but the check is cheap and
	// the failure mode it prevents is a manifest nobody can read back.
	if bytes.ContainsRune(line, '\n') {
		return fault.Internal{Where: "probe.Manifest.Add", Detail: "encoded entry contains a newline"}
	}
	return appendLine(m.path, line)
}

// ReadManifest reads every entry.
//
// A truncated final line is an interrupted append and is dropped with the count
// returned, exactly as Mailman and Macmuffin treat their journals. An
// unparseable line anywhere else is corruption and a hard error — the same
// rule, for the same reason: the tail is the only place a crash can leave a
// partial write.
func ReadManifest(path string) ([]Entry, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fault.IO{Op: "read", Path: path, Err: err}
	}

	var (
		entries []Entry
		skipped int
	)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	complete := bytes.HasSuffix(data, []byte("\n"))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fault.IO{Op: "read", Path: path, Err: err}
	}

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		last := i == len(lines)-1
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if last && !complete {
				skipped = len(line)
				break
			}
			return nil, 0, fault.Parse{Path: path, Line: i + 1, Reason: "manifest entry: " + err.Error()}
		}
		entries = append(entries, e)
	}
	return entries, skipped, nil
}
