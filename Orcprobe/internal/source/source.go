// Package source is the only package in Orcprobe that knows a real store
// exists.
//
// That is the isolation made structural: everything else in this tool is
// physically unable to name the real world, because nothing else contains the
// strings `MAILMAN_HOME` or `~/.mailman`. If a future command has a bug that
// writes where it should not, the worst it can reach is a probe.
//
// The package only ever *resolves* and *reads*. It opens nothing for writing,
// creates nothing, and returns paths rather than handles, so a caller cannot be
// handed something writable by accident.
package source

import (
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/fault"
)

// Env looks up an environment variable, reporting whether it was set. It is
// injected everywhere so resolution is testable without touching the real one.
type Env func(key string) (string, bool)

// MapEnv reads an injected environment, for tests.
func MapEnv(m map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// Kind identifies a tool whose state a probe copies.
type Kind int

// The tools with state on disk. Anno and Dock have none — they operate on
// files, which is why a probe copies a repo (§5.4 of the plan) rather than a
// store for them. Orc has none yet; when it does, it is one entry here.
const (
	Mailman Kind = iota
	Macmuffin
	CQ
	// Orc is the fleet's own store, and the only one that holds plaintext keys
	// — which is why §5.3's rule about credentials bites hardest here.
	Orc
)

// Tool describes where one tool keeps its state and where that lands in a
// probe. The resolution order is each tool's own, copied from its plan: an
// explicit override, then the XDG directory, then a dot-directory in the home.
type Tool struct {
	Kind Kind
	// Name is how the tool is written in prose.
	Name string
	// Command is the binary that reads this state.
	Command string
	// EnvHome is the override variable, which is also what a probe sets to
	// redirect the tool.
	EnvHome string
	// XDGVar and XDGSub are the second resolution step.
	XDGVar string
	XDGSub string
	// DotDir is the fallback under the home directory.
	DotDir string
	// Dir is where this tool's state lands inside a probe, relative to it.
	Dir string
}

// Tools returns every tool with copyable state, in the order a probe copies
// them. Mailman first: it is the one whose loss would matter most, so it is the
// one that fails a creation earliest if it is going to fail.
func Tools() []Tool {
	return []Tool{
		{
			Kind: Mailman, Name: "Mailman", Command: "mailman",
			EnvHome: "MAILMAN_HOME", XDGVar: "XDG_DATA_HOME", XDGSub: "mailman",
			DotDir: ".mailman", Dir: "state/mailman",
		},
		{
			Kind: Macmuffin, Name: "Macmuffin", Command: "muff",
			EnvHome: "MACMUFFIN_HOME", XDGVar: "XDG_DATA_HOME", XDGSub: "macmuffin",
			DotDir: ".macmuffin", Dir: "state/macmuffin",
		},
		{
			Kind: CQ, Name: "Communiqué", Command: "cq",
			EnvHome: "CQ_HOME", XDGVar: "XDG_STATE_HOME", XDGSub: "cq",
			DotDir: ".cq", Dir: "state/cq",
		},
		{
			Kind: Orc, Name: "Orc", Command: "orc",
			EnvHome: "ORC_HOME", XDGVar: "XDG_DATA_HOME", XDGSub: "orc",
			DotDir: ".orc", Dir: "state/orc",
		},
	}
}

// Of returns one tool by kind.
//
// Callers ask by name rather than by position, because the table grew a fourth
// entry once and every `Tools()[2]` in the tree would have silently become a
// different tool. The zero Tool is returned for a kind that does not exist,
// which cannot happen from a constant and would be a compile error if Kind were
// ever built from data.
func Of(kind Kind) Tool {
	for _, tool := range Tools() {
		if tool.Kind == kind {
			return tool
		}
	}
	return Tool{}
}

// Resolve reports where this tool's state lives, by the tool's own rules.
//
// home is passed in rather than looked up so resolution is testable without
// touching the real one — the same reason Mailman's store does it this way.
func (t Tool) Resolve(env Env, home string) (string, error) {
	if env == nil {
		env = os.LookupEnv
	}
	if root, ok := env(t.EnvHome); ok {
		if strings.TrimSpace(root) == "" {
			return "", fault.Usage{Reason: t.EnvHome + " is set but empty"}
		}
		return filepath.Clean(root), nil
	}
	if base, ok := env(t.XDGVar); ok && strings.TrimSpace(base) != "" {
		return filepath.Join(filepath.Clean(base), t.XDGSub), nil
	}
	if home == "" {
		return "", fault.Usage{Reason: "no home directory found; set " + t.EnvHome + " to say where " + t.Name + "'s state is"}
	}
	return filepath.Join(home, t.DotDir), nil
}

// Root is one resolved source: where it is, and whether anything is there.
//
// A tool that has never run has no store, and that is not an error — a probe of
// a machine where Macmuffin has never been used should say "Macmuffin: nothing
// to copy" and carry on, not refuse to exist.
type Root struct {
	Tool    Tool
	Path    string
	Present bool
}

// Find resolves every tool's state and reports which of them exist.
func Find(env Env, home string) ([]Root, error) {
	tools := Tools()
	roots := make([]Root, 0, len(tools))
	for _, t := range tools {
		path, err := t.Resolve(env, home)
		if err != nil {
			return nil, err
		}
		present, err := isDir(path)
		if err != nil {
			return nil, err
		}
		roots = append(roots, Root{Tool: t, Path: path, Present: present})
	}
	return roots, nil
}

func isDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fault.IO{Op: "look at", Path: path, Err: err}
	}
	if !info.IsDir() {
		return false, fault.Conflict{Path: path, Reason: "is not a directory"}
	}
	return true, nil
}

// Contains reports whether path is inside root, or is root itself.
//
// This is the check that keeps a probe from being created inside the thing it
// copies. It compares cleaned absolute paths, so callers that care about
// symlinks pass paths that have already been expanded — Real does exactly that.
func Contains(root, path string) bool {
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

// Real reports whether path is inside any real tool state, for the refusal in
// probe.Create. It returns the offending root so the refusal can name it.
func Real(env Env, home, path string) (string, bool, error) {
	roots, err := Find(env, home)
	if err != nil {
		return "", false, err
	}
	resolved := resolve(path)
	for _, r := range roots {
		if Contains(resolve(r.Path), resolved) {
			return r.Path, true, nil
		}
	}
	return "", false, nil
}

// resolve expands symlinks, including for a path that does not exist yet.
//
// That case is the ordinary one — a probe root is about to be created — and
// getting it wrong is not a cosmetic bug: on macOS a temporary directory is
// reached through /var, which is a link to /private/var. Comparing an expanded
// root against an unexpanded path would then find no overlap, and the refusal
// that keeps a probe from being created inside the real mail store would not
// fire. So the walk goes up to the nearest ancestor that does exist, expands
// that, and puts the remainder back.
func resolve(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}

	current, rest := abs, ""
	for {
		if real, err := filepath.EvalSymlinks(current); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs // nothing along the way exists; the cleaned path is all there is
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}
