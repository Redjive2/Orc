package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// `orc remove role <name> --yes` was refused: "remove role takes one name, got 2
// arguments".
//
// It matters beyond a mistyped command. cq builds exactly that line for the delete
// button in its web interface, so every role removed from a browser failed on the
// agent machine minutes later, in a queue, for a reason that reads as a caller
// mistake. Every other `remove … --yes` was accepted, which is where the habit and
// the generated command both came from.
//
// The flag is accepted and not required, and the two halves have different reasons.
// Nothing is lost here — the command already refuses while anybody holds the role —
// so there is nothing to confirm. And a flag that means "I am sure" must never be
// the thing that refuses a command which did not need asking.
func TestRemoveRoleTakesTheYesEverythingElseTakes(t *testing.T) {
	for _, args := range [][]string{
		{"remove", "role", "scribe", "--yes"},
		{"remove", "role", "scribe"},
		// The flag before the name, since a caller may put it anywhere.
		{"remove", "role", "--yes", "scribe"},
	} {
		r := fullFleet(t)
		r.ok("boss", "new", "role", "scribe", "20", "keeps the documents")

		got := r.run("boss", args...)
		if got.code != fault.CodeOK {
			t.Errorf("`orc %s` exited %d:\n%s%s",
				strings.Join(args, " "), got.code, got.stdout, got.stderr)
			continue
		}
		if !strings.Contains(got.stdout, "removed") {
			t.Errorf("`orc %s` did not say it removed anything:\n%s",
				strings.Join(args, " "), got.stdout)
		}
	}
}

// And the refusal that makes the confirmation unnecessary still holds. A rule that
// accepted `--yes` by making the command less careful would be a worse bug than the
// one it fixed.
func TestRemoveRoleStillRefusesOneSomebodyHolds(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "new", "role", "scribe", "20", "keeps the documents")
	r.ok("boss", "assign", "role", "ember", "scribe")

	got := r.run("boss", "remove", "role", "scribe", "--yes")
	if got.code == fault.CodeOK {
		t.Fatal("a role somebody holds was removed because --yes was passed")
	}
	if !strings.Contains(got.stderr, "ember") {
		t.Errorf("the refusal does not name who holds it:\n%s", got.stderr)
	}
}
