package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/anno/internal/cli"
	"orc/anno/internal/fixture"
	"orc/anno/internal/guard"
)

// result is the full observable outcome of one command.
type result struct {
	code   int
	stdout string
	stderr string
}

// allow is the scope check these tests run under. It is set explicitly rather
// than left nil, because nil means ask the real `muff` — and a suite whose
// results depend on what is installed on the machine running it is not a suite.
func allow(string) error { return nil }

// run executes a command line against the given standard input.
func run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errOut,
		Scope:  allow,
	}, args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// workspace writes files into a fresh directory and returns its path.
func workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (r result) mustSucceed(t *testing.T) result {
	t.Helper()
	if r.code != cli.CodeOK {
		t.Fatalf("exit %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	return r
}

func (r result) mustFail(t *testing.T, code int) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
	if r.stderr == "" {
		t.Errorf("a failure must explain itself on stderr")
	}
	if !strings.HasPrefix(r.stderr, "anno: ") {
		t.Errorf("stderr should be prefixed with the tool name: %q", r.stderr)
	}
	return r
}

const duplicated = `// @:> section code
// @:> symbol Operate
a
// @:> part declarations
b
// @:< declarations
c
// @:> symbol Reduce
d
// @:> part declarations
e
`

// TestDocumentedSession walks every invocation shown in the documentation.
func TestDocumentedSession(t *testing.T) {
	dir := workspace(t, map[string]string{"example.go": fixture.ExampleGo})
	path := filepath.Join(dir, "example.go")

	t.Run("index", func(t *testing.T) {
		got := run(t, "", "index", path).mustSucceed(t).stdout
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) != 11 {
			t.Fatalf("expected 11 lines, got %d:\n%s", len(lines), got)
		}
		if !strings.HasPrefix(lines[1], "[example.go]") {
			t.Errorf("second line should be the file row: %q", lines[1])
		}
		if !strings.Contains(got, "|  |  |  part declarations") {
			t.Errorf("output should include the nested part:\n%s", got)
		}
	})

	t.Run("read section", func(t *testing.T) {
		got := run(t, "", "read", path+"@code").mustSucceed(t).stdout
		if got != fixture.ExampleReadSection {
			t.Errorf("read @code:\n%q\nwant:\n%q", got, fixture.ExampleReadSection)
		}
	})

	t.Run("read symbol", func(t *testing.T) {
		got := run(t, "", "read", path+":Operate").mustSucceed(t).stdout
		if got != fixture.ExampleReadSymbol {
			t.Errorf("read :Operate:\n%q\nwant:\n%q", got, fixture.ExampleReadSymbol)
		}
	})

	t.Run("read part", func(t *testing.T) {
		got := run(t, "", "read", path+"^declarations").mustSucceed(t).stdout
		if got != fixture.ExampleReadPart {
			t.Errorf("read ^declarations:\n%q\nwant:\n%q", got, fixture.ExampleReadPart)
		}
	})

	t.Run("fully qualified chain", func(t *testing.T) {
		got := run(t, "", "read", path+"@code:Operate^declarations").mustSucceed(t).stdout
		if got != fixture.ExampleReadPart {
			t.Errorf("a fully qualified chain should read the same content:\n%q", got)
		}
	})
}

func TestReadWholeFile(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "one\ntwo\n"})
	got := run(t, "", "read", filepath.Join(dir, "a.go")).mustSucceed(t).stdout
	if got != "one\ntwo\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestReadSuppliesATrailingNewline(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nno newline"})
	got := run(t, "", "read", filepath.Join(dir, "a.go")+"@s").mustSucceed(t).stdout
	if got != "no newline\n" {
		t.Errorf("stdout = %q, want a supplied newline", got)
	}
}

func TestReadOfAnEmptyAnnotationEmitsNothing(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "x\n// @:> section s\n"})
	got := run(t, "", "read", filepath.Join(dir, "a.go")+"@s").mustSucceed(t)
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
}

func TestAmbiguityIsReportedWithPasteableCandidates(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": duplicated})
	path := filepath.Join(dir, "a.go")

	got := run(t, "", "read", path+"^declarations").mustFail(t, cli.CodeAmbiguous)
	if !strings.Contains(got.stderr, "2 matches") {
		t.Errorf("stderr should count the matches:\n%s", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("an ambiguous read must emit no content, got %q", got.stdout)
	}

	// Every listed candidate must itself resolve.
	for _, line := range strings.Split(got.stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, path) {
			continue
		}
		run(t, "", "read", line).mustSucceed(t)
	}
}

func TestNotFoundSuggestsTheRightResolver(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": duplicated})
	path := filepath.Join(dir, "a.go")
	got := run(t, "", "read", path+"@declarations").mustFail(t, cli.CodeNotFound)
	if !strings.Contains(got.stderr, "did you mean") {
		t.Errorf("stderr should suggest alternatives:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "^declarations") {
		t.Errorf("stderr should suggest the part resolver:\n%s", got.stderr)
	}
}

func TestOverview(t *testing.T) {
	dir := workspace(t, map[string]string{
		"a.go":   "// @:> section alpha\nx\n",
		"b.go":   "// @:> section beta\ny\n",
		"sub/c":  "// @:> section gamma\nz\n",
		"bin":    "\x00\x01",
		"broken": "// @:> nonsense x\n",
	})

	got := run(t, "", "overview", dir).mustSucceed(t)
	for _, want := range []string{"[a.go]", "[b.go]", "alpha", "beta"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout should contain %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout, "gamma") {
		t.Errorf("overview must not recurse into subdirectories:\n%s", got.stdout)
	}
	for _, want := range []string{"skipping", "bin", "broken"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr should report skipped files, missing %q:\n%s", want, got.stderr)
		}
	}
}

func TestOverviewOfADirectoryWithNothingReadable(t *testing.T) {
	dir := workspace(t, map[string]string{"bin": "\x00"})
	run(t, "", "overview", dir).mustFail(t, cli.CodeNotFound)
}

func TestFind(t *testing.T) {
	dir := workspace(t, map[string]string{
		"a.go": "// @:> section s\n// @:> part target\nfrom a\n",
		"b.go": "// @:> section s\n// @:> part target\nfrom b\n",
		"c.go": "// @:> section s\nnothing here\n",
	})

	got := run(t, "", "find", dir+"^target").mustSucceed(t)
	for _, want := range []string{"a.go@s^target", "b.go@s^target", "from a", "from b", "part target"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout should contain %q:\n%s", want, got.stdout)
		}
	}
}

func TestFindReportsNothingFound(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nx\n"})
	got := run(t, "", "find", dir+"^missing").mustFail(t, cli.CodeNotFound)
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
}

func TestFindSuggestsAcrossFiles(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\n// @:> part p\nx\n"})
	got := run(t, "", "find", dir+"@p").mustFail(t, cli.CodeNotFound)
	if !strings.Contains(got.stderr, "^p") {
		t.Errorf("stderr should suggest the right resolver:\n%s", got.stderr)
	}
}

func TestWriteReplacesContent(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": fixture.ExampleGo})
	path := filepath.Join(dir, "a.go")

	got := run(t, "", "write", path+"^declarations", "var z = 1\n").mustSucceed(t)
	if !strings.Contains(got.stdout, "replaced 4 lines with 1 line") {
		t.Errorf("stdout should summarise the edit: %q", got.stdout)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "var z = 1") {
		t.Errorf("file was not updated:\n%s", after)
	}
	if strings.Contains(string(after), "l = p.L") {
		t.Errorf("old content survived:\n%s", after)
	}
	// The surrounding structure is intact.
	run(t, "", "index", path).mustSucceed(t)
}

func TestWriteFromStandardInput(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	path := filepath.Join(dir, "a.go")

	run(t, "line one\nline two\n", "write", path+"@s", "-").mustSucceed(t)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "// @:> section s\nline one\nline two\n"; string(after) != want {
		t.Errorf("file = %q, want %q", after, want)
	}
}

// TestWriteWhatWasReadIsANoOp is the end-to-end round trip: reading an
// annotation and writing it straight back must not change a single byte.
func TestWriteWhatWasReadIsANoOp(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": fixture.ExampleGo})
	path := filepath.Join(dir, "a.go")

	for _, chain := range []string{"@data", "@types", "@code", ":Pair", ":Operate", "^declarations"} {
		content := run(t, "", "read", path+chain).mustSucceed(t).stdout
		run(t, content, "write", path+chain, "-").mustSucceed(t)

		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != fixture.ExampleGo {
			t.Fatalf("writing back %s changed the file:\n%s", chain, after)
		}
	}
}

func TestWriteRefusesAmbiguousTargets(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": duplicated})
	path := filepath.Join(dir, "a.go")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run(t, "", "write", path+"^declarations", "clobbered\n").mustFail(t, cli.CodeAmbiguous)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("an ambiguous write must not touch the file")
	}
}

func TestWriteRefusesWholeFiles(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "x\n"})
	got := run(t, "", "write", filepath.Join(dir, "a.go"), "y\n").mustFail(t, cli.CodeUsage)
	if !strings.Contains(got.stderr, "whole files") {
		t.Errorf("stderr should explain the refusal:\n%s", got.stderr)
	}
}

func TestWriteRefusesStructuralDamage(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\ntail\n"})
	path := filepath.Join(dir, "a.go")

	run(t, "", "write", path+"@s", "// @:> section other\n").mustFail(t, cli.CodeParse)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "// @:> section s\nold\ntail\n" {
		t.Errorf("a refused write must not touch the file: %q", after)
	}
}

func TestFindRefusesAChainlessTarget(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "x\n"})
	got := run(t, "", "find", dir).mustFail(t, cli.CodeUsage)
	if !strings.Contains(got.stderr, "chain") {
		t.Errorf("stderr should ask for a chain:\n%s", got.stderr)
	}
}

func TestPathsContainingResolverCharacters(t *testing.T) {
	dir := workspace(t, map[string]string{"od:d.go": "// @:> section s\nbody\n"})
	path := filepath.Join(dir, "od:d.go")

	// The path itself contains a colon, and so does the chain.
	got := run(t, "", "read", path+"@s").mustSucceed(t).stdout
	if got != "body\n" {
		t.Errorf("stdout = %q, want %q", got, "body\n")
	}
	// Reading the file whole still works despite the colon.
	if got := run(t, "", "read", path).mustSucceed(t).stdout; !strings.Contains(got, "body") {
		t.Errorf("stdout = %q", got)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "x\n"})
	path := filepath.Join(dir, "a.go")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"frobnicate"}},
		{"index with no argument", []string{"index"}},
		{"index with two arguments", []string{"index", path, path}},
		{"overview with no argument", []string{"overview"}},
		{"read with no argument", []string{"read"}},
		{"find with no argument", []string{"find"}},
		{"write with one argument", []string{"write", path}},
		{"write with three arguments", []string{"write", path, "a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The refusal, and not the whole screen behind it. What each of these
			// screens now shows is pinned in screens_test.go.
			run(t, "", tc.args...).mustFail(t, cli.CodeUsage)
		})
	}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		got := run(t, "", arg).mustSucceed(t)
		if !strings.Contains(got.stdout, "anno index") {
			t.Errorf("%s should print the usage text on stdout:\n%s", arg, got.stdout)
		}
	}
}

func TestMissingPaths(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.go")

	run(t, "", "index", missing).mustFail(t, cli.CodeIO)
	run(t, "", "read", missing+"@s").mustFail(t, cli.CodeIO)
	run(t, "", "overview", filepath.Join(dir, "nodir")).mustFail(t, cli.CodeIO)
	run(t, "", "find", filepath.Join(dir, "nodir")+"^p").mustFail(t, cli.CodeIO)
	run(t, "", "write", missing+"@s", "x").mustFail(t, cli.CodeIO)
}

func TestWrongPathKind(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "x\n"})

	// A directory where a file is wanted, and a file where a directory is.
	got := run(t, "", "index", dir).mustFail(t, cli.CodeIO)
	if !strings.Contains(got.stderr, "directory") {
		t.Errorf("stderr should say it is a directory:\n%s", got.stderr)
	}
	got = run(t, "", "overview", filepath.Join(dir, "a.go")).mustFail(t, cli.CodeIO)
	if !strings.Contains(got.stderr, "directory") {
		t.Errorf("stderr should say it is not a directory:\n%s", got.stderr)
	}
	got = run(t, "", "read", dir+"@s").mustFail(t, cli.CodeIO)
	if !strings.Contains(got.stderr, "is a directory, but this command needs a file") {
		t.Errorf("stderr should explain the mismatch:\n%s", got.stderr)
	}
}

func TestParseErrorsAreReportedTogether(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> nonsense x\n// @:< ghost\n"})
	got := run(t, "", "index", filepath.Join(dir, "a.go")).mustFail(t, cli.CodeParse)
	if !strings.Contains(got.stderr, "nonsense") || !strings.Contains(got.stderr, "ghost") {
		t.Errorf("stderr should report both faults:\n%s", got.stderr)
	}
}

func TestBinaryFilesAreRejectedNotGuessedAt(t *testing.T) {
	dir := workspace(t, map[string]string{"a.bin": "\x00\x01\x02"})
	got := run(t, "", "index", filepath.Join(dir, "a.bin")).mustFail(t, cli.CodeParse)
	if !strings.Contains(got.stderr, "binary") {
		t.Errorf("stderr should say the file is binary:\n%s", got.stderr)
	}
}

func TestWriteConflictLeavesTheFileAlone(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	path := filepath.Join(dir, "a.go")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	run(t, "", "write", path+"@s", "new\n").mustFail(t, cli.CodeIO)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "// @:> section s\nold\n" {
		t.Errorf("the file was disturbed: %q", after)
	}
}

func TestMainSurvivesMissingStreams(t *testing.T) {
	if got := cli.Main(cli.App{}, []string{"help"}); got != cli.CodeInternal {
		t.Errorf("exit %d, want %d when no streams are set", got, cli.CodeInternal)
	}
}

func TestWriteWithoutStandardInput(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{Stdout: &out, Stderr: &errOut},
		[]string{"write", filepath.Join(dir, "a.go") + "@s", "-"})
	if code != cli.CodeUsage {
		t.Errorf("exit %d, want %d", code, cli.CodeUsage)
	}
	if !strings.Contains(errOut.String(), "standard input") {
		t.Errorf("stderr should explain the problem:\n%s", errOut.String())
	}
}

func TestCodeClassifiesEveryError(t *testing.T) {
	if got := cli.Code(nil); got != cli.CodeOK {
		t.Errorf("Code(nil) = %d, want 0", got)
	}
	if got := cli.Code(errUnclassified{}); got != cli.CodeInternal {
		t.Errorf("an unrecognised error should exit %d, got %d", cli.CodeInternal, got)
	}
}

type errUnclassified struct{}

func (errUnclassified) Error() string { return "who knows" }

// TestWriteIsRefusedOutOfScope is the Anno half of Macmuffin's "enforces editing
// even via Anno". The answer is stubbed here; internal/guard tests the exec.
func TestWriteRefusedOutOfScope(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	path := filepath.Join(dir, "a.go")

	var asked []string
	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Scope: func(p string) error {
			asked = append(asked, p)
			return guard.Refused{Path: p, Detail: p + " is outside the scope of fix-the-parser"}
		},
	}, []string{"write", path + "@s", "new\n"})

	// 9 is Macmuffin's scope code, and Anno relays it unchanged so a hook
	// branching on the status sees one answer from either tool.
	if code != guard.CodeOutOfScope {
		t.Errorf("exit %d, want %d", code, guard.CodeOutOfScope)
	}
	if !strings.Contains(errOut.String(), "outside the scope of fix-the-parser") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
	// The check is asked about the file, and asked before anything is read.
	if len(asked) != 1 || asked[0] != path {
		t.Errorf("asked = %v, want just %s", asked, path)
	}
	// And nothing was written. A refusal that still edits the file is not a
	// refusal.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "// @:> section s\nold\n" {
		t.Errorf("the file changed despite the refusal:\n%s", after)
	}
}

// A permitted write is asked about too, and then proceeds.
func TestWriteAsksAndProceeds(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	path := filepath.Join(dir, "a.go")

	asked := 0
	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Scope:  func(string) error { asked++; return nil },
	}, []string{"write", path + "@s", "new\n"})

	if code != cli.CodeOK {
		t.Fatalf("exit %d\n%s", code, errOut.String())
	}
	if asked != 1 {
		t.Errorf("the scope was checked %d times, want once", asked)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "new") {
		t.Errorf("the write did not land:\n%s", after)
	}
}

// Commands that do not write are never checked: a scope constrains editing, and
// a read that asked Macmuffin's permission would be both slower and wrong.
func TestReadsAreNotScopeChecked(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nx\n"})
	path := filepath.Join(dir, "a.go")

	asked := 0
	deny := func(p string) error { asked++; return guard.Refused{Path: p} }

	for _, args := range [][]string{
		{"index", path},
		{"read", path + "@s"},
		{"overview", dir},
		{"find", dir + "@s"},
	} {
		var out, errOut bytes.Buffer
		code := cli.Main(cli.App{Stdout: &out, Stderr: &errOut, Scope: deny}, args)
		if code != cli.CodeOK {
			t.Errorf("%v exited %d\n%s", args, code, errOut.String())
		}
	}
	if asked != 0 {
		t.Errorf("the scope was checked %d times for commands that write nothing", asked)
	}
}
