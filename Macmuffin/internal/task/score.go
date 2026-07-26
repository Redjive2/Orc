package task

import (
	"fmt"
	"strings"

	"orc/common/fault"
)

// Scores run 1 to 5 and are set at creation. Two scores that get used beat six
// that do not, which is why these are the only two.
const (
	MinScore = 1
	MaxScore = 5
)

// Score is a priority or a difficulty: an integer from 1 to 5.
//
// Both scales are the same shape, so they are one type rather than two nearly
// identical ones — but the *kind* travels with the value, because "priority 1"
// and "difficulty 1" mean opposite things about how much attention a task
// wants, and a function that took a bare int could be handed them the wrong way
// round without noticing.
type Score struct {
	value int
	kind  scoreKind
}

type scoreKind int

const (
	kindUnset scoreKind = iota
	kindPriority
	kindDifficulty
)

func (k scoreKind) String() string {
	switch k {
	case kindPriority:
		return "priority"
	case kindDifficulty:
		return "difficulty"
	default:
		return "score"
	}
}

// scales describes each end of each scale, for the error message a caller who
// guessed wrong actually needs.
var scales = map[scoreKind][2]string{
	kindPriority:   {"low", "high"},
	kindDifficulty: {"easy", "hard"},
}

// ParsePriority reads a priority, 1 (low) to 5 (high).
func ParsePriority(raw string) (Score, error) { return parseScore(raw, kindPriority) }

// ParseDifficulty reads a difficulty, 1 (easy) to 5 (hard).
func ParseDifficulty(raw string) (Score, error) { return parseScore(raw, kindDifficulty) }

// NewPriority builds a priority from a number already in hand.
func NewPriority(v int) (Score, error) { return newScore(v, kindPriority) }

// NewDifficulty builds a difficulty from a number already in hand.
func NewDifficulty(v int) (Score, error) { return newScore(v, kindDifficulty) }

// parseScore reads a score as a single digit.
//
// Surrounding space is trimmed, because on a command line it is an artifact of
// quoting rather than a statement of intent. Everything else is refused: the
// scale is 1 to 5, so a valid score is exactly one character, and demanding
// that rejects `01`, `10`, `+1`, and `1.0` without needing a rule for each. A
// score with a leading zero was produced by something that believes this field
// is wider than it is, and that is worth hearing about.
func parseScore(raw string, kind scoreKind) (Score, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != 1 {
		return Score{}, outOfRange(raw, kind)
	}
	c := trimmed[0]
	if c < '0'+MinScore || c > '0'+MaxScore {
		return Score{}, outOfRange(raw, kind)
	}
	return newScore(int(c-'0'), kind)
}

func newScore(v int, kind scoreKind) (Score, error) {
	if v < MinScore || v > MaxScore {
		return Score{}, outOfRange(fmt.Sprint(v), kind)
	}
	s := Score{value: v, kind: kind}
	if err := s.validate(); err != nil {
		return Score{}, err
	}
	return s, nil
}

// outOfRange names the scale as well as the range, because a caller who passed
// 7 needs the bound and a caller who passed 1 meaning "urgent" needs to know
// which end is which.
func outOfRange(raw string, kind scoreKind) error {
	ends := scales[kind]
	return fault.Usage{Reason: fmt.Sprintf(
		"%s must be %d to %d (%d is %s, %d is %s), not %q",
		kind, MinScore, MaxScore, MinScore, ends[0], MaxScore, ends[1], raw)}
}

func (s Score) validate() error {
	const where = "task.Score"
	if err := fault.Check(s.kind != kindUnset, where, "score has no scale"); err != nil {
		return err
	}
	return fault.Check(s.value >= MinScore && s.value <= MaxScore, where,
		"%s is %d, outside %d..%d", s.kind, s.value, MinScore, MaxScore)
}

// Value returns the number.
func (s Score) Value() int { return s.value }

// Zero reports whether the score was never constructed.
func (s Score) Zero() bool { return s.kind == kindUnset }

// String renders the score as it is configured and stored.
func (s Score) String() string {
	if s.Zero() {
		return "unset"
	}
	return fmt.Sprint(s.value)
}

// Label renders the score for a card: the number and which end of its scale it
// sits at, so a reader does not have to remember the direction.
func (s Score) Label() string {
	if s.Zero() {
		return "unset"
	}
	ends := scales[s.kind]
	switch s.value {
	case MinScore:
		return fmt.Sprintf("%d (%s)", s.value, ends[0])
	case MaxScore:
		return fmt.Sprintf("%d (%s)", s.value, ends[1])
	default:
		return fmt.Sprint(s.value)
	}
}

// Tag renders the score for a board column: a letter and a digit, which is
// narrow enough to sit in a table and still say which scale it is.
func (s Score) Tag() string {
	if s.Zero() {
		return "--"
	}
	letter := "P"
	if s.kind == kindDifficulty {
		letter = "D"
	}
	return fmt.Sprintf("%s%d", letter, s.value)
}

// Status is how the work is going: a health signal, not a measure of progress.
//
// It is deliberately separate from completion and from subtask counts. A task
// can be StatusDone and still incomplete — that is precisely the state the
// scale exists to report, and collapsing it into `complete` would delete the
// tool's most useful signal.
type Status int

const (
	// StatusUnset is a task nobody has reported on yet.
	StatusUnset Status = 0
	// StatusBroken is "not working".
	StatusBroken Status = 1
	// StatusSlow is "slow or problematic".
	StatusSlow Status = 2
	// StatusNominal is "nominal".
	StatusNominal Status = 3
	// StatusDone is "done, or basically done".
	StatusDone Status = 4
)

// statuses describes each value: the word it prints and the glyph that carries
// the same information when colour is gone.
var statuses = map[Status]struct {
	word  string
	glyph string
}{
	StatusUnset:   {"unreported", "·"},
	StatusBroken:  {"broken", "✗"},
	StatusSlow:    {"slow", "~"},
	StatusNominal: {"nominal", "●"},
	StatusDone:    {"done", "✓"},
}

// ParseStatus reads a status from the command line.
func ParseStatus(raw string) (Status, error) {
	trimmed := strings.TrimSpace(raw)

	// The numbers are the documented interface, and the words are accepted too
	// because an agent writing `muff status x nominal` has said exactly what it
	// meant and should not be corrected on spelling.
	for s, d := range statuses {
		if s != StatusUnset && strings.EqualFold(trimmed, d.word) {
			return s, nil
		}
	}

	switch trimmed {
	case "1":
		return StatusBroken, nil
	case "2":
		return StatusSlow, nil
	case "3":
		return StatusNominal, nil
	case "4":
		return StatusDone, nil
	}
	return StatusUnset, fault.Usage{Reason: fmt.Sprintf(
		"status must be 1 (%s), 2 (%s), 3 (%s), or 4 (%s), not %q",
		statuses[StatusBroken].word, statuses[StatusSlow].word,
		statuses[StatusNominal].word, statuses[StatusDone].word, raw)}
}

// Valid reports whether s is a reportable status. StatusUnset is a real state
// but not one a caller may set, so it is excluded.
func (s Status) Valid() bool { return s >= StatusBroken && s <= StatusDone }

// Known reports whether s is any defined value, including unset.
func (s Status) Known() bool { return s == StatusUnset || s.Valid() }

// Int returns the number the status is stored and configured as.
func (s Status) Int() int { return int(s) }

// String renders the status as its word.
func (s Status) String() string {
	if d, ok := statuses[s]; ok {
		return d.word
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Glyph returns the mark that carries the status when colour is gone. It is
// always shown beside the word, never instead of it.
func (s Status) Glyph() string {
	if d, ok := statuses[s]; ok {
		return d.glyph
	}
	return "?"
}

// Label renders the status for a board cell: glyph and word together, so a pipe
// through grep keeps the meaning.
func (s Status) Label() string { return s.Glyph() + " " + s.String() }
