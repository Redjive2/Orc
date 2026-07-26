package source_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/common/source"
)

func TestParseSplitsLines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		count  int
		lines  []string
		ending source.Ending
		final  bool
	}{
		{"empty", "", 0, nil, source.LF, false},
		{"single terminated", "a\n", 1, []string{"a"}, source.LF, true},
		{"single unterminated", "a", 1, []string{"a"}, source.LF, false},
		{"blank line", "\n", 1, []string{""}, source.LF, true},
		{"two lines", "a\nb\n", 2, []string{"a", "b"}, source.LF, true},
		{"crlf", "a\r\nb\r\n", 2, []string{"a", "b"}, source.CRLF, true},
		{"mixed mostly crlf", "a\r\nb\r\nc\n", 3, []string{"a", "b", "c"}, source.CRLF, true},
		{"mixed mostly lf", "a\r\nb\nc\n", 3, []string{"a", "b", "c"}, source.LF, true},
		{"lone cr is content", "a\rb\n", 1, []string{"a\rb"}, source.LF, true},
		{"trailing blank", "a\n\n", 2, []string{"a", ""}, source.LF, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := source.Parse("x.go", []byte(tc.text))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := f.Count(); got != tc.count {
				t.Errorf("Count = %d, want %d", got, tc.count)
			}
			if got := f.Lines(); strings.Join(got, "|") != strings.Join(tc.lines, "|") {
				t.Errorf("Lines = %q, want %q", got, tc.lines)
			}
			if got := f.Ending(); got != tc.ending {
				t.Errorf("Ending = %v, want %v", got, tc.ending)
			}
			if got := f.FinalNewline(); got != tc.final {
				t.Errorf("FinalNewline = %v, want %v", got, tc.final)
			}
			if got := f.Bytes(); !bytes.Equal(got, []byte(tc.text)) {
				t.Errorf("Bytes = %q, want %q", got, tc.text)
			}
		})
	}
}

// TestBytesRoundTripsExactly is the property the whole write path depends on:
// a File reproduces its input byte for byte, whatever its line endings.
func TestBytesRoundTripsExactly(t *testing.T) {
	for _, text := range []string{
		"", "a", "a\n", "\n\n\n", "a\r\nb", "a\n\r\nb\r\n", "\r\n", "no newline at end",
		"unicode → ✓\nsecond\n",
	} {
		f, err := source.Parse("x", []byte(text))
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if got := string(f.Bytes()); got != text {
			t.Errorf("round trip of %q gave %q", text, got)
		}
	}
}

func TestParseRejectsBadContent(t *testing.T) {
	if _, err := source.Parse("x", []byte("a\x00b")); !errors.Is(err, fault.ErrParse) {
		t.Errorf("NUL byte should be a parse fault, got %v", err)
	} else if !strings.Contains(err.Error(), "binary") {
		t.Errorf("message %q should say the file is binary", err)
	}
	if _, err := source.Parse("x", []byte{0xff, 0xfe}); !errors.Is(err, fault.ErrParse) {
		t.Errorf("invalid UTF-8 should be a parse fault, got %v", err)
	}
	if _, err := source.Parse("", []byte("a")); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("empty path should be a usage fault, got %v", err)
	}
}

func TestLineAccess(t *testing.T) {
	f, err := source.Parse("x", []byte("one\ntwo\nthree\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.Line(2); err != nil || got != "two" {
		t.Errorf("Line(2) = %q, %v", got, err)
	}
	for _, n := range []int{0, -1, 4} {
		if _, err := f.Line(n); !errors.Is(err, fault.ErrInternal) {
			t.Errorf("Line(%d) error = %v, want internal", n, err)
		}
	}
}

func TestSliceAndByteRange(t *testing.T) {
	f, err := source.Parse("x", []byte("one\ntwo\nthree\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Slice(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two\nthree\n" {
		t.Errorf("Slice(2,3) = %q", got)
	}

	// An empty range yields nothing and is not an error.
	if got, err := f.Slice(3, 2); err != nil || got != nil {
		t.Errorf("Slice(3,2) = %q, %v; want nil, nil", got, err)
	}

	if _, _, err := f.ByteRange(0, 2); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("ByteRange(0,2) should be internal, got %v", err)
	}
	if _, _, err := f.ByteRange(1, 9); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("ByteRange(1,9) should be internal, got %v", err)
	}
	if _, err := f.Slice(1, 9); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Slice past the end should be internal, got %v", err)
	}
}

func TestSliceKeepsAMissingFinalNewline(t *testing.T) {
	f, err := source.Parse("x", []byte("a\nb"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Slice(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\nb" {
		t.Errorf("Slice = %q, want %q", got, "a\nb")
	}
}

func TestInsertOffset(t *testing.T) {
	f, err := source.Parse("x", []byte("aa\nbb\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ line, want int }{{1, 0}, {2, 3}, {3, 6}} {
		got, err := f.InsertOffset(tc.line)
		if err != nil {
			t.Fatalf("InsertOffset(%d): %v", tc.line, err)
		}
		if got != tc.want {
			t.Errorf("InsertOffset(%d) = %d, want %d", tc.line, got, tc.want)
		}
	}
	for _, n := range []int{0, 4} {
		if _, err := f.InsertOffset(n); !errors.Is(err, fault.ErrInternal) {
			t.Errorf("InsertOffset(%d) should be internal, got %v", n, err)
		}
	}
}

func TestSumDetectsChange(t *testing.T) {
	a, err := source.Parse("x", []byte("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := source.Parse("x", []byte("b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Sum() == b.Sum() {
		t.Errorf("different content should hash differently")
	}
	again, err := source.Parse("y", []byte("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Sum() != again.Sum() {
		t.Errorf("identical content should hash identically")
	}
}

func TestBytesIsACopy(t *testing.T) {
	f, err := source.Parse("x", []byte("abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw := f.Bytes()
	raw[0] = 'z'
	if string(f.Bytes()) != "abc\n" {
		t.Errorf("Bytes() exposed internal state")
	}
}

func TestParseDoesNotAliasItsInput(t *testing.T) {
	data := []byte("abc\n")
	f, err := source.Parse("x", data)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'z'
	if string(f.Bytes()) != "abc\n" {
		t.Errorf("Parse aliased the caller's slice")
	}
}

func TestPathAndName(t *testing.T) {
	f, err := source.Parse(filepath.Join("a", "b", "c.go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.Name(), "c.go"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := f.Path(), filepath.Join("a", "b", "c.go"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestEndingProperties(t *testing.T) {
	if got := string(source.LF.Bytes()); got != "\n" {
		t.Errorf("LF bytes = %q", got)
	}
	if got := string(source.CRLF.Bytes()); got != "\r\n" {
		t.Errorf("CRLF bytes = %q", got)
	}
	if got := source.LF.String(); got != "LF" {
		t.Errorf("LF string = %q", got)
	}
	if got := source.CRLF.String(); got != "CRLF" {
		t.Errorf("CRLF string = %q", got)
	}
	if got := source.Ending(9).String(); !strings.Contains(got, "9") {
		t.Errorf("unknown ending string = %q", got)
	}
	if got := string(source.Ending(9).Bytes()); got != "\n" {
		t.Errorf("unknown ending should fall back to LF, got %q", got)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := source.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := string(f.Bytes()); got != "hello\n" {
		t.Errorf("content = %q", got)
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()

	if _, err := source.Load(""); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("empty path should be a usage fault, got %v", err)
	}
	if _, err := source.Load(filepath.Join(dir, "missing")); !errors.Is(err, fault.ErrIO) {
		t.Errorf("missing file should be an i/o fault, got %v", err)
	}
	if _, err := source.Load(dir); !errors.Is(err, fault.ErrIO) {
		t.Errorf("directory should be an i/o fault, got %v", err)
	} else if !strings.Contains(err.Error(), "directory") {
		t.Errorf("message %q should say it is a directory", err)
	}

	binary := filepath.Join(dir, "bin")
	if err := os.WriteFile(binary, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(binary); !errors.Is(err, fault.ErrParse) {
		t.Errorf("binary file should be a parse fault, got %v", err)
	}
}

func TestLoadFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.go")
	if err := os.WriteFile(real, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f, err := source.Load(link)
	if err != nil {
		t.Fatalf("Load through a symlink: %v", err)
	}
	if got := string(f.Bytes()); got != "x\n" {
		t.Errorf("content = %q", got)
	}

	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "gone"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(dangling); !errors.Is(err, fault.ErrIO) {
		t.Errorf("dangling symlink should be an i/o fault, got %v", err)
	}
}

func TestLoadRejectsIrregularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs on windows")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := makeFIFO(fifo); err != nil {
		t.Skipf("cannot create a FIFO: %v", err)
	}
	if _, err := source.Load(fifo); !errors.Is(err, fault.ErrIO) {
		t.Errorf("FIFO should be an i/o fault, got %v", err)
	} else if !strings.Contains(err.Error(), "regular") {
		t.Errorf("message %q should say it is not a regular file", err)
	}
}

func TestLoadRejectsUnreadableFiles(t *testing.T) {
	// Root is refused nothing, and Windows has no mode bit for this: os.Chmod
	// there toggles the read-only attribute and leaves reading alone, so there
	// is no unreadable file to make and the assertion would fail for the wrong
	// reason.
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.go")
	if err := os.WriteFile(path, []byte("x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(path); !errors.Is(err, fault.ErrIO) {
		t.Errorf("unreadable file should be an i/o fault, got %v", err)
	}
}

func TestUniform(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"", true},
		{"a", true},
		{"a\nb\n", true},
		{"a\r\nb\r\n", true},
		{"a\r\nb\n", false},
		{"a\nb\r\n", false},
		{"a\r\nb", true},
	} {
		f, err := source.Parse("x", []byte(tc.text))
		if err != nil {
			t.Fatal(err)
		}
		if got := f.Uniform(); got != tc.want {
			t.Errorf("Uniform(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
