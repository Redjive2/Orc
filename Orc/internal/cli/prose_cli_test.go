package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// The rule lives in every agent's system prompt, and a rule nobody can check is one
// that drifts. `orc prose` is the other half of it: the same judgement a reviewer
// would make, available to the agent that did the writing.

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProseRefusesWritingThatBreaksTheRule(t *testing.T) {
	r := fullFleet(t)
	dir := t.TempDir()
	bad := write(t, dir, "bad.md", "This is honestly the plan.\n")

	got := r.run("boss", "prose", bad)
	if got.code != fault.CodeConflict {
		t.Fatalf("exit %d, want %d:\n%s%s", got.code, fault.CodeConflict, got.stdout, got.stderr)
	}
	// The word, and where. "Does not meet the rule" with no location is a refusal
	// somebody has to go looking for.
	for _, want := range []string{"honestly", "banned word", "bad.md:1"} {
		if !strings.Contains(got.stdout+got.stderr, want) {
			t.Errorf("the report does not say %q:\n%s%s", want, got.stdout, got.stderr)
		}
	}
}

func TestProseAcceptsPlainWriting(t *testing.T) {
	r := fullFleet(t)
	dir := t.TempDir()
	good := write(t, dir, "good.md", "Orc reads the store.\nIt starts a session for each agent.\n")

	got := r.ok("boss", "prose", good)
	if !strings.Contains(got.stdout, "100%") {
		t.Errorf("plain writing did not score 100%%:\n%s", got.stdout)
	}
}

// A directory is walked for documents and nothing else. An agent that had to rewrite
// every comment in a package to land a change would stop running this.
func TestProseWalksDocumentsOnly(t *testing.T) {
	r := fullFleet(t)
	dir := t.TempDir()
	write(t, dir, "notes.md", "Orc reads the store.\n")
	write(t, dir, "code.go", "// This is honestly a comment.\n")

	got := r.ok("boss", "prose", dir)
	if strings.Contains(got.stdout, "code.go") {
		t.Errorf("it measured source:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "notes.md") {
		t.Errorf("it skipped the document:\n%s", got.stdout)
	}
}

// Every agent may run it. It reads and prints and changes nothing, so gating it
// would only stop the agent whose writing is judged from checking it first.
func TestAnyAgentMayMeasureItsOwnWriting(t *testing.T) {
	r := fullFleet(t)
	dir := t.TempDir()
	good := write(t, dir, "good.md", "Orc reads the store.\n")

	if got := r.run("ember", "prose", good); got.code != fault.CodeOK {
		t.Errorf("an ordinary agent could not check its writing: exit %d\n%s%s",
			got.code, got.stdout, got.stderr)
	}
}
