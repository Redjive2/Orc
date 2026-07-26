package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// `orc instruct` is where somebody decides how an agent thinks, so most of what is
// worth testing is who may — and that the two composition rules survive the trip
// through a command line unchanged.

func instructable(t *testing.T) *rig {
	t.Helper()
	r := fullFleet(t)
	// The toolkit's `instruct` permission, on the role atlas holds.
	r.ok("boss", "assign", "permission", "architect", "instruct")
	return r
}

func file(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSettingAndReadingBackALayer. `orc instruct <target>` prints the text and
// nothing else, so redirecting it round-trips.
func TestInstructSetAndPrint(t *testing.T) {
	r := instructable(t)

	r.ok("boss", "instruct", "system", "--set", file(t, "ask before you guess\n"))

	got := r.ok("boss", "instruct", "system")
	if got.stdout != "ask before you guess\n" {
		t.Errorf("printing a layer gave %q, want the text alone", got.stdout)
	}
}

// It says when the change takes effect, because a prompt edited while agents are
// running has changed the next session and not this one.
func TestInstructSaysWhenItTakesEffect(t *testing.T) {
	r := instructable(t)

	got := r.ok("boss", "instruct", "system", "--set", file(t, "ask first"))
	if !strings.Contains(got.stdout, "keep the instructions they started with") {
		t.Errorf("it does not say when it applies:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "orc refresh") {
		t.Errorf("it does not say how to apply it now:\n%s", got.stdout)
	}

	// A wake message has nothing to restart.
	wake := r.ok("boss", "instruct", "wake", "--set", file(t, "carry on"))
	if !strings.Contains(wake.stdout, "next wake") {
		t.Errorf("a wake message should say the next wake uses it:\n%s", wake.stdout)
	}
	if strings.Contains(wake.stdout, "refresh") {
		t.Errorf("a wake message has no session to restart:\n%s", wake.stdout)
	}
}

// TestTheFleetsLayerIsTheOperatorsAlone — §8's fence, and the one no permission
// opens, because it reaches every agent there is.
func TestInstructSystemIsOperatorOnly(t *testing.T) {
	r := instructable(t)
	path := file(t, "every agent reads this")

	got := r.run("atlas", "instruct", "system", "--set", path)
	if got.code != fault.CodeDenied {
		t.Fatalf("exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if !strings.Contains(got.stderr, "operator") {
		t.Errorf("the refusal should say whose it is:\n%s", got.stderr)
	}

	// Holding `instruct` does not open it: atlas has the permission and is still
	// refused, which is the whole point of fencing it separately.
	if !strings.Contains(got.stderr, "every agent") {
		t.Errorf("the refusal should say why:\n%s", got.stderr)
	}
}

// A role's or an agent's needs the permission.
func TestInstructNeedsThePermission(t *testing.T) {
	r := instructable(t)
	path := file(t, "you write the parser")

	// quill holds `engineer`, which does not carry `instruct`.
	if got := r.run("quill", "instruct", "role", "engineer", "--set", path); got.code != fault.CodeDenied {
		t.Errorf("a role prompt without the permission exited %d, want %d", got.code, fault.CodeDenied)
	}
	// atlas holds it.
	if got := r.run("atlas", "instruct", "role", "engineer", "--set", path); got.code != fault.CodeOK {
		t.Errorf("with the permission it exited %d\n%s", got.code, got.stderr)
	}
}

// And an agent's needs ancestry as well: the permission says you may instruct at
// all, the tree says whom.
func TestInstructIdentityNeedsControl(t *testing.T) {
	r := instructable(t)
	path := file(t, "you are covering the parser")

	// atlas holds `instruct` but ember is its peer, not its subordinate.
	got := r.run("atlas", "instruct", "identity", "ember", "--set", path)
	if got.code == fault.CodeOK {
		t.Error("a peer instructed somebody it does not control")
	}

	// And nobody instructs themselves: an agent writing its own standing
	// instructions is an agent deciding what it is for.
	self := r.run("atlas", "instruct", "identity", "atlas", "--set", path)
	if self.code == fault.CodeOK {
		t.Error("an agent wrote its own standing instructions")
	}
	if !strings.Contains(self.stderr, "what it is for") {
		t.Errorf("the refusal should say why:\n%s", self.stderr)
	}
}

// TestShowIsTheComposition. Layered configuration is only debuggable if the
// composition can be seen.
func TestInstructShow(t *testing.T) {
	r := instructable(t)

	r.ok("boss", "instruct", "system", "--set", file(t, "ask before you guess"))
	r.ok("boss", "instruct", "role", "engineer", "--set", file(t, "you write the parser"))
	r.ok("boss", "instruct", "identity", "ember", "--set", file(t, "covering for atlas"))

	got := r.ok("boss", "instruct", "show", "ember")
	for _, want := range []string{
		"# the fleet", "ask before you guess",
		"# the engineer role", "you write the parser",
		"# ember", "covering for atlas",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the composed prompt lacks %q:\n%s", want, got.stdout)
		}
	}

	// Additive: the fleet's layer is still there under the agent's own.
	fleet := strings.Index(got.stdout, "ask before you guess")
	own := strings.Index(got.stdout, "covering for atlas")
	if fleet > own {
		t.Errorf("the layers are out of order:\n%s", got.stdout)
	}
}

// An agent with nothing set says so rather than printing an empty document.
func TestInstructShowWithNothingSet(t *testing.T) {
	r := instructable(t)

	got := r.ok("boss", "instruct", "show", "ember")
	if !strings.Contains(got.stdout, "claude's own instructions") {
		t.Errorf("an uninstructed agent should say so:\n%s", got.stdout)
	}
}

// TestTheOverviewSaysWhatIsSetAndWhen — §9's last-changed column, which is where
// "why is it behaving differently" starts.
func TestInstructOverview(t *testing.T) {
	r := instructable(t)
	r.ok("boss", "instruct", "system", "--set", file(t, "ask before you guess"))

	got := r.ok("boss", "instruct")
	if !strings.Contains(got.stdout, "system") {
		t.Errorf("the overview does not list the fleet's layer:\n%s", got.stdout)
	}
	// Its size, and who last touched it.
	if !strings.Contains(got.stdout, "boss") {
		t.Errorf("the overview does not say who changed it:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, " B") && !strings.Contains(got.stdout, "KiB") {
		t.Errorf("the overview does not say how large it is:\n%s", got.stdout)
	}
	// And the sentence that stops somebody using a prompt where they needed a
	// permission.
	if !strings.Contains(got.stdout, "a prompt asks and a permission enforces") {
		t.Errorf("the overview does not say what a prompt is not:\n%s", got.stdout)
	}
}

// A fleet that has set nothing says so, rather than printing a bare heading.
func TestInstructOverviewEmpty(t *testing.T) {
	r := instructable(t)

	got := r.ok("boss", "instruct")
	if !strings.Contains(got.stdout, "nothing is set") {
		t.Errorf("an uninstructed fleet should say so:\n%s", got.stdout)
	}
}

// Clearing removes the layer, and the composition loses it.
func TestInstructClear(t *testing.T) {
	r := instructable(t)
	r.ok("boss", "instruct", "system", "--set", file(t, "ask before you guess"))

	r.ok("boss", "instruct", "system", "--clear")

	if got := r.ok("boss", "instruct", "show", "ember"); strings.Contains(got.stdout, "ask before you guess") {
		t.Errorf("a cleared layer is still composed:\n%s", got.stdout)
	}
}

// The wake chain, through the command line.
func TestInstructWakeOverrides(t *testing.T) {
	r := instructable(t)

	r.ok("boss", "instruct", "wake", "--set", file(t, "carry on"))
	r.ok("boss", "instruct", "wake", "identity", "ember", "--set", file(t, "finish the lexer"))

	if got := r.ok("boss", "instruct", "wake", "identity", "ember"); !strings.Contains(got.stdout, "finish the lexer") {
		t.Errorf("the agent's own wake message:\n%s", got.stdout)
	}
	// The fleet's is still its own, unchanged by the more specific one.
	if got := r.ok("boss", "instruct", "wake"); !strings.Contains(got.stdout, "carry on") {
		t.Errorf("the fleet's wake message was overwritten:\n%s", got.stdout)
	}
}

// A layer over its bound is refused rather than truncated, through the CLI as well
// as in the store.
func TestInstructRefusesAnOversizedLayer(t *testing.T) {
	r := instructable(t)
	huge := file(t, strings.Repeat("x", 17<<10))

	got := r.run("boss", "instruct", "system", "--set", huge)
	if got.code != fault.CodeUsage {
		t.Errorf("an oversized layer exited %d, want %d\n%s", got.code, fault.CodeUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "KiB") {
		t.Errorf("the refusal should give the arithmetic:\n%s", got.stderr)
	}
}

func TestInstructRefusals(t *testing.T) {
	r := instructable(t)

	for _, tc := range []struct {
		what string
		args []string
		code int
	}{
		{"a target that is not one", []string{"instruct", "frobnicate"}, fault.CodeUsage},
		{"a role that does not exist", []string{"instruct", "role", "nobody"}, fault.CodeNotFound},
		{"an identity that does not exist", []string{"instruct", "identity", "nobody"}, fault.CodeNotFound},
		{"a role with no name", []string{"instruct", "role"}, fault.CodeUsage},
		{"show with no identity", []string{"instruct", "show"}, fault.CodeUsage},
		{"a file that is not there", []string{"instruct", "system", "--set", "/nonexistent"}, fault.CodeIO},
	} {
		if got := r.run("boss", tc.args...); got.code != tc.code {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, tc.code, got.stderr)
		}
	}
}

// TestTheWakeCycleSendsTheStoredMessage. Without this, `orc instruct wake` writes a
// file nothing reads — a command that looks like it works and does not.
func TestWakeUsesTheInstructedMessage(t *testing.T) {
	r := instructable(t)
	r.ok("boss", "employ", "ember")
	feed(t, r, "ember", ago(40, "Stop", "", ""))

	// Nothing set: the built-in bottom.
	if got := r.ok("boss", "wake", "--dry-run"); !strings.Contains(got.stdout, "would wake") {
		t.Fatalf("nothing was woken:\n%s", got.stdout)
	}

	r.ok("boss", "instruct", "wake", "--set", file(t, "carry on with the parser"))
	got := r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "wake's message") {
		t.Errorf("the fleet's wake message is not what would be sent:\n%s", got.stdout)
	}

	// The agent's own overrides it.
	r.ok("boss", "instruct", "wake", "identity", "ember", "--set", file(t, "finish the lexer first"))
	got = r.ok("boss", "wake", "--dry-run")
	if !strings.Contains(got.stdout, "identity's message") {
		t.Errorf("the agent's own message should win:\n%s", got.stdout)
	}
}

// An explicit --message is the operator saying what to send this time, and beats
// everything stored.
func TestWakeFlagBeatsTheStoredMessage(t *testing.T) {
	r := instructable(t)
	r.ok("boss", "employ", "ember")
	feed(t, r, "ember", ago(40, "Stop", "", ""))
	r.ok("boss", "instruct", "wake", "identity", "ember", "--set", file(t, "the stored one"))

	got := r.ok("boss", "wake", "--message", "no, this instead", "--dry-run")
	if strings.Contains(got.stdout, "identity's message") {
		t.Errorf("the stored message overrode an explicit one:\n%s", got.stdout)
	}
}
