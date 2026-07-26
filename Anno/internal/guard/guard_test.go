package guard_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"orc/anno/internal/guard"
	"orc/common/fault"
)

// TestARefusalIsExitNine, and carries Macmuffin's own message.
func TestRefused(t *testing.T) {
	err := guard.Refused{Path: "a.go", Detail: "a.go is outside the scope of fix-the-parser"}

	if !errors.Is(err, fault.ErrScope) {
		t.Error("a refusal should read as a scope fault, so both tools exit 9")
	}
	if !strings.Contains(err.Error(), "outside the scope of fix-the-parser") {
		t.Errorf("the message should be Macmuffin's own: %q", err)
	}
	// Without a message it still says something true rather than nothing.
	bare := guard.Refused{Path: "a.go"}
	if !strings.Contains(bare.Error(), "a.go") {
		t.Errorf("a detail-less refusal should still name the path: %q", bare)
	}
}

// fake builds a script named `muff` that exits with the given code and stderr,
// and puts it on PATH. Nothing here mocks exec: the point is to test what Exec
// does with a real process.
func fake(t *testing.T, code int, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}

	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(stderr) + " >&2\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "muff"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func timeout() <-chan time.Time { return time.After(5 * guard.Deadline) }

// patient is long enough that a loaded machine cannot turn a refusal into a
// timeout, which would pass by failing open and prove nothing.
const patient = 30 * time.Second

func TestExecRefusesOnNine(t *testing.T) {
	fake(t, guard.CodeOutOfScope, "muff: internal/render.go is outside the scope of fix-the-parser.\n\n  in scope:  internal/tree/\n")

	// A generous bound: this test is about what an exit 9 means, not about how
	// fast a machine under `go test -race` can fork a shell.
	err := guard.ExecWithin(patient, "internal/render.go")
	if err == nil {
		t.Fatal("exit 9 was not treated as a refusal")
	}
	if !errors.Is(err, fault.ErrScope) {
		t.Errorf("err = %v, want a scope fault", err)
	}
	// Macmuffin's message comes through, minus the tool prefix Anno is about to
	// print itself.
	got := err.Error()
	if strings.HasPrefix(got, "muff:") {
		t.Errorf("the message keeps a second tool prefix: %q", got)
	}
	for _, want := range []string{"outside the scope of fix-the-parser", "in scope", "internal/tree/"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message should keep %q:\n%s", want, got)
		}
	}
}

// TestEverythingExceptADefiniteNoIsAYes. Anno worked before Macmuffin existed
// and has to keep working where it does not.
func TestExecFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		what string
		code int
	}{
		{"in scope", 0},
		{"usage", 1},
		{"no such task", 2},
		{"an unreadable store", 5},
		{"no identity", 7},
		{"an escaping path", 11},
		{"a crash inside muff", 70},
		{"a status nobody has defined", 42},
	} {
		t.Run(tc.what, func(t *testing.T) {
			fake(t, tc.code, "some diagnostic")
			if err := guard.ExecWithin(patient, "a.go"); err != nil {
				t.Errorf("exit %d refused the write: %v", tc.code, err)
			}
		})
	}
}

func TestExecWithoutMacmuffin(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on it
	if err := guard.Exec("a.go"); err != nil {
		t.Errorf("with no muff installed, Exec = %v; there is nothing to enforce", err)
	}
}

// A muff that hangs must cost a pause, not a session.
func TestExecGivesUpOnAHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "muff"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	done := make(chan error, 1)
	go func() { done <- guard.Exec("a.go") }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a hung muff refused the write: %v", err)
		}
	case <-timeout():
		t.Fatal("Exec did not return; a stalled Macmuffin froze anno write")
	}
}
