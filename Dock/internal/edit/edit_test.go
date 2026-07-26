package edit_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/common/source"
	"orc/dock/internal/doc"
	"orc/dock/internal/edit"
	"orc/dock/internal/fixture"
	"orc/dock/internal/scan"
)

// staged writes a document and loads it.
func staged(t *testing.T, body string) (string, source.File, doc.Doc) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guide.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := doc.Build(path, scan.Scan(string(f.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	return path, f, d
}

func section(t *testing.T, d doc.Doc, number string) doc.Section {
	t.Helper()
	s, ok := d.ByNumber(number)
	if !ok {
		t.Fatalf("§%s not found", number)
	}
	return s
}

// TestWriteReadRoundTrip is the property the whole package exists to hold:
// writing back exactly what was read leaves the file byte-identical.
//
// It is Anno's FuzzPipeline property, which found four real defects there. Here
// it runs over every section of the corpus, in both spans.
func TestWriteReadRoundTrip(t *testing.T) {
	for name, body := range map[string]string{
		"guide":       fixture.Guide,
		"grammar":     fixture.Grammar,
		"trouble":     fixture.Trouble,
		"crlf":        "# §1 A\r\n\r\nbody\r\n\r\n## §1.1 B\r\n\r\nmore\r\n",
		"no final nl": "# §1 A\n\nbody",
		"empty own":   "# §1 A\n## §1.1 B\n\ntext\n",
		"tabs":        "# §1 A\n\n\tindented\n  spaced\n",
	} {
		t.Run(name, func(t *testing.T) {
			for _, tree := range []bool{false, true} {
				path, f, d := staged(t, body)
				original := string(f.Bytes())

				for _, s := range d.Sections() {
					span := s.Own()
					if tree {
						span = s.Tree()
					}
					content := ""
					if !span.Empty() {
						raw, err := f.Slice(span.Start(), span.End())
						if err != nil {
							t.Fatal(err)
						}
						content = string(raw)
					}

					plan, err := edit.Prepare(f, d, s, tree, content)
					if err != nil {
						t.Fatalf("§%s (tree=%v): Prepare: %v", s.Number(), tree, err)
					}
					if got := string(plan.Result()); got != original {
						t.Errorf("§%s (tree=%v) did not round-trip:\n got %q\nwant %q",
							s.Number(), tree, got, original)
					}
				}
				_ = path
			}
		})
	}
}

// TestTheTreeIsInvariant. A write may change a section's words, never the
// document's shape.
func TestTheTreeIsInvariant(t *testing.T) {
	_, f, d := staged(t, fixture.Guide)
	for _, s := range d.Sections() {
		plan, err := edit.Prepare(f, d, s, false, "replaced prose\n")
		if err != nil {
			t.Fatalf("§%s: %v", s.Number(), err)
		}
		after, err := doc.Build("guide.md", scan.Scan(string(plan.Result())))
		if err != nil {
			t.Fatalf("§%s: the result does not parse: %v", s.Number(), err)
		}
		if after.Len() != d.Len() {
			t.Errorf("§%s: section count changed from %d to %d", s.Number(), d.Len(), after.Len())
		}
		for i, was := range d.Sections() {
			is, _ := after.At(i)
			if is.Number() != was.Number() || is.Name() != was.Name() {
				t.Errorf("§%s: writing changed §%s %q into §%s %q",
					s.Number(), was.Number(), was.Name(), is.Number(), is.Name())
			}
		}
	}
}

func TestRefusedContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		number  string
		tree    bool
		content string
		want    string
	}{
		{"a section in own prose", "1.2", false, "text\n\n## §1.3 New\n\nmore\n", "may not declare a section"},
		{"a deeper section in own prose", "1.2", false, "### §1.2.9 New\n", "may not declare a section"},
		{"a sibling under --tree", "1.2", true, "text\n\n## §1.3 Sibling\n", "would end"},
		{"a shallower section under --tree", "1.2", true, "# §2 Chapter\n", "would end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, f, d := staged(t, fixture.Guide)
			_, err := edit.Prepare(f, d, section(t, d, tc.number), tc.tree, tc.content)
			if err == nil {
				t.Fatal("the content was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("reason %q does not mention %q", err.Error(), tc.want)
			}
			if got := fault.Code(err); got != fault.CodeUsage {
				t.Errorf("code = %d, want usage", got)
			}
		})
	}
}

// TestAnUnmarkedHeadingIsProse: it is allowed, and has to be, or the round trip
// breaks for any section whose prose contains one.
func TestAnUnmarkedHeadingIsProse(t *testing.T) {
	_, f, d := staged(t, "# §1 A\n\ntext\n")
	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "text\n\n## Just A Heading\n\nmore\n")
	if err != nil {
		t.Fatalf("an unmarked heading was refused: %v", err)
	}
	if !strings.Contains(string(plan.Result()), "## Just A Heading") {
		t.Error("the heading did not survive the write")
	}
}

// TestAMalformedLinkIsRefused: content carrying a broken destination would
// otherwise produce a file that indexes fine and whose links have vanished.
func TestAMalformedLinkIsRefused(t *testing.T) {
	_, f, d := staged(t, "# §1 A\n\ntext\n")
	_, err := edit.Prepare(f, d, section(t, d, "1"), false, "see [x](§1..2)\n")
	if err == nil {
		t.Fatal("a malformed link was accepted")
	}
	if !errors.Is(err, fault.ErrConflict) {
		t.Errorf("not a conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "malformed link") {
		t.Errorf("reason does not say what was wrong: %v", err)
	}
}

func TestWritingIntoAnEmptySection(t *testing.T) {
	for _, body := range []string{
		"# §1 A\n## §1.1 B\n\ntext\n", // §1 has no own prose
		"# §1 A\n",                    // §1 has nothing at all, and no final line after it
		"# §1 A",                      // no final newline either
	} {
		t.Run(body, func(t *testing.T) {
			path, f, d := staged(t, body)
			plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "new prose\n")
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if err := edit.Commit(plan); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "new prose") {
				t.Errorf("the content was not written:\n%q", got)
			}
			// The document still parses, and still has its sections.
			after, err := doc.Build(path, scan.Scan(string(got)))
			if err != nil {
				t.Fatalf("the result does not parse: %v\n%q", err, got)
			}
			if after.Len() != d.Len() {
				t.Errorf("section count changed from %d to %d:\n%q", d.Len(), after.Len(), got)
			}
		})
	}
}

func TestLineEndingsFollowTheFile(t *testing.T) {
	_, f, d := staged(t, "# §1 A\r\n\r\nbody\r\n")
	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "one\ntwo\n")
	if err != nil {
		t.Fatal(err)
	}
	got := string(plan.Result())
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("a bare newline survived into a CRLF file: %q", got)
	}
	if !strings.Contains(got, "one\r\ntwo\r\n") {
		t.Errorf("content did not adopt the file's endings: %q", got)
	}
}

// TestMixedEndingsAreLeftAlone: a file that already mixes styles has none to
// impose, and rewriting its terminators would change lines nobody addressed.
func TestMixedEndingsAreLeftAlone(t *testing.T) {
	_, f, d := staged(t, "# §1 A\n\nlf line\r\ncrlf line\n")
	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "one\ntwo\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan.Result()), "one\ntwo\n") {
		t.Errorf("content was rewritten in a file with no dominant style: %q", plan.Result())
	}
}

func TestContentGainsATerminator(t *testing.T) {
	_, f, d := staged(t, "# §1 A\n\nbody\n\n## §1.1 B\n\nmore\n")
	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "no newline here")
	if err != nil {
		t.Fatal(err)
	}
	// Without a terminator the next heading would be swallowed onto this line.
	after, err := doc.Build("guide.md", scan.Scan(string(plan.Result())))
	if err != nil {
		t.Fatalf("the result does not parse: %v\n%q", err, plan.Result())
	}
	if after.Len() != 2 {
		t.Errorf("a missing terminator ate a section:\n%q", plan.Result())
	}
}

func TestOversizedContentIsRefused(t *testing.T) {
	_, f, d := staged(t, "# §1 A\n\ntext\n")
	_, err := edit.Prepare(f, d, section(t, d, "1"), false, strings.Repeat("x", edit.MaxContent+1))
	if err == nil || fault.Code(err) != fault.CodeUsage {
		t.Errorf("oversized content was accepted or misclassified: %v", err)
	}
}

func TestSummary(t *testing.T) {
	_, f, d := staged(t, fixture.Guide)
	plan, err := edit.Prepare(f, d, section(t, d, "1.1"), false, "one\ntwo\n")
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Summary()
	if !strings.Contains(got, "§1.1") || !strings.Contains(got, "2 lines") {
		t.Errorf("summary = %q", got)
	}
}

func TestPlanResultIsACopy(t *testing.T) {
	_, f, d := staged(t, "# §1 A\n\ntext\n")
	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "new\n")
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Result()
	got[0] = 'X'
	if plan.Result()[0] == 'X' {
		t.Error("Result returned the live slice")
	}
}

// FuzzWriteReadRoundTrip is Anno's FuzzPipeline property over documents: for
// every section of a generated document, writing back exactly what was read
// must leave the file byte-identical, in both spans.
//
// It found nothing here that the table above had not already, but it is the
// mechanism that found four real defects in Anno — including two line-ending
// corruptions — so it is worth having pointed at the same class of code.
func FuzzWriteReadRoundTrip(f *testing.F) {
	for _, s := range []string{
		fixture.Guide, fixture.Grammar,
		"# §1 A\n\nbody\n", "# §1 A\r\n\r\nbody\r\n", "# §1 A\n\nbody",
		"# §1 A\n## §1.1 B\n", "# §1 A\n\n```\n## §1.1 fake\n```\n",
		"# §1 A\n\n\ttabbed\n", "# §1 A\n\n## Unmarked\n\ntext\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		file, err := source.Parse("f.md", []byte(body))
		if err != nil {
			return // not a document source can hold; nothing to write into
		}
		r := scan.Scan(string(file.Bytes()))
		d, err := doc.Build("f.md", r)
		if err != nil {
			return
		}

		for _, s := range d.Sections() {
			for _, tree := range []bool{false, true} {
				span := s.Own()
				if tree {
					span = s.Tree()
				}
				content := ""
				if !span.Empty() {
					raw, err := file.Slice(span.Start(), span.End())
					if err != nil {
						t.Fatalf("§%s: Slice: %v", s.Number(), err)
					}
					content = string(raw)
				}

				plan, err := edit.Prepare(file, d, s, tree, content)
				if err != nil {
					// Refusing is allowed; corrupting is not. The only legal
					// refusals here are content rules, never an internal fault.
					if errors.Is(err, fault.ErrInternal) {
						t.Fatalf("§%s (tree=%v): internal fault: %v", s.Number(), tree, err)
					}
					continue
				}
				if got := string(plan.Result()); got != body {
					t.Errorf("§%s (tree=%v) did not round-trip:\n got %q\nwant %q",
						s.Number(), tree, got, body)
				}
			}
		}
	})
}
