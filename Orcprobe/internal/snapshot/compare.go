package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orc/orcprobe/internal/fault"
)

// Change is one difference between two trees.
type Change struct {
	// Rel is the path, relative to each tree's root, in slash form.
	Rel string
	// Kind is what happened, phrased from the left tree to the right one.
	Kind Kind
	// Bytes is the size change, right minus left.
	Bytes int64
}

// Kind is how a path differs.
type Kind string

// The three differences. There is deliberately no "moved": a move is a removal
// and an addition, and pretending to detect one would mean guessing at intent
// from two files that happen to share content.
const (
	Added   Kind = "added"
	Removed Kind = "removed"
	Changed Kind = "changed"
)

// Diff is the whole comparison.
type Diff struct {
	Changes []Change
	// Same counts paths that are identical in both trees, so a report can say
	// how much did *not* change — the number that turns "12 changes" into
	// either "barely touched" or "rewritten".
	Same int
	// Truncated is how many changes were dropped from Changes because the
	// listing hit its limit. It is reported rather than silently dropped: a
	// diff that quietly shows the first hundred differences reads as a diff
	// that found a hundred.
	Truncated int
}

// Empty reports whether the trees are identical.
func (d Diff) Empty() bool { return len(d.Changes) == 0 && d.Truncated == 0 }

// Count returns how many differences there were in total, listed or not.
func (d Diff) Count() int { return len(d.Changes) + d.Truncated }

// MaxChanges bounds a listing. A probe compared against a world that has moved
// on can differ in every file, and a table with a hundred thousand rows in it
// is not an answer.
const MaxChanges = 500

// Compare walks two trees and reports how they differ.
//
// Content is compared by digest rather than by size and modification time. Two
// copies of a store made a second apart have different timestamps and identical
// contents, and it is the contents that matter — a diff that reported every
// file as changed because the copy is newer would be worse than no diff.
//
// A tree that does not exist compares as empty rather than as an error, so a
// probe taken before some tool existed can still be diffed against one taken
// after.
func Compare(left, right string) (Diff, error) {
	var out Diff

	leftFiles, err := fingerprint(left)
	if err != nil {
		return out, err
	}
	rightFiles, err := fingerprint(right)
	if err != nil {
		return out, err
	}

	seen := make(map[string]bool, len(leftFiles)+len(rightFiles))
	var changes []Change

	for rel, l := range leftFiles {
		seen[rel] = true
		r, ok := rightFiles[rel]
		switch {
		case !ok:
			changes = append(changes, Change{Rel: rel, Kind: Removed, Bytes: -l.size})
		case l.digest != r.digest:
			changes = append(changes, Change{Rel: rel, Kind: Changed, Bytes: r.size - l.size})
		default:
			out.Same++
		}
	}
	for rel, r := range rightFiles {
		if seen[rel] {
			continue
		}
		changes = append(changes, Change{Rel: rel, Kind: Added, Bytes: r.size})
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Rel < changes[j].Rel })
	if len(changes) > MaxChanges {
		out.Truncated = len(changes) - MaxChanges
		changes = changes[:MaxChanges]
	}
	out.Changes = changes
	return out, nil
}

// entry is one file's identity for comparison.
type entry struct {
	digest string
	size   int64
}

// fingerprint digests every file in a tree, keyed by relative path.
//
// Directories are not entries. An empty directory that exists in one tree and
// not the other is a difference nobody is looking for, and counting it would
// make every diff of a freshly created probe report dozens of changes.
func fingerprint(root string) (map[string]entry, error) {
	out := map[string]entry{}

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fault.IO{Op: "look at", Path: root, Err: err}
	}
	if !info.IsDir() {
		return nil, fault.Conflict{Path: root, Reason: "is not a directory"}
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fault.IO{Op: "walk", Path: path, Err: err}
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fault.IO{Op: "locate", Path: path, Err: relErr}
		}
		if rel == "." || d.IsDir() {
			return nil
		}
		slash := filepath.ToSlash(rel)

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			dest, err := os.Readlink(path)
			if err != nil {
				return fault.IO{Op: "read the link at", Path: path, Err: err}
			}
			// A link is its destination: two probes whose links point
			// elsewhere differ, even though neither has file content.
			sum := sha256.Sum256([]byte("link:" + dest))
			out[slash] = entry{digest: hex.EncodeToString(sum[:])}
			return nil

		case d.Type().IsRegular():
			sum, size, err := digestFile(path)
			if err != nil {
				return err
			}
			out[slash] = entry{digest: sum, size: size}
			return nil

		default:
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func digestFile(path string) (string, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", 0, fault.IO{Op: "read", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fault.IO{Op: "read", Path: path, Err: err}
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Within narrows a diff to one subtree, for a report that wants to say what
// changed in the mail store rather than in the probe as a whole.
func (d Diff) Within(prefix string) Diff {
	prefix = strings.TrimSuffix(filepath.ToSlash(prefix), "/") + "/"
	var out Diff
	for _, c := range d.Changes {
		if strings.HasPrefix(c.Rel, prefix) {
			out.Changes = append(out.Changes, Change{
				Rel: strings.TrimPrefix(c.Rel, prefix), Kind: c.Kind, Bytes: c.Bytes,
			})
		}
	}
	return out
}

// SameTree reports whether two trees are byte-identical, cheaply enough to be
// used as an assertion. It is what the inertness tests compare with.
func SameTree(left, right string) (bool, error) {
	a, err := Digest(left)
	if err != nil {
		return false, err
	}
	b, err := Digest(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal([]byte(a), []byte(b)), nil
}
