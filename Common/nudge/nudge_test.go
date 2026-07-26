package nudge_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"orc/common/nudge"
)

// probe records what the nudge decided to do, so the decision can be tested
// without a process anywhere near it.
type probe struct {
	env     map[string]string
	missing bool  // cq is not installed
	failed  error // the child could not be started
	started [][]string
}

func (p *probe) nudger() nudge.Nudger {
	return nudge.Nudger{
		Look: func(k string) (string, bool) { v, ok := p.env[k]; return v, ok },
		Find: func(name string) (string, error) {
			if p.missing {
				return "", errors.New("not found in $PATH")
			}
			return "/usr/local/bin/" + name, nil
		},
		Start: func(path string, args []string) error {
			p.started = append(p.started, append([]string{path}, args...))
			return p.failed
		},
	}
}

func mirrored() map[string]string {
	return map[string]string{nudge.Server: "https://cq.example"}
}

func TestAMirroredMachineNudges(t *testing.T) {
	p := &probe{env: mirrored()}
	if !p.nudger().Fire() {
		t.Fatal("a machine with a server configured should nudge")
	}
	if len(p.started) != 1 {
		t.Fatalf("started = %v", p.started)
	}
	if got := p.started[0][1:]; !slices.Equal(got, []string{"sync", "--nudge"}) {
		t.Errorf("ran %v, want the coalescing form", got)
	}
}

// TestAMachineWithNoMirrorDoesNothing is the common case: most machines in Orc
// have no cq, and Mailman must cost them nothing.
func TestAMachineWithNoMirrorDoesNothing(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{nudge.Server: ""},
	} {
		p := &probe{env: env}
		if p.nudger().Fire() {
			t.Errorf("env %v should not nudge", env)
		}
		if len(p.started) != 0 {
			t.Errorf("started %v", p.started)
		}
	}
}

// TestApplyingASyncedActionDoesNotNudge is the loop guard.
//
// cq applies a queued action by running `mailman send`. If that nudged, the
// nudge would sync, the sync would apply, and each round would ask for another.
// cq's lock bounds it, but the work doubles for nothing.
func TestApplyingASyncedActionDoesNotNudge(t *testing.T) {
	env := mirrored()
	env[nudge.Suppress] = "1"

	p := &probe{env: env}
	if p.nudger().Fire() {
		t.Error("a command run by cq should not ask cq to sync")
	}
}

// A suppression variable set to "0" or empty means the operator turned it off,
// which is the opposite of what an unset-versus-set check would conclude.
func TestSuppressionCanBeTurnedOff(t *testing.T) {
	for _, v := range []string{"", "0"} {
		env := mirrored()
		env[nudge.Suppress] = v

		p := &probe{env: env}
		if !p.nudger().Fire() {
			t.Errorf("%s=%q should not suppress", nudge.Suppress, v)
		}
	}
}

// TestAnotherAccountsChangeIsNotMine is the one that matters most.
//
// A nudge inherits the environment of whichever tool changed something. On the
// agent machine that is usually an agent authenticated as itself. If the nudge
// fired, cq would read that agent's mailbox and publish it as the operator's
// own inbox — wrong, and a disclosure of the agent's mail to the operator.
func TestAnotherAccountsChangeIsNotMine(t *testing.T) {
	env := mirrored()
	env[nudge.Mirrored] = "redjive"
	env[nudge.OrcUser] = "bob"
	// No CQ_KEY: cq can only read whoever the environment names.

	p := &probe{env: env}
	if p.nudger().Fire() {
		t.Error("an agent's own change should not sync the operator's mirror")
	}
	if len(p.started) != 0 {
		t.Errorf("started %v", p.started)
	}
}

func TestTheMirroredAccountsOwnChangeDoesNudge(t *testing.T) {
	for _, who := range []string{"redjive", "REDJIVE"} {
		env := mirrored()
		env[nudge.Mirrored] = "redjive"
		env[nudge.OrcUser] = who

		p := &probe{env: env}
		if !p.nudger().Fire() {
			t.Errorf("%s=%q is the mirrored account and should nudge", nudge.OrcUser, who)
		}
	}
}

// With nothing to compare against, the nudge goes ahead: cq itself does the
// strict check, and refusing here would break the single-account setup where
// only one of the two variables is ever set.
func TestAnUnstatedIdentityStillNudges(t *testing.T) {
	for _, env := range []map[string]string{
		{nudge.Server: "https://cq.example"},
		{nudge.Server: "https://cq.example", nudge.Mirrored: "redjive"},
		{nudge.Server: "https://cq.example", nudge.OrcUser: "bob"},
	} {
		p := &probe{env: env}
		if !p.nudger().Fire() {
			t.Errorf("env %v should nudge and let cq decide", env)
		}
	}
}

// TestWithItsOwnCredentialAnyAgentsChangeNudges is the case the mirror exists
// for.
//
// Agents are what send the operator mail. They run as themselves, so if only the
// operator's own commands could nudge, incoming mail — the whole point — would
// never trigger a sync. cq's own credential is what makes an agent's nudge safe:
// the agent triggers it, and cq still reads the operator's mailbox.
func TestWithItsOwnCredentialAnyAgentsChangeNudges(t *testing.T) {
	env := mirrored()
	env[nudge.Mirrored] = "redjive"
	env[nudge.MirrorKey] = "the-operators-orc-key"
	env[nudge.OrcUser] = "some-agent"

	p := &probe{env: env}
	if !p.nudger().Fire() {
		t.Error("an agent's mail to the operator should reach the mirror")
	}
}

func TestAnUninstalledCQIsNotAFailure(t *testing.T) {
	p := &probe{env: mirrored(), missing: true}
	if p.nudger().Fire() {
		t.Error("a machine without cq should report nothing started")
	}
}

// TestAChildThatWillNotStartIsSwallowed: the caller has already delivered the
// mail, and the mirror being unreachable is not its problem.
func TestAChildThatWillNotStartIsSwallowed(t *testing.T) {
	p := &probe{env: mirrored(), failed: errors.New("fork: resource temporarily unavailable")}
	if p.nudger().Fire() {
		t.Error("a failed start should report false, not panic or return an error")
	}
}

func TestTheBinaryNameCanBeOverridden(t *testing.T) {
	env := mirrored()
	env[nudge.Binary] = "cq-testing"

	p := &probe{env: env}
	if !p.nudger().Fire() {
		t.Fatal("Fire should have started something")
	}
	if got := p.started[0][0]; !strings.HasSuffix(got, "cq-testing") {
		t.Errorf("ran %q, want the overridden name", got)
	}
}

// TestAfterUsesTheRealOperatingSystem covers the package-level entry point,
// which every caller uses and no seam reaches.
//
// It runs for real, against a script standing in for cq, and asserts the
// property the caller depends on: the child outlives the call rather than being
// waited for. A nudge that blocked would add a second to every `mailman send`.
func TestAfterUsesTheRealOperatingSystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in for cq is a shell script")
	}
	dir := t.TempDir()
	done := filepath.Join(dir, "ran")

	// Sleeps first, so a parent that waited would take a second and the file
	// would already exist when Fire returned.
	script := "#!/bin/sh\nsleep 1\necho \"$@\" > " + done + "\n"
	fake := filepath.Join(dir, "cq")
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv(nudge.Server, "https://cq.example")
	t.Setenv(nudge.Binary, fake)
	t.Setenv(nudge.Suppress, "")

	nudge.After()

	// Not waited for: the child is still sleeping.
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Errorf("Fire waited for the child; stat = %v", err)
	}

	// And it really was started, with the right arguments.
	if got := strings.TrimSpace(string(waitFor(t, done))); got != "sync --nudge" {
		t.Errorf("child ran with %q", got)
	}
}

// waitFor polls until the child has written its file, so the test does not
// depend on a fixed sleep being long enough.
func waitFor(t *testing.T, path string) []byte {
	t.Helper()
	for range 100 {
		if b, err := os.ReadFile(path); err == nil {
			return b
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the child never wrote %s", path)
	return nil
}
