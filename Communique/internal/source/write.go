package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// Applying an edit made from the mirror.
//
// This is the one place cq writes something other than its own state, and the
// only place it writes the operator's files. Three rules hold it, and each
// exists because the alternative loses work that cannot be got back:
//
//  1. **Inside the checkout, always.** A path is resolved against the mirrored
//     root and refused if it lands anywhere else — after following symlinks, not
//     before, because a link is exactly how a path that looks contained stops
//     being contained.
//  2. **Only what was expected.** Every verb but a create carries the digest of
//     what the operator was editing, and the file must still match it. A
//     snapshot is minutes old by the time somebody acts on it; without this, a
//     write from a phone silently discards whatever an agent did in between.
//  3. **The write itself is atomic.** A temporary file beside the original, then
//     a rename. A half-written source file is worse than an unwritten one.
//
// Together the second and third also make these self-guarding against being
// applied twice: after a successful write the file no longer matches Base, so a
// repeat refuses rather than overwriting.

// applyLibrary performs one library verb.
func (c *CLI) applyLibrary(action protocol.Action) error {
	if c.LibraryRoot == "" {
		return fault.Usage{Reason: "this machine mirrors no repository, so it has nothing to edit; set CQ_LIBRARY and sync"}
	}
	target, err := c.resolve(action.Args.Path)
	if err != nil {
		return err
	}

	switch action.Op {
	case protocol.OpWrite:
		if err := c.expect(target, action.Args.Base); err != nil {
			return err
		}
		return writeAtomic(target, action.Args.Text)

	case protocol.OpCreate:
		if _, err := os.Lstat(target); err == nil {
			return fault.Conflict{Reason: action.Args.Path + " already exists; edit it instead"}
		} else if !os.IsNotExist(err) {
			return fault.IO{Op: "check", Subject: action.Args.Path, Err: err}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fault.IO{Op: "create the directory for", Subject: action.Args.Path, Err: err}
		}
		return writeAtomic(target, action.Args.Text)

	case protocol.OpDelete:
		if err := c.expect(target, action.Args.Base); err != nil {
			return err
		}
		if err := os.Remove(target); err != nil {
			return fault.IO{Op: "delete", Subject: action.Args.Path, Err: err}
		}
		return nil

	case protocol.OpMakeDir:
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fault.IO{Op: "create", Subject: action.Args.Path, Err: err}
		}
		return nil

	case protocol.OpRemoveDir:
		// A directory, and nothing else. os.Remove takes files too, so without
		// this a `rmdir` aimed at a file would delete it while carrying no Base —
		// slipping past the one precondition that stops a stale mirror throwing
		// away work done since it was taken.
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) {
				return fault.Conflict{Reason: action.Args.Path + " is already gone"}
			}
			return fault.IO{Op: "check", Subject: action.Args.Path, Err: err}
		}
		if !info.IsDir() {
			return fault.Conflict{Reason: action.Args.Path +
				" is not a directory; delete it instead, which checks it still holds what you saw"}
		}

		// Empty only. A recursive delete is the one action nobody can undo and
		// nobody can preview from a phone, and `rmdir` refusing a full directory
		// is what makes "remove this folder" a safe thing to tap.
		if err := os.Remove(target); err != nil {
			if isNotEmpty(err) {
				return fault.Conflict{Reason: action.Args.Path +
					" is not empty; cq removes only empty directories, so delete what is in it first"}
			}
			return fault.IO{Op: "remove", Subject: action.Args.Path, Err: err}
		}
		return nil

	default:
		return fault.Internal{Where: "source.applyLibrary", Detail: "no handler for " + string(action.Op)}
	}
}

// resolve turns a relative path into an absolute one inside the checkout, or
// refuses.
//
// The containment check is made on the *resolved* path, after symlinks. A
// checked-then-followed path is the shape of every escape: `Docs/notes` may be a
// link to `/etc`, and a check made before following it would pass.
func (c *CLI) resolve(rel string) (string, error) {
	if rel == "" {
		return "", fault.Usage{Reason: "no path given"}
	}
	if filepath.IsAbs(rel) {
		return "", fault.Usage{Reason: "a path may not be absolute"}
	}

	root, err := filepath.EvalSymlinks(c.LibraryRoot)
	if err != nil {
		return "", fault.IO{Op: "resolve the checkout at", Subject: c.LibraryRoot, Err: err}
	}
	target := filepath.Join(root, filepath.FromSlash(rel))

	// The target may not exist yet — a create, a mkdir — so the nearest existing
	// ancestor is what gets resolved, and the rest is checked lexically against
	// it. `..` in the tail cannot escape, because Join has already collapsed it.
	probe := target
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			probe = resolved
			break
		}
		if !os.IsNotExist(err) {
			return "", fault.IO{Op: "resolve", Subject: rel, Err: err}
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fault.Usage{Reason: "the path " + rel + " does not resolve inside the checkout"}
		}
		probe = parent
	}
	if !within(root, probe) || !within(root, target) {
		return "", fault.Usage{Reason: rel + " resolves outside the mirrored checkout, so cq will not touch it"}
	}
	return target, nil
}

// within reports whether path is root or is inside it.
func within(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// expect refuses unless the file still holds what the operator was editing.
func (c *CLI) expect(target, base string) error {
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return fault.Conflict{Reason: "the file is gone; it was deleted after this was queued"}
		}
		return fault.IO{Op: "read", Subject: target, Err: err}
	}
	got := Digest(string(data))
	if got != base {
		return fault.Conflict{Reason: "the file changed after this was queued, so the edit was not applied; " +
			"open it again and redo the change on what is there now"}
	}
	return nil
}

// Digest is the precondition an edit carries, and the same function the browser
// must use: SHA-256 of the exact bytes, in lower-case hex.
func Digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// writeAtomic replaces a file's contents without ever leaving a partial one.
//
// The sequence is the store's own, rather than a second copy of it here: a
// temporary file beside the original, flushed, renamed over it, and the
// directory flushed after. This is somebody's repository, so it deserves at
// least the care cq takes with its own state.
func writeAtomic(target, text string) error {
	// The original's permissions are kept when it had any: a file that became
	// 0600 because it was edited from a phone is a file somebody has to fix.
	perm := fs.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
	}
	return atomic.WriteFile(target, []byte(text), perm)
}

// isNotEmpty reports the error that means "this directory has things in it".
//
// The message is not matched, because it is the operating system's and differs
// between them. Nor is the errno: unix says ENOTEMPTY and Windows says
// ERROR_DIR_NOT_EMPTY, which is a different number under a different name.
//
// fs.ErrExist is what both are defined to match — the standard library maps
// each of them onto it — so it is the one test that is true on either. Asking
// for ENOTEMPTY by name compiles fine on Windows and is simply never true
// there, which would have turned a clear refusal into a raw i/o error.
func isNotEmpty(err error) bool {
	return errors.Is(err, fs.ErrExist)
}
