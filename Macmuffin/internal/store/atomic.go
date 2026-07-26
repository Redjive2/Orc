package store

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"orc/common/fault"
)

// Permissions. The store holds drafts nobody else should see and a journal that
// decides who may edit what, so nothing in it is readable by anyone but its
// owner. This is stated once, here, and applied everywhere.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// tempFile is the part of *os.File that an atomic write depends on.
type tempFile interface {
	io.Writer
	Name() string
	Sync() error
	Chmod(fs.FileMode) error
	Close() error
}

// ops is the set of filesystem operations the store performs.
//
// Bundling them makes every failure path reachable from a test: a full disk, a
// read-only store, a rename that fails after a successful fsync. Each of those
// must leave the store exactly as it was, and that is a claim worth proving
// rather than assuming. The pattern is Anno's, in edit.commit.
type ops struct {
	stat       func(string) (fs.FileInfo, error)
	readFile   func(string) ([]byte, error)
	readDir    func(string) ([]fs.DirEntry, error)
	mkdirAll   func(string, fs.FileMode) error
	createTemp func(dir, pattern string) (tempFile, error)
	openAppend func(string) (*os.File, error)
	rename     func(from, to string) error
	remove     func(string) error
	removeAll  func(string) error
	syncDir    func(string)
}

func realOps() ops {
	return ops{
		stat:     func(p string) (fs.FileInfo, error) { return os.Stat(p) },
		readFile: os.ReadFile,
		readDir:  func(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) },
		mkdirAll: os.MkdirAll,
		createTemp: func(dir, pattern string) (tempFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		openAppend: func(p string) (*os.File, error) {
			return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode)
		},
		rename:    os.Rename,
		remove:    os.Remove,
		removeAll: os.RemoveAll,
		syncDir: func(dir string) {
			// A rename is only durable once the directory entry is flushed. A
			// directory that cannot be opened for this is not a reason to fail a
			// write that has already succeeded, so the errors are deliberately
			// discarded here and nowhere else.
			d, err := os.Open(dir)
			if err != nil {
				return
			}
			_, _ = d.Stat()
			_ = d.Sync()
			_ = d.Close()
		},
	}
}

// writeFile replaces path atomically: a temporary file in the same directory,
// flushed, permissioned, and renamed into place, then the directory flushed so
// the rename survives a crash. Any failure removes the temporary file and
// leaves the original untouched.
//
// Every mutable file in the store goes through here. Nothing is ever written by
// opening the real path for truncation, because a process killed midway through
// that leaves a file that is neither the old content nor the new.
func (s *Store) writeFile(path string, data []byte) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := s.ops.mkdirAll(dir, dirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}

	tmp, err := s.ops.createTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fault.IO{Op: "create a temporary file beside", Path: path, Err: err}
	}
	name := tmp.Name()

	// From here every exit removes the temporary file.
	abandon := func(op string, cause error) error {
		_ = tmp.Close()
		_ = s.ops.remove(name)
		return fault.IO{Op: op, Path: path, Err: cause}
	}

	if _, err := tmp.Write(data); err != nil {
		return abandon("write", err)
	}
	if err := tmp.Sync(); err != nil {
		return abandon("flush", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		return abandon("set permissions on", err)
	}
	if err := tmp.Close(); err != nil {
		_ = s.ops.remove(name)
		return fault.IO{Op: "close", Path: path, Err: err}
	}
	if err := s.ops.rename(name, path); err != nil {
		_ = s.ops.remove(name)
		return fault.IO{Op: "replace", Path: path, Err: err}
	}

	s.ops.syncDir(dir)
	return nil
}

// writeNew writes a file that must not already exist.
//
// A creation record is write-once, and that is how task names are kept unique:
// two agents running `create` on the same name at the same instant must not
// both succeed. The existence check and the rename are not atomic together, so
// the check is a courtesy that produces a good error; O_EXCL in newFile is what
// actually decides the race.
func (s *Store) writeNew(path string, data []byte) error {
	if _, err := s.ops.stat(path); err == nil {
		return fault.Conflict{Path: path, Reason: "already exists; mailman never rewrites a stored message"}
	} else if !os.IsNotExist(err) {
		return fault.IO{Op: "check for", Path: path, Err: err}
	}
	return s.writeFile(path, data)
}

// readFile reads a file, mapping a missing one to a not-found fault so callers
// can distinguish it from a real i/o problem without inspecting errno.
func (s *Store) readFile(path string) ([]byte, error) {
	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fault.NotFound{Target: path}
		}
		return nil, fault.IO{Op: "read", Path: path, Err: err}
	}
	return data, nil
}

// appendLine adds one line to an append-only file.
//
// The lock is held for the whole operation, so two processes cannot interleave
// partial lines. O_APPEND alone would very nearly do, but "very nearly" is not
// a property to build a claim on.
func (s *Store) appendLine(path string, line []byte) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if len(line) == 0 {
		return fault.Internal{Where: "store.appendLine", Detail: "refusing to append an empty line to " + path}
	}
	for _, b := range line {
		if b == '\n' {
			return fault.Internal{Where: "store.appendLine", Detail: "journal line contains a newline"}
		}
	}
	if len(line)+1 > MaxJournalLine {
		return fault.Internal{Where: "store.appendLine",
			Detail: "journal line is longer than the limit"}
	}

	if err := s.ops.mkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}

	f, err := s.ops.openAppend(path)
	if err != nil {
		return fault.IO{Op: "open for appending", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fault.IO{Op: "append to", Path: path, Err: err}
	}
	// Flushed before returning: a command that reports success must have
	// survived a power cut, or an agent will act on mail that is not there.
	if err := f.Sync(); err != nil {
		return fault.IO{Op: "flush", Path: path, Err: err}
	}
	return nil
}
