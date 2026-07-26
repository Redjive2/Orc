package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// `orc model` is a second way to change what a session costs, so most of what is
// worth testing here is that it is not a second set of rules: the same control
// check, the same budget arithmetic, the same journal as `employ`.

func TestModelReports(t *testing.T) {
	r := fullFleet(t)

	got := r.ok("boss", "model", "ember")
	for _, want := range []string{"ember", "is on", "load"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("`orc model ember` does not say %q:\n%s", want, got.stdout)
		}
	}
	// An identity nobody has employed costs nothing, and saying "load 6" without
	// that would read as spending that is happening.
	if !strings.Contains(got.stdout, "not employed") {
		t.Errorf("an unemployed identity's report should say so:\n%s", got.stdout)
	}
}

// TestRetuningDoesNotEmploy. "Run this on opus next time" and "start this now" are
// different intents, and the command for the first must not do the second.
func TestModelDoesNotEmploy(t *testing.T) {
	r := fullFleet(t)

	got := r.ok("boss", "model", "ember", "opus")
	if !strings.Contains(got.stdout, "retuned") {
		t.Errorf("the change was not reported:\n%s", got.stdout)
	}
	if len(r.populates) != 0 {
		t.Errorf("retuning started a session: %v", r.populates)
	}
	if !strings.Contains(got.stdout, "orc employ ember") {
		t.Errorf("it should say how the change takes effect:\n%s", got.stdout)
	}

	// And it is what the next employ starts it on.
	started := r.ok("boss", "employ", "ember")
	if !strings.Contains(started.stdout, "opus") {
		t.Errorf("employ did not use the retuned model:\n%s", started.stdout)
	}
}

// TestRetuningAnEmployedIdentityMovesTheLoad — the whole reason this goes through
// the budget rather than round it.
func TestModelChangesTheLoad(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember", "--model", "haiku", "--effort", "low")

	got := r.ok("boss", "model", "ember", "opus", "--effort", "high")
	if !strings.Contains(got.stdout, "load") || !strings.Contains(got.stdout, "→") {
		t.Errorf("the load change is not shown:\n%s", got.stdout)
	}
	// haiku/low is 1; opus/high is 9.
	if !strings.Contains(got.stdout, "1") || !strings.Contains(got.stdout, "9") {
		t.Errorf("the arithmetic is not the session load:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "fleet") {
		t.Errorf("what the fleet now spends is not reported:\n%s", got.stdout)
	}
}

// TestABudgetRefusalIsTheSameRefusalEmployGives. A second way to spend that skipped
// the check would make the budget advisory.
func TestModelIsBudgeted(t *testing.T) {
	r := fullFleet(t)
	// A boss with a small budget. `lead` carries spawn(24), which is enough for
	// opus/max — the first version of this test used it and proved nothing.
	r.ok("boss", "new", "permission", "one-small-thing", "40", "spawn(2)")
	r.ok("boss", "new", "role", "junior", "50", "runs", "one", "small", "thing")
	r.ok("boss", "assign", "permission", "junior", "one-small-thing")
	r.hire("boss", "min")
	r.ok("boss", "assign", "role", "min", "junior")
	r.hire("min", "spark")
	r.ok("boss", "assign", "role", "spark", "engineer")
	r.ok("min", "employ", "spark", "--model", "haiku", "--effort", "low")

	// haiku/low is 1 and fits in spawn(2); opus/max is 18 and does not.
	got := r.run("min", "model", "spark", "opus", "--effort", "max")
	if got.code != fault.CodeDenied {
		t.Fatalf("an unaffordable retune exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if !strings.Contains(got.stderr, "load") {
		t.Errorf("the refusal should show the arithmetic:\n%s", got.stderr)
	}

	// And it did not happen: the identity is still on what it was.
	after := r.ok("min", "model", "spark")
	if !strings.Contains(after.stdout, "haiku") {
		t.Errorf("a refused retune changed the model anyway:\n%s", after.stdout)
	}
}

// Only the boss directs an agent. Raising your own load would be granting yourself
// budget, so it is the same refusal.
func TestModelNeedsControl(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	// A peer: quill and ember both report to boss.
	if got := r.run("quill", "model", "ember", "opus"); got.code == fault.CodeOK {
		t.Error("a peer retuned somebody else's agent")
	}
	// And an agent cannot retune itself.
	got := r.run("ember", "model", "ember", "opus")
	if got.code == fault.CodeOK {
		t.Error("an agent raised its own load")
	}
	if !strings.Contains(got.stderr, "subordinate") {
		t.Errorf("the refusal should say why:\n%s", got.stderr)
	}

	// Reading is not directing, though: anyone who can see the fleet may ask.
	if got := r.run("quill", "model", "ember"); got.code != fault.CodeOK {
		t.Errorf("reading what an agent is on exited %d\n%s", got.code, got.stderr)
	}
}

// TestARunningSessionIsNotRestartedWithoutAsking. A model is fixed when Claude
// starts, so taking effect means replacing the session — and that costs the
// conversation, which is not a decision to make inside a settings change.
func TestModelLeavesTheSessionAlone(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember", "--model", "haiku")
	before := len(r.populates)

	got := r.ok("boss", "model", "ember", "sonnet")
	if len(r.populates) != before {
		t.Errorf("the session was replaced without being asked: %v", r.populates)
	}
	// It says what that means, and both ways to make it take effect.
	for _, want := range []string{"keeps", "--now", "refresh"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the message does not mention %q:\n%s", want, got.stdout)
		}
	}
}

// --now is how the operator asks for it, and it says what it cost.
func TestModelNowRestarts(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember", "--model", "haiku")
	before := len(r.populates)

	got := r.ok("boss", "model", "ember", "opus", "--now")
	if len(r.populates) != before+1 {
		t.Fatalf("--now did not start a new session: %v", r.populates)
	}
	if last := r.populates[len(r.populates)-1]; !strings.Contains(last, "opus") {
		t.Errorf("the new session is not on the new model: %s", last)
	}
	if !strings.Contains(got.stdout, "fresh context") {
		t.Errorf("--now should say what it cost:\n%s", got.stdout)
	}
}

// A script that sets the model every pass should be a no-op on the passes where
// nothing changed, not a failure.
func TestModelUnchangedIsNotAnError(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "model", "ember", "opus")

	got := r.ok("boss", "model", "ember", "opus")
	if !strings.Contains(got.stdout, "already on") {
		t.Errorf("an unchanged retune should say so:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "retuned") {
		t.Errorf("nothing changed, so nothing should be reported as changed:\n%s", got.stdout)
	}
}

// The effort half works on its own: `orc model ember --effort high` is a change.
func TestModelEffortAlone(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember", "--model", "sonnet", "--effort", "low")

	got := r.ok("boss", "model", "ember", "--effort", "high")
	if !strings.Contains(got.stdout, "retuned") || !strings.Contains(got.stdout, "sonnet") {
		t.Errorf("changing only the effort should keep the model:\n%s", got.stdout)
	}
}

func TestModelRefusals(t *testing.T) {
	r := fullFleet(t)

	for _, tc := range []struct {
		what string
		args []string
		code int
	}{
		{"no identity", []string{"model"}, fault.CodeUsage},
		{"too many", []string{"model", "ember", "opus", "extra"}, fault.CodeUsage},
		{"a model orc cannot budget", []string{"model", "ember", "gpt-9"}, fault.CodeUsage},
		{"a bad effort", []string{"model", "ember", "opus", "--effort", "frantic"}, fault.CodeUsage},
		{"nobody by that name", []string{"model", "nobody", "opus"}, fault.CodeNotFound},
	} {
		if got := r.run("boss", tc.args...); got.code != tc.code {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, tc.code, got.stderr)
		}
	}
}

// The change is in the journal, with who made it — an identity whose load changed is
// one whose journal says when and by whom.
func TestModelIsJournalled(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "model", "ember", "opus", "--effort", "high")

	// Reading it back through a fresh command proves the event replays, which is
	// what a journal is for. `orc model` is the reader rather than `orc status`:
	// a card for an unemployed identity shows what it *is*, not what it would be
	// started on, which is the question this one answers.
	got := r.ok("boss", "model", "ember")
	if !strings.Contains(got.stdout, "opus") || !strings.Contains(got.stdout, "high") {
		t.Errorf("the retune did not survive a reload:\n%s", got.stdout)
	}
}
