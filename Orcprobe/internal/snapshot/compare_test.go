package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCompareFindsEveryKindOfDifference(t *testing.T) {
	left := tree(t, map[string]string{
		"same.txt":    "unchanged",
		"changed.txt": "before",
		"gone.txt":    "removed",
	})
	right := tree(t, map[string]string{
		"same.txt":    "unchanged",
		"changed.txt": "after!!",
		"new.txt":     "added",
	})

	d, err := Compare(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if d.Same != 1 {
		t.Fatalf("counted %d identical files, want 1", d.Same)
	}

	found := map[string]Kind{}
	for _, c := range d.Changes {
		found[c.Rel] = c.Kind
	}
	for rel, want := range map[string]Kind{
		"changed.txt": Changed,
		"gone.txt":    Removed,
		"new.txt":     Added,
	} {
		if found[rel] != want {
			t.Fatalf("%s came back as %q, want %q", rel, found[rel], want)
		}
	}
}

// TestCompareIgnoresTimestamps is why the comparison is by digest. Two copies
// of a store made a second apart differ in every modification time and in no
// content, and a diff that called that "everything changed" would be useless.
func TestCompareIgnoresTimestamps(t *testing.T) {
	left := tree(t, map[string]string{"a.txt": "same", "b/c.txt": "same too"})
	right := filepath.Join(t.TempDir(), "copy")
	if _, err := Copy(right, left, Options{}); err != nil {
		t.Fatal(err)
	}
	// Make the copy's timestamps unmistakably different.
	if err := os.Chtimes(filepath.Join(right, "a.txt"), zeroTime(), zeroTime()); err != nil {
		t.Fatal(err)
	}

	d, err := Compare(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("a byte-identical copy differed: %+v", d.Changes)
	}
	if d.Same != 2 {
		t.Fatalf("counted %d identical files, want 2", d.Same)
	}
}

func TestCompareTreatsAMissingTreeAsEmpty(t *testing.T) {
	left := tree(t, map[string]string{"a.txt": "x"})
	d, err := Compare(left, filepath.Join(t.TempDir(), "never-made"))
	if err != nil {
		t.Fatalf("comparing against a tree that is not there failed: %v", err)
	}
	if len(d.Changes) != 1 || d.Changes[0].Kind != Removed {
		t.Fatalf("got %+v, want one removal", d.Changes)
	}
}

func TestCompareIgnoresEmptyDirectories(t *testing.T) {
	left := tree(t, map[string]string{"a.txt": "x"})
	right := tree(t, map[string]string{"a.txt": "x"})
	if err := os.MkdirAll(filepath.Join(right, "empty", "deeper"), 0o700); err != nil {
		t.Fatal(err)
	}

	d, err := Compare(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("empty directories were reported as differences: %+v", d.Changes)
	}
}

func TestCompareTruncatesLoudly(t *testing.T) {
	files := map[string]string{}
	for i := range MaxChanges + 10 {
		files[filepath.Join("many", itoaTest(i)+".txt")] = "x"
	}
	left := tree(t, files)
	right := tree(t, map[string]string{})

	d, err := Compare(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Changes) != MaxChanges {
		t.Fatalf("listed %d changes, want the cap of %d", len(d.Changes), MaxChanges)
	}
	if d.Truncated != 10 {
		t.Fatalf("reported %d truncated, want 10 — a silent cap reads as a complete answer", d.Truncated)
	}
	if d.Count() != MaxChanges+10 {
		t.Fatalf("Count is %d, want every difference including the unlisted ones", d.Count())
	}
}

func TestWithinNarrowsToASubtree(t *testing.T) {
	left := tree(t, map[string]string{"state/mailman/a": "1", "repo/b": "1"})
	right := tree(t, map[string]string{"state/mailman/a": "2", "repo/b": "2"})

	d, err := Compare(left, right)
	if err != nil {
		t.Fatal(err)
	}
	mail := d.Within("state/mailman")
	if len(mail.Changes) != 1 || mail.Changes[0].Rel != "a" {
		t.Fatalf("narrowing gave %+v, want just the mail store's own path", mail.Changes)
	}
}

// zeroTime is an unmistakably different modification time.
func zeroTime() time.Time { return time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC) }

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
