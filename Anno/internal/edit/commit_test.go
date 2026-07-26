package edit_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"orc/anno/internal/edit"
	"orc/common/fault"
	"orc/common/source"
)

const before = "// @:> section s\nold\n"

// staged writes a file and prepares a plan against it.
func staged(t *testing.T) (string, edit.Plan) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, mustPrepFile(t, f, "@s", "new\n")
}

// intact asserts that a failed commit changed nothing and left no debris.
func intact(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the original file is gone: %v", err)
	}
	if string(got) != before {
		t.Errorf("the original file was modified: %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("debris left behind: %v", names)
	}
}

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

// TestCommitFailurePathsLeaveTheOriginalAlone drives every step of the commit
// sequence into failure and asserts the same thing each time: the file on disk
// is untouched and no temporary file survives.
func TestCommitFailurePathsLeaveTheOriginalAlone(t *testing.T) {
	boom := errors.New("injected failure")

	for _, step := range []string{"write", "sync", "chmod", "close"} {
		t.Run(step, func(t *testing.T) {
			path, plan := staged(t)
			fs := edit.FakeOps()
			fs.SetCreateTemp(func(dir, pattern string) (edit.TempFile, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &brokenFile{real: f, failOn: step, err: boom}, nil
			})

			err := edit.CommitWith(plan, fs)
			if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
				t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
			}
			intact(t, path)
		})
	}

	t.Run("rename", func(t *testing.T) {
		path, plan := staged(t)
		fs := edit.FakeOps()
		fs.SetRename(func(string, string) error { return boom })

		err := edit.CommitWith(plan, fs)
		if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
			t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
		}
		intact(t, path)
	})

	t.Run("create temporary file", func(t *testing.T) {
		path, plan := staged(t)
		fs := edit.FakeOps()
		fs.SetCreateTemp(func(string, string) (edit.TempFile, error) { return nil, boom })

		err := edit.CommitWith(plan, fs)
		if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
			t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
		}
		intact(t, path)
	})
}

func TestCommitSucceedsThroughTheSeam(t *testing.T) {
	path, plan := staged(t)
	if err := edit.CommitWith(plan, edit.FakeOps()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "// @:> section s\nnew\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestCommitReportsAFailedReread(t *testing.T) {
	path, plan := staged(t)
	boom := errors.New("cannot re-read")
	fs := edit.FakeOps()
	fs.SetReadFile(func(string) ([]byte, error) { return nil, boom })

	err := edit.CommitWith(plan, fs)
	if !errors.Is(err, fault.ErrIO) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, want an i/o fault wrapping the injected one", err)
	}
	intact(t, path)
}

func TestSyncDirIgnoresAnUnopenableDirectory(t *testing.T) {
	// A completed rename is not undone because the directory cannot be flushed;
	// the step simply gives up.
	edit.FakeOps().SyncDir(filepath.Join(t.TempDir(), "no-such-directory"))
}
