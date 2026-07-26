package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// These tests are in the store package rather than store_test because they
// replace the filesystem operations, which are deliberately unexported: the
// injection points exist for this file, not for callers.

var faultEpoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

func newFaultStore(t *testing.T) (*Store, task.Name, user.Name, task.Score, task.Score) {
	t.Helper()
	s, err := Open(t.TempDir(), clock.NewFake(faultEpoch, time.Millisecond))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	name, err := task.ParseName("fix-the-parser")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := user.Parse("alice")
	if err != nil {
		t.Fatal(err)
	}
	p, err := task.NewPriority(3)
	if err != nil {
		t.Fatal(err)
	}
	d, err := task.NewDifficulty(3)
	if err != nil {
		t.Fatal(err)
	}
	return s, name, alice, p, d
}

// failingTemp wraps a temp file so a chosen step fails.
type failingTemp struct {
	TempFile
	failWrite bool
	failSync  bool
	failChmod bool
	failClose bool
}

func (f *failingTemp) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("no space left on device")
	}
	return f.TempFile.Write(p)
}

func (f *failingTemp) Sync() error {
	if f.failSync {
		return errors.New("flush failed")
	}
	return f.TempFile.Sync()
}

func (f *failingTemp) Chmod(m fs.FileMode) error {
	if f.failChmod {
		return errors.New("chmod failed")
	}
	return f.TempFile.Chmod(m)
}

func (f *failingTemp) Close() error {
	if f.failClose {
		_ = f.TempFile.Close()
		return errors.New("close failed")
	}
	return f.TempFile.Close()
}

// breakTemp returns an ops setter that fails one step of the atomic write.
func breakTemp(pick func(*failingTemp)) func(*Ops) {
	return func(o *Ops) {
		real := RealOps()
		o.SetCreateTemp(func(dir, pattern string) (TempFile, error) {
			f, err := real.createTemp(dir, pattern)
			if err != nil {
				return nil, err
			}
			wrapped := &failingTemp{TempFile: f}
			pick(wrapped)
			return wrapped, nil
		})
	}
}

// TestEveryCreateFailureLeavesNoTask is the claim the atomic write makes:
// whichever step fails, no half-made task appears and no temporary file
// survives to be mistaken for one.
func TestEveryCreateFailureLeavesNoTask(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*Ops)
	}{
		{"mkdir fails", func(o *Ops) {
			o.SetMkdirAll(func(string, fs.FileMode) error { return errors.New("read-only file system") })
		}},
		{"createTemp fails", func(o *Ops) {
			o.SetCreateTemp(func(string, string) (TempFile, error) { return nil, errors.New("too many open files") })
		}},
		{"write fails", breakTemp(func(f *failingTemp) { f.failWrite = true })},
		{"sync fails", breakTemp(func(f *failingTemp) { f.failSync = true })},
		{"chmod fails", breakTemp(func(f *failingTemp) { f.failChmod = true })},
		{"close fails", breakTemp(func(f *failingTemp) { f.failClose = true })},
		{"rename fails after a successful flush", func(o *Ops) {
			o.SetRename(func(string, string) error { return errors.New("cross-device link") })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, name, alice, p, d := newFaultStore(t)

			ops := RealOps()
			tc.break_(&ops)
			s.WithOps(ops)

			if _, err := s.Create(name, alice, p, d); !errors.Is(err, fault.ErrIO) {
				t.Fatalf("Create = %v, want an i/o fault", err)
			}

			s.WithOps(RealOps())
			if ok, _ := s.Has(name); ok {
				t.Error("a failed create left a task behind")
			}
			assertNoTempFiles(t, s.Root())
		})
	}
}

// TestASuccessfulCreateIsTheOnlyOneThatShows: the same harness with nothing
// broken must succeed, or the table above proves nothing.
func TestASuccessfulCreateIsTheOnlyOneThatShows(t *testing.T) {
	s, name, alice, p, d := newFaultStore(t)
	if _, err := s.Create(name, alice, p, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ok, _ := s.Has(name); !ok {
		t.Error("the task should exist")
	}
	assertNoTempFiles(t, s.Root())
}

func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(d.Name(), ".tmp-") {
			t.Errorf("a temporary file survived: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the store: %v", err)
	}
}

// TestApplyFailuresLeaveTheTaskUnchanged: an event that could not be written
// must not appear to have happened.
func TestApplyFailuresLeaveTheTaskUnchanged(t *testing.T) {
	s, name, alice, p, d := newFaultStore(t)
	if _, err := s.Create(name, alice, p, d); err != nil {
		t.Fatal(err)
	}

	ops := RealOps()
	ops.SetOpenAppend(func(string) (*os.File, error) { return nil, errors.New("permission denied") })
	s.WithOps(ops)

	_, err := s.Apply(name, func(task.Task) (task.Event, error) {
		return task.Scope(alice, s.Now(), []string{"internal/tree/"})
	})
	if !errors.Is(err, fault.ErrIO) {
		t.Fatalf("Apply with an unwritable journal = %v, want an i/o fault", err)
	}

	s.WithOps(RealOps())
	got, err := s.Load(name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Scoped() {
		t.Error("a failed append left the scope in place")
	}
}

func TestReadFailuresAreClassified(t *testing.T) {
	s, name, alice, p, d := newFaultStore(t)
	if _, err := s.Create(name, alice, p, d); err != nil {
		t.Fatal(err)
	}

	ops := RealOps()
	ops.SetReadFile(func(string) ([]byte, error) { return nil, errors.New("input/output error") })
	s.WithOps(ops)

	if _, err := s.Load(name); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Load with a failing read = %v, want an i/o fault", err)
	}
	if _, err := s.Apply(name, func(task.Task) (task.Event, error) {
		return task.Push(alice, s.Now())
	}); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Apply with a failing read = %v, want an i/o fault", err)
	}
}

func TestListFailuresAreReported(t *testing.T) {
	s, _, _, _, _ := newFaultStore(t)

	ops := RealOps()
	ops.SetReadDir(func(string) ([]fs.DirEntry, error) { return nil, errors.New("permission denied") })
	s.WithOps(ops)

	if _, err := s.Names(); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Names with a failing list = %v, want an i/o fault", err)
	}
}

// TestStatFailuresDoNotBecomeAbsence: a stat that fails for a reason other than
// "not there" must not be read as "not there", or the write-once guard on a
// creation record would let one task overwrite another.
func TestStatFailuresDoNotBecomeAbsence(t *testing.T) {
	s, name, alice, p, d := newFaultStore(t)

	ops := RealOps()
	ops.SetStat(func(string) (fs.FileInfo, error) { return nil, errors.New("input/output error") })
	s.WithOps(ops)

	if _, err := s.Has(name); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Has with a failing stat = %v, want an i/o fault", err)
	}
	if _, err := s.Create(name, alice, p, d); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Create with a failing stat = %v, want an i/o fault", err)
	}
}
