package logbook_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/logbook"
)

// The three cycles that keep a fleet alive used to write to the null device, so
// none of this existed. What matters now is that it stays bounded on somebody
// else's machine, that the tail says what the level was, and that an absent log
// and an unreadable one stay different facts.

func write(t *testing.T, home string, k logbook.Kind, lines ...string) {
	t.Helper()
	w, err := logbook.Open(home, k)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestACycleWritesAndIsReadBack(t *testing.T) {
	home := t.TempDir()
	write(t, home, logbook.Sync, "level=INFO msg=mirrored", `level=ERROR msg="server refused"`)

	got, err := logbook.Tail(home, logbook.Sync, 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d lines, want 2", len(got))
	}
	if got[0].Level != "INFO" || got[1].Level != "ERROR" {
		t.Errorf("levels came back as %q and %q", got[0].Level, got[1].Level)
	}
}

func TestALineWithNoLevelKeepsItsTextAndGetsNone(t *testing.T) {
	// A child process's output is not slog's. Inventing a severity for it would
	// have the screen colouring something it made up.
	home := t.TempDir()
	write(t, home, logbook.Wake, "orc: restarting into the new build")

	got, _ := logbook.Tail(home, logbook.Wake, 10)
	if len(got) != 1 {
		t.Fatalf("read %d lines, want 1", len(got))
	}
	if got[0].Level != "" {
		t.Errorf("an unlevelled line was given the level %q", got[0].Level)
	}
	if !strings.Contains(got[0].Text, "restarting") {
		t.Errorf("the text did not survive: %q", got[0].Text)
	}
}

func TestACycleThatHasNeverRunIsNoLinesAndNoError(t *testing.T) {
	// Most machines run one of the three and not the other two. That is a state to
	// draw, not a failure to report.
	got, err := logbook.Tail(t.TempDir(), logbook.Tend, 10)
	if err != nil {
		t.Fatalf("Tail on a cycle that has never run: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("read %d lines from nothing", len(got))
	}
}

func TestTheTailIsTheEndAndIsBounded(t *testing.T) {
	home := t.TempDir()
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("level=INFO msg=round n=%d", i)
	}
	write(t, home, logbook.Sync, lines...)

	got, _ := logbook.Tail(home, logbook.Sync, 5)
	if len(got) != 5 {
		t.Fatalf("asked for 5 lines and got %d", len(got))
	}
	if !strings.Contains(got[4].Text, "n=99") {
		t.Errorf("the last line is %q, not the last thing written", got[4].Text)
	}
	// Asking for more than the cap gets the cap, because this rides in every
	// snapshot and a caller must not be able to make that unbounded.
	if got, _ := logbook.Tail(home, logbook.Sync, 10_000); len(got) > logbook.MaxTail {
		t.Errorf("read %d lines, over the %d cap", len(got), logbook.MaxTail)
	}
}

func TestALogThatGrewTooLargeLosesItsOldestHalfAndNotAllOfIt(t *testing.T) {
	// This is an unattended file on somebody's machine, so it has to be bounded.
	// Truncating to nothing would mean the moment a log fills is the moment its
	// evidence disappears — reliably the moment somebody needed it.
	home := t.TempDir()
	big := strings.Repeat("level=INFO msg=padding filler=xxxxxxxxxxxxxxxxxxxxxxxx\n", 8000)
	if err := os.MkdirAll(logbook.Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logbook.Path(home, logbook.Sync), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := size(t, home); got <= logbook.MaxBytes {
		t.Fatalf("the fixture is only %d bytes; it must start over the %d bound", got, logbook.MaxBytes)
	}

	write(t, home, logbook.Sync, "level=INFO msg=after")

	if got := size(t, home); got > logbook.MaxBytes {
		t.Errorf("the log is still %d bytes, over the %d bound", got, logbook.MaxBytes)
	}
	got, _ := logbook.Tail(home, logbook.Sync, logbook.MaxTail)
	if len(got) == 0 {
		t.Fatal("trimming emptied the log")
	}
	if !strings.Contains(got[len(got)-1].Text, "after") {
		t.Error("the line written after the trim is not the last one")
	}
	// The kept half must not start with the tail of a line the trim cut through.
	if first := got[0].Text; !strings.HasPrefix(first, "level=") {
		t.Errorf("the log now starts mid-line: %q", first)
	}
}

func size(t *testing.T, home string) int64 {
	t.Helper()
	info, err := os.Stat(logbook.Path(home, logbook.Sync))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return info.Size()
}

func TestACycleNameCannotBecomeAPath(t *testing.T) {
	// These become file names. A path assembled from whatever a caller passed is
	// how a log directory grows a `../`.
	home := t.TempDir()
	if _, err := logbook.Open(home, logbook.Kind("../../escape")); err == nil {
		t.Error("a cycle called ../../escape was opened")
	}
	if _, err := logbook.Tail(home, logbook.Kind("../../escape"), 10); err == nil {
		t.Error("a cycle called ../../escape was read")
	}
	if _, err := os.Stat(filepath.Join(home, "..", "..", "escape.log")); err == nil {
		t.Error("a file was written outside the log directory")
	}
}

func TestAppendingDoesNotTruncateWhatAnEarlierRunWrote(t *testing.T) {
	// A watcher restarted into a new build continues the same log. The restart is
	// usually the thing being investigated, so losing what came before it would
	// lose the question.
	home := t.TempDir()
	write(t, home, logbook.Tend, "level=INFO msg=before")
	write(t, home, logbook.Tend, "level=INFO msg=after")

	got, _ := logbook.Tail(home, logbook.Tend, 10)
	if len(got) != 2 {
		t.Fatalf("read %d lines after two runs, want 2", len(got))
	}
	if !strings.Contains(got[0].Text, "before") {
		t.Error("the second run truncated the first")
	}
}
