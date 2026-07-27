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
	"slices"
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

// Why a folder produced nothing.
//
// A walk that returns only documents makes a folder holding none of them
// indistinguishable from a folder that is not there — and the folder that is not
// there is the one somebody has just made. These are what a folder can be instead
// of a document, and each is a different thing to do about it.
type Why int

const (
	// Empty: nothing in it at all. Usually a folder made a moment ago.
	Empty Why = iota
	// NoDocuments: files, but none dock reads. See Extensions.
	NoDocuments
	// Unreadable: it is there and its contents cannot be listed — a folder whose
	// permissions do not admit this user. Silently skipping it reads as "nothing
	// here", which is the opposite of what is true.
	Unreadable
	// NotWalked: skipped by name — a dot-folder, or one of skipDirs. Named rather
	// than descended into, so `.private` is visible as a decision rather than as
	// an absence.
	NotWalked
)

func (w Why) String() string {
	switch w {
	case NoDocuments:
		return "nothing dock reads"
	case Unreadable:
		return "cannot be read"
	case NotWalked:
		return "not walked"
	default:
		return "empty"
	}
}

// Folder is a directory a walk found nothing in, and why.
type Folder struct {
	Path string
	Why  Why
}

// Walk returns every document under dir, in a deterministic order.
//
// Determinism is not incidental: overview's output is a golden test, and a walk
// that varied by filesystem would make it unreproducible. filepath.WalkDir
// already sorts each directory's entries, and this adds only the skipping.
func Walk(dir string) ([]string, error) {
	docs, _, err := WalkTree(dir)
	return docs, err
}

// WalkTree is Walk, and also the folders it passed through that hold no document.
//
// The two come from one walk because they are one question asked twice: a caller
// showing a tree needs both what is in it and what is in it that has nothing to
// show. Doing it in two passes would mean two answers that can disagree about a
// tree being written to while it is read.
//
// A folder with documents under it is not reported — it is already visible in the
// paths — and neither is one whose *children* hold documents, since the tree of
// names still leads there.
func WalkTree(dir string) ([]string, []Folder, error) {
	var docs []string
	var folders []Folder
	// How many documents were found under each directory, so a folder is called
	// barren only when nothing beneath it is a document either.
	under := map[string]int{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is skipped, not fatal: one bad corner of
			// a tree must not cost the whole overview. It is named, though —
			// see Unreadable.
			if d != nil && d.IsDir() {
				// WalkDir visits a directory once to announce it and again to
				// report that it could not be read, so the first visit has already
				// filed it as an ordinary folder. Unreadable is the truer of the
				// two, and a folder listed twice under two reasons is a folder
				// nobody trusts the listing about.
				delete(under, path)
				folders = append(folders, Folder{Path: path, Why: Unreadable})
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				folders = append(folders, Folder{Path: path, Why: NotWalked})
				return fs.SkipDir
			}
			if _, seen := under[path]; !seen {
				under[path] = 0
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // a symlinked document would be walked twice
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !IsDocument(path) {
			// Marked so the folder can say "files, but none I read" rather than
			// "empty", which would be a lie somebody would go looking for.
			if _, seen := under[filepath.Dir(path)]; seen {
				under[filepath.Dir(path)] |= hasFiles
			}
			return nil
		}
		docs = append(docs, path)
		for at := filepath.Dir(path); ; at = filepath.Dir(at) {
			if _, seen := under[at]; !seen {
				break
			}
			under[at] |= hasDocument
			if at == dir {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fault.IO{Op: "walk", Path: dir, Err: err}
	}

	for path, state := range under {
		if state&hasDocument != 0 {
			continue
		}
		why := Empty
		if state&hasFiles != 0 {
			why = NoDocuments
		}
		folders = append(folders, Folder{Path: path, Why: why})
	}
	slices.SortFunc(folders, func(a, b Folder) int { return strings.Compare(a.Path, b.Path) })
	return docs, folders, nil
}

// What a directory turned out to hold. Bits rather than a count: the only
// questions are "any document beneath it" and "anything at all in it".
const (
	hasDocument = 1 << iota
	hasFiles
)
