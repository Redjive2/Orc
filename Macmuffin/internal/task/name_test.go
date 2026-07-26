package task_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/task"
)

func TestParseNameNormalises(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "fix-the-parser", "fix-the-parser"},
		{"spaces become dashes", "Fix The Parser", "fix-the-parser"},
		{"underscores become dashes", "fix_the_parser", "fix-the-parser"},
		{"mixed separators", "Fix_The Parser", "fix-the-parser"},
		{"runs collapse", "fix   the___parser", "fix-the-parser"},
		{"dashes collapse too", "fix---the--parser", "fix-the-parser"},
		{"surrounding space", "  fix the parser  ", "fix-the-parser"},
		{"trailing separator drops", "fix-the-parser-", "fix-the-parser"},
		{"trailing underscore drops", "fix_the_parser_", "fix-the-parser"},
		{"uppercase", "FIXTHEPARSER", "fixtheparser"},
		{"digits", "fix-parser-2", "fix-parser-2"},
		{"leading digit", "2nd-pass", "2nd-pass"},
		{"dots survive", "anno.render", "anno.render"},
		{"single character", "x", "x"},
		{"tabs and newlines are separators", "fix\tthe\nparser", "fix-the-parser"},
		{"longest allowed", strings.Repeat("a", 80), strings.Repeat("a", 80)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := task.ParseName(tc.raw)
			if err != nil {
				t.Fatalf("ParseName(%q): %v", tc.raw, err)
			}
			if got.String() != tc.want {
				t.Errorf("ParseName(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if got.Zero() {
				t.Error("a parsed name should not be zero")
			}
		})
	}
}

// TestParseNameRejects covers every way a name could become dangerous as a path
// element, ambiguous as an argument, or unusable as a handle.
func TestParseNameRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"only space", "   "},
		{"only separators", "-_-"},
		{"traversal", ".."},
		{"single dot", "."},
		{"reserved pool", "pool"},
		{"reserved all", "all"},
		{"reserved none", "none"},
		{"reserved uppercase", "POOL"},
		{"reserved after normalising", "  Pool  "},
		{"slash", "a/b"},
		{"backslash", `a\b`},
		{"looks like a flag", "--force"},
		{"leading dot", ".hidden"},
		{"nul", "a\x00b"},
		{"colon", "a:b"},
		{"at sign", "a@b"},
		{"quote", `a"b`},
		{"non-ascii", "café"},
		{"emoji", "ship-it-🚀"},
		{"invalid utf8", "\xff\xfe"},
		{"too long", strings.Repeat("a", 81)},
		{"too long after normalising", strings.Repeat("a ", 45)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := task.ParseName(tc.raw); !errors.Is(err, fault.ErrUsage) {
				t.Errorf("ParseName(%q) = %q, %v; want a usage fault", tc.raw, got, err)
			}
		})
	}
}

// TestParseNameIsIdempotent is the property that makes a Name safe to
// re-normalise anywhere: a name that has been through ParseName once must go
// through unchanged the second time, or a lookup could miss a task that exists.
func TestParseNameIsIdempotent(t *testing.T) {
	for _, raw := range []string{
		"fix-the-parser", "Fix The Parser", "  a_b  ", "2nd-pass", "anno.render",
		"x", "a---b",
	} {
		first, err := task.ParseName(raw)
		if err != nil {
			t.Fatalf("ParseName(%q): %v", raw, err)
		}
		second, err := task.ParseName(first.String())
		if err != nil {
			t.Fatalf("re-parsing %q: %v", first, err)
		}
		if !first.Equal(second) {
			t.Errorf("ParseName is not idempotent: %q -> %q -> %q", raw, first, second)
		}
	}
}

// TestRenamedReportsTheMapping: an agent that asked for one spelling and got
// another should be able to see that it happened.
func TestRenamedReportsTheMapping(t *testing.T) {
	got, err := task.ParseName("Fix The Parser")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Renamed("Fix The Parser") {
		t.Error("a normalised name should report that it changed")
	}
	if got.Renamed("fix-the-parser") {
		t.Error("an already-normal name should not report a change")
	}
}

func TestNameOrdering(t *testing.T) {
	a, err := task.ParseName("alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := task.ParseName("beta")
	if err != nil {
		t.Fatal(err)
	}
	if a.Compare(b) >= 0 {
		t.Error("alpha should sort before beta")
	}
	if a.Compare(a) != 0 {
		t.Error("a name should compare equal to itself")
	}
	if !a.Equal(a) || a.Equal(b) {
		t.Error("Equal disagrees with Compare")
	}

	var zero task.Name
	if !zero.Zero() || zero.String() != "" {
		t.Error("the zero Name should report itself as zero")
	}
}

// FuzzParseName is milestone 1's acceptance criterion. Whatever arrives, the
// answer must be either a refusal or a name that is safe as a path element and
// stable under re-parsing.
func FuzzParseName(f *testing.F) {
	for _, seed := range []string{
		"fix-the-parser", "Fix The Parser", "", "..", "a/b", "--force",
		strings.Repeat("a", 90), "café", "a___b", "-", "x", "anno.render",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		name, err := task.ParseName(raw)
		if err != nil {
			if !errors.Is(err, fault.ErrUsage) && !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("ParseName(%q) failed with an unclassified error: %v", raw, err)
			}
			return
		}

		s := name.String()
		// Safe as a path element: nothing that could escape a directory or be
		// read as an option.
		if strings.ContainsAny(s, `/\:*?"<>| `+"\x00\n\r\t") {
			t.Fatalf("ParseName(%q) produced an unsafe name %q", raw, s)
		}
		if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "-") {
			t.Fatalf("ParseName(%q) produced %q", raw, s)
		}
		if len(s) > 80 {
			t.Fatalf("ParseName(%q) produced %d characters", raw, len(s))
		}

		// Stable: parsing the result must give the same thing back, or a
		// lookup could miss a task that exists.
		again, err := task.ParseName(s)
		if err != nil {
			t.Fatalf("ParseName(%q) produced %q, which does not re-parse: %v", raw, s, err)
		}
		if !again.Equal(name) {
			t.Fatalf("ParseName is not idempotent: %q -> %q -> %q", raw, s, again)
		}
	})
}
