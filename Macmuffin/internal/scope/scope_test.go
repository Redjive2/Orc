package scope_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/scope"
)

func parse(t *testing.T, entries ...string) scope.Set {
	t.Helper()
	s, err := scope.Parse(entries)
	if err != nil {
		t.Fatalf("Parse(%v): %v", entries, err)
	}
	return s
}

func TestParseNormalises(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"internal/tree/", "internal/tree/"},
		{"internal/tree", "internal/tree"},
		{"./internal/tree", "internal/tree"},
		{"internal//tree/", "internal/tree/"},
		{"internal/./tree", "internal/tree"},
		{"  cmd/anno/main.go  ", "cmd/anno/main.go"},
		{"*.go", "*.go"},
		{".", "./"},
		{"./", "./"},
	} {
		got := parse(t, tc.raw)
		if entries := got.Entries(); len(entries) != 1 || entries[0] != tc.want {
			t.Errorf("Parse(%q) = %v, want [%q]", tc.raw, entries, tc.want)
		}
	}

	// Duplicates collapse, order survives.
	got := parse(t, "b/", "a", "b/", "a")
	if strings.Join(got.Entries(), ",") != "b/,a" {
		t.Errorf("Parse did not dedupe in order: %v", got.Entries())
	}
}

func TestParseRejects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
	}{
		{"empty list", nil},
		{"empty entry", []string{""}},
		{"only space", []string{"   "}},
		{"absolute", []string{"/etc/passwd"}},
		{"escapes", []string{"../outside"}},
		{"escapes after cleaning", []string{"internal/../.."}},
		{"parent alone", []string{".."}},
		{"recursive glob", []string{"internal/**/x.go"}},
		{"nul", []string{"a\x00b"}},
		{"too long", []string{strings.Repeat("a", 2000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := scope.Parse(tc.entries); !errors.Is(err, fault.ErrUsage) {
				t.Errorf("Parse(%v) = %v, want a usage fault", tc.entries, err)
			}
		})
	}

	// Every bad entry is reported at once, not one per round trip.
	_, err := scope.Parse([]string{"good", "/absolute", "../escape"})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), "escape") {
		t.Errorf("both problems should be reported: %v", err)
	}
}

func TestMatches(t *testing.T) {
	set := parse(t, "internal/tree/", "cmd/anno/main.go", "*.md")

	for _, tc := range []struct {
		path string
		want bool
	}{
		// A directory entry covers the directory and everything under it.
		{"internal/tree", true},
		{"internal/tree/tree.go", true},
		{"internal/tree/deep/nested/file.go", true},
		// But not a sibling whose name merely starts the same way.
		{"internal/treehouse", false},
		{"internal/treehouse/x.go", false},
		{"internal/render/render.go", false},
		// An exact entry is exactly that.
		{"cmd/anno/main.go", true},
		{"cmd/anno/other.go", false},
		{"cmd/anno", false},
		// A glob matches one path element, as path.Match does.
		{"README.md", true},
		{"docs/README.md", false},
		{"README.txt", false},
	} {
		got, err := set.Matches(tc.path)
		if err != nil {
			t.Errorf("Matches(%q): %v", tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Matches(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestWholeWorktreeScope: `.` is spelled explicitly so it can never be the
// accidental result of a cleaned empty string.
func TestWholeWorktreeScope(t *testing.T) {
	set := parse(t, ".")
	for _, p := range []string{"a", "a/b/c.go", "deep/nested/thing"} {
		got, err := set.Matches(p)
		if err != nil || !got {
			t.Errorf("Matches(%q) = %v, %v; a scope of . covers everything", p, got, err)
		}
	}
}

// TestMatchesRefusesEscapes. A matcher that quietly accepted `../etc/passwd`
// would be the whole vulnerability.
func TestMatchesRefusesEscapes(t *testing.T) {
	set := parse(t, ".")
	for _, p := range []string{"../outside", "../../etc/passwd", "a/../../b"} {
		if _, err := set.Matches(p); !errors.Is(err, fault.ErrEscape) {
			t.Errorf("Matches(%q) = %v, want an escape fault", p, err)
		}
	}
	for _, p := range []string{"/etc/passwd", ""} {
		if _, err := set.Matches(p); err == nil {
			t.Errorf("Matches(%q) was accepted", p)
		}
	}
}

// TestZeroSetMatchesNothing: a scope that failed to load enforces everything
// rather than nothing, which is the safe direction.
func TestZeroSetMatchesNothing(t *testing.T) {
	var set scope.Set
	if !set.Empty() || set.Len() != 0 {
		t.Error("the zero Set should be empty")
	}
	got, err := set.Matches("anything")
	if err != nil || got {
		t.Errorf("the zero Set matched %v, %v", got, err)
	}
}

// TestResolveFollowsSymlinks is the property the whole package exists for: a
// scope check a symlink can walk around is decoration.
func TestResolveFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "internal", "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "tree", "tree.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "internal", "tree", "sneak")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	rel, err := scope.Resolve(root, "internal/tree/tree.go")
	if err != nil {
		t.Fatalf("Resolve on a real file: %v", err)
	}
	if rel != "internal/tree/tree.go" {
		t.Errorf("Resolve = %q", rel)
	}

	// Through the link the path is outside the root and must be refused, even
	// though its spelling sits squarely inside the scope.
	if _, err := scope.Resolve(root, "internal/tree/sneak/secret"); !errors.Is(err, fault.ErrEscape) {
		t.Errorf("Resolve through a symlink = %v, want an escape fault", err)
	}
	if _, err := scope.Resolve(root, "../"); !errors.Is(err, fault.ErrEscape) {
		t.Errorf("Resolve on the parent = %v, want an escape fault", err)
	}
	if _, err := scope.Resolve(root, outside); !errors.Is(err, fault.ErrEscape) {
		t.Errorf("Resolve on an absolute outside path = %v, want an escape fault", err)
	}
}

// TestResolveAllowsANewFile: creating a file inside the scope is ordinary, and
// must not need the file to exist first.
func TestResolveAllowsANewFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}

	rel, err := scope.Resolve(root, "internal/brand/new/file.go")
	if err != nil {
		t.Fatalf("Resolve on a new file: %v", err)
	}
	if rel != "internal/brand/new/file.go" {
		t.Errorf("Resolve = %q", rel)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "internal", "away")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := scope.Resolve(root, "internal/away/new.go"); !errors.Is(err, fault.ErrEscape) {
		t.Errorf("a new file through an escaping link = %v, want an escape fault", err)
	}
}

func TestResolveRejectsBadArguments(t *testing.T) {
	if _, err := scope.Resolve("", "x"); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Resolve with no root = %v, want an internal fault", err)
	}
	if _, err := scope.Resolve(t.TempDir(), " "); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Resolve with no path = %v, want an internal fault", err)
	}
	root := t.TempDir()
	if got, err := scope.Resolve(root, "."); err != nil || got != "." {
		t.Errorf("Resolve(root, \".\") = %q, %v", got, err)
	}
}

// FuzzMatches is milestone 5's acceptance criterion: no path that escapes the
// root may ever match, for any scope.
func FuzzMatches(f *testing.F) {
	for _, seed := range []string{
		"internal/tree/tree.go", "../outside", "a/../b", "", "/abs", ".",
		"internal/tree", "README.md", "a/b/../../../c",
	} {
		f.Add("internal/tree/", seed)
	}
	f.Add(".", "../x")
	f.Add("*.go", "x.go")

	f.Fuzz(func(t *testing.T, entry, target string) {
		set, err := scope.Parse([]string{entry})
		if err != nil {
			return // a scope that will not parse is never consulted
		}
		got, err := set.Matches(target)
		if err != nil {
			if !errors.Is(err, fault.ErrEscape) && !errors.Is(err, fault.ErrInternal) && !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Matches(%q, %q) failed with an unclassified error: %v", entry, target, err)
			}
			return
		}
		if got {
			clean := filepath.ToSlash(filepath.Clean(target))
			if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
				t.Fatalf("scope %q matched the escaping path %q", entry, target)
			}
		}
	})
}
