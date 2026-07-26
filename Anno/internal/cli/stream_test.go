package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/anno/internal/cli"
	"orc/common/fault"
)

// failWriter fails after letting a fixed number of writes through, which lets a
// test choose exactly which of a command's writes breaks.
type failWriter struct {
	allow int
	err   error
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.allow <= 0 {
		return 0, w.err
	}
	w.allow--
	return len(p), nil
}

// panicWriter is the only way to prove Main recovers rather than crashing.
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("stdout exploded") }

// failReader stands in for a standard input that cannot be read.
type failReader struct{ err error }

func (r failReader) Read([]byte) (int, error) { return 0, r.err }

func TestOutputFailuresAreReported(t *testing.T) {
	dir := workspace(t, map[string]string{
		"a.go": "// @:> section s\n// @:> part p\nbody\n",
		"b.go": "// @:> section s\n// @:> part p\nbody\n",
	})
	path := filepath.Join(dir, "a.go")
	boom := errors.New("stdout is gone")

	for _, tc := range []struct {
		name  string
		allow int
		args  []string
	}{
		{"index", 0, []string{"index", path}},
		{"overview first table", 0, []string{"overview", dir}},
		{"overview separator", 1, []string{"overview", dir}},
		{"read", 0, []string{"read", path + "^p"}},
		{"read whole file", 0, []string{"read", path}},
		{"find header", 0, []string{"find", dir + "^p"}},
		{"find row", 1, []string{"find", dir + "^p"}},
		{"find content", 2, []string{"find", dir + "^p"}},
		{"find separator", 3, []string{"find", dir + "^p"}},
		{"help", 0, []string{"help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errOut strings.Builder
			code := cli.Main(cli.App{
				Stdout: &failWriter{allow: tc.allow, err: boom},
				Stderr: &errOut,
				Scope:  allow,
			}, tc.args)
			if code == cli.CodeOK {
				t.Fatalf("exit 0, want a failure when stdout cannot be written")
			}
		})
	}
}

func TestWriteSummaryFailureIsReported(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	var errOut strings.Builder
	code := cli.Main(cli.App{
		Stdout: &failWriter{err: errors.New("gone")},
		Stderr: &errOut,
		Scope:  allow,
	}, []string{"write", filepath.Join(dir, "a.go") + "@s", "new\n"})
	if code == cli.CodeOK {
		t.Errorf("exit 0, want a failure when the summary cannot be written")
	}
}

func TestMainRecoversFromAPanic(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nx\n"})
	var errOut strings.Builder
	code := cli.Main(cli.App{Stdout: panicWriter{}, Stderr: &errOut}, []string{"index", filepath.Join(dir, "a.go")})
	if code != cli.CodeInternal {
		t.Fatalf("exit %d, want %d", code, cli.CodeInternal)
	}
	// The message says "this is a bug" rather than "a bug in anno": the tool
	// names itself in the "anno:" prefix already, and the vocabulary moved to
	// orc/common where it is shared with every other tool.
	for _, want := range []string{"panic", "anno:", "this is a bug"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr %q should mention %q", errOut.String(), want)
		}
	}
}

func TestStandardInputFailureIsReported(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nold\n"})
	var out, errOut strings.Builder
	code := cli.Main(cli.App{
		Stdin:  failReader{err: errors.New("pipe broke")},
		Stdout: &out,
		Stderr: &errOut,
		Scope:  allow,
	}, []string{"write", filepath.Join(dir, "a.go") + "@s", "-"})
	if code != cli.CodeIO {
		t.Fatalf("exit %d, want %d", code, cli.CodeIO)
	}
	if !strings.Contains(errOut.String(), "standard input") {
		t.Errorf("stderr %q should name the failure", errOut.String())
	}
}

func TestUnreadableFilesAreReportedNotSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nx\n"})
	path := filepath.Join(dir, "a.go")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	// The path exists, so locate accepts it; the read then fails.
	run(t, "", "read", path+"@s").mustFail(t, cli.CodeIO)
	run(t, "", "write", path+"@s", "y\n").mustFail(t, cli.CodeIO)
	run(t, "", "index", path).mustFail(t, cli.CodeIO)
}

func TestUnlistableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can list anything")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "closed")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	run(t, "", "overview", dir).mustFail(t, cli.CodeIO)
	run(t, "", "find", dir+"^p").mustFail(t, cli.CodeIO)
}

func TestBrokenAnnotationsInATargetedFile(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\n// @:< ghost\n"})
	path := filepath.Join(dir, "a.go")
	run(t, "", "read", path+"@s").mustFail(t, cli.CodeParse)
	run(t, "", "write", path+"@s", "x\n").mustFail(t, cli.CodeParse)
}

func TestBrokenFilesInADirectoryAreSkipped(t *testing.T) {
	dir := workspace(t, map[string]string{
		"good.go":   "// @:> section s\n// @:> part p\nfound\n",
		"broken.go": "// @:< ghost\n",
		"binary":    "\x00",
	})
	got := run(t, "", "find", dir+"^p").mustSucceed(t)
	if !strings.Contains(got.stdout, "found") {
		t.Errorf("the readable file should still be searched:\n%s", got.stdout)
	}
	for _, want := range []string{"broken.go", "binary"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr should report skipping %q:\n%s", want, got.stderr)
		}
	}
}

func TestEmptyTargets(t *testing.T) {
	run(t, "", "read", "").mustFail(t, cli.CodeUsage)
	run(t, "", "overview", "").mustFail(t, cli.CodeUsage)
	run(t, "", "find", "").mustFail(t, cli.CodeUsage)
}

func TestSuggestionsAreCapped(t *testing.T) {
	// Twelve parts share a name; addressing them as sections lists ten and says
	// how many were left out.
	var b strings.Builder
	b.WriteString("// @:> section s\n")
	for i := range 12 {
		b.WriteString("// @:> symbol y")
		b.WriteString(string(rune('a' + i)))
		b.WriteString("\n// @:> part dup\nx\n")
	}
	dir := workspace(t, map[string]string{"a.go": b.String()})

	got := run(t, "", "read", filepath.Join(dir, "a.go")+"@dup").mustFail(t, cli.CodeNotFound)
	if !strings.Contains(got.stderr, "and 2 more") {
		t.Errorf("stderr should cap the list and say what it left out:\n%s", got.stderr)
	}
}

func TestExitCodesCoverEveryFault(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fault.Usage{Reason: "r"}, cli.CodeUsage},
		{fault.NotFound{Target: "t"}, cli.CodeNotFound},
		{fault.Ambiguous{Target: "t"}, cli.CodeAmbiguous},
		{fault.Parse{Path: "p", Reason: "r"}, cli.CodeParse},
		{fault.Unbalanced{Path: "p", Name: "n"}, cli.CodeParse},
		{fault.IO{Op: "read", Path: "p"}, cli.CodeIO},
		{fault.Conflict{Path: "p", Reason: "r"}, cli.CodeConflict},
		{fault.Internal{Where: "w", Detail: "d"}, cli.CodeInternal},
	} {
		if got := cli.Code(tc.err); got != tc.want {
			t.Errorf("Code(%T) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
