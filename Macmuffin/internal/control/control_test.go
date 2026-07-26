package control_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/control"
)

func agent(t *testing.T, s string) user.Name {
	t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// patient is long enough that a loaded machine cannot turn an answer into a
// timeout. A timeout here refuses, so a test racing one would pass for the
// wrong reason.
const patient = 30 * time.Second

// fake writes a script named `orc` that exits with the given code, and puts it
// on PATH. Nothing is mocked: the point is what Exec does with a real process.
func fake(t *testing.T, code int, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(stderr, "'", `'\''`) + "' >&2\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "orc"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestControlIsAYes(t *testing.T) {
	fake(t, 0, "")
	if err := control.ExecWithin(patient, agent(t, "bob")); err != nil {
		t.Errorf("exit 0 was not read as control: %v", err)
	}
}

func TestRefusedIsOrcsWords(t *testing.T) {
	fake(t, fault.CodeDenied, "orc: bob may not direct carol: carol is not below bob in the tree")

	err := control.ExecWithin(patient, agent(t, "carol"))
	if !errors.Is(err, fault.ErrDenied) {
		t.Fatalf("err = %v, want a denial", err)
	}
	// Orc's reason survives, and Macmuffin does not say it again in its own
	// words — the reader would get the same sentence twice.
	got := err.Error()
	if !strings.Contains(got, "not below bob in the tree") {
		t.Errorf("orc's reason was lost: %q", got)
	}
	if strings.HasPrefix(got, "orc:") {
		t.Errorf("the message keeps a second tool prefix: %q", got)
	}
	if strings.Count(got, "may not") > 1 {
		t.Errorf("the refusal is doubled: %q", got)
	}
}

func TestUnknownAgentSaysAgent(t *testing.T) {
	fake(t, fault.CodeNotFound, "orc: nothing matches \"nobody\"")

	err := control.ExecWithin(patient, agent(t, "nobody"))
	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	// `assign` takes an agent *and* a task, so a bare "nothing matches" would
	// leave the reader guessing which one was missing.
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("the message should say it is the agent: %q", err)
	}
}

// TestEverythingElseIsRefused is the difference from Anno's scope guard, and
// the reason this package exists separately: a permission check that evaporates
// when its authority is unreachable is not a permission check.
func TestFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		what string
		code int
	}{
		{"a broken store", fault.CodeIO},
		{"no credential", fault.CodeAuth},
		{"a parse failure", fault.CodeParse},
		{"a crash inside orc", fault.CodeInternal},
		{"a status nobody has defined", 42},
	} {
		t.Run(tc.what, func(t *testing.T) {
			fake(t, tc.code, "some diagnostic")

			err := control.ExecWithin(patient, agent(t, "bob"))
			if err == nil {
				t.Fatalf("exit %d was read as permission to direct an agent", tc.code)
			}
			if !errors.Is(err, fault.ErrUnavailable) {
				t.Errorf("err = %v, want unavailable", err)
			}
		})
	}
}

func TestNoOrcInstalledRefuses(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := control.ExecWithin(patient, agent(t, "bob"))
	if !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("with no orc, err = %v, want unavailable", err)
	}
	// It says what to do instead, because "orc is not installed" is not
	// actionable to an agent that just wanted the task.
	if !strings.Contains(err.Error(), "muff claim") {
		t.Errorf("the refusal should redirect: %v", err)
	}
}

func TestHangRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orc"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	done := make(chan error, 1)
	go func() { done <- control.ExecWithin(200*time.Millisecond, agent(t, "bob")) }()

	select {
	case err := <-done:
		if !errors.Is(err, fault.ErrUnavailable) {
			t.Errorf("a hung orc gave %v, want a refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecWithin did not return; a stalled orc froze muff assign")
	}
}

func TestRejectsNoAgent(t *testing.T) {
	if err := control.ExecWithin(patient, user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Exec with no agent = %v", err)
	}
}

// --- identity ------------------------------------------------------------

// fakeOut writes an `orc` that prints to stdout and exits 0.
func fakeOut(t *testing.T, stdout string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '" + stdout + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "orc"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestIdentityVerified(t *testing.T) {
	fakeOut(t, "alice")
	if err := control.VerifiedWithin(patient, agent(t, "alice")); err != nil {
		t.Errorf("a matching identity was refused: %v", err)
	}
}

// TestAKeyBelongingToSomebodyElseIsRefused. Not a mistake anyone makes by
// accident: the credential is valid and names another agent.
func TestIdentityMismatchRefused(t *testing.T) {
	fakeOut(t, "bob")

	err := control.VerifiedWithin(patient, agent(t, "alice"))
	if !errors.Is(err, fault.ErrAuth) {
		t.Fatalf("err = %v, want an auth failure", err)
	}
	for _, want := range []string{"alice", "bob"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q: %v", want, err)
		}
	}
}

func TestBadCredentialRefused(t *testing.T) {
	fake(t, fault.CodeAuth, "orc: authentication failed")

	err := control.VerifiedWithin(patient, agent(t, "alice"))
	if !errors.Is(err, fault.ErrAuth) {
		t.Errorf("exit 7 was not treated as a refusal: %v", err)
	}
	var unverifiable control.Unverifiable
	if errors.As(err, &unverifiable) {
		t.Error("a definite no was reported as 'could not check'; the two mean opposite things")
	}
}

// TestNoAuthorityIsNotARefusal is the distinction the whole design rests on:
// "you are not who you say" and "nobody could be asked" must not be the same
// answer, or muff stops working on any machine without a fleet.
func TestUnverifiableIsNotARefusal(t *testing.T) {
	cases := map[string]func(){
		"no orc installed":                func() { t.Setenv("PATH", t.TempDir()) },
		"a store orc cannot read":         func() { fake(t, fault.CodeIO, "orc: the fleet will not load") },
		"a crash inside orc":              func() { fake(t, fault.CodeInternal, "orc: panic") },
		"an identity that will not parse": func() { fakeOut(t, "not a valid name") },
	}

	for what, setup := range cases {
		t.Run(what, func(t *testing.T) {
			setup()

			err := control.VerifiedWithin(patient, agent(t, "alice"))
			var unverifiable control.Unverifiable
			if !errors.As(err, &unverifiable) {
				t.Fatalf("%s gave %v, want Unverifiable", what, err)
			}
			if errors.Is(err, fault.ErrAuth) {
				t.Error("an unanswerable question was reported as a failed authentication")
			}
		})
	}
}

func TestVerifiedRejectsNoIdentity(t *testing.T) {
	if err := control.VerifiedWithin(patient, user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Verified with no identity = %v", err)
	}
}
