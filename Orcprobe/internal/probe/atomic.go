package probe

import (
	"os"
	"path/filepath"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// writeFile replaces path atomically: a temporary file in the same directory,
// flushed, permissioned, and renamed into place, then the directory flushed so
// the rename survives a crash. Any failure removes the temporary file and
// leaves the original untouched.
//
// The sequence is Anno's commit and Mailman's writeFile, followed exactly.
// Nothing in a probe is ever written by opening the real path for truncation:
// a process killed midway through that leaves a file which is neither the old
// content nor the new, and probe.json in that state would make a probe that no
// command will open and nothing will clean up.
func writeFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, snapshot.DirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fault.IO{Op: "create a temporary file beside", Path: path, Err: err}
	}
	name := tmp.Name()

	// From here every exit removes the temporary file.
	abandon := func(op string, cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fault.IO{Op: op, Path: path, Err: cause}
	}

	if _, err := tmp.Write(data); err != nil {
		return abandon("write", err)
	}
	if err := tmp.Sync(); err != nil {
		return abandon("flush", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return abandon("set permissions on", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fault.IO{Op: "close", Path: path, Err: err}
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fault.IO{Op: "replace", Path: path, Err: err}
	}

	syncDir(dir)
	return nil
}

// writeNew writes a file that must not already exist. probe.json is the case
// that matters: a rename onto an existing name is the uniqueness check for a
// probe's identity, enforced by the filesystem rather than by a read-then-write
// another process can interleave.
func writeNew(path string, data []byte, mode os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		return fault.Conflict{Path: path, Reason: "already exists"}
	} else if !os.IsNotExist(err) {
		return fault.IO{Op: "check for", Path: path, Err: err}
	}
	return writeFile(path, data, mode)
}

// appendLine adds one line to an append-only file, flushed before returning.
func appendLine(path string, line []byte) error {
	for _, b := range line {
		if b == '\n' {
			return fault.Internal{Where: "probe.appendLine", Detail: "line contains a newline"}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), snapshot.DirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, snapshot.FileMode)
	if err != nil {
		return fault.IO{Op: "open for appending", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fault.IO{Op: "append to", Path: path, Err: err}
	}
	if err := f.Sync(); err != nil {
		return fault.IO{Op: "flush", Path: path, Err: err}
	}
	return nil
}

// syncDir flushes a directory entry so a rename survives a crash. A directory
// that cannot be opened for this is not a reason to fail a write that already
// succeeded, so the errors are discarded here and nowhere else.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_, _ = d.Stat()
	_ = d.Sync()
	_ = d.Close()
}
