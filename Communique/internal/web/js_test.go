package web_test

import (
	"os"
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

// TestEveryModuleParsesAsAModule checks what the browser will actually do with
// each file, which is not what `node --check` does with it by default.
//
// The site loads these as ES modules, and a module is strict-mode: two function
// declarations of one name is a SyntaxError that takes the *whole page* down —
// no interface, no error anybody can act on, just a blank screen and a message in
// a console most readers never open. As a plain script the same file is legal and
// the second declaration silently wins.
//
// That gap is not theoretical. A `hold(route)` added at the foot of app.js met a
// `hold(file)` two hundred lines up, `node --check app.js` passed, every test
// passed — the tests import the pure modules, and app.js is imported by nothing —
// and the site would not load. Copying to a `.mjs` is what makes node read them
// the way a browser does.
//
// Every file, not only the ones with tests. app.js is the one with no test to
// import it and the one whose failure is total, which is exactly the file a
// hand-written list would leave out.
func TestEveryModuleParsesAsAModule(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not installed, so the modules were not parsed: %v", err)
	}

	files, err := filepath.Glob("app/*.js")
	if err != nil {
		t.Fatalf("looking for the interface's modules: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no modules found; this check would silently pass for ever")
	}

	dir := t.TempDir()
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		// The extension is the whole trick: node decides script or module by it,
		// and only the module reading is the one the browser does.
		as := filepath.Join(dir, strings.TrimSuffix(filepath.Base(file), ".js")+".mjs")
		if err := os.WriteFile(as, body, 0o600); err != nil {
			t.Fatalf("writing %s: %v", as, err)
		}
		// Imports are not resolved by --check, so a copy with no siblings is fine:
		// what is being asked is whether this file is a legal module, not whether
		// the graph links up. The suite above covers that.
		cmd := exec.CommandContext(t.Context(), node, "--check", as)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s is not a valid module, so the site will not load:\n%s", file, out)
		}
	}
}
