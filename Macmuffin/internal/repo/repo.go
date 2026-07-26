// Package repo resolves a directory to the git worktree that contains it.
//
// It reads `.git` directly rather than running `git`. A subprocess could be
// defeated by an alias, a shim, a `git` that is not on PATH, or a hook — and
// this is the lookup that decides which task's scope is being enforced, so it
// must not be steerable by the environment it is protecting.
//
// Only two shapes matter. In a main working tree, `.git` is a directory. In a
// linked worktree it is a file whose first line is `gitdir: <path>`, pointing
// into the main repository's `worktrees/<name>` directory. Anything else is not
// a worktree, and is reported as such rather than guessed at.
package repo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/common/fault"
)

// MaxGitFileSize bounds the `.git` file of a linked worktree. It holds one
// line; anything larger is not the file this package is looking for.
const MaxGitFileSize = 64 << 10

// maxDepth bounds the walk up towards the root. A tree deeper than this is not
// a checkout.
const maxDepth = 256

// Worktree is a resolved git worktree.
type Worktree struct {
	// root is the directory holding `.git`, with symlinks resolved.
	root string
	// gitDir is the repository directory this worktree's `.git` names.
	gitDir string
	// main is the common repository every worktree of one project shares.
	main string
	// linked reports whether this is a linked worktree rather than the main one.
	linked bool
}

// Root returns the worktree's top directory.
func (w Worktree) Root() string { return w.root }

// GitDir returns the repository directory this worktree uses.
func (w Worktree) GitDir() string { return w.gitDir }

// Main returns the common repository shared by every worktree of one project.
//
// It is what makes "these two worktrees belong to the same project" answerable,
// which is the check that stops a task being bound to a worktree of some other
// repository entirely.
func (w Worktree) Main() string { return w.main }

// Linked reports whether this is a linked worktree.
func (w Worktree) Linked() bool { return w.linked }

// Zero reports whether the worktree was never resolved.
func (w Worktree) Zero() bool { return w.root == "" }

// Find walks up from dir looking for a worktree.
//
// A directory outside any checkout is not an error here — it is a fact the
// caller has to handle, and returning a zero Worktree with ok false says so
// without forcing an error path onto the ordinary case of running `muff`
// somewhere that is not a repository.
func Find(dir string) (Worktree, bool, error) {
	if strings.TrimSpace(dir) == "" {
		return Worktree{}, false, fault.Internal{Where: "repo.Find", Detail: "no directory given"}
	}

	current, err := filepath.Abs(dir)
	if err != nil {
		return Worktree{}, false, fault.IO{Op: "resolve", Path: dir, Err: err}
	}
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		current = resolved
	}

	for range maxDepth {
		marker := filepath.Join(current, ".git")
		info, err := os.Lstat(marker)
		switch {
		case err == nil:
			w, err := describe(current, marker, info)
			if err != nil {
				return Worktree{}, false, err
			}
			return w, true, nil
		case !os.IsNotExist(err):
			return Worktree{}, false, fault.IO{Op: "stat", Path: marker, Err: err}
		}

		parent := filepath.Dir(current)
		if parent == current {
			return Worktree{}, false, nil
		}
		current = parent
	}
	return Worktree{}, false, nil
}

// At resolves one directory, requiring it to *be* a worktree root rather than
// merely to sit inside one.
//
// Binding a task to a subdirectory would make the scope's meaning depend on
// where the binding happened to be made, so the distinction is enforced rather
// than smoothed over.
func At(dir string) (Worktree, error) {
	w, ok, err := Find(dir)
	if err != nil {
		return Worktree{}, err
	}
	if !ok {
		return Worktree{}, fault.Usage{Reason: fmt.Sprintf("%s is not inside a git worktree", dir)}
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return Worktree{}, fault.IO{Op: "resolve", Path: dir, Err: err}
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if abs != w.root {
		return Worktree{}, fault.Usage{Reason: fmt.Sprintf(
			"%s is inside the worktree at %s but is not its root; bind the root", dir, w.root)}
	}
	return w, nil
}

// describe reads a `.git` marker and works out what kind of worktree it is.
func describe(root, marker string, info os.FileInfo) (Worktree, error) {
	if info.IsDir() {
		// A main working tree: `.git` is the repository itself.
		return Worktree{root: root, gitDir: marker, main: marker}, nil
	}
	if !info.Mode().IsRegular() {
		return Worktree{}, fault.Parse{Path: marker, Reason: fmt.Sprintf(
			"%s is neither a directory nor a file (%s), so this is not a worktree", marker, info.Mode().Type())}
	}
	if info.Size() > MaxGitFileSize {
		return Worktree{}, fault.Parse{Path: marker, Reason: fmt.Sprintf(
			"%s is %d bytes; a worktree's .git file holds one line", marker, info.Size())}
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		return Worktree{}, fault.IO{Op: "read", Path: marker, Err: err}
	}

	gitDir, err := parseGitFile(marker, data)
	if err != nil {
		return Worktree{}, err
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if resolved, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = resolved
	}

	return Worktree{root: root, gitDir: gitDir, main: commonDir(gitDir), linked: true}, nil
}

// parseGitFile reads the `gitdir:` line a linked worktree's `.git` holds.
func parseGitFile(path string, data []byte) (string, error) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	text := strings.TrimSpace(string(line))

	const prefix = "gitdir:"
	if !strings.HasPrefix(text, prefix) {
		return "", fault.Parse{Path: path, Reason: fmt.Sprintf(
			"%s does not begin with %q, so this is not a linked worktree", path, prefix)}
	}
	dir := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if dir == "" {
		return "", fault.Parse{Path: path, Reason: path + " names no repository directory"}
	}
	return dir, nil
}

// commonDir reduces a linked worktree's git directory to the repository every
// worktree of the project shares.
//
// Git puts a linked worktree's directory at `<main>/worktrees/<name>`, so the
// two path elements above it are the common repository. A directory that is not
// shaped that way is its own main, which is the conservative reading: two
// worktrees are only judged to be the same project when it is certain.
func commonDir(gitDir string) string {
	parent := filepath.Dir(gitDir)
	if filepath.Base(parent) == "worktrees" {
		return filepath.Dir(parent)
	}
	return gitDir
}

// SameProject reports whether two worktrees belong to one repository.
func SameProject(a, b Worktree) bool {
	if a.Zero() || b.Zero() {
		return false
	}
	return a.main == b.main
}
