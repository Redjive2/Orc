// Package root finds the tree a document belongs to, and keeps paths inside it.
//
// Dock keeps no state anywhere, so a root is not a store: it is only the
// boundary that bounds a walk and bars a link from addressing something outside
// the corpus. A .dock file marks one explicitly and may be empty; a repository
// marks one implicitly; and a lone document is its own root, so Dock works on a
// directory nobody has prepared.
//
// Containment is checked after resolving symlinks. A check a symlink can walk
// around is decoration, and this one is what stands between a link in a
// document and an arbitrary file on the machine.
package root

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"orc/common/fault"
)

// Marker names a doc root explicitly. It may be empty: Dock reads nothing from
// it, and its presence is the whole of its content.
const Marker = ".dock"

// repoMarkers are the implicit roots, checked when no Marker is found.
var repoMarkers = []string{".git", ".hg"}

// skipDirs are never walked. A doc root is usually a repository, and a
// repository holds far more machinery than documentation — walking .git is
// thousands of files that cannot be documents.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "target": true,
}

// Find returns the doc root governing a path.
//
// The order is explicit first, implicit second, and the path itself last, so a
// tree that says what it is always wins over one that has to be guessed at.
func Find(path string) (string, error) {
	start, err := filepath.Abs(path)
	if err != nil {
		return "", fault.IO{Op: "resolve", Path: path, Err: err}
	}
	if info, err := os.Stat(start); err == nil && !info.IsDir() {
		start = filepath.Dir(start)
	}

	repo := ""
	for dir := start; ; {
		if exists(filepath.Join(dir, Marker)) {
			return dir, nil
		}
		if repo == "" {
			for _, m := range repoMarkers {
				if exists(filepath.Join(dir, m)) {
					repo = dir
					break
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if repo != "" {
		return repo, nil
	}
	return start, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Within resolves a path against a root and reports where it lands.
//
// It returns the path relative to the root. A path that resolves outside is a
// fault.Escape, which is its own exit code precisely so a containment failure
// is never mistaken for an ordinary "not in scope" refusal.
//
// Symlinks are resolved on both sides first, so a link pointing out of the tree
// is caught rather than followed. A path that does not exist yet is resolved as
// far as it does exist, because a target may legitimately name a document
// nobody has written.
func Within(root, path string) (string, error) {
	realRoot, err := resolve(root)
	if err != nil {
		return "", err
	}
	realPath, err := resolve(path)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return "", fault.Escape{Path: path, Root: root}
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fault.Escape{Path: path, Root: root}
	}
	return filepath.ToSlash(rel), nil
}

// resolve makes a path absolute and follows symlinks as far as the path exists.
func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fault.IO{Op: "resolve", Path: path, Err: err}
	}
	// EvalSymlinks fails on a path that does not exist, so resolve the longest
	// existing prefix and rejoin the rest. A document that has not been written
	// yet still has a place in the tree.
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return filepath.Join(real, rest), nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return filepath.Join(abs, rest), nil
		}
		rest = filepath.Join(filepath.Base(abs), rest)
		abs = parent
	}
}

// Extensions are the files a walk considers documents.
//
// This table exists because of what happens without it. Dock's markers are
// ordinary markdown, so a source file containing documentation in a string
// literal — a fixture, a help text, a test — parses as a document and reports
// its examples as broken sections. Walking Dock's own repository produced
// exactly that: pages of numbering faults from Go files that are not
// documentation and were never meant to be read as it.
//
// So a walk reads documentation, and `index` and `read` still take any path a
// caller names explicitly. Naming a file is a decision; sweeping a tree is not.
var Extensions = map[string]bool{
	".md": true, ".markdown": true, ".mdown": true, ".mkd": true,
	".txt": true, ".rst": true, ".adoc": true, ".asciidoc": true,
	".org": true, ".text": true,
}

// IsDocument reports whether a walk would consider path a document.
func IsDocument(path string) bool {
	return Extensions[strings.ToLower(filepath.Ext(path))]
}

// Walk returns every document under dir, in a deterministic order.
//
// Determinism is not incidental: overview's output is a golden test, and a walk
// that varied by filesystem would make it unreproducible. filepath.WalkDir
// already sorts each directory's entries, and this adds only the skipping.
func Walk(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is skipped, not fatal: one bad corner of
			// a tree must not cost the whole overview.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // a symlinked document would be walked twice
		}
		if !d.Type().IsRegular() || !IsDocument(path) {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, fault.IO{Op: "walk", Path: dir, Err: err}
	}
	return out, nil
}
