package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/cli"
	"orc/dock/internal/fixture"
	"orc/dock/internal/style"
)

// corpus writes the fixture doc set to a temporary directory and returns it.
func corpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"guide.md":   fixture.Guide,
		"grammar.md": fixture.Grammar,
		"trouble.md": fixture.Trouble,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// run drives one command and returns stdout, stderr, and the exit code.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Plain())
	code := app.Main(args)
	return out.String(), errs.String(), code
}

func TestReadReturnsOwnProseByDefault(t *testing.T) {
	dir := corpus(t)
	out, errs, code := run(t, "read", filepath.Join(dir, "guide.md")+"§1.2")
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	if !strings.Contains(out, "A section is a heading carrying a number") {
		t.Errorf("own prose missing:\n%s", out)
	}
	// The subsection is the whole point of the default: it must not appear.
	if strings.Contains(out, "Numbering") {
		t.Errorf("read returned the subtree by default:\n%s", out)
	}
}

func TestReadTreeIncludesSubsections(t *testing.T) {
	dir := corpus(t)
	out, _, code := run(t, "read", filepath.Join(dir, "guide.md")+"§1.2", "--tree")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "§1.2.1 Numbering") {
		t.Errorf("--tree did not include the subsection:\n%s", out)
	}
}

// TestReadNeverPrintsTheHeading. The heading is structure, not content: read
// does not spend a line naming the thing the caller just named.
func TestReadNeverPrintsTheHeading(t *testing.T) {
	dir := corpus(t)
	for _, args := range [][]string{
		{"read", filepath.Join(dir, "guide.md") + "§1.2"},
		{"read", filepath.Join(dir, "guide.md") + "§1.2", "--tree"},
	} {
		out, _, _ := run(t, args...)
		if strings.Contains(out, "## §1.2 Sections") {
			t.Errorf("%v printed the section's own heading:\n%s", args, out)
		}
	}
}

func TestReadByName(t *testing.T) {
	dir := corpus(t)
	out, errs, code := run(t, "read", filepath.Join(dir, "guide.md")+"§'Install'")
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	if !strings.Contains(out, "go install") {
		t.Errorf("wrong section:\n%s", out)
	}
}

// TestReadIsVerbatim is what makes read and write inverses. Line endings and
// indentation come back exactly as they went in.
func TestReadIsVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.md")
	body := "# §1 A\r\n\r\n\tindented\r\n  two spaces\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := run(t, "read", path+"§1")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	const want = "\r\n\tindented\r\n  two spaces\r\n"
	if out != want {
		t.Errorf("read is not verbatim:\n got %q\nwant %q", out, want)
	}
}

func TestIndex(t *testing.T) {
	dir := corpus(t)
	out, errs, code := run(t, "index", filepath.Join(dir, "guide.md"))
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	for _, want := range []string{"§1.2.1", "Numbering", "→2"} {
		if !strings.Contains(out, want) {
			t.Errorf("index is missing %q:\n%s", want, out)
		}
	}

	// The corpus is small, so the backlinks are real rather than unknown. These
	// are the same counts fixture.GuideIndex pins, arrived at by walking the
	// tree instead of by being told — which is what makes the golden a
	// statement about Dock rather than about the test that built it.
	if strings.Contains(out, "←?") {
		t.Errorf("a small corpus left backlinks uncounted:\n%s", out)
	}
	for _, want := range []string{"→5 ←3", "→0 ←2", "→0 ←1"} {
		if !strings.Contains(out, want) {
			t.Errorf("index is missing the counts %q:\n%s", want, out)
		}
	}

	// Every row but the file row is a section of the fixture, so the body of the
	// table matches the golden — once the padding is collapsed, since the
	// temporary directory's name stretches the column the path sits in.
	golden := strings.Split(strings.TrimRight(fixture.GuideIndex, "\n"), "\n")
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) != len(golden) {
		t.Fatalf("index has %d rows, want %d", len(got), len(golden))
	}
	for i := 2; i < len(golden)-1; i++ {
		if squeeze(got[i]) != squeeze(golden[i]) {
			t.Errorf("row %d:\n got %q\nwant %q", i, squeeze(got[i]), squeeze(golden[i]))
		}
	}
}

// squeeze collapses runs of spaces, so a comparison is about a row's content
// rather than about how wide the widest cell in the table happened to be.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestBacklinksAreLeftUnknownOnALargeTree. A count nobody measured must not
// read as a count of none, and an index of one file must not pay to read a
// whole repository.
func TestBacklinksAreLeftUnknownOnALargeTree(t *testing.T) {
	dir := corpus(t)
	for i := 0; i < cli.MaxBacklinkScan; i++ {
		name := filepath.Join(dir, fmt.Sprintf("filler%03d.md", i))
		if err := os.WriteFile(name, []byte("# §1 Filler\n\ntext\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, _, code := run(t, "index", filepath.Join(dir, "guide.md"))
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "←?") {
		t.Errorf("a large tree was scanned anyway:\n%s", out)
	}
}

func TestExitCodes(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"ok", []string{"read", guide + "§1.1"}, fault.CodeOK},
		{"no command", nil, fault.CodeUsage},
		{"unknown command", []string{"frobnicate"}, fault.CodeUsage},
		{"unknown flag", []string{"read", guide + "§1", "--nope"}, fault.CodeUsage},
		{"not a target", []string{"read", "https://example.com"}, fault.CodeUsage},
		{"same-file with no document", []string{"read", "§1.2"}, fault.CodeUsage},
		{"anno target", []string{"read", "x.go@code:Operate"}, fault.CodeUsage},
		{"malformed section", []string{"read", guide + "§1..2"}, fault.CodeParse},
		{"no such section", []string{"read", guide + "§9.9"}, fault.CodeNotFound},
		{"no such file", []string{"read", filepath.Join(dir, "nope.md") + "§1"}, fault.CodeNotFound},
		{"index missing file", []string{"index", filepath.Join(dir, "nope.md")}, fault.CodeIO},
		{"help", []string{"help"}, fault.CodeOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code := run(t, tc.args...)
			if code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestNotFoundListsWhatExists: every listed line is a valid target, so the fix
// is a copy-paste.
func TestNotFoundListsWhatExists(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	_, errs, _ := run(t, "read", guide+"§9.9")
	if !strings.Contains(errs, "§1.1") || !strings.Contains(errs, "Install") {
		t.Errorf("the diagnostic does not list what the document has:\n%s", errs)
	}
	// Pull a suggested target back out and check it resolves.
	for _, line := range strings.Split(errs, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, guide+"§") {
			continue
		}
		suggestion := strings.Fields(line)[0]
		if _, _, code := run(t, "read", suggestion); code != fault.CodeOK {
			t.Errorf("suggested target %q does not resolve", suggestion)
		}
	}
}

// TestAFileNamedLikeATargetWins is the conservative reading: what exists on
// disk decides the split.
func TestAFileNamedLikeATargetWins(t *testing.T) {
	dir := t.TempDir()
	odd := filepath.Join(dir, "guide§1.md")
	if err := os.WriteFile(odd, []byte("# §1 Odd\n\nthe odd one\n"), 0o644); err != nil {
		t.Skipf("this filesystem will not hold the name: %v", err)
	}
	out, errs, code := run(t, "read", odd+"§1")
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	if !strings.Contains(out, "the odd one") {
		t.Errorf("the file named like a target did not win:\n%s", out)
	}
}

func TestDiagnosticsGoToStderrAndOutputToStdout(t *testing.T) {
	dir := corpus(t)
	out, errs, _ := run(t, "read", filepath.Join(dir, "guide.md")+"§9.9")
	if out != "" {
		t.Errorf("a failure wrote to stdout: %q", out)
	}
	if !strings.HasPrefix(errs, "dock: ") {
		t.Errorf("diagnostic is not prefixed: %q", errs)
	}
}

// failingWriter fails after n successful writes.
type failingWriter struct{ left int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.left <= 0 {
		return 0, os.ErrClosed
	}
	w.left--
	return len(p), nil
}

// TestAClosedPipeFailsTheCommand: a command that could not deliver its output
// has failed, and exiting 0 would be a lie a pipeline then acts on.
func TestAClosedPipeFailsTheCommand(t *testing.T) {
	dir := corpus(t)
	var errs bytes.Buffer
	app := cli.New(&failingWriter{}, &errs, style.Plain())
	if code := app.Main([]string{"read", filepath.Join(dir, "guide.md") + "§1.1"}); code != fault.CodeIO {
		t.Errorf("code = %d, want %d", code, fault.CodeIO)
	}
}

// TestByteBudgets pins what each command costs over the fixture. This is the
// only suite in the tree that asserts an upper bound on output, and it is what
// keeps Dock honest about its own purpose: a change that makes it chattier
// fails a test that says so in bytes.
func TestByteBudgets(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	full := len(fixture.Guide)

	// The budgets sit just above what each command actually costs, so a change
	// that makes Dock chattier fails here rather than being noticed later by a
	// context window.
	for _, tc := range []struct {
		name string
		args []string
		max  int
	}{
		{"read own prose", []string{"read", guide + "§1.2"}, 180},
		{"read subtree", []string{"read", guide + "§1.2", "--tree"}, 260},
		{"read a leaf", []string{"read", guide + "§1.1"}, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, code := run(t, tc.args...)
			if code != fault.CodeOK {
				t.Fatalf("code = %d", code)
			}
			if len(out) > tc.max {
				t.Errorf("%v produced %d bytes, past the budget of %d:\n%s", tc.args, len(out), tc.max, out)
			}
			if len(out) >= full {
				t.Errorf("%v produced %d bytes for a %d byte document — reading it whole would be cheaper",
					tc.args, len(out), full)
			}
		})
	}

	// The index is *not* cheaper than the document it describes, and pretending
	// otherwise would be the kind of claim this suite exists to catch. Its table
	// costs roughly a line per section plus a frame, so it pays only once a
	// document is bigger than its own table — which the 557-byte fixture is not.
	// What always pays is read: a leaf section here is 31 bytes against 557.
	t.Run("index costs a table, not a document", func(t *testing.T) {
		out, _, code := run(t, "index", guide)
		if code != fault.CodeOK {
			t.Fatalf("code = %d", code)
		}
		if len(out) > 720 {
			t.Errorf("the index costs %d bytes, past its budget", len(out))
		}
		leaf, _, _ := run(t, "read", guide+"§1.1")
		if len(leaf)*4 > full {
			t.Errorf("reading a leaf costs %d bytes of a %d byte document; the saving has gone",
				len(leaf), full)
		}
	})
}

func TestPanicsBecomeInternalFaults(t *testing.T) {
	var out, errs bytes.Buffer
	// An app with no Stat function trips the entry guard rather than panicking,
	// which is the same contract seen from outside: a diagnosis, not a crash.
	app := cli.App{Stdout: &out, Stderr: &errs, Out: style.Plain(), Err: style.Plain()}
	if code := app.Main([]string{"index", "x.md"}); code != fault.CodeInternal {
		t.Errorf("code = %d, want %d", code, fault.CodeInternal)
	}
	if !strings.Contains(errs.String(), "internal") {
		t.Errorf("no internal fault reported: %q", errs.String())
	}
}

// runColoured drives a command with colour on, for tests that assert the
// coloured rendering strips back to the plain one.
func runColoured(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Coloured())
	code := app.Main(args)
	return out.String(), errs.String(), code
}

// TestWriteRoundTripsThroughTheCLI: read then write leaves the file untouched,
// which is the guarantee an agent editing documentation depends on.
func TestWriteRoundTripsThroughTheCLI(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	original, err := os.ReadFile(guide)
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{guide + "§1.1", guide + "§1.2", guide + "§1"} {
		content, _, code := run(t, "read", target)
		if code != fault.CodeOK {
			t.Fatalf("read %s: code %d", target, code)
		}
		var out, errs bytes.Buffer
		app := cli.New(&out, &errs, style.Plain())
		app.Stdin = strings.NewReader(content)
		if code := app.Main([]string{"write", target, "-"}); code != fault.CodeOK {
			t.Fatalf("write %s: code %d, stderr %s", target, code, errs.String())
		}
		got, err := os.ReadFile(guide)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("writing back what read returned changed %s:\n got %q\nwant %q", target, got, original)
		}
	}
}

func TestWriteChangesOnlyTheSection(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	if _, _, code := run(t, "write", guide+"§1.1", "brand new prose\n"); code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	got, err := os.ReadFile(guide)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "brand new prose") {
		t.Errorf("the content was not written:\n%s", got)
	}
	// Every heading survives, and so does every other section's prose.
	for _, keep := range []string{
		"# §1 Guide", "## §1.1 Install", "## §1.2 Sections", "### §1.2.1 Numbering",
		"Dock reads documentation", "A section is a heading",
	} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("the write lost %q:\n%s", keep, got)
		}
	}
	// And the old prose of the written section is gone.
	if strings.Contains(string(got), "go install") {
		t.Errorf("the old prose survived:\n%s", got)
	}
}

func TestWriteExitCodes(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"ok", []string{"write", guide + "§1.1", "text\n"}, fault.CodeOK},
		{"a section in own prose", []string{"write", guide + "§1.1", "## §1.9 New\n"}, fault.CodeUsage},
		{"no such section", []string{"write", guide + "§9.9", "text\n"}, fault.CodeNotFound},
		{"anno target", []string{"write", "x.go@code:Op", "text\n"}, fault.CodeUsage},
		{"malformed link", []string{"write", guide + "§1.1", "[x](§1..2)\n"}, fault.CodeConflict},
		{"too few arguments", []string{"write", guide + "§1.1"}, fault.CodeUsage},
		{"not a target", []string{"write", "https://x.example", "t"}, fault.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, code := run(t, tc.args...); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestWriteSaysWhatItDidOnStderr: stdout is for what a command was asked to
// produce, and write was asked to change a file, not to talk about it.
func TestWriteSaysWhatItDidOnStderr(t *testing.T) {
	dir := corpus(t)
	out, errs, _ := run(t, "write", filepath.Join(dir, "guide.md")+"§1.1", "one\ntwo\n")
	if out != "" {
		t.Errorf("write wrote to stdout: %q", out)
	}
	if !strings.Contains(errs, "§1.1") || !strings.Contains(errs, "2 lines") {
		t.Errorf("the summary is unhelpful: %q", errs)
	}
}
