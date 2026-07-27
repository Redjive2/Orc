package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/fault"
)

// The wake cycle's whole risk is that it talks over agents that are working, so most
// of what is tested here is what it does *not* poke.
//
// The decisions are driven through --dry-run: waking for real needs a live
// supervisor on a socket, and a fake one would be testing the fake. What reaches the
// session is `poke`'s path, which has its own tests.

// feed writes an events.jsonl for an identity, with each line a given age.
//
// The schema is the settled one from Finish.md, so a hand-written feed is a valid
// input — which is what lets the cycle be tested with no hook, no session, and no
// clock but the fake.
func feed(t *testing.T, r *rig, who string, events ...string) {
	t.Helper()

	dir := filepath.Join(r.root, "identities", who, "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(events, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ago renders an event of the given kind, that many minutes before now.
func ago(minutes int, name, tool, path string) string {
	at := epoch.Add(-time.Duration(minutes) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	return fmt.Sprintf(`{"at":%q,"session":"s","event":%q,"tool":%q,"path":%q}`, at, name, tool, path)
}

// A fleet with one employed agent, and a session on disk for it.
func wakeable(t *testing.T) *rig {
	t.Helper()
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	return r
}

// TestAWaitingAgentIsWoken — the case the cycle exists for. An agent that finished a
// turn and stopped is not broken; it is waiting for somebody to speak.
func TestWakeWakesTheSilent(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember", ago(40, "Stop", "", ""))

	got := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "would wake") || !strings.Contains(got.stdout, "ember") {
		t.Errorf("a silent agent was not woken:\n%s", got.stdout)
	}
	// It says how long, because "woke ember" without it gives an operator no way
	// to tell a fleet that idles for a minute from one that idles for an hour.
	if !strings.Contains(got.stdout, "waiting") {
		t.Errorf("the wake does not say how long it had been silent:\n%s", got.stdout)
	}
}

// TestAnAgentMidTurnIsLeftAlone. The most important thing this does not do: a
// session running a long build is silent for good reasons, and a poke would queue a
// nudge into the middle of work it is already doing.
func TestWakeLeavesWorkingAgentsAlone(t *testing.T) {
	r := wakeable(t)
	// An hour since it said anything, but the last thing it said was that it had
	// started a tool — it is working, not waiting.
	feed(t, r, "ember", ago(60, "PreToolUse", "Bash", "go test ./..."))

	got := r.ok("boss", "wake", "--dry-run")
	if strings.Contains(got.stdout, "would wake") {
		t.Errorf("an agent mid-turn was woken:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "all working") {
		t.Errorf("the pass should say the fleet is working:\n%s", got.stdout)
	}
}

// Silence shorter than the threshold is somebody thinking between turns.
func TestWakeRespectsTheThreshold(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember", ago(3, "Stop", "", ""))

	if got := r.ok("boss", "wake", "--dry-run"); strings.Contains(got.stdout, "would wake") {
		t.Errorf("an agent quiet for three minutes was woken at the ten-minute default:\n%s", got.stdout)
	}
	// And the threshold is what decides it.
	got := r.ok("boss", "wake", "--after", "1m", "--dry-run")
	if !strings.Contains(got.stdout, "would wake") {
		t.Errorf("--after 1m did not lower the bar:\n%s", got.stdout)
	}
}

// startedAgo backdates a session's start, which is what a feedless session is
// judged from. The fake clock does not advance on its own, so a test that wants an
// old session has to say so.
func startedAgo(t *testing.T, r *rig, who string, minutes int) {
	t.Helper()

	path := filepath.Join(r.root, "identities", who, "session", "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["started"] = epoch.Add(-time.Duration(minutes) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")

	out, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAnAgentThatNeverSpokeIsWoken. Up for an hour and never called a tool is as
// stopped as one that finished and waited, and the more worrying of the two.
func TestWakeWakesASessionWithNoEvents(t *testing.T) {
	r := wakeable(t)
	// No feed at all, and the session has been up for an hour.
	startedAgo(t, r, "ember", 60)

	got := r.run("boss", "wake", "--after", "1m", "--dry-run")

	if got.code != fault.CodeOK {
		t.Fatalf("exited %d\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "would wake") {
		t.Errorf("a session that has never said anything was not woken:\n%s", got.stdout)
	}
}

// TestARestartedSessionIsWoken — the state a fleet spends most of its idle time in,
// and the one the cycle used to be blind to.
//
// A session that has just started has no Stop to its name: Stop fires at the end of
// a turn and it has not had one. Its feed ends at SessionStart, which read as
// working — so a supervisor restart, an `orc refresh`, or an agent employed and not
// yet given a first instruction left an agent sitting at an empty prompt that `wake`
// would never poke, however long the loop ran.
func TestWakeWakesARestartedSession(t *testing.T) {
	r := wakeable(t)
	// It worked, the child exited, the supervisor brought it back — and it has been
	// sitting at the prompt for half an hour since.
	feed(t, r, "ember",
		ago(90, "PreToolUse", "Bash", "go build ./..."),
		ago(45, "SessionEnd", "", ""),
		ago(30, "SessionStart", "", ""))

	got := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "would wake") {
		t.Errorf("a session sitting at a fresh prompt was not woken:\n%s", got.stdout)
	}
}

// And the other half of that: a child that is gone is not waiting for anybody. It is
// `tend`'s to restart, and poking a dead session would be the two backstops reaching
// for the same fleet.
func TestWakeLeavesAnEndedSessionToTend(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember",
		ago(45, "PreToolUse", "Bash", "go build ./..."),
		ago(30, "SessionEnd", "", ""))

	got := r.ok("boss", "wake", "--dry-run")
	if strings.Contains(got.stdout, "would wake") {
		t.Errorf("a session whose child has gone was poked:\n%s", got.stdout)
	}
}

// TestNothingEmployedIsNotAFailure. A fleet with nobody working is a fine state for
// a cycle to find, and it should say so rather than exit non-zero.
func TestWakeWithNothingEmployed(t *testing.T) {
	r := fullFleet(t)

	got := r.ok("boss", "wake")
	if !strings.Contains(got.stdout, "nothing is employed") {
		t.Errorf("an idle fleet should say so:\n%s", got.stdout)
	}
}

// Only what the caller controls. A sweep skips the rest; a named identity is
// refused properly, because that one was a request rather than a scan.
func TestWakeNeedsControl(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember", ago(40, "Stop", "", ""))

	// quill is ember's peer, so its sweep finds nothing of its own to wake.
	swept := r.ok("quill", "wake", "--dry-run")
	if strings.Contains(swept.stdout, "ember") {
		t.Errorf("a peer's sweep reached somebody else's agent:\n%s", swept.stdout)
	}

	// And asking for it by name is refused rather than silently skipped.
	named := r.run("quill", "wake", "ember", "--dry-run")
	if named.code == fault.CodeOK {
		t.Errorf("a peer woke somebody else's agent by name:\n%s", named.stdout)
	}
}

// TestASilenceIsWokenOnce. An agent that does not move after a poke is stuck, not
// idle, and poking it every pass would bury that under nudges.
func TestWakeReportsAStuckAgent(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember", ago(40, "Stop", "", ""))

	// Two passes in one process, which is where the cycle's memory lives.
	first := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(first.stdout, "would wake") {
		t.Fatalf("the first pass did not wake it:\n%s", first.stdout)
	}

	// A second `orc wake` is a second process, and starts fresh — that is the
	// documented shape. Within one cycle the memory holds, which is what
	// --every exercises and what the waker's own unit test pins.
	second := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(second.stdout, "would wake") {
		t.Errorf("a fresh process should look at a quiet fleet with fresh eyes:\n%s", second.stdout)
	}
}

func TestWakeRefusals(t *testing.T) {
	r := wakeable(t)

	for _, tc := range []struct {
		what string
		args []string
		code int
	}{
		{"a silence shorter than a minute", []string{"wake", "--after", "10s"}, fault.CodeUsage},
		{"a cycle tighter than the watch floor", []string{"wake", "--every", "1s"}, fault.CodeUsage},
		{"a duration that is not one", []string{"wake", "--after", "soon"}, fault.CodeUsage},
		{"an interval that is not one", []string{"wake", "--every", "often"}, fault.CodeUsage},
		{"nobody by that name", []string{"wake", "nobody"}, fault.CodeNotFound},
	} {
		got := r.run("boss", tc.args...)
		if got.code != tc.code {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, tc.code, got.stderr)
		}
	}

	// The refusals say what the floor is, not just that there is one.
	if got := r.run("boss", "wake", "--after", "10s"); !strings.Contains(got.stderr, MinQuietText) {
		t.Errorf("the refusal does not name the floor:\n%s", got.stderr)
	}
}

// MinQuietText is what the floor looks like in a message, so the test does not
// duplicate the constant's formatting.
const MinQuietText = "1m0s"

// A message of one's own reaches the agent instead of "continue".
func TestWakeMessage(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember", ago(40, "Stop", "", ""))

	got := r.ok("boss", "wake", "--message", "status?", "--dry-run")
	if !strings.Contains(got.stdout, "would wake") {
		t.Fatalf("nothing was woken:\n%s", got.stdout)
	}
}

// --- agents stopped at a usage limit --------------------------------------

// The silence the cycle could not see.
//
// A limit lands wherever the turn happened to be, which is almost always straight
// after a tool call — so the feed ends on a tool event, and a feed ending on a tool
// event is exactly what `silence` reads as working. Every pass skipped these. Seven
// agents in one real fleet stopped at 03:10 and were still stopped twelve hours
// later, under a waker that was running the whole time.

// limitedFeed writes a feed that ends mid-tool — the shape a limit leaves — and the
// transcript it points at.
func limitedFeed(t *testing.T, r *rig, who, resets string, minutesAgo int) {
	t.Helper()

	dir := filepath.Join(r.root, "identities", who, "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "transcript.jsonl")

	at := epoch.Add(-time.Duration(minutesAgo) * time.Minute).UTC()
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"isApiErrorMessage":true,`+
		`"message":{"role":"assistant","content":[{"type":"text",`+
		`"text":"You've hit your session limit · resets %s"}]}}`,
		at.Format(time.RFC3339Nano), resets)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The feed: a session start carrying the transcript, then a tool call. Nothing
	// in it says anything about a limit — that is the point.
	start := fmt.Sprintf(`{"at":%q,"session":"s","event":"SessionStart","transcript":%q}`,
		epoch.Add(-time.Duration(minutesAgo+5)*time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), path)
	body := start + "\n" + ago(minutesAgo, "PreToolUse", "Bash", "go build ./...") + "\n" +
		ago(minutesAgo, "PostToolUse", "Bash", "") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The bug, stated as a test: an agent stopped at a limit whose reset has passed is
// woken. Before this it was read as mid-tool and skipped forever.
func TestWakeResumesAnAgentWhoseLimitHasLifted(t *testing.T) {
	r := wakeable(t)
	// Hit at 10:00, resetting at 11:00: an hour before the clock this rig runs on.
	limitedFeed(t, r, "ember", "11:00 (UTC)", 120)

	got := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "would wake") {
		t.Errorf("an agent whose limit lifted was left stopped:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "limit lifted") {
		t.Errorf("the wake does not say why it was woken:\n%s", got.stdout)
	}
}

// Before the reset there is nothing to do but say so. Poking would spend the
// agent's next turn on a second refusal — and would record a wake, which is how the
// cycle decides it has already tried.
func TestWakeWaitsForTheLimitToLift(t *testing.T) {
	r := wakeable(t)
	// Hit ten minutes ago, resetting in fifty: still limited.
	limitedFeed(t, r, "ember", "12:50 (UTC)", 10)

	got := r.ok("boss", "wake", "--dry-run")
	if strings.Contains(got.stdout, "would wake") {
		t.Errorf("an agent still at its limit was poked, which spends its next turn on "+
			"another refusal:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "limited") {
		t.Errorf("the pass does not say the agent is at a limit:\n%s", got.stdout)
	}
	// And it is not counted with the fleet that is fine. "all working" over a
	// stopped agent is the report that let this go unnoticed for twelve hours.
	if strings.Contains(got.stdout, "all working") {
		t.Errorf("an agent at a limit was reported as working:\n%s", got.stdout)
	}
}

// It says when, because "limited" without a time is a fact nobody can plan around.
func TestWakeSaysWhenTheLimitLifts(t *testing.T) {
	r := wakeable(t)
	limitedFeed(t, r, "ember", "12:50 (UTC)", 10)

	got := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "for another") || !strings.Contains(got.stdout, "m") {
		t.Errorf("it does not say how long is left:\n%s", got.stdout)
	}
}

// A limit is not a silence that has been nudged. The cycle pokes each silence once
// and then reports it stuck — the right rule for an agent that will not move, and
// the wrong one here, because the reason it did not move was the limit and the
// limit is over.
func TestALimitThatLiftsIsWokenEvenAfterAnEarlierWake(t *testing.T) {
	r := wakeable(t)
	feed(t, r, "ember", ago(40, "Stop", "", ""))
	// An ordinary wake first, which records a mark.
	r.ok("boss", "wake")

	limitedFeed(t, r, "ember", "11:00 (UTC)", 120)
	got := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "would wake") {
		t.Errorf("the earlier wake suppressed the one that resumes it:\n%s", got.stdout)
	}
}

// And a limit already recovered from is not a limit. An agent that was woken and
// worked on must not be reported as stopped for the rest of its life.
func TestAnAgentThatRecoveredIsNotStillLimited(t *testing.T) {
	r := wakeable(t)
	limitedFeed(t, r, "ember", "11:00 (UTC)", 120)

	// It carried on: the transcript's last word is the assistant's, not the error.
	path := filepath.Join(r.root, "identities", "ember", "session", "transcript.jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	more := string(body) + `{"type":"assistant","timestamp":"2026-07-25T11:59:00Z",` +
		`"message":{"role":"assistant","content":[{"type":"text","text":"back to it"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(more), 0o600); err != nil {
		t.Fatal(err)
	}

	got := r.ok("boss", "wake", "--dry-run")
	if strings.Contains(got.stdout, "limit") {
		t.Errorf("an agent that recovered is still called limited:\n%s", got.stdout)
	}
}
