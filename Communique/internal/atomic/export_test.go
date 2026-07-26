package atomic

import "io/fs"

// TempFile is the file interface the commit sequence writes through.
type TempFile = tempFile

// Ops is the set of filesystem operations the commit sequence performs.
type Ops = ops

// FakeOps returns operations that behave normally until overridden.
func FakeOps() Ops { return realOps() }

// SetCreateTemp replaces temporary-file creation.
func (o *Ops) SetCreateTemp(f func(dir, pattern string) (TempFile, error)) { o.createTemp = f }

// SetRename replaces the rename step.
func (o *Ops) SetRename(f func(from, to string) error) { o.rename = f }

// WriteFileWith runs the commit sequence against the given operations.
func WriteFileWith(path string, data []byte, perm fs.FileMode, o Ops) error {
	return writeFile(path, data, perm, o)
}
