package edit_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/commit"
	"orc/common/fault"
	"orc/common/source"
	"orc/dock/internal/doc"
	"orc/dock/internal/edit"
	"orc/dock/internal/scan"
)

const before = "# §1 A\n\nold prose\n"

// planned stages a document and prepares a write against it.
func planned(t *testing.T) (string, edit.Plan) {
	t.Helper()
	path, f, d := staged(t, before)
	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "new prose\n")
	if err != nil {
		t.Fatal(err)
	}
	return path, plan
}

// intact asserts a failed commit changed nothing and left no debris.
func intact(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the original file is gone: %v", err)
	}
	if string(got) != before {
		t.Errorf("the original was modified: %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("debris left behind: %s", e.Name())
		}
	}
}

var boom = errors.New("boom")

// TestEveryFailurePathLeavesTheOriginalAlone is the claim a mutating command
// has to make, and it is worth proving rather than assuming.
func TestEveryFailurePathLeavesTheOriginalAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*commit.Ops)
		want   error
	}{
		{"stat", func(o *commit.Ops) {
			o.Stat = func(string) (os.FileInfo, error) { return nil, boom }
		}, fault.ErrIO},
		{"re-read", func(o *commit.Ops) {
			o.ReadFile = func(string) ([]byte, error) { return nil, boom }
		}, fault.ErrIO},
		{"create temp", func(o *commit.Ops) {
			o.CreateTemp = func(string, string) (commit.TempFile, error) { return nil, boom }
		}, fault.ErrIO},
		{"write", func(o *commit.Ops) {
			o.CreateTemp = wrapTemp(o.CreateTemp, func(f *fakeTemp) { f.writeErr = boom })
		}, fault.ErrIO},
		{"flush", func(o *commit.Ops) {
			o.CreateTemp = wrapTemp(o.CreateTemp, func(f *fakeTemp) { f.syncErr = boom })
		}, fault.ErrIO},
		{"chmod", func(o *commit.Ops) {
			o.CreateTemp = wrapTemp(o.CreateTemp, func(f *fakeTemp) { f.chmodErr = boom })
		}, fault.ErrIO},
		{"close", func(o *commit.Ops) {
			o.CreateTemp = wrapTemp(o.CreateTemp, func(f *fakeTemp) { f.closeErr = boom })
		}, fault.ErrIO},
		{"rename", func(o *commit.Ops) {
			o.Rename = func(string, string) error { return boom }
		}, fault.ErrIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, plan := planned(t)
			ops := commit.Real()
			tc.break_(&ops)

			err := edit.CommitWith(plan, ops)
			if err == nil {
				t.Fatal("the commit succeeded")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("error is not %v: %v", tc.want, err)
			}
			intact(t, path)
		})
	}
}

// TestAChangedFileIsRefused: an edit computed against stale content must not be
// applied over someone else's work.
func TestAChangedFileIsRefused(t *testing.T) {
	path, plan := planned(t)
	if err := os.WriteFile(path, []byte("# §1 A\n\nsomeone else wrote this\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := edit.CommitWith(plan, commit.Real())
	if err == nil {
		t.Fatal("the stale write was applied")
	}
	if !errors.Is(err, fault.ErrConflict) {
		t.Errorf("not a conflict: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "someone else") {
		t.Errorf("the other writer's work was overwritten: %q", got)
	}
}

func TestCommitSucceedsAndPreservesMode(t *testing.T) {
	path, f, d := staged(t, before)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Skipf("cannot set a mode here: %v", err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err = doc.Build(path, scan.Scan(string(f.Bytes())))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := edit.Prepare(f, d, section(t, d, "1"), false, "new prose\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.Commit(plan); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The own span is every line after the heading, the blank one included, so
	// replacing it with content that has no leading blank removes that blank.
	// That is the caller's choice and not a surprise: read returned the blank
	// too, which is why the round trip still holds.
	if string(got) != "# §1 A\nnew prose\n" {
		t.Errorf("wrote %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("mode is %04o, want 0640 — the original's permissions were not carried over", perm)
	}
}

func TestCommitRefusesAnEmptyPlan(t *testing.T) {
	err := edit.CommitWith(edit.Plan{}, commit.Real())
	if err == nil || !errors.Is(err, fault.ErrInternal) {
		t.Errorf("an empty plan was accepted or misclassified: %v", err)
	}
}

// fakeTemp fails at a chosen step of the write sequence.
type fakeTemp struct {
	inner    commit.TempFile
	writeErr error
	syncErr  error
	chmodErr error
	closeErr error
}

func (f *fakeTemp) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.inner.Write(p)
}
func (f *fakeTemp) Name() string { return f.inner.Name() }
func (f *fakeTemp) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.inner.Sync()
}
func (f *fakeTemp) Chmod(m fs.FileMode) error {
	if f.chmodErr != nil {
		return f.chmodErr
	}
	return f.inner.Chmod(m)
}
func (f *fakeTemp) Close() error {
	if f.closeErr != nil {
		// The real file still has to be closed, or the temp cannot be removed
		// on windows and the debris check would fail for the wrong reason.
		_ = f.inner.Close()
		return f.closeErr
	}
	return f.inner.Close()
}

// wrapTemp decorates the real temp-file creation with a chosen failure.
func wrapTemp(real func(string, string) (commit.TempFile, error), set func(*fakeTemp)) func(string, string) (commit.TempFile, error) {
	return func(dir, pattern string) (commit.TempFile, error) {
		inner, err := real(dir, pattern)
		if err != nil {
			return nil, err
		}
		f := &fakeTemp{inner: inner}
		set(f)
		return f, nil
	}
}
