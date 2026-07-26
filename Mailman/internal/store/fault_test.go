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
	"orc/mailman/internal/mail"
)

// These tests are in the store package rather than store_test because they
// replace the filesystem operations, which are deliberately unexported: the
// injection points exist for this file, not for callers.

var faultEpoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

type seq struct{ n uint64 }

func (s *seq) Read(p []byte) (int, error) {
	for i := range p {
		s.n++
		p[i] = byte(s.n)
	}
	return len(p), nil
}

func newStore(t *testing.T) (*Store, user.Name, mail.Message) {
	t.Helper()
	s, err := Open(t.TempDir(), clock.NewFake(faultEpoch, time.Millisecond))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	alice, err := user.Parse("alice")
	if err != nil {
		t.Fatal(err)
	}
	boss, err := user.Parse("boss")
	if err != nil {
		t.Fatal(err)
	}
	key, err := user.NewKey(&seq{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(alice, key); err != nil {
		t.Fatal(err)
	}

	at := s.clock.Now()
	id, err := mail.NewID(at, &seq{n: 99})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.New(id, mail.Ordinary, boss, []user.Name{alice}, nil, "subject", mail.ID{}, 0, at, []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	return s, alice, m
}

// failing wraps a temp file so a chosen step fails.
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

// TestEveryWriteFailureLeavesNoTrace is the claim the atomic-write discipline
// makes: whichever step fails, the store is left exactly as it was and no
// temporary file survives to be mistaken for real data.
func TestEveryWriteFailureLeavesNoTrace(t *testing.T) {
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
		{"write fails", func(o *Ops) {
			real := RealOps()
			o.SetCreateTemp(func(dir, pattern string) (TempFile, error) {
				f, err := real.createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingTemp{TempFile: f, failWrite: true}, nil
			})
		}},
		{"sync fails", func(o *Ops) {
			real := RealOps()
			o.SetCreateTemp(func(dir, pattern string) (TempFile, error) {
				f, err := real.createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingTemp{TempFile: f, failSync: true}, nil
			})
		}},
		{"chmod fails", func(o *Ops) {
			real := RealOps()
			o.SetCreateTemp(func(dir, pattern string) (TempFile, error) {
				f, err := real.createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingTemp{TempFile: f, failChmod: true}, nil
			})
		}},
		{"close fails", func(o *Ops) {
			real := RealOps()
			o.SetCreateTemp(func(dir, pattern string) (TempFile, error) {
				f, err := real.createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingTemp{TempFile: f, failClose: true}, nil
			})
		}},
		{"rename fails after a successful flush", func(o *Ops) {
			o.SetRename(func(string, string) error { return errors.New("cross-device link") })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, m := newStore(t)

			ops := RealOps()
			tc.break_(&ops)
			s.WithOps(ops)

			err := s.Put(m)
			if !errors.Is(err, fault.ErrIO) {
				t.Fatalf("Put = %v, want an i/o fault", err)
			}

			// The message must not have half-appeared.
			s.WithOps(RealOps())
			if ok, _ := s.HasMessage(m.ID()); ok {
				t.Error("a failed write left the message in place")
			}
			assertNoTempFiles(t, s.Root())
		})
	}
}

// assertNoTempFiles walks the store looking for anything left behind. A stray
// temporary file is not merely untidy: a later listing could mistake it for a
// message.
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

// TestASuccessfulWriteIsTheOnlyOneThatShows: the same harness with nothing
// broken must succeed, or the table above proves nothing.
func TestASuccessfulWriteIsTheOnlyOneThatShows(t *testing.T) {
	s, _, m := newStore(t)
	if err := s.Put(m); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ok, _ := s.HasMessage(m.ID()); !ok {
		t.Error("the message should be stored")
	}
	assertNoTempFiles(t, s.Root())
}

func TestJournalAppendFailuresAreReported(t *testing.T) {
	s, alice, m := newStore(t)
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}

	ops := RealOps()
	ops.SetOpenAppend(func(string) (*os.File, error) { return nil, errors.New("permission denied") })
	s.WithOps(ops)

	if _, err := s.Deliver(alice, m.ID()); !errors.Is(err, fault.ErrIO) {
		t.Fatalf("Deliver with an unwritable journal = %v, want an i/o fault", err)
	}

	// And the mailbox is unchanged: no half-delivery.
	s.WithOps(RealOps())
	st, err := s.Replay(alice)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if st.Len() != 0 {
		t.Errorf("a failed delivery left %d entries", st.Len())
	}
}

func TestReadFailuresAreClassified(t *testing.T) {
	s, alice, m := newStore(t)
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}

	ops := RealOps()
	ops.SetReadFile(func(string) ([]byte, error) { return nil, errors.New("input/output error") })
	s.WithOps(ops)

	if _, err := s.Get(m.ID()); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Get with a failing read = %v, want an i/o fault", err)
	}
	if _, err := s.Replay(alice); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Replay with a failing read = %v, want an i/o fault", err)
	}
	// Authentication must still fail closed rather than propagating the i/o
	// error as something a caller might mistake for success.
	if err := s.Authenticate(alice, "irrelevant"); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("Authenticate with a failing read = %v, want an auth fault", err)
	}
}

func TestListFailuresAreReported(t *testing.T) {
	s, _, m := newStore(t)

	ops := RealOps()
	ops.SetReadDir(func(string) ([]fs.DirEntry, error) { return nil, errors.New("permission denied") })
	s.WithOps(ops)

	if _, err := s.Users(); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Users with a failing list = %v, want an i/o fault", err)
	}
	if _, err := s.Receipts(m.ID()); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Receipts with a failing list = %v, want an i/o fault", err)
	}
	if _, err := s.Convos(); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Convos with a failing list = %v, want an i/o fault", err)
	}
}

// TestStatFailuresDoNotBecomeAbsence: a stat that fails for a reason other than
// "not there" must not be read as "not there", or a write-once guard would let
// a message be overwritten.
func TestStatFailuresDoNotBecomeAbsence(t *testing.T) {
	s, alice, m := newStore(t)

	ops := RealOps()
	ops.SetStat(func(string) (fs.FileInfo, error) { return nil, errors.New("input/output error") })
	s.WithOps(ops)

	if _, err := s.HasUser(alice); !errors.Is(err, fault.ErrIO) {
		t.Errorf("HasUser with a failing stat = %v, want an i/o fault", err)
	}
	if _, err := s.HasMessage(m.ID()); !errors.Is(err, fault.ErrIO) {
		t.Errorf("HasMessage with a failing stat = %v, want an i/o fault", err)
	}
	if err := s.Put(m); !errors.Is(err, fault.ErrIO) {
		t.Errorf("Put with a failing stat = %v, want an i/o fault", err)
	}
}
