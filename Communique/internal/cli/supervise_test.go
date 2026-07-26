package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/cli"
)

// TestABinaryWithNoExtensionCannotStartItself is the Windows failure that made
// `cq serve` unusable, reproduced on whatever platform runs this.
//
// `go build -o cq` writes a file with no extension on every platform, and a
// shell will run it. os/exec will not on Windows: it resolves even an absolute
// path through PATHEXT, so the program cannot start a copy of itself, and the
// error it hands back says "executable file not found in %PATH%" while naming a
// path that is plainly right there.
//
// What is tested here is the explanation, because the explanation is the fix:
// an operator who reads it knows to rename the file.
func TestABinaryWithNoExtensionSaysWhatToDo(t *testing.T) {
	got := cli.CannotStart("windows", `C:\Users\redba\.local\bin\cq`)

	for _, want := range []string{"cq.exe", "no", "extension"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not mention %q: %s", want, got)
		}
	}
	// And it must not repeat os/exec's misleading claim about %PATH%.
	if strings.Contains(got, "%PATH%") {
		t.Errorf("the refusal blames the path, which is not the problem: %s", got)
	}
}

// Somewhere with no extension to be missing, the message must still say
// something true rather than telling a unix operator to add `.exe`.
func TestElsewhereTheRefusalDoesNotTalkAboutExtensions(t *testing.T) {
	got := cli.CannotStart("linux", "/home/redjive/.local/bin/cq")

	// And a Windows binary that already has its extension has some other
	// problem, so it must not be told to add one it has.
	named := cli.CannotStart("windows", `C:\bin\cq.exe`)
	if strings.Contains(named, "cq.exe.exe") || strings.Contains(named, "no extension") {
		t.Errorf("a named binary was told to rename itself: %s", named)
	}
	if strings.Contains(got, ".exe") {
		t.Errorf("a unix refusal mentions .exe: %s", got)
	}
	if !strings.Contains(got, "executable") {
		t.Errorf("the refusal should say what to check: %s", got)
	}
}

// The ordinary case: the running test binary can start a copy of itself, so
// nothing warns and the supervisor is used.
func TestARealBinaryIsRestartable(t *testing.T) {
	exe, err := cli.Restartable()
	if err != nil {
		t.Fatalf("the running test binary is not restartable: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(exe) != filepath.Clean(self) {
		t.Errorf("restartable() = %q, want %q", exe, self)
	}
}
