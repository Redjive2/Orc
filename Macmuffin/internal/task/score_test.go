package task_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/task"
)

func TestParseScores(t *testing.T) {
	for _, raw := range []string{"1", "2", "3", "4", "5"} {
		p, err := task.ParsePriority(raw)
		if err != nil {
			t.Fatalf("ParsePriority(%q): %v", raw, err)
		}
		d, err := task.ParseDifficulty(raw)
		if err != nil {
			t.Fatalf("ParseDifficulty(%q): %v", raw, err)
		}
		if p.String() != raw || d.String() != raw {
			t.Errorf("%q round-tripped to %q / %q", raw, p, d)
		}
		if p.Zero() || d.Zero() {
			t.Errorf("a parsed score should not be zero")
		}
	}
}

// TestScoresRejectEverythingElse. A score is a single digit, so demanding
// exactly that rejects every form strconv would tolerate without needing a rule
// for each of them.
func TestScoresRejectEverythingElse(t *testing.T) {
	for _, raw := range []string{
		"", " ", "0", "6", "10", "-1", "+1", "1.0", "one", "high",
		"1_0", "٣", "x", "1e0", "01", "1 2",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := task.ParsePriority(raw); !errors.Is(err, fault.ErrUsage) {
				t.Errorf("ParsePriority(%q) = %v, want a usage fault", raw, err)
			}
			if _, err := task.ParseDifficulty(raw); !errors.Is(err, fault.ErrUsage) {
				t.Errorf("ParseDifficulty(%q) = %v, want a usage fault", raw, err)
			}
		})
	}

	// Surrounding space is the exception, because on a command line it comes
	// from quoting rather than from intent.
	for _, raw := range []string{"1 ", " 1", "  3  "} {
		got, err := task.ParsePriority(raw)
		if err != nil {
			t.Errorf("ParsePriority(%q) = %v, want it trimmed and accepted", raw, err)
			continue
		}
		if got.Value() != int(strings.TrimSpace(raw)[0]-'0') {
			t.Errorf("ParsePriority(%q) = %d", raw, got.Value())
		}
	}
}

// TestTheErrorNamesTheScale is the whole reason priority and difficulty are
// distinguishable: a caller who passed 1 meaning "urgent" has to be told which
// end is which.
func TestTheErrorNamesTheScale(t *testing.T) {
	_, err := task.ParsePriority("9")
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"priority", "1 is low", "5 is high"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the priority error should say %q:\n%s", want, err)
		}
	}

	_, err = task.ParseDifficulty("9")
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"difficulty", "1 is easy", "5 is hard"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the difficulty error should say %q:\n%s", want, err)
		}
	}
}

func TestNewScoreBounds(t *testing.T) {
	for _, v := range []int{0, -1, 6, 100} {
		if _, err := task.NewPriority(v); !errors.Is(err, fault.ErrUsage) {
			t.Errorf("NewPriority(%d) = %v, want a usage fault", v, err)
		}
		if _, err := task.NewDifficulty(v); !errors.Is(err, fault.ErrUsage) {
			t.Errorf("NewDifficulty(%d) = %v, want a usage fault", v, err)
		}
	}
	for v := task.MinScore; v <= task.MaxScore; v++ {
		if _, err := task.NewPriority(v); err != nil {
			t.Errorf("NewPriority(%d): %v", v, err)
		}
	}
}

// TestScoreLabelsSayWhichEnd: a board shows the number, and a card shows what
// the number means, so a reader never has to remember the direction.
func TestScoreLabels(t *testing.T) {
	for _, tc := range []struct {
		v     int
		label string
		tag   string
	}{
		{1, "1 (low)", "P1"},
		{3, "3", "P3"},
		{5, "5 (high)", "P5"},
	} {
		p, err := task.NewPriority(tc.v)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Label(); got != tc.label {
			t.Errorf("priority %d Label() = %q, want %q", tc.v, got, tc.label)
		}
		if got := p.Tag(); got != tc.tag {
			t.Errorf("priority %d Tag() = %q, want %q", tc.v, got, tc.tag)
		}
	}

	d, err := task.NewDifficulty(5)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Label(); got != "5 (hard)" {
		t.Errorf("difficulty 5 Label() = %q", got)
	}
	if got := d.Tag(); got != "D5" {
		t.Errorf("difficulty 5 Tag() = %q, want D5", got)
	}

	// The two scales are distinguishable even at the same number, which is the
	// point of carrying the kind.
	p3, _ := task.NewPriority(3)
	d3, _ := task.NewDifficulty(3)
	if p3.Tag() == d3.Tag() {
		t.Error("priority 3 and difficulty 3 should not render identically")
	}
	if p3 == d3 {
		t.Error("priority 3 and difficulty 3 should not compare equal")
	}
}

func TestZeroScore(t *testing.T) {
	var s task.Score
	if !s.Zero() {
		t.Error("the zero Score should report itself as zero")
	}
	for _, got := range []string{s.String(), s.Label()} {
		if got != "unset" {
			t.Errorf("a zero score rendered %q, want %q", got, "unset")
		}
	}
	if got := s.Tag(); got != "--" {
		t.Errorf("a zero score's tag is %q", got)
	}
	if s.Value() != 0 {
		t.Errorf("a zero score has value %d", s.Value())
	}
}

func TestParseStatus(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want task.Status
	}{
		{"1", task.StatusBroken},
		{"2", task.StatusSlow},
		{"3", task.StatusNominal},
		{"4", task.StatusDone},
		// The words are accepted too: an agent that wrote what it meant should
		// not be corrected on spelling.
		{"broken", task.StatusBroken},
		{"slow", task.StatusSlow},
		{"nominal", task.StatusNominal},
		{"done", task.StatusDone},
		{"NOMINAL", task.StatusNominal},
		{"  done  ", task.StatusDone},
	} {
		got, err := task.ParseStatus(tc.raw)
		if err != nil {
			t.Errorf("ParseStatus(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseStatus(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestParseStatusRejects(t *testing.T) {
	for _, raw := range []string{"", "0", "5", "-1", "fine", "ok", "x", "1.0", "unreported"} {
		if _, err := task.ParseStatus(raw); !errors.Is(err, fault.ErrUsage) {
			t.Errorf("ParseStatus(%q) = %v, want a usage fault", raw, err)
		}
	}

	// "unreported" is refused specifically: it is a real state, but not one a
	// caller may set — a task becomes unreported by never being reported on.
	if _, err := task.ParseStatus("unreported"); err == nil {
		t.Error("unset should not be settable")
	}

	// And the message lists the options, since the numbers alone say nothing.
	_, err := task.ParseStatus("fine")
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"broken", "slow", "nominal", "done"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should list %q:\n%s", want, err)
		}
	}
}

// TestStatusCarriesItsMeaningWithoutColour: every status is a glyph *and* a
// word, so a pipe through grep keeps the information.
func TestStatusLabels(t *testing.T) {
	seenGlyph := map[string]task.Status{}
	seenWord := map[string]task.Status{}

	for _, s := range []task.Status{
		task.StatusUnset, task.StatusBroken, task.StatusSlow,
		task.StatusNominal, task.StatusDone,
	} {
		glyph, word := s.Glyph(), s.String()
		if glyph == "" || glyph == "?" {
			t.Errorf("%v has no glyph", s)
		}
		if word == "" || strings.HasPrefix(word, "Status(") {
			t.Errorf("%v has no word", s)
		}
		if other, dup := seenGlyph[glyph]; dup {
			t.Errorf("%v and %v share the glyph %q", other, s, glyph)
		}
		if other, dup := seenWord[word]; dup {
			t.Errorf("%v and %v share the word %q", other, s, word)
		}
		seenGlyph[glyph], seenWord[word] = s, s

		if got := s.Label(); !strings.Contains(got, glyph) || !strings.Contains(got, word) {
			t.Errorf("%v Label() = %q, should carry both", s, got)
		}
	}

	if got := task.Status(99).String(); !strings.Contains(got, "99") {
		t.Errorf("an undefined status should say so, got %q", got)
	}
	if task.Status(99).Known() || task.Status(99).Valid() {
		t.Error("Status(99) should be neither known nor valid")
	}
}

// TestUnsetIsKnownButNotSettable: a task nobody has reported on is in a real
// state, and it is not one a caller may choose.
func TestUnsetIsKnownButNotSettable(t *testing.T) {
	if !task.StatusUnset.Known() {
		t.Error("unset should be a known status")
	}
	if task.StatusUnset.Valid() {
		t.Error("unset should not be settable")
	}
	for _, s := range []task.Status{task.StatusBroken, task.StatusSlow, task.StatusNominal, task.StatusDone} {
		if !s.Valid() || !s.Known() {
			t.Errorf("%v should be both valid and known", s)
		}
		if s.Int() < 1 || s.Int() > 4 {
			t.Errorf("%v stores as %d, outside 1..4", s, s.Int())
		}
	}
}

func FuzzParseScoreAndStatus(f *testing.F) {
	for _, seed := range []string{"1", "5", "0", "6", "", "nominal", "-1", "01", "  3  "} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		// Whatever arrives, the answer is a refusal or a value inside the scale.
		if s, err := task.ParsePriority(raw); err == nil {
			if s.Value() < task.MinScore || s.Value() > task.MaxScore {
				t.Fatalf("ParsePriority(%q) accepted %d", raw, s.Value())
			}
			if s.Zero() {
				t.Fatalf("ParsePriority(%q) returned a zero score with no error", raw)
			}
		} else if !errors.Is(err, fault.ErrUsage) {
			t.Fatalf("ParsePriority(%q) failed with an unclassified error: %v", raw, err)
		}

		if s, err := task.ParseStatus(raw); err == nil {
			if !s.Valid() {
				t.Fatalf("ParseStatus(%q) accepted the unsettable %v", raw, s)
			}
		} else if !errors.Is(err, fault.ErrUsage) {
			t.Fatalf("ParseStatus(%q) failed with an unclassified error: %v", raw, err)
		}
	})
}
