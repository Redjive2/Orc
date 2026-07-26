package store

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"orc/common/fault"
)

// Permissions. This store holds plaintext keys, so nothing in it is readable by
// anyone but its owner, and the mode is stated once here and applied everywhere.
// It is the boundary the whole permission model rests on — see Plan.md §7.5.
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
// the rename survives a crash. Any failure removes the temporary file and leaves
// the original untouched.
//
// Every mutable file in the store goes through here. Nothing is ever written by
// opening the real path for truncation, because a process killed midway through
// that leaves a file which is neither the old content nor the new — and for a
// key file, that is a credential nobody can authenticate with.
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

	// From here every exit removes the temporary file. A stray temp file holding
	// half a key is exactly what this store must not leave lying about.
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
	// Permissions are set before the rename, so the file is never briefly
	// readable at the temporary file's default mode under a name anything else
	// would look for.
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
// Every creation record goes through here, and the rename is what makes a name
// unique: two agents running `orc new identity` on the same name at the same
// instant must not both succeed. The existence check is a courtesy that produces
// a good error; the filesystem decides the race.
func (s *Store) writeNew(path string, data []byte) error {
	if _, err := s.ops.stat(path); err == nil {
		return fault.Conflict{Path: path, Reason: "already exists"}
	} else if !os.IsNotExist(err) {
		return fault.IO{Op: "check for", Path: path, Err: err}
	}
	return s.writeFile(path, data)
}

// readFile reads a file, mapping a missing one to a not-found fault so callers
// can tell it from a real i/o problem without inspecting errno.
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
// The lock is held for the whole operation by the caller, so two processes cannot
// interleave partial lines. O_APPEND alone would very nearly do, but "very
// nearly" is not a property to build a permission model on.
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
		return fault.Internal{Where: "store.appendLine", Detail: "journal line is longer than the limit"}
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
	// Flushed before returning: a command that reported success must have
	// survived a power cut, or an agent will act on an authority it does not have.
	if err := f.Sync(); err != nil {
		return fault.IO{Op: "flush", Path: path, Err: err}
	}
	return nil
}

// Small local helpers, so this package does not import strconv for two
// conversions and so their failure modes are the ones it wants.

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, fault.Parse{Reason: "empty number"}
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fault.Parse{Reason: "not a number"}
		}
		n = n*10 + int(c-'0')
		if n > 1<<40 {
			return 0, fault.Parse{Reason: "number is too large"}
		}
	}
	return n, nil
}

func quote(s string) string { return `"` + s + `"` }
