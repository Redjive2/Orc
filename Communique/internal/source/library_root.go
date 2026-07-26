package source

import (
	"os"
	"path/filepath"
	"strings"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/settings"
)

// Moving the directory this machine mirrors.
//
// Every other library verb edits a file *inside* the checkout. This one moves the
// checkout, which makes it the one action that changes where the others are
// allowed to write — so it is the one place in the tree where a path arriving
// over the wire decides a boundary rather than being decided by one.
//
// The note on protocol.OpUpgrade's arguments is the rule this sits beside: what a
// machine builds from is not something a browser gets to name, because that path
// reaches a build script. This path reaches a directory walk and an atomic file
// write, never a command line — but it still decides which files those touch, so
// it is checked here, on the machine, against things only the machine knows.
//
// The checks below are not a sandbox and do not pretend to be. Somebody who can
// log into cq can already queue an upgrade, which rebuilds and restarts every
// tool on this machine; a filesystem boundary is not what stands between them and
// it. What they do is make the *accidents* impossible — pointing the library at
// `/`, or at the directory holding the fleet's keys — and those are the ones an
// operator makes at a phone screen without noticing.

// applyLibraryRoot points this machine at a different repository.
func (c *CLI) applyLibraryRoot(action protocol.Action) error {
	if c.Home == "" {
		return fault.Usage{Reason: "this machine has no agent home to record the change in; " +
			"run `cq sync --home <dir>` or set $CQ_HOME"}
	}

	root, err := c.checkLibraryRoot(action.Args.Workspace)
	if err != nil {
		return err
	}

	chosen, err := settings.Read(c.Home)
	if err != nil {
		return err
	}
	chosen.Library = root
	if err := settings.Write(c.Home, chosen); err != nil {
		return err
	}

	// The collector for *this* round keeps the old root. The snapshot being built
	// was already walked, and swapping the root underneath it would send a file
	// list from one directory with a root naming another — which is worse than
	// being one round behind, because it looks correct.
	c.warn("cq: this machine now mirrors %s; the next round is the first to collect it", root)
	return nil
}

// checkLibraryRoot resolves a proposed root and refuses what this machine will
// not accept, naming the way forward each time.
//
// It returns the *resolved* path. A root with a symlink in it would otherwise
// compare unequal to the same directory reached another way, and every later
// containment check — the one that stops an edit climbing out of the checkout —
// is done on resolved paths.
func (c *CLI) checkLibraryRoot(want string) (string, error) {
	if want == "" {
		return "", fault.Usage{Reason: "a library root is a directory to mirror; there is none to move to"}
	}
	if !filepath.IsAbs(want) {
		return "", fault.Usage{Reason: want + " is not an absolute path on this machine, " +
			"so it would mean a different directory depending on where the sync ran"}
	}

	root, err := filepath.EvalSymlinks(want)
	if err != nil {
		return "", fault.Usage{Reason: want + " is not there on this machine — " +
			"check the path, or make the directory first"}
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fault.IO{Op: "read", Subject: want, Err: err}
	}
	if !info.IsDir() {
		return "", fault.Usage{Reason: want + " is a file; the library root is the directory to mirror"}
	}

	// The two directories that must not end up inside the library, in either
	// direction. Inside it, and every file in them is mirrored to the website and
	// writable by its editor; containing it, and the same is true by a longer
	// route — which is how somebody picks `/` or their home directory and mirrors
	// their whole machine without meaning to.
	// The fleet first, because a root that swallows both should say so about the
	// one that matters more: cq's home holds a journal, and the fleet holds every
	// agent's key.
	for _, keep := range []struct{ what, path, fix string }{
		{"the fleet", c.orcHome(), "it holds every agent's key"},
		{"cq's own state", c.Home, "it holds this machine's journal and sync cursor"},
	} {
		if keep.path == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(keep.path)
		if err != nil {
			// Not there is not a conflict: a machine with no fleet has no keys to
			// expose. Anything else is unreadable rather than absent, and a check
			// that cannot run must not pass silently.
			if os.IsNotExist(err) {
				continue
			}
			return "", fault.Usage{Reason: "cannot tell whether " + want + " contains " + keep.what +
				" (" + keep.path + " could not be read), so the move is refused rather than guessed"}
		}
		// Three relationships, three sentences. They are separated because "is",
		// "contains" and "is inside" are different mistakes with different fixes,
		// and one message covering all three would describe none of them: somebody
		// who typed the fleet's own path needs to hear that, not that their choice
		// "contains" something at the same path.
		switch {
		case resolved == root:
			return "", fault.Usage{Reason: want + " is " + keep.what + " itself — " + keep.fix +
				", and the library root is a repository to work in, not cq's own storage"}
		case inside(resolved, root):
			return "", fault.Usage{Reason: want + " contains " + keep.what + " (" + keep.path + ") — " +
				keep.fix + ", and everything under the library root is mirrored to the website " +
				"and editable from it"}
		case inside(root, resolved):
			return "", fault.Usage{Reason: want + " is inside " + keep.what + " (" + keep.path + "), " +
				"which is not a repository to work in"}
		}
	}
	return root, nil
}

// orcHome is where the fleet lives on this machine, or the empty string.
//
// It reads the environment rather than asking orc, because this runs while an
// action is being applied and a subprocess that hangs would stall the queue. The
// default is orc's own: `$ORC_HOME`, else `~/.orc`.
func (c *CLI) orcHome() string {
	if c.Look != nil {
		if v, ok := c.Look("ORC_HOME"); ok && v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orc")
}

// inside reports whether child is at or below parent.
//
// Both are expected to be resolved already. The separator test is what stops
// `/srv/orc-old` reading as inside `/srv/orc`, which a plain prefix check gets
// wrong and which is exactly the pair somebody has during a migration.
func inside(child, parent string) bool {
	if parent == "" || child == "" {
		return false
	}
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}
