package atomic_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
)

const original = "the original contents"

// brokenFile fails at whichever step is selected.
type brokenFile struct {
	real   *os.File
	failOn string
	err    error
}

func (b *brokenFile) Write(p []byte) (int, error) {
	if b.failOn == "write" {
		return 0, b.err
	}
	return b.real.Write(p)
}

func (b *brokenFile) Name() string { return b.real.Name() }

func (b *brokenFile) Sync() error {
	if b.failOn == "sync" {
		return b.err
	}
	return b.real.Sync()
}

func (b *brokenFile) Chmod(m os.FileMode) error {
	if b.failOn == "chmod" {
		return b.err
	}
	return b.real.Chmod(m)
}

func (b *brokenFile) Close() error {
	if b.failOn == "close" {
		_ = b.real.Close()
		return b.err
	}
	return b.real.Close()
}

// TestEveryCommitFailureLeavesTheOriginalIntact drives each step of the sequence
// into failure and asserts the same two things every time: the file on disk is
// unchanged, and no temporary file survives.
func TestEveryCommitFailureLeavesTheOriginalIntact(t *testing.T) {
	boom := errors.New("injected failure")

	for _, step := range []string{"write", "sync", "chmod", "close"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f")
			if err := atomic.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}

			ops := atomic.FakeOps()
			ops.SetCreateTemp(func(d, pattern string) (atomic.TempFile, error) {
				f, err := os.CreateTemp(d, pattern)
				if err != nil {
					return nil, err
				}
				return &brokenFile{real: f, failOn: step, err: boom}, nil
			})

			err := atomic.WriteFileWith(path, []byte("replacement"), 0o600, ops)
			if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
				t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
			}
			assertIntact(t, dir, path)
		})
	}

	t.Run("rename", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "f")
		if err := atomic.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		ops := atomic.FakeOps()
		ops.SetRename(func(string, string) error { return boom })

		err := atomic.WriteFileWith(path, []byte("replacement"), 0o600, ops)
		if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
			t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
		}
		assertIntact(t, dir, path)
	})

	t.Run("create", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "f")
		if err := atomic.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		ops := atomic.FakeOps()
		ops.SetCreateTemp(func(string, string) (atomic.TempFile, error) { return nil, boom })

		err := atomic.WriteFileWith(path, []byte("replacement"), 0o600, ops)
		if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
			t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
		}
		assertIntact(t, dir, path)
	})
}

func assertIntact(t *testing.T, dir, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the original is gone: %v", err)
	}
	if string(got) != original {
		t.Errorf("the original was modified: %q", got)
	}
	assertNoDebris(t, dir, 1)
}
