package render_test

import (
	"strings"
	"testing"

	"orc/common/user"
	"orc/macmuffin/internal/fixture"
	"orc/macmuffin/internal/render"
	"orc/macmuffin/internal/style"
	"orc/macmuffin/internal/task"
	"orc/macmuffin/internal/view"
	"orc/theme"
)

func pool(t *testing.T, scope view.Scope) view.Pool {
	t.Helper()
	all, err := fixture.Tasks()
	if err != nil {
		t.Fatalf("building the corpus: %v", err)
	}
	viewer, err := user.Parse(fixture.Viewer)
	if err != nil {
		t.Fatal(err)
	}

	kept := all
	if scope == view.Active {
		kept = nil
		for _, got := range all {
			if !got.Completed() {
				kept = append(kept, got)
			}
		}
	}
	p, err := view.Of(viewer, kept)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func named(t *testing.T, want string) task.Task {
	t.Helper()
	got, err := fixture.Named(want)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestBoardIsRectangular is the property a golden would only catch by accident:
// every line the same width, every divider in the same column.
func TestBoardIsRectangular(t *testing.T) {
	for _, scope := range []view.Scope{view.Active, view.All} {
		for _, width := range []int{60, 80, 100, 140, 200} {
			out, err := render.Board(pool(t, scope), scope, style.Plain(), width)
			if err != nil {
				t.Fatalf("Board at width %d: %v", width, err)
			}
			assertRectangular(t, out, width, width >= 100)
		}
	}
	// An empty pool must still produce a well-formed frame.
	empty, err := view.Of(mustUser(t, "alice"), nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := render.Board(empty, view.Active, style.Plain(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no tasks") {
		t.Errorf("an empty board should say so:\n%s", out)
	}
	assertRectangular(t, out, 100, true)
}

func assertRectangular(t *testing.T, out string, width int, mustFit bool) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("a table should have at least four lines:\n%s", out)
	}
	want := theme.Width(lines[0])
	if mustFit && want > width {
		t.Errorf("the table is %d cells wide, past the %d it was given:\n%s", want, width, out)
	}
	for i, line := range lines {
		if got := theme.Width(line); got != want {
			t.Errorf("line %d is %d cells, want %d:\n%s", i+1, got, want, out)
		}
	}
}

// TestCardIsRectangular checks every task in the corpus, including the stub
// with no scope and the one with a long checklist.
func TestCardIsRectangular(t *testing.T) {
	all, err := fixture.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{60, 80, 100, 140} {
		for _, got := range all {
			out, err := render.Card(got, style.Plain(), width)
			if err != nil {
				t.Fatalf("Card(%s) at width %d: %v", got.Name(), width, err)
			}
			assertRectangular(t, out, width, true)
		}
	}
}

// TestColourStripsToThePlainRendering is milestone 4's acceptance criterion,
// and the reason colour can never be information: turning it on must change
// only the escape sequences.
func TestColourStripsToThePlainRendering(t *testing.T) {
	for _, scope := range []view.Scope{view.Active, view.All} {
		plain, err := render.Board(pool(t, scope), scope, style.Plain(), 120)
		if err != nil {
			t.Fatal(err)
		}
		coloured, err := render.Board(pool(t, scope), scope, style.Coloured(), 120)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(coloured, "\x1b[") {
			t.Fatal("the coloured board has no escape sequences")
		}
		if strip(coloured) != plain {
			t.Errorf("colour changed the board.\n got:\n%s\nwant:\n%s", strip(coloured), plain)
		}
	}

	all, err := fixture.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range all {
		plain, err := render.Card(got, style.Plain(), 100)
		if err != nil {
			t.Fatal(err)
		}
		coloured, err := render.Card(got, style.Coloured(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if strip(coloured) != plain {
			t.Errorf("colour changed the card for %s.\n got:\n%s\nwant:\n%s", got.Name(), strip(coloured), plain)
		}
	}
}

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

// TestTheMeterNeverLies: a task with work left must never show a full bar, and
// one with any progress must never show an empty one.
func TestTheMeter(t *testing.T) {
	if got := render.Meter(named(t, "think-about-caching")); got != "—" {
		t.Errorf("a task with no subtasks metered %q", got)
	}

	partial := render.Meter(named(t, "fix-the-parser")) // 5 of 8
	if !strings.Contains(partial, "5/8") {
		t.Errorf("the meter should carry its numbers: %q", partial)
	}
	if !strings.Contains(partial, "░") {
		t.Errorf("an unfinished task must show an empty block: %q", partial)
	}

	full := render.Meter(named(t, "retire-the-old-hook")) // 1 of 1
	if strings.Contains(full, "░") {
		t.Errorf("a finished checklist should have no empty blocks: %q", full)
	}
	if !strings.Contains(full, "1/1") {
		t.Errorf("the meter should carry its numbers: %q", full)
	}
}

// TestStateIsReadableWithoutColour: every status and every task state carries a
// word, so a pipe through grep keeps the meaning.
func TestStateIsReadableWithoutColour(t *testing.T) {
	out, err := render.Board(pool(t, view.All), view.All, style.Plain(), 140)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"draft", "done", "nominal", "broken", "unreported"} {
		if !strings.Contains(out, want) {
			t.Errorf("the board should spell out %q:\n%s", want, out)
		}
	}
}

// TestCompletedTasksLeaveTheBoard is §6's retention rule stated as a diff: the
// two scopes differ by exactly the completed row.
func TestCompletedTasksLeaveTheBoard(t *testing.T) {
	active, err := render.Board(pool(t, view.Active), view.Active, style.Plain(), 140)
	if err != nil {
		t.Fatal(err)
	}
	all, err := render.Board(pool(t, view.All), view.All, style.Plain(), 140)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(active, "retire-the-old-hook") {
		t.Errorf("a completed task should leave the board:\n%s", active)
	}
	if !strings.Contains(all, "retire-the-old-hook") {
		t.Errorf("--all should bring it back:\n%s", all)
	}
	// And it sinks below the active work rather than interleaving.
	if i, j := strings.Index(all, "retire-the-old-hook"), strings.Index(all, "ship-the-docs"); i < j {
		t.Error("completed tasks should sort below active ones")
	}
}

func mustUser(t *testing.T, s string) user.Name {
	t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestBoardGolden pins the layout exactly. Colour is off, so the constant is
// readable in a diff and a change to the drawing breaks one test.
func TestBoardGolden(t *testing.T) {
	got, err := render.Board(pool(t, view.Active), view.Active, style.Plain(), 110)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if got != goldenBoard {
		t.Errorf("the board layout changed.\n got:\n%s\nwant:\n%s", got, goldenBoard)
	}
}

// TestBoardAllGolden differs from the board above by exactly the completed row,
// which is the whole of the retention rule stated as a diff.
func TestBoardAllGolden(t *testing.T) {
	got, err := render.Board(pool(t, view.All), view.All, style.Plain(), 110)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if got != goldenBoardAll {
		t.Errorf("the --all board changed.\n got:\n%s\nwant:\n%s", got, goldenBoardAll)
	}
}

func TestCardGolden(t *testing.T) {
	got, err := render.Card(named(t, "fix-the-parser"), style.Plain(), 100)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if got != goldenCard {
		t.Errorf("the card layout changed.\n got:\n%s\nwant:\n%s", got, goldenCard)
	}
}

const goldenBoard = `┌───────────────────────────────────────────────────────────────────────────────────┐
│ pool · alice                                                   3 active · 1 draft │
├──────────────────────────────┬───┬───┬──────────────┬─────────────┬───────┬───────┤
│ task                         │ P │ D │ status       │ progress    │ owner │ with  │
├──────────────────────────────┼───┼───┼──────────────┼─────────────┼───────┼───────┤
│ ship-the-docs                │ 5 │ 2 │ · unreported │ —           │ —     │ —     │
│ fix-the-parser               │ 4 │ 3 │ ● nominal    │ ▓▓▓▓░░░ 5/8 │ bob   │ carol │
│ sweep-the-store              │ 2 │ 4 │ ✗ broken     │ ░░░░░░░ 0/2 │ dave  │ —     │
│ think-about-caching  (draft) │ 1 │ 5 │ · unreported │ —           │ —     │ —     │
└──────────────────────────────┴───┴───┴──────────────┴─────────────┴───────┴───────┘
`

const goldenBoardAll = `┌───────────────────────────────────────────────────────────────────────────────────┐
│ pool · alice                                          3 active · 1 draft · 1 done │
├──────────────────────────────┬───┬───┬──────────────┬─────────────┬───────┬───────┤
│ task                         │ P │ D │ status       │ progress    │ owner │ with  │
├──────────────────────────────┼───┼───┼──────────────┼─────────────┼───────┼───────┤
│ ship-the-docs                │ 5 │ 2 │ · unreported │ —           │ —     │ —     │
│ fix-the-parser               │ 4 │ 3 │ ● nominal    │ ▓▓▓▓░░░ 5/8 │ bob   │ carol │
│ sweep-the-store              │ 2 │ 4 │ ✗ broken     │ ░░░░░░░ 0/2 │ dave  │ —     │
│ think-about-caching  (draft) │ 1 │ 5 │ · unreported │ —           │ —     │ —     │
│ retire-the-old-hook  (done)  │ 3 │ 1 │ ✓ done       │ ▓▓▓▓▓▓▓ 1/1 │ alice │ —     │
└──────────────────────────────┴───┴───┴──────────────┴─────────────┴───────┴───────┘
`

// The one line with backticks in it is spliced: a Go raw string cannot contain one,
// and the card names the command that writes a description the way every other hint
// in this file does.
const goldenCard = `╭─ fix-the-parser ──────────────────────────────────────────────────────── P4  D3  ● nominal  5/8 ─╮
│ owner      bob                                                                                   │
│ author     alice                                                                                 │
│ created    2026-07-24 18:31                                                                      │
│ state      in the pool                                                                           │
│ with       carol                                                                                 │
│ worktree   ../orc-parser                                                                         │
│ described  none yet — ` + "`muff describe fix-the-parser --edit`" + `                                      │
├─ scope ──────────────────────────────────────────────────────────────────────────────────────────┤
│   internal/tree/                                                                                 │
│   internal/marker/                                                                               │
│   cmd/anno/main.go                                                                               │
├─ subtasks ───────────────────────────────────────────────────────────────────────────────────────┤
│ ✓ recover-the-grammar                                                                            │
│ ✓ table-the-sigils                                                                               │
│ ✓ pin-the-example                                                                                │
│ ✓ golden-the-index                                                                               │
│ ✓ classify-every-sigil                                                                           │
│ ○ fuzz-the-parser                                                                                │
│ ○ wire-the-hook                                                                                  │
│ ○ document-the-closers                                                                           │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
`
