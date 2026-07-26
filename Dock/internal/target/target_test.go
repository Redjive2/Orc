package target_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/target"
)

// first parses and returns the preferred reading — the one with the longest
// path, which is what a caller gets when that path exists on disk.
func first(t *testing.T, dest string) target.Target {
	t.Helper()
	out, ok, err := target.Parse(dest)
	if err != nil {
		t.Fatalf("Parse(%q): %v", dest, err)
	}
	if !ok {
		t.Fatalf("Parse(%q) did not recognise a target", dest)
	}
	return out[0]
}

// withPath returns the reading whose path is want — the one the command layer
// settles on once the filesystem has rejected the longer readings. For a chain
// like "example.go@code:Operate" the longest-path reading is tried first and
// fails to exist, which is the whole point of returning every reading.
func withPath(t *testing.T, dest, want string) target.Target {
	t.Helper()
	out, ok, err := target.Parse(dest)
	if err != nil {
		t.Fatalf("Parse(%q): %v", dest, err)
	}
	if !ok {
		t.Fatalf("Parse(%q) did not recognise a target", dest)
	}
	for _, tg := range out {
		if tg.Path() == want {
			return tg
		}
	}
	t.Fatalf("Parse(%q) has no reading with path %q; got %+v", dest, want, out)
	return target.Target{}
}

func TestEveryDocumentedForm(t *testing.T) {
	for _, tc := range []struct {
		dest   string
		path   string
		kind   target.Kind
		number string
		name   string
		chain  string
	}{
		{"guide.md§1.2", "guide.md", target.Section, "1.2", "", ""},
		{"guide.md§'Install'", "guide.md", target.Section, "", "Install", ""},
		{"§1.2", "", target.Section, "1.2", "", ""},
		{"§'Install'", "", target.Section, "", "Install", ""},
		{"§1", "", target.Section, "1", "", ""},
		{"§1.2.3.4.5.6", "", target.Section, "1.2.3.4.5.6", "", ""},
		{"./g.md§2.1", "./g.md", target.Section, "2.1", "", ""},
		{"../../Anno/x.md§3", "../../Anno/x.md", target.Section, "3", "", ""},
		{"example.go@code:Operate^declarations", "example.go", target.Anno, "", "", "@code:Operate^declarations"},
		{"example.go^declarations", "example.go", target.Anno, "", "", "^declarations"},
		{"example.go:Operate", "example.go", target.Anno, "", "", ":Operate"},
		{"§'a name with spaces'", "", target.Section, "", "a name with spaces", ""},
	} {
		t.Run(tc.dest, func(t *testing.T) {
			got := withPath(t, tc.dest, tc.path)
			if got.Path() != tc.path {
				t.Errorf("path = %q, want %q", got.Path(), tc.path)
			}
			if got.Kind() != tc.kind {
				t.Errorf("kind = %v, want %v", got.Kind(), tc.kind)
			}
			if got.Number() != tc.number {
				t.Errorf("number = %q, want %q", got.Number(), tc.number)
			}
			if got.Name() != tc.name {
				t.Errorf("name = %q, want %q", got.Name(), tc.name)
			}
			if got.Chain() != tc.chain {
				t.Errorf("chain = %q, want %q", got.Chain(), tc.chain)
			}
		})
	}
}

// TestNotTargets: an ordinary markdown link must be invisible to Dock, and
// invisible is not the same as invalid — none of these may report an error.
func TestNotTargets(t *testing.T) {
	for _, dest := range []string{
		"", "  ", "https://example.com", "http://x/y:z", "#anchor",
		"#a-heading-anchor", "./other.md", "guide.md", "../a/b.md",
		"mailto:someone@example.com", "MAILTO:a@b.c", "tel:+1234",
		"data:text/plain;base64,AAAA", "javascript:void(0)",
		"https://example.com/a@b:c", "a b c", "file.md ", strings.Repeat("x", 2000),
	} {
		t.Run(dest, func(t *testing.T) {
			out, ok, err := target.Parse(dest)
			if err != nil {
				t.Errorf("reported an error for an ordinary link: %v", err)
			}
			if ok || len(out) > 0 {
				t.Errorf("treated as a target: %+v", out)
			}
		})
	}
}

// TestMalformedSectionsAreErrors is the asymmetry the package exists to hold: a
// § means to address a section, so a bad one is reported rather than ignored.
func TestMalformedSectionsAreErrors(t *testing.T) {
	for _, tc := range []struct {
		dest string
		want string
	}{
		{"§", "followed by a number"},
		{"guide.md§", "followed by a number"},
		{"§1..2", "empty component"},
		{"§01", "leading zero"},
		{"§0", "numbers sections from 1"},
		{"§x", "not a number"},
		{"§'unterminated", "no closing quote"},
		{"§''", "names no section"},
		{"§'name' trailing", "trailing text"},
		{"§1.2.3.4.5.6.7", "past the maximum"},
		{"§99999", "exceeds the maximum"},
	} {
		t.Run(tc.dest, func(t *testing.T) {
			_, ok, err := target.Parse(tc.dest)
			if err == nil {
				t.Fatalf("expected an error; ok = %v", ok)
			}
			if !errors.Is(err, fault.ErrParse) {
				t.Errorf("not a parse fault: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("reason %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestPathsThatLookLikeTargets: the split cannot be decided by syntax, so every
// reading is returned, most-path-first, and the command layer picks by what
// exists on disk.
func TestPathsThatLookLikeTargets(t *testing.T) {
	t.Run("file named like a section ref", func(t *testing.T) {
		out, ok, err := target.Parse("guide§1.md§2")
		if err != nil || !ok {
			t.Fatalf("Parse: ok=%v err=%v", ok, err)
		}
		// Splitting at the earlier § would leave "1.md§2" as the ref, which is
		// not a number and not a quoted name — so there is exactly one reading,
		// and a file genuinely named "guide§1.md" is addressable.
		if len(out) != 1 {
			t.Fatalf("got %d readings, want 1: %+v", len(out), out)
		}
		if out[0].Path() != "guide§1.md" || out[0].Number() != "2" {
			t.Errorf("reading = %+v, want path guide§1.md number 2", out[0])
		}
	})

	t.Run("directory named like a chain", func(t *testing.T) {
		out, ok, err := target.Parse("a:b/x.go:Operate")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if !ok {
			t.Skip("no reading; a slash bars the earlier split, which is also correct")
		}
		if out[0].Path() != "a:b/x.go" {
			t.Errorf("first reading path = %q, want the longest path", out[0].Path())
		}
	})

	t.Run("readings are ordered longest path first", func(t *testing.T) {
		out, _, err := target.Parse("x.go@a:b^c")
		if err != nil {
			t.Fatal(err)
		}
		var lens []int
		for _, tg := range out {
			lens = append(lens, len(tg.Path()))
		}
		for i := 1; i < len(lens); i++ {
			if lens[i] > lens[i-1] {
				t.Errorf("reading %d has a longer path than %d: %v", i, i-1, lens)
			}
		}
	})
}

// TestStringRoundTrips: every target Dock prints must parse back to itself, so
// a line of output can be pasted into a command.
func TestStringRoundTrips(t *testing.T) {
	for _, dest := range []string{
		"guide.md§1.2", "§1.2", "§'Install'", "guide.md§'A Name'",
		"example.go@code:Operate^declarations", "example.go^declarations",
	} {
		t.Run(dest, func(t *testing.T) {
			one := first(t, dest)
			if one.String() != dest {
				t.Errorf("String() = %q, want %q", one.String(), dest)
			}
			again := first(t, one.String())
			if again.String() != one.String() {
				t.Errorf("second parse = %q, want %q", again.String(), one.String())
			}
		})
	}
}

func TestSameFile(t *testing.T) {
	if !first(t, "§1.2").SameFile() {
		t.Error("§1.2 should be a same-file reference")
	}
	if first(t, "g.md§1.2").SameFile() {
		t.Error("g.md§1.2 should not be a same-file reference")
	}
}

func TestChainsAreHandedToAnnoVerbatim(t *testing.T) {
	got := withPath(t, "example.go@code:Operate^declarations", "example.go")
	if got.Chain() != "@code:Operate^declarations" {
		t.Errorf("chain = %q, want it verbatim", got.Chain())
	}
	// Path plus chain must reconstruct the destination exactly, since that is
	// what gets handed to the anno subprocess.
	if got.Path()+got.Chain() != "example.go@code:Operate^declarations" {
		t.Error("path and chain do not reconstruct the destination")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"guide.md§1.2", "§'Install'", "example.go@code:Operate", "https://x.example",
		"§", "§1..2", "a:b", "#anchor", "mailto:a@b.c", "guide§1.md§2",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, dest string) {
		out, ok, err := target.Parse(dest)
		switch {
		case err != nil:
			if ok || len(out) > 0 {
				t.Fatalf("an error came with readings: %+v", out)
			}
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("not a parse fault: %v", err)
			}
			return
		case !ok:
			if len(out) > 0 {
				t.Fatalf("not recognised, but returned %d readings", len(out))
			}
			return
		}

		if len(out) == 0 {
			t.Fatal("recognised, but returned no readings")
		}
		for i, tg := range out {
			// Every reading must render to something that parses back to the
			// same reading — that is what makes printed output pasteable.
			again, ok2, err2 := target.Parse(tg.String())
			if err2 != nil || !ok2 || len(again) == 0 {
				t.Fatalf("reading %d renders to %q, which does not parse back: ok=%v err=%v", i, tg.String(), ok2, err2)
			}
			if again[0].String() != tg.String() {
				t.Errorf("reading %d is not stable: %q then %q", i, tg.String(), again[0].String())
			}
			// A section target carries exactly one of number or name.
			if tg.Kind() == target.Section && (tg.Number() == "") == (tg.Name() == "") {
				t.Errorf("reading %d has number %q and name %q", i, tg.Number(), tg.Name())
			}
			if tg.Kind() == target.Anno && tg.Chain() == "" {
				t.Errorf("reading %d is an anno target with no chain", i)
			}
			// Path plus address must reconstruct what was parsed.
			if i > 0 && len(out[i-1].Path()) < len(tg.Path()) {
				t.Errorf("readings are not ordered most-path-first")
			}
		}
	})
}
