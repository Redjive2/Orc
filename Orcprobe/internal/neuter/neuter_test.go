package neuter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/orcprobe/internal/clock"
)

func fixed() clock.Clock {
	return clock.NewFake(time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC), time.Second)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// task writes one Macmuffin task journal, in the format its plan §4.1 gives.
func task(t *testing.T, root, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(root, tasksDir, name, journalFile)
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	write(t, path, body)
	return path
}

func events(t *testing.T, path string) []event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("the scrub left a line that does not parse: %q", line)
		}
		out = append(out, ev)
	}
	return out
}

// TestOwnedTasksAreReportedNotForged is the correction to what this package
// first did.
//
// Releasing an owner has no valid event: Macmuffin refuses an op it does not
// know, and its `leave` refuses an owner outright ("a task is never orphaned by
// accident"). Appending a `release` anyway would not release the task — it
// would make the whole journal unreadable inside the probe, which is worse than
// the thing it was trying to fix. So the owner stays and the probe says so.
func TestOwnedTasksAreReportedNotForged(t *testing.T) {
	root := t.TempDir()
	path := task(t, root, "refactor",
		`{"op":"scope","by":"alice","paths":["internal/"],"at":"2026-07-01T09:00:00.000Z"}`,
		`{"op":"push","by":"alice","at":"2026-07-01T09:01:00.000Z"}`,
		`{"op":"claim","by":"bob","at":"2026-07-01T09:02:00.000Z"}`,
		`{"op":"status","by":"bob","value":3,"at":"2026-07-01T09:03:00.000Z"}`,
	)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Released) != 0 {
		t.Fatalf("claimed to release %v; there is no valid event for it", rep.Released)
	}
	if len(rep.Unreleased) != 1 || rep.Unreleased[0].Task != "refactor" || rep.Unreleased[0].Owner != "bob" {
		t.Fatalf("reported %v, want refactor still owned by bob", rep.Unreleased)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the journal was written to anyway:\n%s", string(after))
	}

	var deferred bool
	for _, c := range rep.Changes {
		if c.Act == ActDefer && strings.Contains(c.Detail, "still owned by bob") {
			deferred = true
		}
	}
	if !deferred {
		t.Fatal("an owner that could not be released was not recorded as a deferred guarantee")
	}
}

// TestNeverWritesAnOpMacmuffinRefuses is the guard that keeps the mistake from
// coming back. Macmuffin hard-errors on an unknown op, so a journal orcprobe
// touched must contain only ops Macmuffin defines.
func TestNeverWritesAnOpMacmuffinRefuses(t *testing.T) {
	// Macmuffin's vocabulary, from Macmuffin/internal/task/event.go.
	known := map[string]bool{
		"scope": true, "push": true, "claim": true, "status": true,
		"invite": true, "kick": true, "leave": true,
		"sub.add": true, "sub.done": true, "sub.del": true,
		"complete": true, "worktree": true,
	}

	root := t.TempDir()
	path := task(t, root, "refactor",
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`,
		`{"op":"invite","by":"bob","agent":"carol","at":"2026-07-01T09:01:00.000Z"}`,
	)
	if _, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()}); err != nil {
		t.Fatal(err)
	}

	for _, ev := range events(t, path) {
		if !known[ev.Op] {
			t.Fatalf("the scrub wrote op %q, which macmuffin refuses — the whole task would become unreadable", ev.Op)
		}
	}
}

// TestAppendsLeaveNoBlankLines guards a mistake that already happened once: an
// append that could not read the file's last byte added a newline every time,
// leaving a blank line between every event. Orcprobe's own replay skips those,
// so nothing here would have failed — but Macmuffin's replay is another tool's
// code, and a journal only this tool can read is not a journal.
func TestAppendsLeaveNoBlankLines(t *testing.T) {
	root := t.TempDir()
	path := task(t, root, "refactor",
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`,
		`{"op":"invite","by":"bob","agent":"carol","at":"2026-07-01T09:01:00.000Z"}`,
	)

	if _, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Fatalf("the journal does not end with one complete line:\n%q", string(data))
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("line %d is blank; another tool's replay may read that as corruption", i+1)
		}
	}
}

func TestRemovesCollaborators(t *testing.T) {
	root := t.TempDir()
	path := task(t, root, "refactor",
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`,
		`{"op":"invite","by":"bob","agent":"carol","at":"2026-07-01T09:01:00.000Z"}`,
		`{"op":"invite","by":"bob","agent":"dave","at":"2026-07-01T09:02:00.000Z"}`,
		`{"op":"kick","by":"bob","agent":"dave","at":"2026-07-01T09:03:00.000Z"}`,
	)

	rep, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Collaborators != 1 {
		t.Fatalf("removed %d collaborators, want 1 — dave was already kicked", rep.Collaborators)
	}

	got := events(t, path)
	last := got[len(got)-1]
	// A collaborator leaving is ordinary Macmuffin, so this one is appended.
	if last.Op != opLeave || last.By != "carol" {
		t.Fatalf("last event is %+v, want carol leaving", last)
	}
	if _, err := clock.Parse(last.At); err != nil {
		t.Fatalf("the appended event has an unreadable timestamp %q", last.At)
	}
	// History is untouched: the original claim is still there.
	if got[0].Op != "claim" || got[0].By != "bob" {
		t.Fatal("the original claim was edited; a probe must not rewrite history")
	}
}

func TestLeavesUnclaimedTasksAlone(t *testing.T) {
	root := t.TempDir()
	path := task(t, root, "draft",
		`{"op":"scope","by":"alice","paths":["internal/"],"at":"2026-07-01T09:00:00.000Z"}`,
	)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Released) != 0 {
		t.Fatal("an unowned task was released")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a task nobody held was written to")
	}
}

// TestReleaseSurvivesAnInterruptedAppend covers the journal the copy caught
// mid-write: appending onto it must not weld two half-lines together.
func TestReleaseSurvivesAnInterruptedAppend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, tasksDir, "refactor", journalFile)
	write(t, path,
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`+"\n"+
			`{"op":"invite","by":"bob","agent":"carol","at":"2026-07-01T09:01:00.000Z"}`+"\n"+
			`{"op":"stat`)

	if _, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()}); err != nil {
		t.Fatalf("a torn final line failed the scrub: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `{"op":"stat{`) {
		t.Fatal("the release was welded onto the truncated line")
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var ev event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ev); err != nil {
		t.Fatalf("the last line does not parse: %q", lines[len(lines)-1])
	}
	if ev.Op != opLeave {
		t.Fatalf("last event is %q, want the appended leave", ev.Op)
	}
}

func TestCorruptionInTheMiddleIsRefused(t *testing.T) {
	root := t.TempDir()
	task(t, root, "refactor",
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`,
		`not json at all`,
		`{"op":"status","by":"bob","value":3,"at":"2026-07-01T09:02:00.000Z"}`,
	)

	if _, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()}); err == nil {
		t.Fatal("a corrupt journal line was scrubbed over; silently dropping one would drop a claim")
	}
}

func TestUnknownOpsArePassedOver(t *testing.T) {
	root := t.TempDir()
	task(t, root, "refactor",
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`,
		`{"op":"something.macmuffin.added.later","by":"bob","at":"2026-07-01T09:01:00.000Z"}`,
	)

	rep, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()})
	if err != nil {
		t.Fatalf("an op orcprobe has not heard of failed the scrub: %v", err)
	}
	if len(rep.Unreleased) != 1 {
		t.Fatal("the claim was not found past the unknown op")
	}
}

func TestDropsWorktreeBindingsAndOutbox(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, worktreesDir, "abc123.json"), `{"path":"/Users/x/Dev/Orc","task":"refactor"}`)
	write(t, filepath.Join(root, outboxDir, "1.json"), `{"to":"carol","subject":"you were invited"}`)

	rep, err := Run(Spec{MacmuffinDir: root, ProbeDir: root, Clock: fixed()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Worktrees != 1 || rep.Outbox != 1 {
		t.Fatalf("dropped %d worktrees and %d notifications, want 1 and 1", rep.Worktrees, rep.Outbox)
	}
	for _, dir := range []string{worktreesDir, outboxDir} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("%s still holds %d file(s)", dir, len(entries))
		}
	}
}

func TestResetsTheSyncCursor(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, cursorFile), `{"watermark":42}`)
	write(t, filepath.Join(root, pendingFile), "")
	write(t, filepath.Join(root, appliedFile), "{\"id\":\"a1\",\"outcome\":\"ok\"}\n")
	write(t, filepath.Join(root, "sync-token"), "secret")

	rep, err := Run(Spec{CQDir: root, ProbeDir: root, Clock: fixed()})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{cursorFile, pendingFile, "sync-token"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the scrub", name)
		}
	}
	// applied.jsonl is history, not liveness: losing it would make the probe
	// misrepresent what actually happened.
	if _, err := os.Stat(filepath.Join(root, appliedFile)); err != nil {
		t.Fatal("applied.jsonl was removed; it is history, not liveness")
	}
	if len(rep.Changes) == 0 {
		t.Fatal("the cursor reset was not reported")
	}
}

func TestDisablesHooksPointingOutsideTheProbe(t *testing.T) {
	probeDir := t.TempDir()
	claudeDir := filepath.Join(probeDir, "claude")
	settings := `{
	  "model": "opus",
	  "hooks": {
	    "PostToolUse": [
	      {"matcher": "Read", "hooks": [{"type": "command", "command": "anno-hook", "timeout": 10}]},
	      {"matcher": "Edit", "hooks": [{"type": "command", "command": "/Users/x/.local/bin/rogue-hook"}]}
	    ]
	  }
	}`
	write(t, filepath.Join(claudeDir, "settings.json"), settings)

	rep, err := Run(Spec{ClaudeDir: claudeDir, ProbeDir: probeDir, Clock: fixed()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hooks) != 1 || !strings.Contains(rep.Hooks[0], "rogue-hook") {
		t.Fatalf("disabled %v, want just the absolute one outside the probe", rep.Hooks)
	}

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "rogue-hook") {
		t.Fatalf("the outside hook survived:\n%s", text)
	}
	// A bare command resolves through the probe's PATH, where the shims are, so
	// it is exactly what a probe wants and must be kept.
	if !strings.Contains(text, "anno-hook") {
		t.Fatalf("a bare hook command was removed:\n%s", text)
	}
	// Everything else in the file is left alone.
	if !strings.Contains(text, `"model"`) {
		t.Fatalf("unrelated settings were lost:\n%s", text)
	}
}

func TestUnreadableSettingsAreLeftAloneAndReported(t *testing.T) {
	probeDir := t.TempDir()
	claudeDir := filepath.Join(probeDir, "claude")
	write(t, filepath.Join(claudeDir, "settings.json"), "{ this is not json")

	rep, err := Run(Spec{ClaudeDir: claudeDir, ProbeDir: probeDir, Clock: fixed()})
	if err != nil {
		t.Fatalf("an unreadable settings file failed the scrub: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{ this is not json" {
		t.Fatal("a file orcprobe could not parse was rewritten on a guess")
	}
	var reported bool
	for _, c := range rep.Changes {
		if strings.Contains(c.Detail, "could not be read") {
			reported = true
		}
	}
	if !reported {
		t.Fatal("the unreadable file was not reported")
	}
}

func TestRunNeedsAClock(t *testing.T) {
	if _, err := Run(Spec{MacmuffinDir: t.TempDir()}); err == nil {
		t.Fatal("the scrub ran without a clock; its appended events would carry no time")
	}
}
