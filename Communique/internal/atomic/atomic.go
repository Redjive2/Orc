// Package atomic writes files that are never observed half-written.
//
// This is Anno's commit sequence, unchanged because the reasoning did not
// change: write a temporary file in the same directory, flush it, set its mode,
// rename it over the target, then flush the directory so the rename survives a
// crash. Every failure path removes the temporary file and leaves the original
// exactly as it was.
//
// The sequence matters more here than it did in Anno. cq's store is read by one
// process while another writes it — `cq admin operator set` runs beside a live
// `cq serve` — and a rename is the only way to hand a reader a whole new file
// rather than a partial one.
package atomic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"orc/cq/internal/fault"
)

// MaxReadBytes caps what ReadFile will load. Store records are small; anything
// larger means something is wrong, and saying so beats allocating until the
// machine gives up.
const MaxReadBytes = 64 << 20

// tempFile is the part of *os.File the commit sequence uses.
type tempFile interface {
	io.Writer
	Name() string
	Sync() error
	Chmod(fs.FileMode) error
	Close() error
}

// ops is the set of filesystem operations the commit sequence performs.
// Bundling them makes each failure path reachable in a test, which is what turns
// "every failure leaves the original intact" from a claim into a checked one.
type ops struct {
	createTemp func(dir, pattern string) (tempFile, error)
	rename     func(from, to string) error
	remove     func(string) error
}

// replace renames the temporary file over the target, retrying briefly.
//
// The retry is for Windows. Replacing a file there fails outright while
// anything else holds it open — a virus scanner reading it, the search indexer,
// an editor that has not let go — and every one of those lasts milliseconds and
// then stops. Losing a write because a scanner happened to be reading the file
// is a failure about nothing, and one the operator has no way to act on.
//
// It is short and it is bounded: a file that is really held is reported rather
// than waited out. Unix reaches the rename once and never comes back here, so
// this costs nothing where it is not needed.
func replace(fs_ ops, from, to string) error {
	var err error
	for attempt := range renameAttempts {
		if err = fs_.rename(from, to); err == nil {
			return nil
		}
		if attempt < renameAttempts-1 {
			time.Sleep(renamePause)
		}
	}
	return err
}

const (
	renameAttempts = 4
	renamePause    = 20 * time.Millisecond
)

func realOps() ops {
	return ops{
		createTemp: func(dir, pattern string) (tempFile, error) { return os.CreateTemp(dir, pattern) },
		rename:     os.Rename,
		remove:     os.Remove,
	}
}

// WriteFile replaces path's contents atomically.
//
// A reader either sees the previous file or the new one, never a mixture and
// never an empty file. If the target does not exist it is created.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	return writeFile(path, data, perm, realOps())
}

func writeFile(path string, data []byte, perm fs.FileMode, fs_ ops) error {
	if path == "" {
		return fault.Internal{Where: "atomic.WriteFile", Detail: "empty path"}
	}

	dir := filepath.Dir(path)
	tmp, err := fs_.createTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fault.IO{Op: "create a temporary file beside", Subject: path, Err: err}
	}
	name := tmp.Name()

	// From here every exit removes the temporary file.
	abandon := func(op string, cause error) error {
		_ = tmp.Close()
		_ = fs_.remove(name)
		return fault.IO{Op: op, Subject: path, Err: cause}
	}

	if _, err := tmp.Write(data); err != nil {
		return abandon("write", err)
	}
	if err := tmp.Sync(); err != nil {
		return abandon("flush", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return abandon("set permissions on", err)
	}
	if err := tmp.Close(); err != nil {
		_ = fs_.remove(name)
		return fault.IO{Op: "close", Subject: path, Err: err}
	}
	if err := replace(fs_, name, path); err != nil {
		_ = fs_.remove(name)
		return fault.IO{Op: "replace", Subject: path, Err: err}
	}

	syncDir(dir)
	return nil
}

// WriteJSON marshals v and writes it atomically, with a trailing newline so the
// store stays readable with ordinary tools.
func WriteJSON(path string, v any, perm fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.IO{Op: "encode", Subject: path, Err: err}
	}
	return WriteFile(path, append(data, '\n'), perm)
}

// CreateJSON writes v only if path does not exist, and reports a conflict if it
// does. The exclusivity is the filesystem's, not a read-then-write that another
// process can interleave.
func CreateJSON(path string, v any, perm fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.IO{Op: "encode", Subject: path, Err: err}
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if os.IsExist(err) {
			return fault.Conflict{Subject: path, Reason: "already exists"}
		}
		return fault.IO{Op: "create", Subject: path, Err: err}
	}

	abandon := func(op string, cause error) error {
		_ = f.Close()
		_ = os.Remove(path)
		return fault.IO{Op: op, Subject: path, Err: cause}
	}
	if _, err := f.Write(data); err != nil {
		return abandon("write", err)
	}
	if err := f.Sync(); err != nil {
		return abandon("flush", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fault.IO{Op: "close", Subject: path, Err: err}
	}

	syncDir(filepath.Dir(path))
	return nil
}

// ReadFile loads path, refusing anything implausibly large.
//
// The file type is checked *before* the open, not after. Opening a FIFO blocks
// until a writer appears, so a store directory holding one would hang cq rather
// than produce the error the check is there to produce.
func ReadFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fault.NotFound{What: "file", Name: path}
		}
		return nil, fault.IO{Op: "stat", Subject: path, Err: err}
	}
	if !info.Mode().IsRegular() {
		return nil, fault.IO{Op: "read", Subject: path,
			Err: fmt.Errorf("not a regular file (%s)", info.Mode().Type())}
	}
	if info.Size() > MaxReadBytes {
		return nil, fault.IO{Op: "read", Subject: path,
			Err: fmt.Errorf("file is %d bytes, limit is %d", info.Size(), MaxReadBytes)}
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fault.NotFound{What: "file", Name: path}
		}
		return nil, fault.IO{Op: "open", Subject: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, MaxReadBytes+1))
	if err != nil {
		return nil, fault.IO{Op: "read", Subject: path, Err: err}
	}
	if len(data) > MaxReadBytes {
		return nil, fault.IO{Op: "read", Subject: path,
			Err: fmt.Errorf("file grew past the %d byte limit while reading", MaxReadBytes)}
	}
	return data, nil
}

// ReadJSON loads and decodes path. Unknown fields are refused for the same
// reason the wire protocol refuses them: a field this build does not understand
// means the file was written by one that disagrees, and guessing is how a store
// silently loses state.
func ReadJSON(path string, v any) error {
	data, err := ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fault.Parse{Where: path, Reason: err.Error()}
	}
	if dec.More() {
		return fault.Parse{Where: path, Reason: "file carries more than one JSON document"}
	}
	return nil
}

// MkdirAll creates a directory tree with the given mode, reporting failures in
// cq's vocabulary.
func MkdirAll(path string, perm fs.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return fault.IO{Op: "create directory", Subject: path, Err: err}
	}
	return nil
}

// Remove deletes path, treating an already-absent file as success: the caller
// wanted it gone, and it is.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove", Subject: path, Err: err}
	}
	return nil
}

// syncDir flushes a directory entry so a rename survives a crash. A directory
// that cannot be opened for this is not a reason to fail a write that has
// already succeeded.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
