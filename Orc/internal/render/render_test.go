package render_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/render"
	"orc/orc/internal/style"
)

// Rendering has two rules that matter more than how anything looks, and both are
// about what happens when there is not enough room:
//
//   - a value is never truncated to make space for its explanation, because a
//     shortened fact is a wrong fact and a shortened explanation is only shorter;
//   - a number is never truncated at all, because an authority level with a
//     digit missing is a different authority level.
//
// The rest is that nothing degenerate — no rows, no fields, a terminal two
// columns wide — may produce a broken frame or an error instead of a screen.

func plain() style.Palette { return style.Plain() }

// drawCard and drawTable render and split, so a test can talk about lines
// without an error check at every call site.
func drawCard(t *testing.T, c render.Card, width int) []string {
	t.Helper()
	out, err := render.DrawCard(c, plain(), width)
	if err != nil {
		t.Fatalf("DrawCard: %v", err)
	}
	return split(out)
}

func drawTable(t *testing.T, tb render.Table, width int) []string {
	t.Helper()
	out, err := render.DrawTable(tb, plain(), width)
	if err != nil {
		t.Fatalf("DrawTable: %v", err)
	}
	return split(out)
}

// split drops the trailing blank, so "the last line" means the bottom of the
// frame rather than what follows it.
func split(out string) []string {
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

// rectangular checks every line is the same display width, which is the one
// property that makes a box a box. It is a *card* invariant: a table's title
// sits above its rule rather than inside a border, so its lines differ by
// design.
func rectangular(t *testing.T, got []string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("nothing was drawn")
	}
	want := len([]rune(got[0]))
	for i, line := range got {
		if n := len([]rune(line)); n != want {
			t.Errorf("line %d is %d wide, the first is %d:\n%s", i+1, n, want, strings.Join(got, "\n"))
			return
		}
	}
}

// --- cards ---------------------------------------------------------------

func TestACardDrawsItsSectionsAndFields(t *testing.T) {
	card := render.Card{
		Title: "scribe",
		Note:  "employed",
		Sections: []render.Section{
			{Fields: []render.Field{{Label: "boss", Value: "operator"}}},
			{Title: "permissions", Fields: []render.Field{
				{Label: "mail", Value: "yes", Note: "from the role"},
			}},
		},
		Footer: "orc introspect says the same from inside",
	}
	got := drawCard(t, card, 72)

	joined := strings.Join(got, "\n")
	for _, want := range []string{"scribe", "employed", "boss", "operator", "permissions", "from the role", "introspect"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card does not show %q:\n%s", want, joined)
		}
	}
	rectangular(t, got)
}

// TestALongNoteGivesUpSpaceBeforeTheValue is the rule the card exists to keep.
//
// A truncated value would be a wrong fact — a path that is not the path, a name
// that is not the name — where a truncated note is only a shorter explanation.
func TestALongNoteGivesUpSpaceBeforeTheValue(t *testing.T) {
	// Short enough to fit the line on its own, long enough that it cannot fit
	// beside the note. Without both, the test says nothing about which gives way.
	const path = "/Users/operator/Dev/Orc/Communique"
	card := render.Card{
		Title: "scribe",
		Sections: []render.Section{{Fields: []render.Field{{
			Label: "workspace",
			Value: path,
			Note:  "set when the identity was employed, and never since changed",
		}}}},
	}
	got := drawCard(t, card, 72)
	joined := strings.Join(got, "\n")

	if !strings.Contains(joined, path) {
		t.Errorf("the value was truncated to make room for its note:\n%s", joined)
	}
	if strings.Contains(joined, "never since changed") {
		t.Errorf("the note was not shortened, so the line cannot have fitted:\n%s", joined)
	}
	rectangular(t, got)
}

// When even the value cannot fit, it is truncated rather than allowed to break
// the frame: a wrong fact is bad, and a card that is not a rectangle is unusable.
func TestAValueLongerThanTheCardIsTruncatedRatherThanOverflowing(t *testing.T) {
	card := render.Card{
		Title: "scribe",
		Sections: []render.Section{{Fields: []render.Field{{
			Label: "workspace",
			Value: strings.Repeat("x", 400),
		}}}},
	}
	rectangular(t, drawCard(t, card, 60))
}

// A section with nothing in it says so. "no permissions" is information; a gap
// is a reader wondering whether the card failed to draw.
func TestAnEmptySectionSaysSoRatherThanLeavingAGap(t *testing.T) {
	card := render.Card{
		Title:    "scribe",
		Sections: []render.Section{{Title: "permissions", Empty: "no permissions"}},
	}
	joined := strings.Join(drawCard(t, card, 60), "\n")
	if !strings.Contains(joined, "no permissions") {
		t.Errorf("an empty section drew nothing:\n%s", joined)
	}
}

// A card with no title is a programming error, not a screen: there is nothing
// truthful to draw at the top of it.
func TestACardWithoutATitleIsRefused(t *testing.T) {
	for _, title := range []string{"", "   "} {
		if _, err := render.DrawCard(render.Card{Title: title}, plain(), 60); err == nil {
			t.Errorf("a card titled %q should be refused", title)
		}
	}
}

// --- tables --------------------------------------------------------------

func fleetTable(rows [][]render.Cell) render.Table {
	return render.Table{
		Title: "fleet",
		Columns: []render.Column{
			{Header: "name", Align: render.Left, Grow: true, Min: 4},
			{Header: "role", Align: render.Left, Min: 4},
			{Header: "auth", Align: render.Right, Min: 4},
		},
		Rows:  rows,
		Empty: "no identities yet",
	}
}

func TestATableDrawsItsRows(t *testing.T) {
	got := drawTable(t, fleetTable([][]render.Cell{
		{render.Text("scribe"), render.Text("writer"), render.Text("3")},
		{render.Text("porter"), render.Text("courier"), render.Text("1")},
	}), 72)

	joined := strings.Join(got, "\n")
	for _, want := range []string{"fleet", "name", "scribe", "courier", "3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the table does not show %q:\n%s", want, joined)
		}
	}
	bounded(t, got, 72)
}

// TestADegenerateFleetStillDraws: an empty fleet, a fleet on a two-column
// terminal, a row of empty strings. None of them may produce a broken frame or
// an error — the first thing a new operator sees is an empty fleet.
func TestADegenerateFleetStillDraws(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table render.Table
		width int
	}{
		{"no rows", fleetTable(nil), 72},
		{"no rows on a narrow terminal", fleetTable(nil), 2},
		{"one row on a narrow terminal", fleetTable([][]render.Cell{
			{render.Text("scribe"), render.Text("writer"), render.Text("3")},
		}), 2},
		{"empty cells", fleetTable([][]render.Cell{
			{render.Text(""), render.Text(""), render.Text("")},
		}), 72},
		{"a name longer than the terminal", fleetTable([][]render.Cell{
			{render.Text(strings.Repeat("n", 300)), render.Text("writer"), render.Text("3")},
		}), 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bounded(t, drawTable(t, tc.table, tc.width), tc.width)
		})
	}
}

// bounded checks a table fits the terminal it was given and that its two rules
// agree. A table draws its title above the frame rather than inside one, so
// "every line the same width" is the wrong question to ask of it — "nothing
// overflows, and the frame lines up" is the right one.
func bounded(t *testing.T, got []string, width int) {
	t.Helper()
	if len(got) < 2 {
		t.Fatalf("nothing was drawn: %q", got)
	}
	limit := render.Clamp(width)
	for i, line := range got {
		if n := len([]rune(line)); n > limit {
			t.Errorf("line %d is %d wide, past the %d it was given:\n%s", i+1, n, limit, strings.Join(got, "\n"))
			return
		}
	}
	if top, bottom := got[0], got[len(got)-1]; top != bottom {
		t.Errorf("the frame does not close:\ntop:    %q\nbottom: %q", top, bottom)
	}
}

// An empty fleet says what it is rather than drawing an empty frame.
func TestAnEmptyTableSaysSo(t *testing.T) {
	joined := strings.Join(drawTable(t, fleetTable(nil), 72), "\n")
	if !strings.Contains(joined, "no identities yet") {
		t.Errorf("an empty table drew no explanation:\n%s", joined)
	}
}

// TestACrampedTableTruncatesTheGrowingColumnOnly is the table's half of the same
// rule the card keeps: a truncated role name still lines up, and a truncated
// authority level is a different authority level.
func TestACrampedTableTruncatesTheGrowingColumnOnly(t *testing.T) {
	got := drawTable(t, fleetTable([][]render.Cell{
		{render.Text("scribe"), render.Text(strings.Repeat("long-role-", 12)), render.Text("12345")},
	}), 44)

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "12345") {
		t.Errorf("the number was truncated, which makes it a different number:\n%s", joined)
	}
	bounded(t, got, 44)
}

// TestNoTableOverflowsTheTerminalItWasGiven is the invariant the second pass in
// measure exists for.
//
// Only growing columns used to give up width, so a table whose long content sat
// in a column that does not grow — a role name on a narrow terminal — drew a rule
// wider than the terminal and wrapped into nonsense. Every column of text now
// gives way in turn; the numbers still never do.
func TestNoTableOverflowsTheTerminalItWasGiven(t *testing.T) {
	long := strings.Repeat("long-role-", 12)
	for _, width := range []int{2, 40, 48, 72} {
		for _, where := range []string{"name", "role"} {
			cells := []render.Cell{render.Text("scribe"), render.Text("writer"), render.Text("12345")}
			if where == "name" {
				cells[0] = render.Text(long)
			} else {
				cells[1] = render.Text(long)
			}
			got := drawTable(t, fleetTable([][]render.Cell{cells}), width)
			bounded(t, got, width)

			// And the number is intact however cramped the rest became.
			if !strings.Contains(strings.Join(got, "\n"), "12345") {
				t.Errorf("width %d, long %s: the number was truncated:\n%s",
					width, where, strings.Join(got, "\n"))
			}
		}
	}
}

// A table whose rows do not match its columns is a programming error, and one
// caught here rather than drawn as a crooked frame.
func TestARowThatDoesNotMatchTheColumnsIsRefused(t *testing.T) {
	table := fleetTable([][]render.Cell{{render.Text("scribe"), render.Text("writer")}})
	_, err := render.DrawTable(table, plain(), 72)
	if err == nil {
		t.Fatal("a short row should be refused")
	}
	if !strings.Contains(err.Error(), "2 cells") {
		t.Errorf("the refusal should say what was wrong: %v", err)
	}
}

// TestAColumnWithNoAlignmentIsRefused pins a trap rather than a behaviour worth
// having: Align has no valid zero value, so a column that omits it fails at draw
// time with an internal fault. Every real caller sets it. Making Left the
// default would remove the trap, and belongs to whoever owns this file.
func TestAColumnWithNoAlignmentIsRefused(t *testing.T) {
	table := render.Table{
		Title:   "fleet",
		Columns: []render.Column{{Header: "name", Min: 4}},
		Rows:    [][]render.Cell{{render.Text("scribe")}},
	}
	_, err := render.DrawTable(table, plain(), 72)
	if !isInternal(err) {
		t.Errorf("error = %v, want an internal fault naming the alignment", err)
	}
}

func TestATableWithoutColumnsIsRefused(t *testing.T) {
	if _, err := render.DrawTable(render.Table{Title: "fleet"}, plain(), 72); err == nil {
		t.Error("a table with no columns should be refused")
	}
}

// --- width ---------------------------------------------------------------

// Clamp is what keeps every one of the above from having to think about a
// terminal that reports nonsense.
func TestClampKeepsAWidthUsable(t *testing.T) {
	for _, tc := range []struct{ given, want int }{
		{0, render.DefaultWidth},
		{-40, render.DefaultWidth},
		{1, render.MinWidth},
		{render.MinWidth - 1, render.MinWidth},
		{100, 100},
		{render.MaxWidth + 1, render.MaxWidth},
	} {
		if got := render.Clamp(tc.given); got != tc.want {
			t.Errorf("Clamp(%d) = %d, want %d", tc.given, got, tc.want)
		}
	}
}

// Colour is a layer, never information: stripping it from a painted screen must
// give back exactly the plain one, so a terminal without colour loses nothing.
func TestColourAddsNothingButColour(t *testing.T) {
	card := render.Card{
		Title: "scribe",
		Note:  "employed",
		Sections: []render.Section{{Title: "permissions", Fields: []render.Field{
			{Label: "boss", Value: "operator", Note: "since employment"},
		}}},
	}
	bare, err := render.DrawCard(card, style.Plain(), 72)
	if err != nil {
		t.Fatal(err)
	}
	painted, err := render.DrawCard(card, style.Coloured(), 72)
	if err != nil {
		t.Fatal(err)
	}
	if painted == bare {
		t.Fatal("the coloured palette painted nothing, so this proves nothing")
	}
	if got := escapes.ReplaceAllString(painted, ""); got != bare {
		t.Errorf("colour changed the text:\nplain:   %q\nstripped: %q", bare, got)
	}
}

var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Nothing in this package panics on a violated invariant; it returns.
func TestRefusalsAreFaultsNotPanics(t *testing.T) {
	if _, err := render.DrawCard(render.Card{}, plain(), 72); !isInternal(err) {
		t.Errorf("DrawCard: %v, want an internal fault", err)
	}
	if _, err := render.DrawTable(render.Table{}, plain(), 72); !isInternal(err) {
		t.Errorf("DrawTable: %v, want an internal fault", err)
	}
}

func isInternal(err error) bool { return errors.Is(err, fault.ErrInternal) }
