package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"orc/anno/internal/render"
	"orc/common/fault"
)

// Walking a whole tree, the way Dock walks one.
//
// `overview` used to read one directory: the files directly inside it, and a list of
// the folders below so a reader knew where to look next. That made understanding a
// package a matter of running the command once per directory, and it made "what is
// annotated in here?" a question nobody could answer about a tree.
//
// The rules are Dock's, deliberately, because the two tools sweep the same
// repositories and a reader should not have to hold two sets of exclusions in their
// head. Where this differs, it is because a document and a source file are different
// things, and those places say so.

// skipDirs are never walked.
//
// A tree Anno is pointed at is usually a repository, and a repository holds far more
// machinery than source: `.git` alone is thousands of files that are not text and
// could never carry an annotation. Dot-directories go the same way — they are
// somebody's tooling, not their code.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true,
}

// atRootOnly are skipped where a build system puts them and nowhere else.
//
// `target` is Rust's build directory, and skipping it anywhere at all cost Anno its
// own `internal/target` package — a directory named for what it does, in the tree the
// tool was written in. Dock's list is shared because two tools pointed at one
// repository should agree about what is in it; agreeing to hide somebody's source
// code is not what that was for.
//
// At the root it is still the build directory, which is what the rule was about.
var atRootOnly = map[string]bool{"target": true}

// swept is what one walk found: the files worth reading, the folders it refused to
// go into, and every folder it did walk.
//
// The walked folders are handed back rather than resolved here because the walk
// cannot answer the question they exist for. "This folder has nothing to show" has
// two causes — nothing in it at all, and nothing in it that turned out to be
// annotated — and the second is only known after the files have been read. The walk
// reports what it saw; the caller says what it meant.
type swept struct {
	files   []string
	folders []render.Folder
	// walked is every directory beneath the root, root excluded: the root is what
	// the question was about, and a listing that named it would be answering
	// "where is this folder" with "here".
	walked []string
	// holds marks a directory with at least one readable file somewhere beneath it,
	// which is the difference between a folder that is empty and one whose contents
	// simply carry no annotations.
	holds map[string]bool
}

// sweep returns every file under dir that Anno could annotate, in a deterministic
// order, and the folders that turned out to hold none.
//
// Determinism matters for the same reason it does in Dock: the output is a golden
// test, and a walk that varied by filesystem would make it unreproducible.
// filepath.WalkDir already sorts each directory's entries, so this adds only the
// skipping.
//
// Both answers come from one walk because they are one question asked twice — what
// is in this tree, and what is in it that has nothing to show. Two passes would be
// two answers that can disagree about a tree being written to while it is read.
func sweep(dir string) (swept, error) {
	// The root is checked before the walk, because WalkDir reports a missing or
	// wrong-kind root through the same callback as every other error and this
	// command's answer to those two is not "nothing was found" — it is "that is not
	// a directory", which is about the caller's own invocation.
	if dir == "" {
		return swept{}, fault.Usage{Reason: "empty directory path"}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return swept{}, fault.IO{Op: "stat", Path: dir, Err: err}
	}
	if !info.IsDir() {
		return swept{}, fault.IO{Op: "list", Path: dir, Err: errors.New("not a directory")}
	}

	out := swept{holds: map[string]bool{}}
	walked := map[string]bool{}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Except at the root. Every other unreadable directory is a corner of
			// somebody's tree; the root is the thing they asked about by name, and
			// answering "here is a list of one folder, which cannot be read" is a
			// screen pretending to have done the job.
			if path == dir {
				return err
			}
			// An unreadable corner is skipped rather than fatal: one directory
			// somebody's permissions exclude must not cost the whole sweep. It is
			// named, though, because a folder that silently contributes nothing is
			// indistinguishable from one that is genuinely empty.
			if d != nil && d.IsDir() {
				// WalkDir announces a directory once and reports its failure again,
				// so the first visit has already filed it as an ordinary folder.
				// Unreadable is the truer of the two.
				delete(walked, path)
				out.folders = append(out.folders, folderAt(dir, path, "cannot be read"))
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") ||
				(atRootOnly[d.Name()] && filepath.Dir(path) == dir)) {
				out.folders = append(out.folders, folderAt(dir, path, "not walked"))
				return fs.SkipDir
			}
			if path != dir {
				walked[path] = true
			}
			return nil
		}

		// A symlinked file would be rendered twice — once here and once wherever it
		// really lives — and a tree that lists the same annotations under two names
		// is a tree somebody stops trusting.
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}

		out.files = append(out.files, path)
		for at := filepath.Dir(path); at != dir && len(at) > len(dir); at = filepath.Dir(at) {
			out.holds[at] = true
		}
		return nil
	})
	if err != nil {
		return swept{}, fault.IO{Op: "walk", Path: dir, Err: err}
	}

	for path := range walked {
		out.walked = append(out.walked, path)
	}
	slices.Sort(out.walked)
	slices.Sort(out.files)
	return out, nil
}

// barren describes the folders that had nothing to show, given which of the files
// under them turned out to be annotated.
//
// This is the second half of a sweep, and it is separate because it cannot be
// answered until the files have been read. Dock draws the same distinction between a
// folder that is empty and one that holds only things it does not index, and it draws
// it for the same reason: a folder somebody has just made and one they have filled
// with images are different situations, and "nothing here" describes neither.
//
// A folder with an annotated file anywhere beneath it is not listed at all. Its trees
// are already on the screen, above, and naming it again would be describing the same
// folder twice with less detail the second time.
func (w swept) barren(root string, annotated map[string]bool) []render.Folder {
	out := append([]render.Folder(nil), w.folders...)
	// The topmost of each barren branch, and not the forty folders under it. A tree
	// with nothing annotated in it is one fact, and `internal`, `internal/cli`,
	// `internal/store` and the rest of them are that one fact restated until it
	// scrolls the trees off the screen. Named is the highest folder it is true of,
	// which is also the one somebody would go and look at.
	//
	// w.walked is sorted, and a parent's path sorts before its children's, so an
	// ancestor is always decided before the folders beneath it.
	barren := map[string]bool{}
	for _, path := range w.walked {
		if annotated[path] {
			continue
		}
		barren[path] = true
		if barren[filepath.Dir(path)] {
			continue
		}
		why := "empty"
		if w.holds[path] {
			why = "nothing annotated"
		}
		out = append(out, folderAt(root, path, why))
	}
	slices.SortFunc(out, func(a, b render.Folder) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// folderAt names a folder relative to the root of the sweep.
//
// Relative, because a recursive sweep reports folders from anywhere in the tree and
// a bare basename would be ambiguous — three packages called `internal` would print
// as three identical rows. The root itself is never one of these, so the name is
// never empty.
func folderAt(root, path, why string) render.Folder {
	return render.Folder{Name: within(root, path), Why: why}
}

// within names a path relative to the root of the sweep, falling back to the path
// itself where that is not expressible. Never empty, and never absolute for anything
// genuinely under the root, which is what makes one line of a sweep comparable with
// the next.
func within(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" || rel == "." {
		return path
	}
	return rel
}

// unreadable reports whether Anno could not read the file at all, as against having
// read it and failed to make sense of the annotations in it.
//
// The line matters only in a sweep. Asked about one file by name, every failure is
// worth reporting — somebody named that file and deserves to know why it did not
// work. Asked about a tree, most files are not source at all, and a refusal per
// image would bury the thirty trees somebody actually wanted.
//
// It leans on what `source.Load` refuses: a directory, an irregular file, one over
// the size limit, one with a NUL in it, one that is not valid UTF-8. Those are the
// shapes of "not a text file", and none of them is a mistake anybody made.
func unreadable(err error) bool {
	var parse fault.Parse
	if errors.As(err, &parse) {
		return strings.Contains(parse.Reason, "binary file") ||
			strings.Contains(parse.Reason, "not valid UTF-8") ||
			strings.Contains(parse.Reason, "invalid UTF-8")
	}
	var io fault.IO
	return errors.As(err, &io)
}
