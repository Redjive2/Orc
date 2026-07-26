package view_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/orc/internal/view"
)

func TestReadsTheFixtureTranscript(t *testing.T) {
	got, ok := view.ReadProse(filepath.Join("testdata", "transcript.jsonl"))
	if !ok {
		t.Fatal("the fixture transcript could not be read")
	}
	if len(got) != 3 {
		t.Fatalf("%d lines, want 3 (the tool_use block is not prose):\n%+v", len(got), got)
	}
	if got[0].Who != view.Human {
		t.Errorf("the first line is the operator's: %+v", got[0])
	}
	if !strings.Contains(got[2].Text, "outside my write scope") {
		t.Errorf("the last thing said was lost: %+v", got[2])
	}
}

// TestToolCallsAreNotProse. They are already in the event feed, with Orc's own
// verdict attached; taking them from here as well would show every action twice and
// disagree about half of them.
func TestToolBlocksAreSkipped(t *testing.T) {
	for _, p := range mustRead(t, filepath.Join("testdata", "transcript.jsonl")) {
		if strings.Contains(p.Text, "file_path") || strings.Contains(p.Text, "tool_use") {
			t.Errorf("a tool call was rendered as prose: %+v", p)
		}
	}
}

func mustRead(t *testing.T, path string) []view.Prose {
	t.Helper()
	got, ok := view.ReadProse(path)
	if !ok {
		t.Fatal("could not read")
	}
	return got
}

// TestTheTranscriptIsClaudesFormat: everything about reading it degrades, because
// its shape is a compatibility surface and an upgrade must not become an outage of
// the view.
func TestUnrecognisedShapesCostOnlyTheLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"kept"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"a bare string, which claude has also sent"}}`,
		`{"type":"summary","summary":"a shape this build has no opinion about"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"not shown"}]}}`,
		`not json at all`,
		``,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"also kept"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := view.ReadProse(path)
	if !ok {
		t.Fatal("a transcript with unrecognised lines could not be read at all")
	}
	if len(got) != 3 {
		t.Fatalf("%d lines, want the three that said something:\n%+v", len(got), got)
	}
	if got[len(got)-1].Text != "also kept" {
		t.Errorf("reading stopped early: %+v", got)
	}
}

// A missing transcript is reported as unreadable rather than as empty, because the
// pane shows those differently — "it said nothing" and "I could not look" are not
// the same thing to somebody deciding whether to attach directly.
func TestMissingTranscript(t *testing.T) {
	if _, ok := view.ReadProse(filepath.Join(t.TempDir(), "nothing.jsonl")); ok {
		t.Error("a missing transcript reported itself readable")
	}
	if _, ok := view.ReadProse(""); ok {
		t.Error("an empty path reported itself readable")
	}
}

// TestOnlyTheTailIsRead. A long session's transcript is megabytes and the pane shows
// three lines; reading it all on every redraw would make the view cost more than the
// session it is watching.
func TestLargeTranscriptReadsTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")

	var b strings.Builder
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}` + "\n"
	for b.Len() < view.MaxTranscriptBytes+(64<<10) {
		b.WriteString(strings.Replace(line, "%s", strings.Repeat("padding ", 40), 1))
	}
	b.WriteString(strings.Replace(line, "%s", "the last thing said", 1))

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := view.ReadProse(path)
	if !ok {
		t.Fatal("a large transcript could not be read")
	}
	if len(got) == 0 {
		t.Fatal("no prose came back")
	}
	if !strings.Contains(got[len(got)-1].Text, "the last thing said") {
		t.Errorf("the tail was not what was read: %q", got[len(got)-1].Text)
	}
	if len(got) > view.MaxProse {
		t.Errorf("%d lines kept, want at most %d", len(got), view.MaxProse)
	}
}

// Control characters in a transcript would be drawn into the pane, where an escape
// sequence could repaint every row below it.
func TestProseIsSanitised(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"before\u001b[31mred\u001b[0m after\nand a newline"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := mustRead(t, path)
	if len(got) != 1 {
		t.Fatalf("%d lines", len(got))
	}
	if strings.ContainsRune(got[0].Text, 0x1b) {
		t.Errorf("an escape sequence survived into the pane: %q", got[0].Text)
	}
	if strings.ContainsRune(got[0].Text, '\n') {
		t.Errorf("a newline survived into a single-line row: %q", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "and a newline") {
		t.Errorf("the words were lost with the whitespace: %q", got[0].Text)
	}
}
