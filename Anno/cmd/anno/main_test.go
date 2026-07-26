package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orc/anno/internal/cli"
	"orc/anno/internal/fixture"
)

// binary builds the real command once and returns its path. Everything else in
// the suite tests packages directly; this exists to prove that the assembled
// program wires streams and exit codes to the operating system correctly.
func binary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anno")
	build := exec.Command("go", "build", "-o", path, "orc/anno/cmd/anno")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building anno: %v\n%s", err, out)
	}
	return path
}

func TestCommandEndToEnd(t *testing.T) {
	anno := binary(t)

	dir := t.TempDir()
	file := filepath.Join(dir, "example.go")
	if err := os.WriteFile(file, []byte(fixture.ExampleGo), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("index writes the table to stdout", func(t *testing.T) {
		out, err := exec.Command(anno, "index", file).Output()
		if err != nil {
			t.Fatalf("anno index: %v", err)
		}
		if !strings.Contains(string(out), "part declarations") {
			t.Errorf("stdout:\n%s", out)
		}
	})

	t.Run("read and write round trip through the real binary", func(t *testing.T) {
		out, err := exec.Command(anno, "read", file+"^declarations").Output()
		if err != nil {
			t.Fatalf("anno read: %v", err)
		}

		write := exec.Command(anno, "write", file+"^declarations", "-")
		write.Stdin = strings.NewReader(string(out))
		if summary, err := write.Output(); err != nil {
			t.Fatalf("anno write: %v", err)
		} else if !strings.Contains(string(summary), "replaced") {
			t.Errorf("summary: %s", summary)
		}

		after, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != fixture.ExampleGo {
			t.Errorf("the file changed:\n%s", after)
		}
	})

	t.Run("exit codes reach the shell", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want int
		}{
			{"success", []string{"index", file}, cli.CodeOK},
			{"usage", []string{"frobnicate"}, cli.CodeUsage},
			{"not found", []string{"read", file + "@nope"}, cli.CodeNotFound},
			{"i/o", []string{"index", filepath.Join(dir, "missing.go")}, cli.CodeIO},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cmd := exec.Command(anno, tc.args...)
				err := cmd.Run()

				got := 0
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					got = exit.ExitCode()
				} else if err != nil {
					t.Fatalf("running anno: %v", err)
				}
				if got != tc.want {
					t.Errorf("exit %d, want %d", got, tc.want)
				}
			})
		}
	})
}
