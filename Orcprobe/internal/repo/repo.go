// Package repo detaches a copied working tree from the world it came from.
//
// A probe's repo is a full copy, uncommitted work included — the current state
// is the point. What it must not keep is anywhere to send that work. Three
// independent detachments, for the same reason the network has four (plan
// §4.4): this is a place where a mistake is visible to someone other than the
// operator.
//
//  1. every remote is removed from .git/config, so `git push` has no target;
//  2. worktree registrations are removed, so no path in the probe points at a
//     real checkout — a probe worktree over a real one is the escape itself;
//  3. a probe-local git config is written and pointed at by GIT_CONFIG_GLOBAL,
//     so no credential helper and no real identity is in reach.
//
// The config rewrite is line-based rather than a real INI parse. That is enough
// for what it does — drop whole [remote "..."] sections — and it fails safe: an
// unparseable line is kept, and a section it cannot classify is kept, so the
// worst outcome is a remote that survives into the probe, which the shim's
// refusal of `git push` still stops.
package repo

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// ProbeConfig is the git config a probe uses, named so it is obvious in a
// listing.
const ProbeConfig = ".probe-gitconfig"

// Report says what detaching removed, for the manifest.
type Report struct {
	Remotes   []string
	Worktrees int
	// Git is false when the copy has no .git at all: a directory copied as a
	// repo that turns out not to be one is worth saying out loud rather than
	// silently doing nothing to.
	Git bool
}

// Detach removes every route out of a copied repository.
func Detach(dir string) (Report, error) {
	var rep Report

	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, writeConfig(dir)
		}
		return rep, fault.IO{Op: "look at", Path: gitDir, Err: err}
	}
	// A .git *file* is what a worktree checkout has instead of a directory, and
	// it points at the real repository. A probe must never keep one.
	if !info.IsDir() {
		if err := os.Remove(gitDir); err != nil {
			return rep, fault.IO{Op: "remove", Path: gitDir, Err: err}
		}
		rep.Worktrees++
		return rep, writeConfig(dir)
	}
	rep.Git = true

	worktrees := filepath.Join(gitDir, "worktrees")
	if entries, err := os.ReadDir(worktrees); err == nil {
		rep.Worktrees = len(entries)
		if err := os.RemoveAll(worktrees); err != nil {
			return rep, fault.IO{Op: "remove", Path: worktrees, Err: err}
		}
	} else if !os.IsNotExist(err) {
		return rep, fault.IO{Op: "list", Path: worktrees, Err: err}
	}

	names, err := stripRemotes(filepath.Join(gitDir, "config"))
	if err != nil {
		return rep, err
	}
	rep.Remotes = names

	return rep, writeConfig(dir)
}

// stripRemotes removes every [remote "..."] section from a git config.
func stripRemotes(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "read", Path: path, Err: err}
	}

	var (
		out      bytes.Buffer
		removed  []string
		dropping bool
	)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "[") {
			// A new section ends any dropping, and starts one if it is a remote.
			name, isRemote := remoteName(trimmed)
			dropping = isRemote
			if isRemote {
				removed = append(removed, name)
				continue
			}
		}
		if dropping {
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return nil, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := os.WriteFile(path, out.Bytes(), snapshot.FileMode); err != nil {
		return nil, fault.IO{Op: "write", Path: path, Err: err}
	}
	return removed, nil
}

// remoteName reports whether a section header opens a remote, and names it.
func remoteName(header string) (string, bool) {
	body := strings.TrimSuffix(strings.TrimPrefix(header, "["), "]")
	if !strings.HasPrefix(body, "remote") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(body, "remote"))
	name := strings.Trim(rest, `"`)
	if name == "" {
		name = "(unnamed)"
	}
	return name, true
}

// writeConfig writes the probe's own git config.
//
// The identity is deliberately not the operator's. A commit made inside a probe
// that carries their real name and address is a commit that can be mistaken for
// real work later, in a repo that was copied from a real one.
func writeConfig(dir string) error {
	const body = `# Written by orcprobe. This is a probe's git identity, and it is nobody.
#
# There is no credential helper here on purpose: a probe must not be able to
# authenticate to anything, and a helper inherited from the real config would
# hand it the operator's tokens.
[user]
	name = probe
	email = probe@invalid
[credential]
	helper =
[push]
	default = nothing
[commit]
	gpgsign = false
[tag]
	gpgsign = false
`
	path := filepath.Join(dir, ProbeConfig)
	if err := os.WriteFile(path, []byte(body), snapshot.FileMode); err != nil {
		return fault.IO{Op: "write", Path: path, Err: err}
	}
	return nil
}

// Find locates the git working tree containing dir, if any. It walks upward
// looking for .git, which is what git itself does.
func Find(dir string) (string, bool) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}
