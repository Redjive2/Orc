package upgrade_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/upgrade"
)

// An upgrade that builds and installs nowhere useful used to report success.
//
// `Built` says what the build script *claimed*; nothing measured the disk. So a
// `$CQ_BIN` pointing away from where the server runs produced a clean pull, a clean
// build, a restart into the old binary, and a report saying it had worked. There
// was no way to see it from outside, which is why it could keep happening.

// TestTheReportNamesWhatMoved: the files the build wrote are measured, not assumed.
func TestTheReportNamesWhatMoved(t *testing.T) {
	dir := checkout(t)
	target := t.TempDir()

	// Something already there and untouched by the build, so "changed" is not just
	// "everything in the directory".
	old := filepath.Join(target, "stale")
	if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &recorder{out: map[string]string{"rev-parse": "aaaaaaa\n", "sh/build": buildOutput}}
	r.onBuild = func() {
		// What a build does: writes binaries into the target.
		for _, name := range []string{"cq", "orc"} {
			if err := os.WriteFile(filepath.Join(target, name), []byte("new"), 0o755); err != nil {
				t.Error(err)
			}
		}
	}

	report, err := upgrade.Options{Source: dir, Target: target, Run: r.run}.Upgrade(t.Context())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	var moved []string
	for _, path := range report.Replaced {
		moved = append(moved, filepath.Base(path))
	}
	if strings.Join(moved, " ") != "cq orc" {
		t.Errorf("it reported %v as moved, want cq and orc", moved)
	}
	// The one that did not move is the point: a report that named every file would
	// answer "did the build replace this?" with yes, always.
	if !report.Untouched(old) {
		t.Errorf("a file the build never wrote was reported as replaced")
	}
	if report.Untouched(filepath.Join(target, "cq")) {
		t.Errorf("a file the build wrote was reported as untouched")
	}
}

// The case this exists for: the build succeeds and installs nothing here.
func TestABuildThatInstalledNothingSaysSo(t *testing.T) {
	dir := checkout(t)
	target := t.TempDir()

	r := &recorder{out: map[string]string{"rev-parse": "aaaaaaa\n", "sh/build": buildOutput}}
	report, err := upgrade.Options{Source: dir, Target: target, Run: r.run}.Upgrade(t.Context())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if len(report.Replaced) != 0 {
		t.Fatalf("nothing was written, yet %v was reported as replaced", report.Replaced)
	}
	// Not an error — a rebuild of an unchanged tree can legitimately write nothing
	// new — but it is on the record, where the caller that must not restart on it
	// can see it.
	var said string
	for _, step := range report.Steps {
		if step.What == "install" {
			said = step.Error
		}
	}
	if !strings.Contains(said, "no file in") {
		t.Errorf("a build that installed nothing left no note: %+v", report.Steps)
	}
	// And the question a caller asks before restarting answers correctly.
	if !report.Untouched(filepath.Join(target, "cq")) {
		t.Error("a binary that was never written was reported as replaced")
	}
}
