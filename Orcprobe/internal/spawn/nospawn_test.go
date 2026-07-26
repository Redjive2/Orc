package spawn

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyThisPackageStartsProcesses is rule 1 made structural.
//
// Orcprobe must never bring an agent to life inside a probe. The way that is
// guaranteed is not care but arithmetic: every exec in the tree goes through
// this package, so the question "can orcprobe start an agent?" is answered by
// reading one file. If a future command imports os/exec directly, this fails
// and the answer has to be re-argued rather than assumed.
func TestOnlyThisPackageStartsProcesses(t *testing.T) {
	root := moduleRoot(t)
	allowed := map[string]bool{
		filepath.Join("internal", "spawn", "spawn.go"): true,
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			name := strings.Trim(imported.Path.Value, `"`)
			if (name == "os/exec" || name == "syscall") && !allowed[rel] && !isSyscallException(rel, name) {
				t.Errorf("%s imports %s; every process orcprobe starts must go through internal/spawn", rel, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// isSyscallException allows the one legitimate raw syscall in the tree: asking
// the terminal how wide it is. It cannot start anything.
func isSyscallException(rel, name string) bool {
	return name == "syscall" && rel == filepath.Join("cmd", "orcprobe", "width_unix.go")
}

// TestNothingNamesAClaudeSession is the same guarantee from the other side: not
// only can orcprobe not exec arbitrarily, it does not know how to ask for a
// session in the first place.
func TestNothingNamesAClaudeSession(t *testing.T) {
	root := moduleRoot(t)
	banned := []string{"claude-code", "claude_session", "StartSession", "SpawnAgent"}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Test files are excluded, and this one is why: the list of things a
		// probe must not know how to ask for cannot itself trip the check.
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, bad := range banned {
				if strings.Contains(lit.Value, bad) {
					t.Errorf("%s contains %q; a probe never starts an agent", rel, bad)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// moduleRoot walks up to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := filepath.Glob(filepath.Join(dir, "go.mod")); err == nil {
			if matches, _ := filepath.Glob(filepath.Join(dir, "go.mod")); len(matches) == 1 {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
