// Package scope decides whether a path is inside a task's declared surface.
//
// This is the part of Macmuffin that can stop someone editing a file, so it is
// written to be wrong in only one direction. Matching is a pure function of its
// input — no filesystem, no clock — and every path is reduced to a
// root-relative form *before* it is matched, with anything that escapes the
// root refused rather than compared. A scope check a symlink can walk around is
// decoration.
//
// Resolve is the one function here that touches the filesystem, because
// resolving a symlink is the only way to know what a path really names.
package scope

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"orc/common/fault"
)

// Bounds on a scope. A surface this wide is not a surface.
const (
	// MaxEntries bounds one task's scope.
	MaxEntries = 512
	// MaxEntryLen bounds one entry.
	MaxEntryLen = 1024
)

// Set is a validated scope: the paths a task may edit.
//
// The zero Set matches nothing, which is the safe direction — a task whose
// scope failed to load enforces everything rather than nothing. That is the
// opposite of the hook's rule in §8.4, and deliberately so: the hook is a
// bystander that must not stall a session, while this is the answer to a
// direct question and has no business guessing.
type Set struct {
	entries []string
}

// Parse validates and normalises scope entries.
//
// Each is cleaned, required to be relative, and required to stay inside the
// root. A trailing slash is kept, because it is what distinguishes "this
// directory and everything under it" from "this exact file".
func Parse(raw []string) (Set, error) {
	if len(raw) == 0 {
		return Set{}, fault.Usage{Reason: "a scope needs at least one path"}
	}
	if len(raw) > MaxEntries {
		return Set{}, fault.Usage{Reason: fmt.Sprintf(
			"a scope of %d entries is over the %d limit", len(raw), MaxEntries)}
	}

	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	var problems []string

	for _, entry := range raw {
		clean, err := normalise(entry)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	if len(problems) > 0 {
		// Every bad entry at once: an agent fixing a scope should get one round
		// trip, not one per path.
		return Set{}, fault.Usage{Reason: strings.Join(problems, "; ")}
	}

	s := Set{entries: out}
	if err := s.validate(); err != nil {
		return Set{}, err
	}
	return s, nil
}

// normalise cleans one entry and refuses the ones that cannot mean anything.
func normalise(entry string) (string, error) {
	trimmed := strings.TrimSpace(entry)
	switch {
	case trimmed == "":
		return "", fmt.Errorf("a scope entry is empty")
	case len(trimmed) > MaxEntryLen:
		return "", fmt.Errorf("scope entry is %d bytes, over the %d limit", len(trimmed), MaxEntryLen)
	case strings.ContainsRune(trimmed, 0):
		return "", fmt.Errorf("scope entry %q contains a NUL byte", entry)
	case filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/"):
		return "", fmt.Errorf("scope entry %q is absolute; scopes are relative to the worktree root", entry)
	case strings.Contains(trimmed, "**"):
		// A recursive glob is refused rather than approximated. `internal/` is
		// what "everything under here" is spelled as, and two ways to say one
		// thing is two things to keep in step.
		return "", fmt.Errorf("scope entry %q uses **; write a directory like %q instead",
			entry, strings.SplitN(trimmed, "**", 2)[0])
	}

	// Slashes are the wire form on every platform, so a Windows-style entry is
	// converted rather than refused.
	unix := filepath.ToSlash(trimmed)
	dir := strings.HasSuffix(unix, "/")

	clean := path.Clean(unix)
	if clean == "." || clean == "/" {
		// The whole worktree. Spelled explicitly so it is never the accidental
		// result of a cleaned empty string.
		return "./", nil
	}
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("scope entry %q escapes the worktree root", entry)
	}
	if dir {
		clean += "/"
	}
	return clean, nil
}

func (s Set) validate() error {
	const where = "scope.Set"
	if err := fault.Check(len(s.entries) > 0, where, "scope has no entries"); err != nil {
		return err
	}
	for _, e := range s.entries {
		if err := fault.Check(e != "", where, "scope has an empty entry"); err != nil {
			return err
		}
		if err := fault.Check(!strings.HasPrefix(e, "/"), where, "scope entry %q is absolute", e); err != nil {
			return err
		}
		if err := fault.Check(!strings.HasPrefix(e, "../"), where, "scope entry %q escapes", e); err != nil {
			return err
		}
	}
	return nil
}

// Entries returns the normalised scope, in the order it was declared.
func (s Set) Entries() []string { return append([]string(nil), s.entries...) }

// Empty reports whether the set would match nothing.
func (s Set) Empty() bool { return len(s.entries) == 0 }

// Len returns how many entries the scope holds.
func (s Set) Len() int { return len(s.entries) }

// Matches reports whether a root-relative path is inside the scope.
//
// rel must already be relative, cleaned, and known not to escape — Resolve is
// what produces such a path. Passing anything else is refused rather than
// guessed at, because a matcher that quietly accepted `../etc/passwd` would be
// the whole vulnerability.
//
// An entry matches three ways: as a directory when it ends in a slash, as a
// glob when it contains a metacharacter, and as an exact path otherwise.
func (s Set) Matches(rel string) (bool, error) {
	if s.Empty() {
		return false, nil
	}
	clean, err := checkRelative(rel)
	if err != nil {
		return false, err
	}

	for _, entry := range s.entries {
		switch {
		case entry == "./":
			return true, nil

		case strings.HasSuffix(entry, "/"):
			dir := strings.TrimSuffix(entry, "/")
			// The directory itself counts as inside it, so a scope of
			// `internal/` covers `internal` as well as everything under it.
			if clean == dir || strings.HasPrefix(clean, dir+"/") {
				return true, nil
			}

		case isGlob(entry):
			ok, err := path.Match(entry, clean)
			if err != nil {
				// A malformed pattern is a defect in the stored scope, not a
				// reason to let the edit through.
				return false, fault.Parse{Reason: fmt.Sprintf("scope entry %q is not a valid pattern: %s", entry, err)}
			}
			if ok {
				return true, nil
			}

		default:
			if clean == entry {
				return true, nil
			}
		}
	}
	return false, nil
}

// checkRelative refuses anything that is not already a safe relative path.
func checkRelative(rel string) (string, error) {
	if rel == "" {
		return "", fault.Internal{Where: "scope.Matches", Detail: "empty path"}
	}
	unix := filepath.ToSlash(rel)
	if strings.HasPrefix(unix, "/") {
		return "", fault.Internal{Where: "scope.Matches", Detail: "path " + rel + " is absolute; resolve it first"}
	}
	clean := path.Clean(unix)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fault.Escape{Path: rel}
	}
	return clean, nil
}

func isGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// Resolve reduces a path to a form Matches can take: relative to root, cleaned,
// and with symlinks followed.
//
// Symlinks are resolved because a scope check a symlink can walk around is
// decoration — an agent could otherwise point a link at a file outside its
// scope and edit it through the link. Everything that still escapes the root
// after resolution is an Escape, which is a different outcome from being merely
// out of scope and carries a different exit code.
//
// A path that does not exist yet is resolved as far as its nearest existing
// ancestor, so creating a new file inside the scope is allowed while creating
// one through a symlink that leaves the root is not.
func Resolve(root, target string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fault.Internal{Where: "scope.Resolve", Detail: "no root given"}
	}
	if strings.TrimSpace(target) == "" {
		return "", fault.Internal{Where: "scope.Resolve", Detail: "no path given"}
	}

	realRoot, err := resolveExisting(root)
	if err != nil {
		return "", err
	}

	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	realTarget, err := resolveExisting(abs)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil {
		return "", fault.Escape{Path: target, Root: root}
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fault.Escape{Path: target, Root: root}
	}
	if rel == "." {
		// The root itself. Named explicitly so a caller checking the worktree
		// directory gets a path rather than an empty string.
		return ".", nil
	}
	return rel, nil
}

// resolveExisting follows symlinks as far as the path exists, then appends the
// rest verbatim. A file about to be created has no link to follow, but its
// parent directory might.
func resolveExisting(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fault.IO{Op: "resolve", Path: p, Err: err}
	}

	rest := ""
	current := abs
	for range 64 { // a bounded walk: a path deeper than this is not real
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if rest == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, rest), nil
		}
		if !os.IsNotExist(err) {
			return "", fault.IO{Op: "resolve", Path: p, Err: err}
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding anything that exists.
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
	return abs, nil
}
