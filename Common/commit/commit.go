// Package commit replaces a file's contents atomically.
//
// The sequence is the same one every Orc tool needs and none of them should
// write twice: re-read and re-hash the target so an edit computed against stale
// content is refused rather than applied over someone else's work; write the new
// content to a temporary file in the same directory; flush it; give it the
// original's permissions; rename it into place; and flush the directory so the
// rename survives a crash. Any failure removes the temporary file and leaves the
// original untouched.
//
// It is only the file replacement. Deciding *what* to write — Anno's splice
// planning, Dock's, a store's serialisation — stays with the tool that knows
// about the content. That split is what keeps the shared part small and
// finished: three tools genuinely do this identically, and the thing they do
// identically is thirty lines of ordering that is easy to get subtly wrong.
package commit

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"

	"orc/common/fault"
)

// TempFile is the part of *os.File that committing a write depends on.
type TempFile interface {
	io.Writer
	Name() string
	Sync() error
	Chmod(os.FileMode) error
	Close() error
}

// Ops is the set of filesystem operations Replace performs.
//
// The fields are exported because they are a test seam, and the tests that
// matter live in the tools rather than here: bundling the operations makes
// every failure path reachable, and each one has to prove that the original
// file is untouched and no debris is left. That is a claim worth proving rather
// than assuming, and it cannot be proven from outside the module unless the
// seam is reachable from outside the module.
type Ops struct {
	Stat       func(string) (os.FileInfo, error)
	ReadFile   func(string) ([]byte, error)
	CreateTemp func(dir, pattern string) (TempFile, error)
	Rename     func(from, to string) error
	Remove     func(string) error
	SyncDir    func(string)
}

// Real performs the operations against the actual filesystem.
func Real() Ops {
	return Ops{
		Stat:     os.Stat,
		ReadFile: os.ReadFile,
		CreateTemp: func(dir, pattern string) (TempFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		Rename: os.Rename,
		Remove: os.Remove,
		SyncDir: func(dir string) {
			// A rename is only durable once the directory entry is flushed. A
			// directory that cannot be opened for this is not a reason to fail a
			// write that has already succeeded.
			d, err := os.Open(dir)
			if err != nil {
				return
			}
			_ = d.Sync()
			_ = d.Close()
		},
	}
}

// Request is one file replacement.
type Request struct {
	// Path is the file to replace. It must already exist: its permissions are
	// carried onto the replacement, and a missing file is a caller's mistake
	// rather than a case to invent a mode for.
	Path string
	// Content is what the file will hold.
	Content []byte
	// Expect is the SHA-256 the file must currently have. A nil Expect skips
	// the staleness check, which is right for a caller that has just written
	// the content it is about to replace and wrong for almost anyone else.
	Expect *[32]byte
	// Tag names the writing tool in the temporary file's name, so debris left
	// by a crash is attributable. Empty means "orc".
	Tag string
	// Where names the caller in an Internal fault. Empty means "commit.Replace".
	Where string
}

func (r Request) tag() string {
	if r.Tag == "" {
		return "orc"
	}
	return r.Tag
}

func (r Request) where() string {
	if r.Where == "" {
		return "commit.Replace"
	}
	return r.Where
}

// Replace writes the request to disk through the real filesystem.
func Replace(r Request) error { return ReplaceWith(r, Real()) }

// ReplaceWith writes the request through the given operations.
func ReplaceWith(r Request, fs Ops) error {
	if r.Path == "" {
		return fault.Internal{Where: r.where(), Detail: "request has no path"}
	}

	info, err := fs.Stat(r.Path)
	if err != nil {
		return fault.IO{Op: "stat", Path: r.Path, Err: err}
	}
	current, err := fs.ReadFile(r.Path)
	if err != nil {
		return fault.IO{Op: "read", Path: r.Path, Err: err}
	}
	if r.Expect != nil && sha256.Sum256(current) != *r.Expect {
		return fault.Conflict{Path: r.Path, Reason: "file changed on disk since it was read; nothing was written"}
	}

	dir := filepath.Dir(r.Path)
	tmp, err := fs.CreateTemp(dir, "."+filepath.Base(r.Path)+"."+r.tag()+"-*")
	if err != nil {
		return fault.IO{Op: "create temporary file beside", Path: r.Path, Err: err}
	}
	tmpName := tmp.Name()

	// From here on every exit removes the temporary file.
	abandon := func(op string, cause error) error {
		_ = tmp.Close()
		_ = fs.Remove(tmpName)
		return fault.IO{Op: op, Path: r.Path, Err: cause}
	}

	if _, err := tmp.Write(r.Content); err != nil {
		return abandon("write", err)
	}
	if err := tmp.Sync(); err != nil {
		return abandon("flush", err)
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return abandon("set permissions on", err)
	}
	if err := tmp.Close(); err != nil {
		_ = fs.Remove(tmpName)
		return fault.IO{Op: "close", Path: r.Path, Err: err}
	}
	if err := fs.Rename(tmpName, r.Path); err != nil {
		_ = fs.Remove(tmpName)
		return fault.IO{Op: "replace", Path: r.Path, Err: err}
	}

	fs.SyncDir(dir)
	return nil
}
