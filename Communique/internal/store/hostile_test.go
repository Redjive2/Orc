package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// readOnly makes dir unwritable for the rest of the test.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// TestFailuresAreReportedNotSwallowed drives the store against a filesystem that
// refuses it. Every one of these is an i/o fault reaching the caller — a store
// that quietly returned success would leave the user's reply nowhere.
func TestFailuresAreReportedNotSwallowed(t *testing.T) {
	t.Run("snapshot cannot be written", func(t *testing.T) {
		s := open(t)
		readOnly(t, filepath.Join(s.Root(), "machines"))
		if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("snapshot cannot be replaced", func(t *testing.T) {
		s := open(t)
		if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); err != nil {
			t.Fatal(err)
		}
		readOnly(t, filepath.Join(s.Root(), "machines", "studio"))
		if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("machines cannot be listed", func(t *testing.T) {
		s := open(t)
		if err := os.RemoveAll(filepath.Join(s.Root(), "machines")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Machines(); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("an action cannot be queued", func(t *testing.T) {
		s := open(t)
		readOnly(t, filepath.Join(s.Root(), "queue"))
		if _, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("the queue cannot be listed", func(t *testing.T) {
		s := open(t)
		if err := os.RemoveAll(filepath.Join(s.Root(), "queue")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Queue(); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
		if _, err := s.Pending("studio"); !errors.Is(err, fault.ErrIO) {
			t.Errorf("Pending: error = %v, want an i/o fault", err)
		}
		if _, err := s.Prune(at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("Prune: error = %v, want an i/o fault", err)
		}
		if err := s.MarkSent([]protocol.ActionID{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("MarkSent: error = %v, want an i/o fault", err)
		}
	})

	t.Run("a settled entry cannot be written back", func(t *testing.T) {
		s := open(t)
		a, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
		if err != nil {
			t.Fatal(err)
		}
		readOnly(t, filepath.Join(s.Root(), "queue"))
		err = s.Complete([]protocol.Result{{ActionID: a.ID, OK: true, At: at.Add(time.Second)}})
		if !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("a pruned entry cannot be deleted", func(t *testing.T) {
		s := open(t)
		a, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
		if err != nil {
			t.Fatal(err)
		}
		done := at.Add(time.Second)
		if err := s.Complete([]protocol.Result{{ActionID: a.ID, OK: true, At: done}}); err != nil {
			t.Fatal(err)
		}
		readOnly(t, filepath.Join(s.Root(), "queue"))
		if _, err := s.Prune(done.Add(time.Hour)); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("the version file cannot be read", func(t *testing.T) {
		if !modeBitsBite() {
			t.Skip("this machine cannot make a file unreadable to its owner")
		}
		root := t.TempDir()
		if _, err := store.Open(root); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "version"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "version"), 0o600) })
		if _, err := store.Open(root); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("the store directory cannot be created", func(t *testing.T) {
		dir := t.TempDir()
		blocked := filepath.Join(dir, "file")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Open(filepath.Join(blocked, "store")); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})
}

// modeBitsBite reports whether this machine can genuinely deny the process
// access to a file it owns.
//
// Two machines cannot, and the tests that chmod something all need one that
// can. Root is
// refused nothing. Windows has no mode bit for this at all: os.Chmod there
// toggles the read-only attribute and leaves reading alone, and does not make a
// directory unwritable — so the failure these tests provoke simply does not
// happen, and the assertion would fail for the wrong reason.
func modeBitsBite() bool {
	return os.Geteuid() != 0 && runtime.GOOS != "windows"
}
