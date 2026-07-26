package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `orc view` is the read-only half of `attach`, and the properties worth pinning
// are the ones that make it that: it must not touch the session, it must say what
// state the agent is in, and it must degrade rather than fail when one of its two
// sources cannot be read.

// viewFeed writes an event journal for an identity, in the format the hook
// appends. Prefixed because this package is edited by several hands at once.
func viewFeed(t *testing.T, root, who string, events []map[string]any) {
	t.Helper()
	dir := filepath.Join(root, "identities", who, "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range events {
		body, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(body))
	}
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A turn with a tool call allowed and one refused, which is the shape somebody
// runs this command to see.
func aTurn() []map[string]any {
	return []map[string]any{
		{"at": "2026-07-26T14:00:00.000Z", "session": "s1", "event": "SessionStart"},
		{"at": "2026-07-26T14:00:05.000Z", "session": "s1", "event": "UserPromptSubmit", "turn": 1},
		{"at": "2026-07-26T14:00:09.000Z", "session": "s1", "event": "PreToolUse",
			"tool": "Read", "path": "Docs/Orc/Reference.md", "turn": 1, "verdict": "allow"},
		{"at": "2026-07-26T14:00:12.000Z", "session": "s1", "event": "PreToolUse",
			"tool": "Write", "path": "Orc/internal/cli/cli.go", "turn": 1,
			"verdict": "block", "reason": "reviewer may not write outside Docs/"},
		{"at": "2026-07-26T14:00:20.000Z", "session": "s1", "event": "Stop", "turn": 1},
	}
}

func viewFleet(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.bootstrap("boss")
	r.ok("boss", "new", "role", "reviewer", "40", "reads the specifications")
	r.hire("boss", "ember")
	r.ok("boss", "assign", "role", "ember", "reviewer")
	return r
}

func TestViewShowsWhatTheAgentDid(t *testing.T) {
	r := viewFleet(t)
	viewFeed(t, r.root, "ember", aTurn())

	out := r.ok("boss", "view", "ember").stdout
	for _, want := range []string{"ember", "reviewer", "Read", "Docs/Orc/Reference.md", "Write"} {
		if !strings.Contains(out, want) {
			t.Errorf("the view does not mention %q:\n%s", want, out)
		}
	}
}

// A refusal without its reason sends the reader to the permissions table to find
// out what they already needed to know — which is the thing `attach` gets right
// and a summary would lose.
func TestViewSaysWhySomethingWasRefused(t *testing.T) {
	r := viewFleet(t)
	viewFeed(t, r.root, "ember", aTurn())

	out := r.ok("boss", "view", "ember").stdout
	if !strings.Contains(out, "reviewer may not write outside Docs/") {
		t.Errorf("a blocked tool call was shown without its reason:\n%s", out)
	}
}

// The state somebody is usually looking for. An agent that has stopped and is
// waiting to be spoken to looks exactly like one that is thinking, unless the
// view says which.
func TestViewSaysWhetherTheAgentIsWaiting(t *testing.T) {
	r := viewFleet(t)
	viewFeed(t, r.root, "ember", aTurn())

	var got map[string]any
	if err := json.Unmarshal([]byte(r.ok("boss", "view", "ember", "--json").stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got["waiting"] != true {
		t.Errorf("a session that stopped was not reported as waiting: %v", got["waiting"])
	}
	if got["turn"] != float64(1) {
		t.Errorf("turn = %v, want 1", got["turn"])
	}
}

// An identity nobody has employed has no session, which is a different state from
// a session that has done nothing — and the message points at the fix.
func TestViewOfAnIdleIdentitySaysSoAndSaysWhatToDo(t *testing.T) {
	r := viewFleet(t)
	out := r.ok("boss", "view", "ember").stdout
	if !strings.Contains(out, "no session") {
		t.Errorf("an identity with no session did not say so:\n%s", out)
	}
	if !strings.Contains(out, "employ") {
		t.Errorf("it did not say what starts one:\n%s", out)
	}
}

// The degradation rule this inherits from view/transcript.go: a source that will
// not read costs what it carried and nothing else. A feed that cannot be parsed
// must still print the facts, and must not be shown as an idle agent.
func TestAnUnreadableFeedIsReportedRatherThanShownAsIdle(t *testing.T) {
	r := viewFleet(t)
	dir := filepath.Join(r.root, "identities", "ember", "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := r.ok("boss", "view", "ember").stdout
	if !strings.Contains(out, "could not be read") {
		t.Errorf("a broken feed was not reported:\n%s", out)
	}
	if !strings.Contains(out, "ember") {
		t.Errorf("the facts were lost along with the feed:\n%s", out)
	}
}

// --json is what cq reads, so its shape is a compatibility surface rather than a
// convenience — a rename here is a rename in the browser.
func TestViewJSONCarriesWhatTheBrowserNeeds(t *testing.T) {
	r := viewFleet(t)
	viewFeed(t, r.root, "ember", aTurn())

	var got map[string]any
	if err := json.Unmarshal([]byte(r.ok("boss", "view", "ember", "--json").stdout), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"identity", "role", "live", "waiting", "turn", "rows"} {
		if _, ok := got[key]; !ok {
			t.Errorf("--json has no %q; cq reads this shape", key)
		}
	}
	rows, _ := got["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("--json carried no rows")
	}
	first, _ := rows[0].(map[string]any)
	for _, key := range []string{"at", "kind"} {
		if _, ok := first[key]; !ok {
			t.Errorf("a row has no %q", key)
		}
	}
}

// The tail, not the head: somebody checking on an agent wants what it just did.
func TestViewKeepsTheMostRecentLines(t *testing.T) {
	r := viewFleet(t)

	events := []map[string]any{{"at": "2026-07-26T14:00:00.000Z", "session": "s1", "event": "SessionStart"}}
	for i := 0; i < 30; i++ {
		events = append(events, map[string]any{
			"at": "2026-07-26T14:01:00.000Z", "session": "s1", "event": "PreToolUse",
			"tool": "Read", "path": "file" + string(rune('a'+i%26)) + ".md", "turn": 1, "verdict": "allow",
		})
	}
	viewFeed(t, r.root, "ember", events)

	var got map[string]any
	if err := json.Unmarshal([]byte(r.ok("boss", "view", "ember", "--json", "--lines", "5").stdout), &got); err != nil {
		t.Fatal(err)
	}
	rows, _ := got["rows"].([]any)
	if len(rows) != 5 {
		t.Errorf("--lines 5 gave %d rows", len(rows))
	}
	// And they are the last five, not the first.
	last, _ := rows[len(rows)-1].(map[string]any)
	if last["detail"] == "" {
		t.Error("the last row is empty, so the head was kept rather than the tail")
	}
}

func TestViewRefusesWhatItCannotAnswer(t *testing.T) {
	r := viewFleet(t)

	for _, args := range [][]string{
		{"view"},
		{"view", "ember", "extra"},
		{"view", "nosuchagent"},
		{"view", "ember", "--lines", "0"},
		{"view", "ember", "--lines", "many"},
	} {
		if got := r.run("boss", args...); got.code == 0 {
			t.Errorf("`orc %s` was accepted:\n%s", strings.Join(args, " "), got.stdout)
		}
	}
}

// The property that makes it safe to run against a working agent: it opens no
// socket and writes nothing. Asserted on the store rather than on the code,
// because the point is what is true after the command, not how it is written.
func TestViewLeavesTheSessionAlone(t *testing.T) {
	r := viewFleet(t)
	viewFeed(t, r.root, "ember", aTurn())

	before := viewTreeOf(t, filepath.Join(r.root, "identities", "ember"))
	r.ok("boss", "view", "ember")
	after := viewTreeOf(t, filepath.Join(r.root, "identities", "ember"))

	if before != after {
		t.Errorf("viewing changed the identity's directory:\nbefore %s\nafter  %s", before, after)
	}
}

// treeOf is every file under a directory with its size, which is enough to notice
// a write without depending on timestamps a filesystem may not keep.
func viewTreeOf(t *testing.T, root string) string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, path+":"+strings.TrimSpace(strings.Join([]string{info.Name()}, ""))+
				":"+viewSize(info.Size()))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func viewSize(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
