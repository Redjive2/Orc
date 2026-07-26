package edit

import (
	"os"

	"orc/anno/internal/target"
	"orc/common/commit"
	"orc/common/source"
)

// Verify exposes the post-splice structural check to tests.
//
// Prepare's content rules make it very hard to reach this guard through the
// public API, which is the point: it is a backstop against a defect in those
// rules. Testing it directly is the only way to confirm the backstop works.
func Verify(f source.File, result []byte, m target.Match, steps []target.Step) error {
	return verify(f, result, m, steps)
}

// The commit sequence moved to common/commit, but the failure-injection tests
// stay here: they assert Anno's claim that a failed write leaves the original
// file untouched and no debris behind, and that claim is Anno's to make. These
// shims keep the seam's shape unchanged, so commit_test.go did not have to be
// rewritten to follow the code.

// TempFile is the file interface Commit writes through.
type TempFile = commit.TempFile

// Ops is a set of filesystem operations, defaulting to the real ones. It is a
// defined type rather than an alias so the setters below can be methods, which
// is what keeps commit_test.go unchanged.
type Ops commit.Ops

// FakeOps builds an operation set that behaves normally until overridden.
func FakeOps() Ops { return Ops(commit.Real()) }

// SetCreateTemp replaces temporary-file creation.
func (o *Ops) SetCreateTemp(f func(dir, pattern string) (TempFile, error)) { o.CreateTemp = f }

// SetRename replaces the rename step.
func (o *Ops) SetRename(f func(from, to string) error) { o.Rename = f }

// SetStat replaces the stat step.
func (o *Ops) SetStat(f func(string) (os.FileInfo, error)) { o.Stat = f }

// SetReadFile replaces the re-read step.
func (o *Ops) SetReadFile(f func(string) ([]byte, error)) { o.ReadFile = f }

// CommitWith runs the commit sequence against the given operations.
//
// SyncDir needs no shim: it is a field of func type, so a test calls it
// directly as fs.SyncDir(dir), exactly as it did when the field was unexported
// and this file supplied the method.
func CommitWith(p Plan, o Ops) error { return commitWith(p, commit.Ops(o)) }
