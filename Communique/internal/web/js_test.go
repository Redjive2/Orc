package web_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestJavaScript runs the pure modules' own tests under node.
//
// They are the parts of the interface with logic worth checking — the markdown
// renderer above all, which turns other people's text into elements. The view
// layer is not tested here: it is checked by eye against the sketch in the
// plan, and a headless assertion about element placement would be a test of
// this test rather than of the page.
//
// node is not a build dependency. When it is absent the suite says so and
// moves on, rather than failing on a machine that was never going to run it.
func TestJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not installed, so the interface's own tests did not run: %v", err)
	}

	// Every test file, found rather than listed: a file named here by hand is a
	// file that stops running the day someone forgets to add the next one.
	files, err := filepath.Glob("app/test/*.test.js")
	if err != nil {
		t.Fatalf("looking for the interface's tests: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test files found; the interface's own tests would silently not run")
	}
	args := []string{"--test"}
	for _, f := range files {
		args = append(args, strings.TrimPrefix(f, "app/"))
	}

	cmd := exec.CommandContext(t.Context(), node, args...)
	cmd.Dir = "app"
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "fail 0") {
		t.Errorf("node reported failures:\n%s", out)
	}
	t.Logf("%s", lastLines(string(out), 8))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
