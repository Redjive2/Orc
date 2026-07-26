package anno_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"orc/dock/internal/anno"
)

// annotated is a file carrying the annotations the real binary will be asked
// about: one unambiguous target, and one name that appears twice.
const annotated = `package example

// @:> section code
// @:> symbol Operate
func Operate() string {
	// @:> part decls
	x := "a"
	// @:< decls
	return x
}

// @:> symbol Reduce
func Reduce() string {
	// @:> part decls
	y := "b"
	// @:< decls
	return y
}
`

// buildAnno builds the real anno binary from the sibling module.
//
// The recorder tests above pin what Dock does with each answer; this one pins
// that the answers are what Dock thinks they are. An interop contract checked
// only against a stub is a contract checked against my assumptions about the
// other tool rather than against the tool.
func buildAnno(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("building the real anno binary is slow")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build with")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate the source tree")
	}
	annoDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "Anno")
	if _, err := os.Stat(filepath.Join(annoDir, "go.mod")); err != nil {
		t.Skipf("anno's module is not beside dock's: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "anno")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/anno")
	cmd.Dir = annoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build anno: %v\n%s", err, out)
	}
	return bin
}

func TestAgainstTheRealBinary(t *testing.T) {
	bin := buildAnno(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "example.go")
	if err := os.WriteFile(path, []byte(annotated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Put the built binary on PATH under the name New looks for, so this
	// exercises the same lookup a real invocation does.
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	tool := anno.New()
	if !tool.Available() {
		t.Fatal("anno was built but not found on PATH")
	}

	t.Run("a target that exists", func(t *testing.T) {
		got := tool.Read(path + ":Operate")
		if got.Verdict != anno.Exists {
			t.Fatalf("verdict = %v (%s)", got.Verdict, got.Why)
		}
		if !strings.Contains(got.Content, "func Operate()") {
			t.Errorf("content does not look like the annotation:\n%s", got.Content)
		}
	})

	t.Run("a target that does not", func(t *testing.T) {
		got := tool.Check(path + ":Nowhere")
		if got.Verdict != anno.Missing {
			t.Errorf("verdict = %v, want missing (%s)", got.Verdict, got.Why)
		}
	})

	t.Run("an ambiguous target", func(t *testing.T) {
		got := tool.Check(path + "^decls")
		if got.Verdict != anno.Ambiguous {
			t.Fatalf("verdict = %v, want ambiguous (%s)", got.Verdict, got.Why)
		}
		// anno lists every candidate fully qualified, and each is a valid
		// target — which is what makes carrying them through worth doing.
		if len(got.Candidates) < 2 {
			t.Fatalf("got %d candidates, want at least 2: %q", len(got.Candidates), got.Candidates)
		}
		for _, c := range got.Candidates {
			if !strings.Contains(c, "^decls") {
				t.Errorf("candidate %q does not look like a target", c)
			}
			if again := tool.Check(c); again.Verdict != anno.Exists {
				t.Errorf("candidate %q does not resolve: %v (%s)", c, again.Verdict, again.Why)
			}
		}
	})

	t.Run("a file that is not there", func(t *testing.T) {
		got := tool.Check(filepath.Join(dir, "nope.go") + ":Operate")
		if got.Verdict == anno.Exists {
			t.Error("a missing file resolved")
		}
	})
}
