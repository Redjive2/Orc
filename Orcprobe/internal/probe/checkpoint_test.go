package probe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/orcprobe/internal/fault"
)

// mailPath is a file inside the probe's copied mail store, used as the thing
// that changes between a save and a restore.
const mailPath = "state/mailman/users/alice/journal.jsonl"

func saved(t *testing.T) (*Store, *Probe) {
	t.Helper()
	w := newWorld(t, "mailman", "macmuffin", "cq")
	s := newStore(t)
	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	return s, report.Probe
}

func TestSaveAndRestore(t *testing.T) {
	s, p := saved(t)
	live := p.Path(filepath.FromSlash(mailPath))

	before, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}

	point, err := s.Save(p, "before-migration")
	if err != nil {
		t.Fatal(err)
	}
	if point.Files == 0 {
		t.Fatal("the checkpoint captured nothing")
	}

	// Wreck the probe the way an experiment would.
	if err := os.WriteFile(live, []byte("{\"op\":\"nonsense\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path("state", "mailman", "extra.txt"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Restore(p, "before-migration"); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the restore did not put the file back:\n%s", string(after))
	}
	// A restore replaces rather than merges: a file created since the save is
	// gone, or a rewind would leave the probe in a state that never existed.
	if _, err := os.Stat(p.Path("state", "mailman", "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("a file created after the checkpoint survived the rewind")
	}
}

// TestRestoreKeepsTheProbesIdentity is the line between "rewind the contents"
// and "rewind the probe". Keys, the stamp, and the record are what a probe *is*,
// and a rewind that changed who you are inside it would be unreasonable about.
func TestRestoreKeepsTheProbesIdentity(t *testing.T) {
	s, p := saved(t)
	if _, err := s.Save(p, "point"); err != nil {
		t.Fatal(err)
	}

	identitiesBefore, err := os.ReadFile(p.Path(IdentitiesFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(p, "point"); err != nil {
		t.Fatal(err)
	}

	for _, part := range []string{RecordFile, ProbeStamp, IdentitiesFile, EnvFile} {
		if _, err := os.Stat(p.Path(part)); err != nil {
			t.Fatalf("%s did not survive the restore: %v", part, err)
		}
	}
	identitiesAfter, err := os.ReadFile(p.Path(IdentitiesFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(identitiesBefore) != string(identitiesAfter) {
		t.Fatal("the restore rewrote the probe's keys")
	}
}

// TestRestoreIsRecorded keeps the manifest honest: a rewind is an event in the
// probe's history, not a way to erase one.
func TestRestoreIsRecorded(t *testing.T) {
	s, p := saved(t)
	if _, err := s.Save(p, "point"); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(p, "point"); err != nil {
		t.Fatal(err)
	}

	entries, _, err := ReadManifest(p.Path(ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var savedIt, restoredIt bool
	for _, e := range entries {
		if strings.Contains(e.Detail, "saved") {
			savedIt = true
		}
		if strings.Contains(e.Detail, "restored") {
			restoredIt = true
		}
	}
	if !savedIt || !restoredIt {
		t.Fatal("the manifest does not record both the save and the rewind")
	}
}

func TestSaveRefusesToOverwrite(t *testing.T) {
	s, p := saved(t)
	if _, err := s.Save(p, "point"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Save(p, "point")
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("a second save under the same label returned %v; it must refuse rather than discard the first", err)
	}
}

func TestRestoreNamesTheLabelsItHas(t *testing.T) {
	s, p := saved(t)
	if _, err := s.Save(p, "one"); err != nil {
		t.Fatal(err)
	}
	err := s.Restore(p, "two")
	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("restoring an unknown label returned %v", err)
	}
	if !strings.Contains(err.Error(), "one") {
		t.Fatalf("the error does not list the labels that exist:\n%v", err)
	}
}

func TestCheckpointsAreListed(t *testing.T) {
	s, p := saved(t)
	for _, label := range []string{"first", "second"} {
		if _, err := s.Save(p, label); err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.Checkpoints(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("listed %d checkpoints, want 2", len(points))
	}
}

func TestCheckLabel(t *testing.T) {
	for _, good := range []string{"before-migration", "v1", "a.b_c"} {
		if _, err := CheckLabel(good); err != nil {
			t.Fatalf("CheckLabel(%q) refused a good label: %v", good, err)
		}
	}
	for _, bad := range []string{"", "..", "-leading", "has space", "/etc/passwd"} {
		if _, err := CheckLabel(bad); err == nil {
			t.Fatalf("CheckLabel(%q) accepted a label that is a path element", bad)
		}
	}
}

// TestRestoreLeavesNoDebris covers the swap: a restore builds the new copy
// beside the live one and renames it in, so a probe never ends up holding the
// staging or the previous directory.
func TestRestoreLeavesNoDebris(t *testing.T) {
	s, p := saved(t)
	if _, err := s.Save(p, "point"); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(p, "point"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(p.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".restoring") || strings.HasSuffix(e.Name(), ".previous") {
			t.Fatalf("the restore left %s behind", e.Name())
		}
	}
}
