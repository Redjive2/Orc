package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNudgingIsSilentAndDoesNotFailTheCommand runs the real spawn path.
//
// It stands a deliberately hostile script in for cq — one that writes to both
// streams and exits non-zero — and checks the two things an agent depends on:
// the command's own output is untouched, and its exit code does not depend on
// whether the mirror could be reached. An agent parsing `mailman send` must not
// have to know cq exists.
func TestNudgingIsSilentAndDoesNotFailTheCommand(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "cq")
	script := "#!/bin/sh\necho noise-on-stdout\necho noise-on-stderr >&2\nexit 3\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CQ_SERVER", "https://cq.example")
	t.Setenv("CQ_BIN", fake)

	r := newRig(t, "boss", "dave")
	got := r.ok("boss", "send", "hello", "dave", "a body")

	for _, stream := range []struct{ name, text string }{
		{"stdout", got.stdout},
		{"stderr", got.stderr},
	} {
		if strings.Contains(stream.text, "noise") {
			t.Errorf("the nudge leaked into %s:\n%s", stream.name, stream.text)
		}
	}
	if !strings.Contains(got.stdout, "sent") {
		t.Errorf("the command's own output went missing:\n%s", got.stdout)
	}
}

// TestAnUnmirroredMachineSpawnsNothing: the script would exit 3 if it ran, and
// a nudge that ignored the absence of a server would fail every command on
// every machine in Orc that does not mirror.
func TestAnUnmirroredMachineSpawnsNothing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	fake := filepath.Join(dir, "cq")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CQ_SERVER", "")
	t.Setenv("CQ_BIN", fake)

	r := newRig(t, "boss", "dave")
	r.ok("boss", "send", "hello", "dave", "a body")

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("something was spawned on a machine with no mirror: %v", err)
	}
}
