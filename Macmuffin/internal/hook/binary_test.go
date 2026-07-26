package hook_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orc/macmuffin/internal/hook"
	"orc/macmuffin/internal/store"
)

// TestTheRealBinary builds and runs muff-hook the way Claude Code does: a
// process, an event on stdin, an exit code out.
//
// Everything else in this package tests hook.Run, which cannot catch a mistake
// in main — the wrong stream wired up, a swallowed exit code, an environment the
// process reads differently from the tests.
func TestTheRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	binary := filepath.Join(t.TempDir(), "muff-hook")
	build := exec.Command("go", "build", "-o", binary, "orc/macmuffin/cmd/muff-hook")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building muff-hook: %v", err)
	}

	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	run := func(input []byte) (int, string) {
		t.Helper()
		cmd := exec.Command(binary)
		cmd.Stdin = bytes.NewReader(input)
		cmd.Dir = r.tree
		// The store is found through the environment, which is the only way a
		// hook has of being told anything.
		cmd.Env = append(os.Environ(), store.EnvHome+"="+r.root)

		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()

		var exit *exec.ExitError
		code := 0
		if err != nil {
			if !asExit(err, &exit) {
				t.Fatalf("running the hook: %v", err)
			}
			code = exit.ExitCode()
		}
		// A PreToolUse hook that says nothing on stdout costs the session
		// nothing, and anything it did print would be read as JSON.
		if stdout.Len() != 0 {
			t.Errorf("the hook wrote to stdout: %q", stdout.String())
		}
		return code, stderr.String()
	}

	code, stderr := run(r.event("Edit", filepath.Join(r.tree, "internal/render.go")))
	if code != hook.CodeBlock {
		t.Errorf("an out-of-scope edit exited %d, want %d\n%s", code, hook.CodeBlock, stderr)
	}
	if !strings.Contains(stderr, "outside the scope of fix-the-parser") {
		t.Errorf("stderr:\n%s", stderr)
	}

	if code, stderr := run(r.event("Edit", filepath.Join(r.tree, "internal/tree/tree.go"))); code != hook.CodeOK {
		t.Errorf("an in-scope edit exited %d\n%s", code, stderr)
	}
	if code, stderr := run([]byte("not json at all")); code != hook.CodeOK {
		t.Errorf("nonsense on stdin exited %d\n%s", code, stderr)
	}
	if code, stderr := run(nil); code != hook.CodeOK {
		t.Errorf("empty stdin exited %d\n%s", code, stderr)
	}
}

func asExit(err error, out **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*out = e
	}
	return ok
}
