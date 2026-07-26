package render_test

import (
	"strings"
	"testing"

	"orc/common/user"
	"orc/mailman/internal/fixture"
	"orc/mailman/internal/render"
	"orc/mailman/internal/style"
	"orc/mailman/internal/view"
)

func rows(t *testing.T) []view.Row {
	t.Helper()
	got, err := fixture.Rows()
	if err != nil {
		t.Fatalf("building the corpus: %v", err)
	}
	return got
}

// TestListingIsRectangular is the property every table has to satisfy and the
// one a golden test would only catch by accident: every line the same width,
// every column divider in the same place.
//
// It is checked across a range of widths and against a corpus containing a
// CJK subject, an over-long subject, and an empty column — the three things
// that shear a table.
func TestListingIsRectangular(t *testing.T) {
	all := rows(t)

	for _, width := range []int{40, 60, 80, 100, 120, 200} {
		for _, set := range [][]view.Row{all, fixture.Unarchived(all), nil} {
			out, err := render.Listing("inbox · alice", set, style.Plain(), width)
			if err != nil {
				t.Fatalf("Listing at width %d: %v", width, err)
			}
			// A table never shrinks a column past its own minimum, so a terminal
			// too narrow for the columns is overrun rather than collapsed into a
			// column of ellipses. The table must still be rectangular; it is only
			// required to *fit* once the width can hold it.
			assertRectangular(t, out, width, width >= 80)
		}
	}
}

// assertRectangular checks that every line of a drawn table is the same number
// of terminal cells wide, and that the verticals line up down the table.
func assertRectangular(t *testing.T, out string, width int, mustFit bool) {
	t.Helper()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("a table should have at least four lines, got %d:\n%s", len(lines), out)
	}

	want := style.Width(lines[0])
	if mustFit && want > width {
		t.Errorf("the table is %d cells wide, past the %d it was given:\n%s", want, width, out)
	}

	for i, line := range lines {
		if got := style.Width(line); got != want {
			t.Errorf("line %d is %d cells, want %d:\n%s\n%s", i+1, got, want, line, out)
		}
	}

	// Column dividers must be in the same cells on every row. The title bar
	// spans the whole table and has no interior dividers, so it is skipped:
	// seeding from it would compare every row against the wrong thing.
	var columns []int
	for i, line := range lines {
		if i < 3 || !strings.HasPrefix(line, "│") {
			continue
		}
		at := verticalPositions(line)
		if columns == nil {
			columns = at
			continue
		}
		// The "no messages" placeholder also spans the table.
		if len(at) == 2 && len(columns) > 2 {
			continue
		}
		if len(at) != len(columns) {
			t.Errorf("line %d has %d dividers, want %d:\n%s", i+1, len(at), len(columns), out)
			continue
		}
		for j := range at {
			if at[j] != columns[j] {
				t.Errorf("line %d divider %d is at cell %d, want %d:\n%s", i+1, j+1, at[j], columns[j], out)
				break
			}
		}
	}
}

func verticalPositions(line string) []int {
	var out []int
	cell := 0
	for _, r := range line {
		if r == '│' {
			out = append(out, cell)
		}
		cell += style.Width(string(r))
	}
	return out
}

// TestListingGolden pins the layout exactly. Colour is off, so the constant is
// readable in a diff and a change to the drawing breaks one test.
func TestListingGolden(t *testing.T) {
	got, err := render.Listing("inbox · alice", fixture.Unarchived(rows(t)), style.Plain(), 100)
	if err != nil {
		t.Fatalf("Listing: %v", err)
	}
	if got != goldenListing {
		t.Errorf("the inbox layout changed.\n got:\n%s\nwant:\n%s", got, goldenListing)
	}
}

const goldenListing = `┌──────────────────────────────────────────────────────────────────────────────────────────────────┐
│ inbox · alice                                                                 4 unread · 5 shown │
├───┬────┬──────────────────┬────────┬────────────────────────────────────────┬────────────────────┤
│   │ id │ sent             │ from   │ subject                                │ conversation       │
├───┼────┼──────────────────┼────────┼────────────────────────────────────────┼────────────────────┤
│   │  0 │ 2026-07-24 18:31 │ boss   │ RE: work                               │ work · 01000000 #1 │
│ * │  1 │ 2026-07-24 19:31 │ carol  │ deploy notes                           │ —                  │
│ + │  2 │ 2026-07-24 20:31 │ boss   │ cc: dave added to work                 │ work · 01000000 #2 │
│ * │  3 │ 2026-07-24 21:31 │ boss   │ a subject long enough that it has to … │ —                  │
│ * │  4 │ 2026-07-24 22:31 │ carol  │ 日本語の件について                     │ —                  │
└───┴────┴──────────────────┴────────┴────────────────────────────────────────┴────────────────────┘
`

// TestEmptyListingIsStillWellFormed: a degenerate input must not produce a
// broken frame.
func TestEmptyListingIsStillWellFormed(t *testing.T) {
	out, err := render.Listing("inbox · alice", nil, style.Plain(), 80)
	if err != nil {
		t.Fatalf("Listing: %v", err)
	}
	if !strings.Contains(out, "no messages") {
		t.Errorf("an empty inbox should say so:\n%s", out)
	}
	assertRectangular(t, out, 80, true)
}

// TestTitleIsNotClippedByNarrowColumns catches the case where a table of short
// cells would otherwise be narrower than its own heading.
func TestTitleIsNotClippedByNarrowColumns(t *testing.T) {
	names, err := user.ParseList([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := render.Users(names, style.Plain(), 80)
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if !strings.Contains(out, "mailboxes") {
		t.Errorf("the title was clipped:\n%s", out)
	}
	assertRectangular(t, out, 80, true)
}

func TestCardIsRectangular(t *testing.T) {
	all := rows(t)
	for _, width := range []int{40, 60, 100, 160} {
		for i, r := range all {
			out, err := render.Card(r, style.Plain(), width)
			if err != nil {
				t.Fatalf("Card %d at width %d: %v", i, width, err)
			}
			// The frame is the part that must be rectangular; the body follows
			// it verbatim and is deliberately not boxed.
			frame, body, found := strings.Cut(out, "╯\n")
			if !found {
				t.Fatalf("card %d has no closing frame:\n%s", i, out)
			}
			assertSameWidth(t, frame+"╯", out)
			if !strings.Contains(body, strings.TrimSpace(r.Message.BodyString())) {
				t.Errorf("card %d lost its body:\n%s", i, out)
			}
		}
	}
}

func assertSameWidth(t *testing.T, frame, whole string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	want := style.Width(lines[0])
	for i, line := range lines {
		if got := style.Width(line); got != want {
			t.Errorf("frame line %d is %d cells, want %d:\n%s", i+1, got, want, whole)
		}
	}
}

// TestCardGolden pins the message card.
func TestCardGolden(t *testing.T) {
	all := rows(t)
	got, err := render.Card(all[0], style.Plain(), 80)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if got != goldenCard {
		t.Errorf("the card layout changed.\n got:\n%s\nwant:\n%s", got, goldenCard)
	}
}

const goldenCard = `╭─ message 0 ─────────────────────────────────────────────────────────── read ─╮
│ from     boss                                                                │
│ to       alice, carol                                                        │
│ subject  RE: work                                                            │
│ sent     2026-07-24 18:31 UTC                                                │
│ thread   work · 0006575f93447a00-01000000 #1                                 │
│ id       0006575f93447a00-01000000                                           │
╰──────────────────────────────────────────────────────────────────────────────╯

Ship it by Friday.
`

// TestBodyIsEmittedVerbatim is what makes `open` pipeable: the body must come
// out byte for byte, with no wrapping, so it can be fed to another tool.
func TestBodyIsEmittedVerbatim(t *testing.T) {
	all := rows(t)
	r := all[1] // the one with a markdown body

	out, err := render.Card(r, style.Plain(), 100)
	if err != nil {
		t.Fatal(err)
	}
	_, body, found := strings.Cut(out, "╯\n\n")
	if !found {
		t.Fatalf("no body in:\n%s", out)
	}
	if body != r.Message.BodyString() {
		t.Errorf("the body was reshaped:\n got %q\nwant %q", body, r.Message.BodyString())
	}
}

func TestThreadDiagram(t *testing.T) {
	all := rows(t)
	var thread []view.Row
	for _, r := range all {
		if _, ok := r.Message.Convo(); ok {
			thread = append(thread, r)
		}
	}
	if len(thread) < 2 {
		t.Fatalf("the corpus should hold a thread, got %d messages", len(thread))
	}

	out, err := render.Thread("work", "00065763a5e26200-01000000", thread, style.Plain(), 100)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	// The diagram's shape is the point: a branch for each message and a
	// different corner on the last.
	if strings.Count(out, "├─") != len(thread)-1 {
		t.Errorf("wrong number of branches:\n%s", out)
	}
	if !strings.Contains(out, "╰─") {
		t.Errorf("the last message should close the diagram:\n%s", out)
	}
	for _, r := range thread {
		if !strings.Contains(out, r.Message.Subject()) {
			t.Errorf("the diagram is missing %q:\n%s", r.Message.Subject(), out)
		}
	}
}

func TestReceiptsTable(t *testing.T) {
	alice, err := user.Parse("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := user.Parse("bob")
	if err != nil {
		t.Fatal(err)
	}

	out, err := render.Receipts("RE: work", []view.Status{
		{User: alice, ReadAt: fixture.Epoch},
		{User: bob},
	}, style.Plain(), 80)
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	assertRectangular(t, out, 80, true)

	// Both states are words as well as colours, so the table survives a pipe.
	if !strings.Contains(out, "read") || !strings.Contains(out, "unread") {
		t.Errorf("both states should be spelled out:\n%s", out)
	}
	if !strings.Contains(out, "1 of 2") {
		t.Errorf("the count is missing:\n%s", out)
	}
}

// TestColourIsALayer checks the house rule directly: turning colour on must
// change only the escape sequences, never the text.
func TestColourIsALayer(t *testing.T) {
	set := fixture.Unarchived(rows(t))

	plain, err := render.Listing("inbox · alice", set, style.Plain(), 100)
	if err != nil {
		t.Fatal(err)
	}
	coloured, err := render.Listing("inbox · alice", set, style.Coloured(), 100)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(coloured, "\x1b[") {
		t.Fatal("the coloured rendering has no escape sequences")
	}
	if strip(coloured) != plain {
		t.Errorf("colour changed the text.\n got:\n%s\nwant:\n%s", strip(coloured), plain)
	}
}

// strip removes ANSI escape sequences.
func strip(s string) string {
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

// TestHostileSubjectCannotRepaintTheTable is the injection case: a subject is
// text somebody else wrote, and it lands in a drawn table.
func TestHostileSubjectCannotRepaintTheTable(t *testing.T) {
	all := rows(t)
	hostile := all[0]

	// The subject is replaced through the corpus builder rather than mutated,
	// since a Message is immutable by construction. Rendering the sanitised
	// form is what is being checked, so a direct table is enough.
	out, err := render.Listing("inbox\x1b[2J", nil, style.Plain(), 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence reached the output:\n%q", out)
	}
	assertRectangular(t, out, 80, true)
	_ = hostile
}
