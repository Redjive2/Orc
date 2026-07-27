package render_test

import (
	"path/filepath"
	"strings"
	"testing"

	"orc/common/user"
	"orc/orc/internal/render"
	"orc/orc/internal/style"
	"orc/orc/internal/view"
	"orc/theme"
)

// The pane is drawn from the same fixture the view folds, so the screen these tests
// pin is the screen a real session produces — the schema is settled, so a
// hand-written feed is a valid input.
func paneFixture(t *testing.T) render.Screen {
	t.Helper()

	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	session, err := view.Load(filepath.Join("..", "view", "testdata", "events.jsonl"), who)
	if err != nil {
		t.Fatal(err)
	}
	prose, ok := view.ReadProse(filepath.Join("..", "view", "testdata", "transcript.jsonl"))

	return render.Screen{
		Session:        session,
		Facts:          view.Facts{Role: "engineer", Authority: 60, Model: "sonnet", Effort: "medium", Load: 4, Mail: 2, Task: "extract-common-source"},
		Prose:          prose,
		ProseAvailable: ok,
		Compose:        []string{"the account verifier goes in Common"},
	}
}

func draw(t *testing.T, s render.Screen, p style.Palette, width, height int) string {
	t.Helper()
	got, err := render.DrawPane(s, p, width, height)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPaneShowsWhatHappened(t *testing.T) {
	got := draw(t, paneFixture(t), style.Plain(), 100, 24)

	for _, want := range []string{
		"ember", "engineer(60)", "sonnet/medium", "load 4", "turn 1", // the header
		"14:22:09", "read", "Common/user/user.go", // an allowed action
		"edit", "Common/account/account.go", "✗ denied", // and a refused one
		"outside write(Macmuffin/**)", // with its reason, on its own line
		"waiting for input",           // the state that matters most
		"compose",                     // the mode is never in doubt
		"^S send", "^Q leave",         // and how to get out
		"mail 2", "task extract-common-source",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the pane does not show %q:\n%s", want, got)
		}
	}
}

// TestColourIsALayer is Finish.md's condition for this stream: the coloured pane
// stripped of escapes must be byte-for-byte the plain one.
func TestPaneColourStripsToPlain(t *testing.T) {
	s := paneFixture(t)

	plain := draw(t, s, style.Plain(), 100, 24)
	coloured := draw(t, s, style.Coloured(), 100, 24)

	if coloured == plain {
		t.Fatal("the coloured pane emitted no colour at all")
	}
	if got := stripSGR(coloured); got != plain {
		t.Errorf("colour changed the pane.\n got:\n%s\nwant:\n%s", got, plain)
	}
}

// Every row is the same width, or the frame does not close. This is the property
// that breaks when a pad measures a painted string instead of a plain one.
func TestPaneIsRectangular(t *testing.T) {
	for _, width := range []int{48, 72, 100, 140} {
		for _, height := range []int{10, 16, 24} {
			s := paneFixture(t)
			lines := strings.Split(strings.TrimRight(draw(t, s, style.Plain(), width, height), "\n"), "\n")

			if len(lines) != height {
				t.Errorf("%dx%d drew %d lines", width, height, len(lines))
			}
			for i, line := range lines {
				if got := runeWidth(line); got != width {
					t.Errorf("%dx%d line %d is %d wide:\n%s", width, height, i, got, line)
				}
			}
		}
	}
}

// The same, with colour on: escapes occupy no columns, so a painted pane must
// measure exactly as its plain twin does.
func TestPaneIsRectangularInColour(t *testing.T) {
	s := paneFixture(t)
	for _, line := range strings.Split(strings.TrimRight(draw(t, s, style.Coloured(), 100, 20), "\n"), "\n") {
		if got := runeWidth(stripSGR(line)); got != 100 {
			t.Errorf("a coloured line is %d wide:\n%q", got, line)
		}
	}
}

// TestAMissingTranscriptCostsTheProseNotThePane — Plan.md §6.2's honest limit, and
// a condition of this stream.
func TestMissingTranscriptKeepsThePane(t *testing.T) {
	s := paneFixture(t)
	s.Prose, s.ProseAvailable = nil, false

	got := draw(t, s, style.Plain(), 100, 24)

	// The feed is still all there.
	for _, want := range []string{"Common/user/user.go", "✗ denied", "waiting for input"} {
		if !strings.Contains(got, want) {
			t.Errorf("the feed lost %q when the transcript went:\n%s", want, got)
		}
	}
	// And it says so, naming the way to see the session itself.
	if !strings.Contains(got, "transcript could not be read") || !strings.Contains(got, "--direct") {
		t.Errorf("the pane does not say the prose is missing:\n%s", got)
	}
}

// A session with no feed yet is the first thing anybody attaching to a fresh agent
// sees, so it says what is happening rather than drawing an empty box.
func TestEmptyFeed(t *testing.T) {
	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	got := draw(t, render.Screen{Session: view.Session{Identity: who}, Facts: view.NoFacts()}, style.Plain(), 80, 12)

	if !strings.Contains(got, "no events yet") {
		t.Errorf("an empty feed drew nothing to say so:\n%s", got)
	}
	if !strings.Contains(got, "ember") {
		t.Errorf("the header is gone:\n%s", got)
	}
	// An unknown mail count is not zero, and the footer must not claim otherwise.
	if strings.Contains(got, "mail 0") || strings.Contains(got, "mail -1") {
		t.Errorf("the footer invented a count it does not have:\n%s", got)
	}
}

// A long path must not push the verdict off the end, and must not wrap: the feed is
// scanned down a column, and one wrapped row breaks the scan.
func TestLongPathsTruncate(t *testing.T) {
	s := paneFixture(t)
	long := "Common/" + strings.Repeat("some-deep-directory/", 12) + "file.go"
	for i := range s.Session.Rows {
		s.Session.Rows[i].Detail = long
	}

	got := draw(t, s, style.Plain(), 80, 20)
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if runeWidth(line) != 80 {
			t.Fatalf("a long path broke the frame:\n%s", got)
		}
	}
	if !strings.Contains(got, "✗ denied") {
		t.Errorf("a long path pushed the verdict off the row:\n%s", got)
	}
}

// The compose box grows with the message but never eats the feed it is about.
func TestComposeIsBounded(t *testing.T) {
	s := paneFixture(t)
	s.Compose = strings.Split(strings.Repeat("a line\n", 30), "\n")

	got := draw(t, s, style.Plain(), 80, 18)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 18 {
		t.Fatalf("a long message drew %d lines into an 18-row pane", len(lines))
	}
	if !strings.Contains(got, "14:22:") {
		t.Errorf("the compose box ate the whole feed:\n%s", got)
	}
}

func TestNoticeReplacesTheComposeLabel(t *testing.T) {
	s := paneFixture(t)
	s.Notice = "sent"

	if got := draw(t, s, style.Plain(), 80, 14); !strings.Contains(got, "sent") {
		t.Errorf("the notice is not shown:\n%s", got)
	}
}

// runeWidth measures a drawn line.
//
// It defers to theme.Width, which is the tree's one measurement authority and the
// same function the drawing code lays out with. A second implementation here was the
// first thing this test grew, and it disagreed immediately: it counted box-drawing
// characters as double-width, so every correctly drawn line looked too long. A test
// that measures differently from the code does not check the code, it checks the
// test.
func runeWidth(s string) int { return theme.Width(s) }

// stripSGR removes colour escapes.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
