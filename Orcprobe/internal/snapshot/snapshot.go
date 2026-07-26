// Package snapshot copies a directory tree, read-only at the source.
//
// Everything here opens the source with O_RDONLY and never anything else. That
// is the whole security argument of the tool restated as code: the only
// function in Orcprobe that touches a real path is Copy, and Copy cannot write
// through it.
//
// The copy is a plain streaming copy. APFS cloning (clonefile) would make a
// large mail store snapshot in milliseconds and cost no disk until it diverged,
// and the plan calls for it — but it needs a raw darwin syscall, so it is
// deliberately deferred to the milestone that needs it (checkpoints), rather
// than smuggled in here where a mistake would corrupt the one copy that matters.
//
// Two properties of the stores being copied make a live copy safe, and both are
// inherited rather than assumed: journals are append-only, and messages are
// write-once. So a copy taken while agents are working can only ever catch a
// partial *final* journal line — which each tool already drops with a note — and
// never a half-written message. A live snapshot is therefore an earlier state,
// not a torn one.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orc/orcprobe/internal/fault"
)

// Permissions for what a probe creates. A probe holds copies of private mail
// and freshly minted keys, so nothing in it is readable by anyone but its
// owner — the same boundary Mailman's store rests on.
const (
	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
	ExecMode fs.FileMode = 0o700
)

// Limits. Each turns a hostile or broken source into a clear message rather
// than an out-of-memory kill or an infinite walk.
const (
	// MaxFiles bounds one copied tree. A mail store with millions of messages
	// is a real thing; ten million files is a loop.
	MaxFiles = 2_000_000
	// MaxDepth bounds nesting.
	MaxDepth = 64
)

// Drop is something the copy refused to bring across, and why. Every drop is
// reported to the caller so it can land in the probe's manifest: a file that
// silently did not come across is how a probe quietly stops resembling the
// world it was taken from.
type Drop struct {
	Rel string
	Why string
}

// Report is what one copied tree amounted to.
type Report struct {
	Files    int
	Dirs     int
	Symlinks int
	Bytes    int64
	// Digest is a content digest of the copy, used to answer "has the source
	// moved since this probe was taken" without copying it again.
	Digest string
	Drops  []Drop
}

// Options tune one copy.
type Options struct {
	// Exclude reports whether a path, relative to the source root and in slash
	// form, should be left out entirely.
	Exclude func(rel string) bool
}

// Copy duplicates the tree at src into dst, which must not already exist.
//
// Symlinks are the interesting case. One pointing inside the source tree is
// recreated, because it means the same thing in the copy. One pointing outside
// is dropped and recorded — following it would copy a piece of the real
// filesystem into a probe, and recreating it would leave a probe with a live
// pointer at the real world. Both are exactly the failure this tool exists to
// prevent, so neither is a judgement call made quietly.
//
// Anything that is not a file, directory, or symlink — a socket, a device, a
// fifo — is dropped and recorded. None of them can occur in a store any Orc
// tool writes, so finding one means the source is not what it claims to be.
func Copy(dst, src string, opt Options) (Report, error) {
	var rep Report

	srcInfo, err := os.Stat(src)
	if err != nil {
		return rep, fault.IO{Op: "look at", Path: src, Err: err}
	}
	if !srcInfo.IsDir() {
		return rep, fault.Conflict{Path: src, Reason: "is not a directory"}
	}
	if _, err := os.Stat(dst); err == nil {
		return rep, fault.Conflict{Path: dst, Reason: "already exists; orcprobe never copies onto an existing tree"}
	} else if !os.IsNotExist(err) {
		return rep, fault.IO{Op: "check for", Path: dst, Err: err}
	}

	digest := sha256.New()
	if err := os.MkdirAll(dst, DirMode); err != nil {
		return rep, fault.IO{Op: "create", Path: dst, Err: err}
	}

	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fault.IO{Op: "walk", Path: path, Err: err}
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return fault.IO{Op: "locate", Path: path, Err: relErr}
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)

		if opt.Exclude != nil && opt.Exclude(slash) {
			rep.Drops = append(rep.Drops, Drop{Rel: slash, Why: "excluded"})
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if depth := strings.Count(slash, "/") + 1; depth > MaxDepth {
			return fault.Parse{Path: path, Reason: fmt.Sprintf("nested deeper than %d directories", MaxDepth)}
		}
		if rep.Files+rep.Dirs > MaxFiles {
			return fault.Parse{Path: src, Reason: fmt.Sprintf("holds more than %d entries", MaxFiles)}
		}

		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, DirMode); err != nil {
				return fault.IO{Op: "create", Path: target, Err: err}
			}
			rep.Dirs++
			mix(digest, "dir", slash, 0)
			return nil

		case d.Type()&fs.ModeSymlink != 0:
			return copySymlink(&rep, digest, src, path, target, slash)

		case d.Type().IsRegular():
			n, err := copyFile(target, path)
			if err != nil {
				return err
			}
			rep.Files++
			rep.Bytes += n
			mix(digest, "file", slash, n)
			if err := mixContent(digest, target); err != nil {
				return err
			}
			return nil

		default:
			rep.Drops = append(rep.Drops, Drop{Rel: slash, Why: "not a file, directory, or symlink"})
			return nil
		}
	})
	if err != nil {
		// A failed copy leaves nothing behind: a half-copied store that looks
		// like a probe is worse than no probe at all.
		_ = os.RemoveAll(dst)
		return Report{}, err
	}

	sort.Slice(rep.Drops, func(i, j int) bool { return rep.Drops[i].Rel < rep.Drops[j].Rel })
	rep.Digest = hex.EncodeToString(digest.Sum(nil))
	return rep, nil
}

// copySymlink recreates a link that stays inside the tree and drops one that
// does not. See Copy's comment for why this is not a judgement call.
func copySymlink(rep *Report, digest io.Writer, src, path, target, slash string) error {
	dest, err := os.Readlink(path)
	if err != nil {
		return fault.IO{Op: "read the link at", Path: path, Err: err}
	}

	absolute := dest
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(filepath.Dir(path), dest)
	}
	if !inside(src, absolute) {
		rep.Drops = append(rep.Drops, Drop{Rel: slash, Why: "symlink to " + dest + ", outside the copied tree"})
		return nil
	}
	if err := os.Symlink(dest, target); err != nil {
		return fault.IO{Op: "recreate the link at", Path: target, Err: err}
	}
	rep.Symlinks++
	mix(digest, "link:"+dest, slash, 0)
	return nil
}

// copyFile writes one regular file, source opened read-only.
func copyFile(dst, src string) (int64, error) {
	in, err := os.OpenFile(src, os.O_RDONLY, 0)
	if err != nil {
		return 0, fault.IO{Op: "read", Path: src, Err: err}
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return 0, fault.IO{Op: "look at", Path: src, Err: err}
	}

	// The mode is taken from the source, narrowed to the owner. A probe never
	// widens permissions: a store copied from a badly permissioned original
	// should not carry that mistake into a directory holding minted keys.
	mode := FileMode
	if info.Mode().Perm()&0o100 != 0 {
		mode = ExecMode
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, fault.IO{Op: "create", Path: dst, Err: err}
	}

	n, err := io.Copy(out, in)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return 0, fault.IO{Op: "copy into", Path: dst, Err: err}
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return 0, fault.IO{Op: "flush", Path: dst, Err: err}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return 0, fault.IO{Op: "close", Path: dst, Err: err}
	}
	return n, nil
}

// Digest computes a content digest of an existing tree, in the same form Copy
// produces, so a probe's record can be compared against the world it came from.
func Digest(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fault.IO{Op: "walk", Path: path, Err: err}
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fault.IO{Op: "locate", Path: path, Err: relErr}
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)

		switch {
		case d.IsDir():
			mix(h, "dir", slash, 0)
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			dest, err := os.Readlink(path)
			if err != nil {
				return fault.IO{Op: "read the link at", Path: path, Err: err}
			}
			absolute := dest
			if !filepath.IsAbs(absolute) {
				absolute = filepath.Join(filepath.Dir(path), dest)
			}
			if !inside(root, absolute) {
				return nil // dropped by Copy, so not part of the digest
			}
			mix(h, "link:"+dest, slash, 0)
			return nil
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return fault.IO{Op: "look at", Path: path, Err: err}
			}
			mix(h, "file", slash, info.Size())
			return mixContent(h, path)
		default:
			return nil
		}
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// mix folds one entry's identity into the digest. The record is
// length-prefixed so two different trees cannot produce the same stream by
// arranging their names to run together.
func mix(h io.Writer, kind, rel string, size int64) {
	// Writing to a hash cannot fail; the error is discarded here and nowhere
	// else, and only because sha256.Write is documented never to return one.
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%d\n", kind, len(rel), rel, size)
}

func mixContent(h io.Writer, path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return fault.IO{Op: "read", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(h, f); err != nil {
		return fault.IO{Op: "read", Path: path, Err: err}
	}
	return nil
}

func inside(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(path, root)
}
