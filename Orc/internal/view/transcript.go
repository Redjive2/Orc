package view

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"unicode"
)

// The transcript is Claude's file, not Orc's.
//
// Plan.md §6.2's honest limit: its shape is a compatibility surface, so everything
// here degrades and nothing here fails. An unreadable, absent, or unrecognised
// transcript costs the prose and not the pane — the event feed is Orc's own record
// and is what the screen is actually built from.
//
// That is also why this decoder is the opposite of every other one in the tree.
// Elsewhere an unknown field is refused, because it means a newer Orc wrote a file an
// older one is misreading. Here an unknown field means Claude shipped a release, and
// refusing would turn every upgrade into an outage of the view.

// MaxTranscriptBytes bounds what is read.
//
// A long session's transcript is megabytes and a pane shows a handful of lines, so
// only the tail is read. It is a byte bound rather than a line bound because the
// point is to cap the work, and one line can be a whole file's contents.
const MaxTranscriptBytes = 256 << 10

// MaxProse bounds how many messages are kept.
const MaxProse = 64

// MaxProseLine is where a single line is cut. Prose is shown as a hint of what the
// agent said, not as a reader for it — `--direct` is the reader.
const MaxProseLine = 400

// Speaker is who said a line of prose.
type Speaker string

// The speakers this view distinguishes. Anything else is dropped rather than guessed
// at: a line attributed to the wrong party is worse than a line not shown.
const (
	Assistant Speaker = "assistant"
	Human     Speaker = "user"
)

// Prose is one thing somebody said.
type Prose struct {
	Who  Speaker
	Text string
}

// entry is the part of a transcript line this reads. Everything else is ignored,
// deliberately.
type entry struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one item of a structured content array.
type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ReadProse returns what was said, oldest first.
//
// The bool reports whether the transcript could be read at all, which is what the
// pane needs to decide between showing prose and saying that there is none — those
// are different states and an operator should not have to guess which they are in.
func ReadProse(path string) ([]Prose, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	if info.Size() > MaxTranscriptBytes {
		if _, err := f.Seek(info.Size()-MaxTranscriptBytes, 0); err != nil {
			return nil, false
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxTranscriptBytes)

	var out []Prose
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if first && info.Size() > MaxTranscriptBytes {
			// The seek landed mid-line; that fragment is not JSON and is not an
			// error either.
			first = false
			continue
		}
		first = false

		got, ok := proseOf(line)
		if !ok {
			continue
		}
		out = append(out, got)
	}
	// A scanner error still returns what was read: half a transcript is more use
	// than none, and the pane says the prose is partial by simply having less of it.

	if over := len(out) - MaxProse; over > 0 {
		out = out[over:]
	}
	return out, true
}

// proseOf reads one transcript line, reporting whether it said anything.
func proseOf(line []byte) (Prose, bool) {
	line = trimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return Prose{}, false
	}

	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Prose{}, false
	}

	who := Speaker(strings.ToLower(strings.TrimSpace(e.Message.Role)))
	if who == "" {
		who = Speaker(strings.ToLower(strings.TrimSpace(e.Type)))
	}
	if who != Assistant && who != Human {
		return Prose{}, false
	}

	text := textOf(e.Message.Content)
	if text == "" {
		return Prose{}, false
	}
	return Prose{Who: who, Text: text}, true
}

// textOf pulls the words out of a content field.
//
// Claude has sent content as a bare string and as an array of typed blocks. Both are
// handled, and anything else yields nothing — which costs one line of prose and
// nothing else.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return clean(plain)
	}

	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var parts []string
	for _, b := range blocks {
		// Tool calls and their results are in the blocks too. They are already in
		// the event feed, with Orc's own verdict attached, so taking them from here
		// as well would show every action twice and disagree about half of them.
		if b.Type != "" && b.Type != "text" {
			continue
		}
		if got := clean(b.Text); got != "" {
			parts = append(parts, got)
		}
	}
	return strings.Join(parts, " ")
}

// clean flattens a message to one line the pane can draw.
func clean(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		// Control characters in a transcript would be drawn into the pane, where
		// an escape sequence could repaint every row below it.
		if unicode.IsControl(r) {
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
		if b.Len() > MaxProseLine {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
