// Package activity reads what a session actually did, out of Claude's transcript.
//
// Two questions come out of one pass, because they are written on the same lines:
// what each turn *cost* — the token counters Claude records per assistant message —
// and what it *touched*, from the tool results recorded beside them. Reading the
// file twice to answer them separately would double the only expensive thing here.
//
// The transcript is Claude's file, not Orc's, and this follows internal/view's rules
// for it exactly:
//
//   - **Unknown fields are ignored.** Everywhere else in this tree an unknown field
//     is refused, because it means a newer Orc wrote a file an older one is
//     misreading. Here it means Claude shipped a release, and refusing would turn
//     every upgrade into an outage of the measurement.
//   - **Everything degrades and nothing fails.** A line that will not parse is
//     skipped and counted; a file that will not open is no reading at all. A missing
//     number is *absent*, never zero — a zero is a measurement, and there is a
//     difference between "it read nothing" and "nobody could tell".
//
// It is incremental by construction. A long session is megabytes and a rollup runs
// every few seconds, so a Cursor records where the last read stopped and the next
// one starts there.
package activity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
)

// MaxLine bounds one transcript line.
//
// A single line can be a whole file's contents — a Write's result carries what was
// written — so the scanner needs a large buffer, and a line past even this is
// skipped rather than allowed to define how much memory a rollup uses.
const MaxLine = 8 << 20

// Tokens is what a turn cost, kept as the four things Claude reports plus the web
// calls it made.
//
// They are separate because they differ by orders of magnitude and mean different
// things: on one real session, 2,582 input tokens against 616,670,401 cache reads.
// A single "tokens" figure would be a cache-read figure wearing a general name.
type Tokens struct {
	Input       int64 `json:"input,omitempty"`
	Output      int64 `json:"output,omitempty"`
	CacheCreate int64 `json:"cache_create,omitempty"`
	CacheRead   int64 `json:"cache_read,omitempty"`
	WebCalls    int64 `json:"web_calls,omitempty"`
}

// New is what the turn caused to be produced: everything but the cache reads.
//
// It is the figure worth putting in a headline, because it is the one that answers
// "how much work was done" rather than "how big was the context".
func (t Tokens) New() int64 { return t.Input + t.Output + t.CacheCreate }

// Add sums two sets of counters.
func (t *Tokens) Add(other Tokens) {
	t.Input += other.Input
	t.Output += other.Output
	t.CacheCreate += other.CacheCreate
	t.CacheRead += other.CacheRead
	t.WebCalls += other.WebCalls
}

// Files is what a turn touched, counted from the tool results.
//
// Calls and lines are both kept, and they answer different questions: forty edits of
// one line is a session working carefully, and one edit of four hundred is a session
// that rewrote a file. A count that merged them would hide both.
type Files struct {
	Reads      int   `json:"reads,omitempty"`
	Edits      int   `json:"edits,omitempty"`
	Writes     int   `json:"writes,omitempty"`
	ReadLines  int64 `json:"read_lines,omitempty"`
	Added      int64 `json:"added,omitempty"`
	Removed    int64 `json:"removed,omitempty"`
	WriteLines int64 `json:"write_lines,omitempty"`
	// Touched is how many distinct paths this bucket saw. It is distinct *within
	// the bucket* and not across a window: a rollup adds buckets, and adding two
	// distinct-counts over the same file would claim two files. A screen showing a
	// day says so.
	Touched int `json:"touched,omitempty"`
}

// Add sums two sets of file counters. Touched is summed like the rest and carries
// the caveat above with it.
func (f *Files) Add(other Files) {
	f.Reads += other.Reads
	f.Edits += other.Edits
	f.Writes += other.Writes
	f.ReadLines += other.ReadLines
	f.Added += other.Added
	f.Removed += other.Removed
	f.WriteLines += other.WriteLines
	f.Touched += other.Touched
}

// Bucket is one hour of one session, on one model at one effort.
//
// Split by model and effort because that is the split every question about cost
// eventually wants — what does opus at high effort actually cost — and because a
// session can change either mid-conversation.
type Bucket struct {
	At     time.Time
	Model  string
	Effort string
	Turns  int
	Tokens Tokens
	Files  Files
}

// Key is what makes two buckets the same bucket.
func (b Bucket) Key() string {
	return clock.Format(b.At) + "\x00" + b.Model + "\x00" + b.Effort
}

// Cursor is where a read stopped, so the next one can start there.
//
// Size is recorded as well as Offset because it is how a rotation is noticed: a
// transcript that is *smaller* than where the last read finished is not the file
// that was being read.
type Cursor struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

// Reading is what one pass over a transcript found.
type Reading struct {
	Buckets []Bucket
	Cursor  Cursor
	// Reset reports that the file had shrunk and was read from the beginning
	// again. The hour it lands in may be double-counted, and saying so is the
	// point: an operator can see a doubled hour, and cannot see a lost one.
	Reset bool
	// Skipped counts lines that would not parse. Ordinary in small numbers — a
	// rollup can read a transcript while Claude is appending to it — and a signal
	// in large ones.
	Skipped int
	// Turns is how many assistant turns were counted, across every bucket.
	Turns int
}

// entry is the part of a transcript line this reads. Everything else is ignored,
// deliberately: this is a compatibility surface and the less of it that is named,
// the less there is to break.
type entry struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Effort    string `json:"effort"`
	Sidechain bool   `json:"isSidechain"`
	Message   struct {
		Model string `json:"model"`
		Usage *usage `json:"usage"`
	} `json:"message"`
	Result json.RawMessage `json:"toolUseResult"`
}

type usage struct {
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	ServerTool  struct {
		Search int64 `json:"web_search_requests"`
		Fetch  int64 `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

// result is the shape of a tool's result, in the three forms that carry file work.
// Which one a line is, is told from which fields it has — the tool's *name* is on
// the assistant's message rather than here, and matching the shape needs no
// cross-referencing between two lines.
type result struct {
	Type string `json:"type"`
	// A read.
	File *struct {
		FilePath string `json:"filePath"`
		NumLines int64  `json:"numLines"`
		Content  string `json:"content"`
	} `json:"file"`
	// An edit or a write.
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
	Patch    []struct {
		Lines []string `json:"lines"`
	} `json:"structuredPatch"`
}

// Read walks a transcript from a cursor and returns what it found.
//
// Only turns belonging to `session` are counted. That is load-bearing rather than
// tidy: a project directory holds every session anybody has run in it, including
// the operator's own, and Orc mints the ids it resumes — so a turn counts when Orc
// started the conversation it belongs to, and not otherwise.
func Read(path, session string, from Cursor) (Reading, error) {
	if strings.TrimSpace(path) == "" {
		return Reading{}, fault.Internal{Where: "activity.Read", Detail: "no transcript named"}
	}
	if strings.TrimSpace(session) == "" {
		return Reading{}, fault.Internal{Where: "activity.Read", Detail: "no session named"}
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A session whose transcript is not there yet is not a failure. The
			// hook records the path on the first event, which can be ahead of
			// Claude creating the file.
			return Reading{Cursor: Cursor{Path: path}}, nil
		}
		return Reading{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return Reading{}, fault.IO{Op: "stat", Path: path, Err: err}
	}

	got := Reading{Cursor: Cursor{Path: path, Size: info.Size()}}
	start := from.Offset
	if from.Path != path || info.Size() < from.Offset {
		// A different file, or one that has shrunk: either way the offset describes
		// something that is no longer there.
		start, got.Reset = 0, from.Path != "" && from.Offset > 0
	}
	if start > 0 {
		if _, err := f.Seek(start, 0); err != nil {
			return Reading{}, fault.IO{Op: "seek", Path: path, Err: err}
		}
	}

	byKey := map[string]*Bucket{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxLine)

	read := start
	for scanner.Scan() {
		line := scanner.Bytes()
		read += int64(len(line)) + 1

		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			got.Skipped++
			continue
		}
		if e.SessionID != session || e.Message.Usage == nil && len(e.Result) == 0 {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			got.Skipped++
			continue
		}

		key := Bucket{At: at.UTC().Truncate(time.Hour), Model: alias(e.Message.Model), Effort: e.Effort}
		into, ok := byKey[key.Key()]
		if !ok {
			into = &Bucket{At: key.At, Model: key.Model, Effort: key.Effort}
			byKey[key.Key()] = into
		}

		if u := e.Message.Usage; u != nil {
			into.Turns++
			got.Turns++
			into.Tokens.Add(Tokens{
				Input:       u.Input,
				Output:      u.Output,
				CacheCreate: u.CacheCreate,
				CacheRead:   u.CacheRead,
				WebCalls:    u.ServerTool.Search + u.ServerTool.Fetch,
			})
		}
		if len(e.Result) > 0 {
			into.Files.Add(filesOf(e.Result))
		}
	}
	if err := scanner.Err(); err != nil {
		// Half a transcript is more use than none: what was read is returned, the
		// cursor stops where the reader did, and the next pass carries on from
		// there. A line too long for the buffer is the usual cause.
		got.Skipped++
	}

	got.Cursor.Offset = read
	got.Buckets = sorted(byKey)
	return got, nil
}

// filesOf reads one tool result for the file work it describes.
//
// By shape rather than by tool name: the name is on the assistant's message and the
// result is on the user's, so matching the shape avoids threading two lines
// together for a number that the shape already determines.
func filesOf(raw json.RawMessage) Files {
	var r result
	if err := json.Unmarshal(raw, &r); err != nil {
		// A result this does not recognise — a Bash result, an image, an
		// interrupted call — is not file work and is not damage.
		return Files{}
	}

	var got Files
	switch {
	case r.File != nil:
		got.Reads = 1
		got.Touched = 1
		if r.File.NumLines > 0 {
			got.ReadLines = r.File.NumLines
		} else {
			got.ReadLines = int64(lines(r.File.Content))
		}

	case len(r.Patch) > 0:
		got.Edits = 1
		got.Touched = 1
		for _, hunk := range r.Patch {
			for _, line := range hunk.Lines {
				switch {
				case strings.HasPrefix(line, "+"):
					got.Added++
				case strings.HasPrefix(line, "-"):
					got.Removed++
				}
			}
		}

	case r.Type == "create":
		got.Writes = 1
		got.Touched = 1
		got.WriteLines = int64(lines(r.Content))
	}
	return got
}

// lines counts the lines in a body, treating a final newline as ending the last
// line rather than starting another.
func lines(body string) int {
	if body == "" {
		return 0
	}
	return bytes.Count([]byte(strings.TrimSuffix(body, "\n")), []byte("\n")) + 1
}

// alias reduces a model id to the word a budget is written in.
//
// `claude-opus-5` and whatever it is called next both weigh the same to a tariff,
// and a rollup keyed by the full id would start a new series on every release. An
// id this does not recognise is kept as it is: a wrong guess about what a model
// costs is worse than an unfamiliar word on a screen.
func alias(id string) string {
	got := strings.ToLower(strings.TrimSpace(id))
	for _, known := range []string{"haiku", "sonnet", "opus"} {
		if strings.Contains(got, known) {
			return known
		}
	}
	return got
}

// sorted returns the buckets oldest first, then by model and effort, so a rollup
// appends in a stable order and two runs over the same bytes write the same file.
func sorted(byKey map[string]*Bucket) []Bucket {
	out := make([]Bucket, 0, len(byKey))
	for _, b := range byKey {
		out = append(out, *b)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func less(a, b Bucket) bool {
	if !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	if a.Model != b.Model {
		return a.Model < b.Model
	}
	return a.Effort < b.Effort
}
