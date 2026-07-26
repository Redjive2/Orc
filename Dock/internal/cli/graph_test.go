package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/anno"
	"orc/dock/internal/cli"
	"orc/dock/internal/style"
)

// linked writes a small corpus whose links exercise every state: resolved,
// dangling, unchecked, cross-file, same-file, and a cycle.
func linked(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"guide.md": "# §1 Guide\n\n" +
			"See [the grammar](./grammar.md§2.1) and [Install](§1.1).\n\n" +
			"## §1.1 Install\n\n" +
			"Run it. Compare [Operate](../code/example.go@code:Operate).\n\n" +
			"## §1.2 Sections\n\n" +
			"A section is a heading. Something [went missing](§9.9).\n",
		"grammar.md": "# §1 Preface\n\nShort.\n\n# §2 Grammar\n\n" +
			"## §2.1 Targets\n\nBack to [Install](./guide.md§1.1).\n",
		"trouble.md": "# §1 Symptoms\n\nCheck [Install](./guide.md§1.1) first.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestLinksDiagram is the golden for the whole graph rendering layer.
func TestLinksDiagram(t *testing.T) {
	dir := linked(t)
	out, errs, code := run(t, "links", filepath.Join(dir, "guide.md")+"§1.1")
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}

	const want = `guide.md§1.1  Install
  │
  ├─→ ? ../code/example.go@code:Operate  Operate  (anno resolves this)
  │
  ├─←   grammar.md§2.1                   Install
  ├─←   guide.md§1                       Install
  └─←   trouble.md§1                     Install
`
	got := strings.ReplaceAll(out, dir+"/", "")
	if got != want {
		t.Errorf("diagram does not match:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestBacklinksNameTheirSource: a backlink that printed the section's own name
// back at it would answer nothing. The question is "who cites this".
func TestBacklinksNameTheirSource(t *testing.T) {
	dir := linked(t)
	out, _, _ := run(t, "links", filepath.Join(dir, "guide.md")+"§1.1")
	for _, want := range []string{"grammar.md§2.1", "guide.md§1", "trouble.md§1"} {
		if !strings.Contains(strings.ReplaceAll(out, dir+"/", ""), want) {
			t.Errorf("the diagram does not name %s as a citer:\n%s", want, out)
		}
	}
}

func TestLinksOfASectionWithNone(t *testing.T) {
	dir := linked(t)
	out, _, code := run(t, "links", filepath.Join(dir, "grammar.md")+"§1")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "no links") {
		t.Errorf("a section with no links should say so:\n%s", out)
	}
}

var escape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// TestTheDiagramStripsToItsPlainSelf: colour is a layer, never information.
func TestTheDiagramStripsToItsPlainSelf(t *testing.T) {
	dir := linked(t)
	plain, _, _ := run(t, "links", filepath.Join(dir, "guide.md")+"§1.1")
	coloured, _, _ := runColoured(t, "links", filepath.Join(dir, "guide.md")+"§1.1")
	if coloured == plain {
		t.Fatal("the coloured palette emitted nothing")
	}
	if got := escape.ReplaceAllString(coloured, ""); got != plain {
		t.Errorf("colour changed the diagram:\n--- stripped ---\n%s\n--- plain ---\n%s", got, plain)
	}
}

func TestCheck(t *testing.T) {
	dir := linked(t)
	out, errs, code := run(t, "check", dir)
	if code != fault.CodeNotFound {
		t.Fatalf("code = %d, want %d (something is broken)", code, fault.CodeNotFound)
	}
	report := strings.ReplaceAll(out, dir+"/", "")

	// The one broken link, with its position.
	if !strings.Contains(report, "guide.md:11:35") {
		t.Errorf("the dangling link is not reported with its position:\n%s", report)
	}
	if !strings.Contains(report, "§9.9") {
		t.Errorf("the report does not name what was missing:\n%s", report)
	}
	// The summary states what was *not* verified alongside what was.
	if !strings.Contains(report, "3 documents checked") || !strings.Contains(report, "1 dangling") {
		t.Errorf("the summary is incomplete:\n%s", report)
	}
	if !strings.Contains(report, "left to anno") {
		t.Errorf("the report hides what it could not check:\n%s", report)
	}
	// A report that already said everything is not followed by a diagnostic.
	if errs != "" {
		t.Errorf("check wrote to stderr after reporting: %q", errs)
	}
}

func TestCheckOfACleanCorpus(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.md": "# §1 A\n\n[b](./b.md§1)\n",
		"b.md": "# §1 B\n\n[a](./a.md§1)\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, errs, code := run(t, "check", dir)
	if code != fault.CodeOK {
		t.Fatalf("a clean corpus failed: code = %d\n%s%s", code, out, errs)
	}
	if !strings.Contains(out, "nothing broken") {
		t.Errorf("a clean report does not say so:\n%s", out)
	}
	// The cycle between a and b did not hang or double-count.
	if !strings.Contains(out, "2 links") {
		t.Errorf("wrong link count:\n%s", out)
	}
}

// TestCheckReportsUnreadableDocuments: being unreadable is exactly the kind of
// thing check exists to find, where overview only notes it in passing.
func TestCheckReportsUnreadableDocuments(t *testing.T) {
	dir := linked(t)
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("## §1.1 Orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := run(t, "check", dir)
	if code != fault.CodeNotFound {
		t.Errorf("code = %d", code)
	}
	if !strings.Contains(out, "broken.md") || !strings.Contains(out, "no open parent") {
		t.Errorf("the numbering fault is not reported:\n%s", out)
	}
	if !strings.Contains(out, "unreadable document") {
		t.Errorf("the summary does not count it:\n%s", out)
	}
}

func TestCheckDefaultsToTheCurrentDirectory(t *testing.T) {
	dir := linked(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	out, _, _ := run(t, "check")
	if !strings.Contains(out, "3 documents checked") {
		t.Errorf("check with no argument did not walk the current directory:\n%s", out)
	}
}

func TestLinksExitCodes(t *testing.T) {
	dir := linked(t)
	guide := filepath.Join(dir, "guide.md")
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"ok", []string{"links", guide + "§1.1"}, fault.CodeOK},
		{"no such section", []string{"links", guide + "§9.9"}, fault.CodeNotFound},
		{"no such file", []string{"links", filepath.Join(dir, "nope.md") + "§1"}, fault.CodeNotFound},
		{"anno target", []string{"links", "x.go@code:Operate"}, fault.CodeUsage},
		{"not a target", []string{"links", "https://example.com"}, fault.CodeUsage},
		{"no argument", []string{"links"}, fault.CodeUsage},
		{"too many arguments", []string{"check", "a", "b"}, fault.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, code := run(t, tc.args...); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestEveryDiagramLineIsPasteable: a line that looks like a target must be one,
// so following a backlink is a copy-paste.
func TestEveryDiagramLineIsPasteable(t *testing.T) {
	dir := linked(t)
	out, _, _ := run(t, "links", filepath.Join(dir, "guide.md")+"§1.1")
	checked := 0
	for _, line := range strings.Split(out, "\n") {
		i := strings.IndexAny(line, "→←")
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[i:])
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		if ref == "?" || ref == "✗" {
			continue // an unresolved endpoint is shown as written, not as a node
		}
		if !strings.Contains(ref, "§") {
			continue
		}
		if _, _, code := run(t, "read", filepath.Join(dir, ref)); code != fault.CodeOK {
			t.Errorf("diagram line %q does not resolve as a target", ref)
		}
		checked++
	}
	if checked == 0 {
		t.Errorf("no targets found in:\n%s", out)
	}
}

// fakeAnno answers as the binary would, so check's integration is testable
// without a process.
type fakeAnno struct {
	code   int
	stderr string
	calls  int
}

func (f *fakeAnno) Run(_ context.Context, args ...string) (string, string, int, error) {
	f.calls++
	return "", f.stderr, f.code, nil
}

// TestCheckAsksAnnoAboutCodeTargets is the milestone's point: the "left to
// anno" links become checked ones.
func TestCheckAsksAnnoAboutCodeTargets(t *testing.T) {
	dir := linked(t)

	t.Run("resolved", func(t *testing.T) {
		fake := &fakeAnno{code: fault.CodeOK}
		var out, errs bytes.Buffer
		app := cli.New(&out, &errs, style.Plain())
		app.Anno = anno.NewWith(fake)
		code := app.Main([]string{"check", dir})

		if fake.calls != 1 {
			t.Errorf("asked anno %d times, want 1", fake.calls)
		}
		if strings.Contains(out.String(), "left to anno") {
			t.Errorf("a checked target was still counted as unchecked:\n%s", out.String())
		}
		// The corpus still has its one genuinely dangling doc link.
		if code != fault.CodeNotFound {
			t.Errorf("code = %d, want %d", code, fault.CodeNotFound)
		}
		_ = errs
	})

	t.Run("missing becomes dangling", func(t *testing.T) {
		fake := &fakeAnno{code: fault.CodeNotFound, stderr: "anno: no annotation matches \"example.go@code:Operate\"\n"}
		var out, errs bytes.Buffer
		app := cli.New(&out, &errs, style.Plain())
		app.Anno = anno.NewWith(fake)
		app.Main([]string{"check", dir})

		report := strings.ReplaceAll(out.String(), dir+"/", "")
		if !strings.Contains(report, "2 dangling") {
			t.Errorf("anno's answer did not reach the report:\n%s", report)
		}
		if !strings.Contains(report, "no annotation matches") {
			t.Errorf("anno's reason was not carried through:\n%s", report)
		}
		_ = errs
	})
}

// TestCheckWithoutAnnoLeavesCodeTargetsUnchecked. A missing tool must never
// turn into a broken document.
func TestCheckWithoutAnnoLeavesCodeTargetsUnchecked(t *testing.T) {
	dir := linked(t)
	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Plain())
	app.Anno = anno.Tool{} // unavailable
	app.Main([]string{"check", dir})

	report := out.String()
	if !strings.Contains(report, "left to anno") {
		t.Errorf("the report does not say what it could not check:\n%s", report)
	}
	if strings.Contains(report, "2 dangling") {
		t.Errorf("an unaskable target was called broken:\n%s", report)
	}
}

// recordingAnno remembers what it was asked about.
type recordingAnno struct{ asked []string }

func (r *recordingAnno) Run(_ context.Context, args ...string) (string, string, int, error) {
	if len(args) > 1 {
		r.asked = append(r.asked, args[1])
	}
	return "", "", fault.CodeOK, nil
}

// TestCodeTargetsResolveFromTheLinkingDocument is a regression test.
//
// A destination is written relative to the document that declares it. Handing
// anno the raw text resolved it against dock's working directory instead, so
// every code target failed for anno's own reasons and stayed unchecked — the
// symptom being a check that looked like it had run and had verified nothing.
func TestCodeTargetsResolveFromTheLinkingDocument(t *testing.T) {
	base := t.TempDir()
	docs := filepath.Join(base, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# §1 Guide\n\nSee [Operate](../code/example.go@code:Operate).\n"
	if err := os.WriteFile(filepath.Join(docs, "guide.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recordingAnno{}
	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Plain())
	app.Anno = anno.NewWith(rec)
	app.Main([]string{"check", docs})

	if len(rec.asked) != 1 {
		t.Fatalf("asked anno %d times, want 1: %v", len(rec.asked), rec.asked)
	}
	want := filepath.Join(base, "code", "example.go") + "@code:Operate"
	if rec.asked[0] != want {
		t.Errorf("anno was asked about\n got %q\nwant %q", rec.asked[0], want)
	}
}

// TestALinkOutOfTheTreeSaysSo. "no document at ../../../../notes.md" and "that
// is outside the tree at all" are the same words to a graph and different
// problems to a person, so check distinguishes them.
func TestALinkOutOfTheTreeSaysSo(t *testing.T) {
	base := t.TempDir()
	docs := filepath.Join(base, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .dock marker makes docs/ the root explicitly, so the boundary is not a
	// guess about where a repository begins.
	if err := os.WriteFile(filepath.Join(docs, ".dock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// One link leaves the tree; one is merely missing inside it.
	body := "# §1 Guide\n\nOut: [away](../outside.md§1).\nIn: [gone](./missing.md§1).\n"
	if err := os.WriteFile(filepath.Join(docs, "guide.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := run(t, "check", docs)
	if code != fault.CodeNotFound {
		t.Fatalf("code = %d, want %d", code, fault.CodeNotFound)
	}
	if !strings.Contains(out, "escapes the tree") {
		t.Errorf("the out-of-tree link was not distinguished:\n%s", out)
	}
	if !strings.Contains(out, "no document at") {
		t.Errorf("the merely-missing link lost its own reason:\n%s", out)
	}
	// A link out of the tree is a broken link, not a containment breach: dock
	// keeps its exit code and leaves 11 to orcprobe, where a path leaving the
	// probe really is the thing to alarm on.
	if code == 11 {
		t.Error("check exited with the escape code")
	}
}

// TestALinkInsideTheTreeIsNotAnEscape guards the obvious false positive.
func TestALinkInsideTheTreeIsNotAnEscape(t *testing.T) {
	dir := linked(t)
	out, _, _ := run(t, "check", dir)
	if strings.Contains(out, "escapes the tree") {
		t.Errorf("an ordinary broken link was called an escape:\n%s", out)
	}
}
