package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// validate checks that scan did its job. Reaching it requires the corrupt line
// tables scan is supposed to never produce.

func TestValidateRejectsCorruptLineTables(t *testing.T) {
	raw := []byte("abc\ndef\n")

	for _, tc := range []struct {
		name  string
		lines []line
		want  string
	}{
		{
			"gap between lines",
			[]line{{start: 0, content: 3, term: 4}, {start: 5, content: 7, term: 8}},
			"expected 4",
		},
		{
			"disordered offsets",
			[]line{{start: 0, content: 4, term: 3}},
			"disordered",
		},
		{
			"oversized terminator",
			[]line{{start: 0, content: 0, term: 3}},
			"3 byte terminator",
		},
		{
			"content spanning a newline",
			[]line{{start: 0, content: 5, term: 5}},
			"contains a newline",
		},
		{
			"lines do not cover the file",
			[]line{{start: 0, content: 3, term: 4}},
			"cover 4 of 8",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := File{path: "x.go", raw: raw, lines: tc.lines}
			err := f.validate()
			if !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("error = %v, want an internal fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsWhatScanProduces(t *testing.T) {
	for _, text := range []string{"", "a", "a\n", "a\r\nb", "\n\n"} {
		f := File{path: "x", raw: []byte(text), lines: scan([]byte(text))}
		if err := f.validate(); err != nil {
			t.Errorf("validate(%q) = %v, want nil", text, err)
		}
	}
}

func TestLoadRefusesOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file: the size check runs before any bytes are read.
	if err := f.Truncate(MaxSize + 1); err != nil {
		_ = f.Close()
		t.Skipf("cannot create a sparse file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Load(path)
	if !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
	if !strings.Contains(err.Error(), "limit is") {
		t.Errorf("message %q should state the limit", err)
	}
}
