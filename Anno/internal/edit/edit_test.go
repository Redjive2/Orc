package edit_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/anno/internal/edit"
	"orc/anno/internal/fixture"
	"orc/anno/internal/target"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
)

// prep resolves a target against text and prepares a replacement.
func prep(t *testing.T, path, text, addr, content string) (edit.Plan, error) {
	t.Helper()
	f, err := source.Parse(path, []byte(text))
	if err != nil {
		t.Fatalf("source.Parse: %v", err)
	}
	tr, err := tree.Build(f)
	if err != nil {
		t.Fatalf("tree.Build: %v", err)
	}
	tgt, err := target.ParseOne(addr)
	if err != nil {
		t.Fatalf("target.ParseOne: %v", err)
	}
	matches, err := target.Resolve(tr, tgt.Steps())
	if err != nil {
		t.Fatalf("target.Resolve: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("target %q matched %d nodes, want 1", addr, len(matches))
	}
	return edit.Prepare(f, matches[0], tgt.Steps(), content)
}

func mustPrep(t *testing.T, path, text, addr, content string) edit.Plan {
	t.Helper()
	p, err := prep(t, path, text, addr, content)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return p
}

func TestReplaceAnnotationContent(t *testing.T) {
	got := mustPrep(t, "x.go", fixture.ExampleGo, "x.go^declarations", "var z = 1\n").Result()
	want := strings.Replace(fixture.ExampleGo,
		"\tvar (\n\t\tl = p.L\n\t\tr = p.R\n\t)\n",
		"var z = 1\n", 1)
	if string(got) != want {
		t.Errorf("result:\n%s\nwant:\n%s", got, want)
	}
}

// TestWritingWhatWasReadChangesNothing is the round-trip property: feeding an
// annotation's own span back to it must leave the file byte-identical.
func TestWritingWhatWasReadChangesNothing(t *testing.T) {
	f, err := source.Parse("x.go", []byte(fixture.ExampleGo))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := tree.Build(f)
	if err != nil {
		t.Fatal(err)
	}

	var check func(ns []tree.Node, prefix string)
	check = func(ns []tree.Node, prefix string) {
		for _, n := range ns {
			addr := prefix + string(n.Kind().Resolver()) + n.Name()
			span, err := f.Slice(n.Span().Start(), n.Span().End())
			if err != nil {
				t.Fatalf("%s: %v", addr, err)
			}
			plan, err := prep(t, "x.go", fixture.ExampleGo, "x.go"+addr, string(span))
			if err != nil {
				t.Fatalf("%s: Prepare: %v", addr, err)
			}
			if got := string(plan.Result()); got != fixture.ExampleGo {
				t.Errorf("%s: writing back its own content changed the file:\n%s", addr, got)
			}
			check(n.Children(), addr)
		}
	}
	check(tr.Children(), "")
}

func TestReplacementUsesTheFilesLineEndings(t *testing.T) {
	src := "// @:> section s\r\nold\r\n"
	got := mustPrep(t, "x.go", src, "x.go@s", "one\ntwo\n").Result()
	if want := "// @:> section s\r\none\r\ntwo\r\n"; string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestReplacementAcceptsContentWithoutATrailingNewline(t *testing.T) {
	got := mustPrep(t, "x.go", "// @:> section s\nold\n", "x.go@s", "new").Result()
	if want := "// @:> section s\nnew\n"; string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestReplacementPreservesAMissingFinalNewline(t *testing.T) {
	got := mustPrep(t, "x.go", "// @:> section s\nold", "x.go@s", "new\n").Result()
	if want := "// @:> section s\nnew"; string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestReplacementKeepsTheRestOfTheFileIntact(t *testing.T) {
	src := "before\n// @:> section s\nold\n// @:> section t\nafter\n"
	got := mustPrep(t, "x.go", src, "x.go@s", "new\n").Result()
	want := "before\n// @:> section s\nnew\n// @:> section t\nafter\n"
	if string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestEmptyContentDeletesTheSpan(t *testing.T) {
	src := "// @:> section s\nold\nlines\n// @:> section t\nx\n"
	plan := mustPrep(t, "x.go", src, "x.go@s", "")
	if want := "// @:> section s\n// @:> section t\nx\n"; string(plan.Result()) != want {
		t.Errorf("result = %q, want %q", plan.Result(), want)
	}
	if plan.NewLines() != 0 {
		t.Errorf("NewLines = %d, want 0", plan.NewLines())
	}
}

func TestWritingIntoAnEmptyAnnotationInserts(t *testing.T) {
	// The section's span is empty because its marker is the last line.
	src := "x\n// @:> section s\n"
	plan := mustPrep(t, "x.go", src, "x.go@s", "added\n")
	if want := "x\n// @:> section s\nadded\n"; string(plan.Result()) != want {
		t.Errorf("result = %q, want %q", plan.Result(), want)
	}
	if !plan.Replaced().Empty() {
		t.Errorf("Replaced = %s, want empty", plan.Replaced())
	}
}

func TestPlanReportsWhatItDid(t *testing.T) {
	plan := mustPrep(t, "x.go", fixture.ExampleGo, "x.go^declarations", "a\nb\n")
	if got, want := plan.Path(), "x.go"; got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
	if got, want := plan.Replaced().String(), "25:28"; got != want {
		t.Errorf("Replaced = %s, want %s", got, want)
	}
	if got := plan.NewLines(); got != 2 {
		t.Errorf("NewLines = %d, want 2", got)
	}
	if got, want := plan.Qualified(), "x.go@code:Operate^declarations"; got != want {
		t.Errorf("Qualified = %q, want %q", got, want)
	}
	summary := plan.Summary()
	for _, want := range []string{"x.go", "4 lines", "2 lines", "25:28"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q should mention %q", summary, want)
		}
	}
}

func TestSummaryUsesSingularAndPointsAtEmptySpans(t *testing.T) {
	one := mustPrep(t, "x.go", "// @:> section s\nold\n", "x.go@s", "new\n")
	if !strings.Contains(one.Summary(), "1 line with 1 line") {
		t.Errorf("summary = %q, want singular wording", one.Summary())
	}
	empty := mustPrep(t, "x.go", "x\n// @:> section s\n", "x.go@s", "new\n")
	if !strings.Contains(empty.Summary(), "line 3") {
		t.Errorf("summary = %q, want the insertion line", empty.Summary())
	}
}

func TestResultIsACopy(t *testing.T) {
	plan := mustPrep(t, "x.go", "// @:> section s\nold\n", "x.go@s", "new\n")
	r := plan.Result()
	r[0] = 'Z'
	if plan.Result()[0] == 'Z' {
		t.Errorf("Result() exposed internal state")
	}
}

func TestRejectsContentThatWouldEndTheAnnotation(t *testing.T) {
	for _, tc := range []struct{ name, addr, content string }{
		{"same kind", "x.go@s", "// @:> section other\n"},
		{"shallower kind", "x.go:sym", "// @:> section other\n"},
		{"same kind by next", "x.go@s", "// @:; section other\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "// @:> section s\n// @:> symbol sym\nold\n"
			_, err := prep(t, "x.go", src, tc.addr, tc.content)
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), "would end the annotation") {
				t.Errorf("message %q should explain the refusal", err)
			}
		})
	}
}

func TestAcceptsContentThatNestsDeeper(t *testing.T) {
	src := "// @:> section s\nold\n"
	plan := mustPrep(t, "x.go", src, "x.go@s", "// @:> symbol inner\nbody\n")
	if !strings.Contains(string(plan.Result()), "symbol inner") {
		t.Errorf("result = %q", plan.Result())
	}
}

func TestRejectsContentWithAnUnbalancedClose(t *testing.T) {
	_, err := prep(t, "x.go", "// @:> section s\nold\n", "x.go@s", "// @:< ghost\n")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "never opened") {
		t.Errorf("message %q should explain the refusal", err)
	}
}

func TestAcceptsContentWithABalancedClose(t *testing.T) {
	src := "// @:> section s\nold\n"
	plan := mustPrep(t, "x.go", src, "x.go@s", "// @:> symbol i\nx\n// @:< i\n")
	if !strings.Contains(string(plan.Result()), "@:< i") {
		t.Errorf("result = %q", plan.Result())
	}
}

func TestRejectsMalformedMarkersInContent(t *testing.T) {
	_, err := prep(t, "x.go", "// @:> section s\nold\n", "x.go@s", "// @:> nonsense x\n")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "not valid") {
		t.Errorf("message %q should say the content is invalid", err)
	}
}

func TestRejectsContentWithNulBytes(t *testing.T) {
	_, err := prep(t, "x.go", "// @:> section s\nold\n", "x.go@s", "a\x00b\n")
	if !errors.Is(err, fault.ErrUsage) {
		t.Fatalf("error = %v, want a usage fault", err)
	}
}

func TestRejectsContentThatClosesAnEnclosingAnnotation(t *testing.T) {
	// The close is balanced by count against the symbol the content opens, but
	// it names the section being written, which would cut that section short.
	src := "// @:> section s\nold\ntail\n"
	_, err := prep(t, "x.go", src, "x.go@s", "// @:> symbol i\nx\n// @:< s\n")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "never opened") {
		t.Errorf("message %q should explain the refusal", err)
	}
}

func TestClosesMustNestWithinTheContent(t *testing.T) {
	// Opening two symbols and closing the outer one implicitly closes the
	// inner, so a later close of the inner name is no longer balanced.
	src := "// @:> section s\nold\n"
	_, err := prep(t, "x.go", src, "x.go@s",
		"// @:> symbol a\n// @:> symbol b\n// @:< a\n// @:< b\n")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
}

func TestCommitWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	src := "// @:> section s\nold\n"
	if err := os.WriteFile(path, []byte(src), 0o640); err != nil {
		t.Fatal(err)
	}

	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPrepFile(t, f, "@s", "new\n")
	if err := edit.Commit(plan); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "// @:> section s\nnew\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("permissions = %v, want 0640", got)
	}

	// No temporary files are left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only x.go", names)
	}
}

// mustPrepFile prepares a plan against an already-loaded file.
func mustPrepFile(t *testing.T, f source.File, chain, content string) edit.Plan {
	t.Helper()
	tr, err := tree.Build(f)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := target.ParseOne(f.Path() + chain)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := target.Resolve(tr, tgt.Steps())
	if err != nil || len(matches) != 1 {
		t.Fatalf("Resolve: %v, %d matches", err, len(matches))
	}
	plan, err := edit.Prepare(f, matches[0], tgt.Steps(), content)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return plan
}

func TestCommitRefusesAFileChangedUnderneathIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("// @:> section s\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPrepFile(t, f, "@s", "new\n")

	// Someone else edits the file between the read and the write.
	if err := os.WriteFile(path, []byte("// @:> section s\nmeanwhile\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = edit.Commit(plan)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "// @:> section s\nmeanwhile\n" {
		t.Errorf("the other write was clobbered: %q", got)
	}
}

func TestCommitReportsAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("// @:> section s\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPrepFile(t, f, "@s", "new\n")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := edit.Commit(plan); !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
}

func TestCommitReportsAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("// @:> section s\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPrepFile(t, f, "@s", "new\n")

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := edit.Commit(plan); !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "// @:> section s\nold\n" {
		t.Errorf("the original was disturbed: %q", got)
	}
}

func TestCommitRejectsAnEmptyPlan(t *testing.T) {
	if err := edit.Commit(edit.Plan{}); !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want an internal fault", err)
	}
}

func TestPrepareRejectsAnEmptyMatch(t *testing.T) {
	f, err := source.Parse("x.go", []byte("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Prepare(f, target.Match{}, nil, "x"); !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want an internal fault", err)
	}
}

func TestMixedLineEndingsAreLeftAlone(t *testing.T) {
	// The file has no house style, so the content's own terminators survive
	// rather than being rewritten to whichever style happens to dominate.
	src := "// @:> section s\nold\r\n"
	got := mustPrep(t, "x.go", src, "x.go@s", "one\r\ntwo\n").Result()
	if want := "// @:> section s\none\r\ntwo\n"; string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestUniformFileImposesItsOwnLineEndings(t *testing.T) {
	src := "// @:> section s\r\nold\r\n"
	got := mustPrep(t, "x.go", src, "x.go@s", "one\ntwo\n").Result()
	if want := "// @:> section s\r\none\r\ntwo\r\n"; string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestMixedFilePreservesAMissingFinalNewline(t *testing.T) {
	src := "a\r\n// @:> section s\nold"
	got := mustPrep(t, "x.go", src, "x.go@s", "new\r\n").Result()
	if want := "a\r\n// @:> section s\nnew"; string(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}
