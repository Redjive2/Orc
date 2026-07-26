package scan_test

import (
	"strings"
	"testing"

	"orc/dock/internal/scan"
)

// kinds renders a scan as one character per line, so a whole document's
// classification is one readable string in a table test.
func kinds(r scan.Result) string {
	var b strings.Builder
	for _, l := range r.Lines() {
		switch l.Kind() {
		case scan.Text:
			b.WriteByte('.')
		case scan.Heading:
			b.WriteByte('H')
		case scan.Fence:
			b.WriteByte('F')
		case scan.Code:
			b.WriteByte('c')
		case scan.Comment:
			b.WriteByte('#')
		}
	}
	return b.String()
}

func TestClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"prose", "one\ntwo", ".."},
		{"heading", "# Title\nbody", "H."},
		{"all six levels", "#a\n# a\n## a\n### a\n#### a\n##### a\n###### a\n####### a", ".HHHHHH."},
		{"fence", "a\n```\ncode\n```\nb", ".FcF."},
		{"tilde fence", "~~~\ncode\n~~~", "FcF"},
		{"unterminated fence", "```\ncode\nmore", "Fcc"},
		{"comment one line", "<!-- x -->\na", ".."},
		{"comment spanning", "<!--\nhidden\n-->\na", ".##."},
		{"indented four spaces is not a heading", "    # x", "."},
		{"indented three spaces is a heading", "   # x", "H"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := kinds(scan.Scan(tc.in)); got != tc.want {
				t.Errorf("kinds = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAFenceNeverLeaks is the property the whole package exists for. Dock's own
// documentation shows example headings and example links inside fences, and
// every one of them must be invisible.
func TestAFenceNeverLeaks(t *testing.T) {
	doc := "# Real §1 Heading\n" +
		"```markdown\n" +
		"## Example §1.1 Not A Heading\n" +
		"[not a link](./x.md§1)\n" +
		"```\n" +
		"[a link](./y.md§2)\n"

	r := scan.Scan(doc)
	var heads []string
	for _, l := range r.Lines() {
		if l.Kind() == scan.Heading {
			heads = append(heads, l.Head())
		}
	}
	if len(heads) != 1 || heads[0] != "Real §1 Heading" {
		t.Errorf("headings = %q, want just the real one", heads)
	}
	links := r.Links()
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1: %+v", len(links), links)
	}
	if links[0].Dest() != "./y.md§2" {
		t.Errorf("dest = %q, want the one outside the fence", links[0].Dest())
	}
}

func TestHeadingText(t *testing.T) {
	for _, tc := range []struct {
		in    string
		level int
		want  string
	}{
		{"# Title", 1, "Title"},
		{"## §1.2 Sections", 2, "§1.2 Sections"},
		{"###   spaced   ", 3, "spaced"},
		{"## Closed ##", 2, "Closed"},
		{"## Hash#Inside", 2, "Hash#Inside"},
		{"## Trailing hashes not spaced##", 2, "Trailing hashes not spaced##"},
		{"#", 1, ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			lines := scan.Scan(tc.in).Lines()
			if len(lines) != 1 {
				t.Fatalf("got %d lines", len(lines))
			}
			if lines[0].Kind() != scan.Heading {
				t.Fatalf("kind = %v, want heading", lines[0].Kind())
			}
			if got := lines[0].Level(); got != tc.level {
				t.Errorf("level = %d, want %d", got, tc.level)
			}
			if got := lines[0].Head(); got != tc.want {
				t.Errorf("head = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNotHeadings(t *testing.T) {
	for _, in := range []string{"#nospace", "text # x", "    # indented", "####### seven"} {
		if k := scan.Scan(in).Lines()[0].Kind(); k == scan.Heading {
			t.Errorf("%q classified as a heading", in)
		}
	}
}

func TestLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string // "text -> dest", in order
	}{
		{"simple", "see [the grammar](./g.md§2.1) here", []string{"the grammar -> ./g.md§2.1"}},
		{"same file", "start with [Install](§1.1)", []string{"Install -> §1.1"}},
		{"anno target", "[how](../a/example.go@code:Operate)", []string{"how -> ../a/example.go@code:Operate"}},
		{"two on a line", "[a](x§1) and [b](y§2)", []string{"a -> x§1", "b -> y§2"}},
		{"image skipped", "![alt](pic.png)", nil},
		{"image then link", "![alt](p.png) [a](x§1)", []string{"a -> x§1"}},
		{"in code span", "`[a](x§1)`", nil},
		{"code span then link", "`code` [a](x§1)", []string{"a -> x§1"}},
		{"escaped bracket", `\[a](x§1)`, nil},
		{"angle destination", "[a](<x §1.md>)", []string{"a -> x §1.md"}},
		{"title dropped", `[a](x§1 "the title")`, []string{"a -> x§1"}},
		{"balanced parens", "[a](x(1)§2)", []string{"a -> x(1)§2"}},
		{"unclosed", "[a](x§1", nil},
		{"empty text", "[](x§1)", []string{" -> x§1"}},
		{"in comment", "<!-- [a](x§1) -->", nil},
		{"after comment", "<!-- c --> [a](x§1)", []string{"a -> x§1"}},
		{"before comment", "[a](x§1) <!-- c -->", []string{"a -> x§1"}},
		{"ordinary link kept", "[docs](https://example.com)", []string{"docs -> https://example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, l := range scan.Scan(tc.in).Links() {
				got = append(got, l.Text()+" -> "+l.Dest())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("link %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestColumnsAreRunes: a column that counted bytes would point into the middle
// of a character in any document with a § in it, which is all of them.
func TestColumnsAreRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"[a](x§1)", 1},
		{"ab [a](x§1)", 4},
		{"§§§ [a](x§1)", 5},
		{"日本語 [a](x§1)", 5},
	} {
		t.Run(tc.in, func(t *testing.T) {
			links := scan.Scan(tc.in).Links()
			if len(links) != 1 {
				t.Fatalf("got %d links", len(links))
			}
			if got := links[0].Col(); got != tc.want {
				t.Errorf("col = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLineNumbersAndText(t *testing.T) {
	r := scan.Scan("one\r\ntwo\nthree\n")
	lines := r.Lines()
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, want := range []string{"one", "two", "three"} {
		if lines[i].Num() != i+1 {
			t.Errorf("line %d numbered %d", i, lines[i].Num())
		}
		if lines[i].Text() != want {
			t.Errorf("text %d = %q, want %q (CR must be stripped)", i, lines[i].Text(), want)
		}
	}
}

// TestResultsAreCopies: a caller must not be able to reach into a scan and
// change it, since everything downstream treats it as frozen.
func TestResultsAreCopies(t *testing.T) {
	r := scan.Scan("# a\n[x](y§1)")
	got := r.Lines()
	got[0] = scan.Line{}
	if r.Lines()[0].Kind() != scan.Heading {
		t.Error("mutating the returned slice changed the result")
	}
	ls := r.Links()
	ls[0] = scan.Link{}
	if r.Links()[0].Dest() != "y§1" {
		t.Error("mutating the returned links changed the result")
	}
}

func TestScanIsTotal(t *testing.T) {
	for _, in := range []string{
		"", "\n", "\n\n\n", "```", "~~~", "<!--", "-->", "[", "](", "[]()",
		"`", "``", "```` ````", "\x00", "# \x00", "[a](\x00)", strings.Repeat("[a](x)", 200),
	} {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("Scan(%q) panicked: %v", in, p)
				}
			}()
			scan.Scan(in)
		}()
	}
}

// TestLinksPerLineAreBounded: a generated file of nothing but links must not
// turn into an unbounded slice.
func TestLinksPerLineAreBounded(t *testing.T) {
	line := strings.Repeat("[a](x§1)", scan.MaxLinksPerLine+50)
	if got := len(scan.Scan(line).Links()); got != scan.MaxLinksPerLine {
		t.Errorf("got %d links, want the cap of %d", got, scan.MaxLinksPerLine)
	}
}

func FuzzScan(f *testing.F) {
	for _, s := range []string{
		"# a\n[x](y§1)\n", "```\n# no\n```\n", "<!-- [a](b) -->\n",
		"## §1.1 T\n`[a](b)`\n", "~~~\n~~~\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		r := scan.Scan(in)

		// One classified line per input line, always.
		if got, want := len(r.Lines()), len(scan.Scan(in).Lines()); got != want {
			t.Fatalf("scan is not deterministic: %d then %d", want, got)
		}

		for _, l := range r.Lines() {
			// A fence never leaks: nothing inside one is ever a heading.
			if l.Kind() == scan.Code && l.Level() != 0 {
				t.Errorf("line %d inside a fence carries a heading level", l.Num())
			}
			if l.Kind() != scan.Heading && (l.Level() != 0 || l.Head() != "") {
				t.Errorf("line %d is %v but carries heading data", l.Num(), l.Kind())
			}
			if l.Kind() == scan.Heading && (l.Level() < 1 || l.Level() > 6) {
				t.Errorf("line %d has heading level %d", l.Num(), l.Level())
			}
		}

		lines := r.Lines()
		for _, lk := range r.Links() {
			if lk.Line() < 1 || lk.Line() > len(lines) {
				t.Fatalf("link on line %d, outside 1..%d", lk.Line(), len(lines))
			}
			if lk.Col() < 1 {
				t.Errorf("link at column %d", lk.Col())
			}
			// A link is never reported from inside code or a comment.
			if k := lines[lk.Line()-1].Kind(); k == scan.Code || k == scan.Comment {
				t.Errorf("link reported on a %v line", k)
			}
		}
	})
}
