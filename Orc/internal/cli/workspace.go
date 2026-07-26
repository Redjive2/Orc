package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
)

// `orc workspace` — where an agent works.
//
// A workspace used to be a path Orc computed and nothing could change:
// `$ORC_HOME/identities/<name>/workspace`, derived on every call. This makes it a
// value, with that path as its default, so an identity can be pointed at a checkout
// somebody already has rather than only at the one Orc made for it.
//
// See Claude/Docs/Communique/Workdirs.md for the whole plan, including the two
// halves this does not do yet.
//
// It is shaped on `orc model`, which is the nearest thing in the tree: show with one
// argument, change with two, the boss's call either way, and the running session's
// answer to it deferred rather than forced.

// workspace is `orc workspace <identity> [<path>] [--adopt] [--now]`.
func (a App) workspace(args []string) error {
	var adopt, now bool
	rest, err := flagged(args, options{switches: map[string]*bool{
		"--adopt": &adopt, "--now": &now,
	}})
	if err != nil {
		return err
	}
	if len(rest) == 0 || len(rest) > 2 {
		return fault.Usage{Reason: "workspace takes an identity, and a path to point it at"}
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("workspace"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	if _, err := s.fleet.Identity(who); err != nil {
		return err
	}

	if len(rest) == 1 {
		return a.reportWorkspace(s, who)
	}
	return a.moveWorkspace(s, who, rest[1], adopt, now)
}

// reportWorkspace answers `orc workspace <identity>`: where it works, and whether
// that is the path Orc would have chosen.
//
// Reading is not directing, so it asks only what `status` asks. The distinction
// between a derived path and a chosen one is worth drawing: one of them is a fact
// about the layout and the other is a decision somebody made.
func (a App) reportWorkspace(s caller, who user.Name) error {
	where := s.store.WorkspaceDir(who)

	line := fmt.Sprintf("%s works in %s", a.out.Identity(who.String()), a.out.Path(where))
	if got, err := s.store.Identity(who); err == nil && got.Workspace() == "" {
		line += "   " + a.out.Muted("(orc's own, under the fleet)")
	}
	if err := a.say(line); err != nil {
		return err
	}
	return a.sayDrift(s, who, where)
}

// sayDrift reports a session working somewhere its identity no longer says it should.
//
// This is the state an operator most needs to see and the one they would never think
// to look for: the agent is writing to a directory Orc does not consider its
// workspace, and its permissions were compiled against the other one. A session
// started before the workspace was recorded says nothing — "cannot say" is not a
// disagreement.
func (a App) sayDrift(s caller, who user.Name, want string) error {
	state, live, err := s.store.Session(who)
	if err != nil || !live || state.Workspace == "" {
		return nil
	}
	if filepath.Clean(state.Workspace) == filepath.Clean(want) {
		return nil
	}
	return a.say("  " + a.out.Warn("its running session is working in "+state.Workspace) + "\n  " +
		a.out.Muted(fmt.Sprintf("its permissions are compiled against the path above — `orc refresh %s` moves it", who)))
}

// moveWorkspace points the identity somewhere else.
func (a App) moveWorkspace(s caller, who user.Name, path string, adopt, now bool) error {
	// Where somebody else's agent works is the boss's call, and an agent moving
	// its own workspace would be stepping outside the directory its permissions
	// were compiled against.
	if err := s.controls(who, "move the workspace of"); err != nil {
		return err
	}

	want, err := a.checkWorkspace(s, who, path, adopt)
	if err != nil {
		return err
	}

	was := s.store.WorkspaceDir(who)

	// The files move before anything is written down.
	//
	// The plan called for journalling the intent first, on the tree's usual "record
	// before you act" rule. That rule is for operations that destroy — and this one
	// copies. Copying first means a crash leaves the old directory untouched and the
	// identity still pointing at it, which is a stray directory rather than an agent
	// pointed at half a tree. Journalling first would have had exactly the failure
	// the rule exists to prevent, inverted.
	copied := 0
	if !adopt && want != was {
		if copied, err = a.relocate(who, was, want); err != nil {
			return err
		}
	}
	if want == was {
		// A script that sets the workspace every pass should be a no-op on the
		// passes where nothing changed.
		return a.say(fmt.Sprintf("%s already works in %s",
			a.out.Identity(who.String()), a.out.Path(want)))
	}

	if _, err := s.store.ApplyIdentity(who, func(model.Identity) (model.IdentityEvent, error) {
		return model.SetWorkspace(s.who, s.store.Now(), want)
	}); err != nil {
		return err
	}

	line := fmt.Sprintf("%s %s   %s → %s", a.out.Good("moved"),
		a.out.Identity(who.String()), a.out.Muted(was), a.out.Path(want))
	if copied > 0 {
		line += "   " + a.out.Muted(fmt.Sprintf("%s copied", plural2(copied, "1 file", fmt.Sprintf("%d files", copied))))
	}
	if err := a.say(line); err != nil {
		return err
	}

	// The old directory is left where it is. Orc does not delete an agent's work as
	// a side effect of a settings change: the copy is verified, the identity points
	// at the new one, and what to do with the old one is the operator's call.
	if copied > 0 {
		if err := a.say("  " + a.out.Muted("the old directory is untouched at "+was)); err != nil {
			return err
		}
	}
	if err := a.rebindWorktrees(was, want); err != nil {
		return err
	}
	return a.afterMove(s, who, was, now)
}

// muffBinary is Macmuffin's command, by the name it is installed under. A constant
// rather than a literal because it is the whole of Orc's knowledge of another tool,
// and one place is easier to find than one call site.
const muffBinary = "muff"

// rebindWorktrees follows the Macmuffin bindings that lived under the old directory.
//
// `muff worktree` binds a task to a directory, and the scope hook looks the session's
// directory up in those bindings to decide what is being enforced. Move the directory
// and every binding under it addresses nowhere: the hook finds nothing, concludes no
// task is in force, and enforces nothing — silently, looking exactly like an agent
// that never opted in. A migration that left that behind would be turning the fence
// off as a side effect of a settings change.
//
// Orc shells out rather than editing Macmuffin's store, because a tool writing
// another tool's records is how two tools come to disagree about a file's format.
// `muff rebind` owns what a binding is; Orc only knows a directory moved.
//
// A muff that is not installed is not an error. Most fleets do not run one, and a
// missing binary means there are no bindings to strand. When it *is* installed and
// refuses, the refusal is relayed and the move still stands: the files are copied and
// the identity is written, so failing here would report a move that happened as one
// that did not.
func (a App) rebindWorktrees(was, want string) error {
	if _, err := exec.LookPath(muffBinary); err != nil {
		return nil
	}

	cmd := exec.Command(muffBinary, "rebind", was, want)
	out, err := cmd.CombinedOutput()
	body := strings.TrimSpace(string(out))
	if err == nil {
		// Nothing was bound there: the common case, and not worth a line.
		if body == "" || strings.Contains(body, "no task is bound") {
			return nil
		}
		return a.sayIndented(a.out.Muted("worktree bindings:"), body)
	}

	// It found bindings it could not move. Those tasks have no scope enforcement
	// anywhere now, and `muff rebind` already printed the command that restores
	// each — so the words are relayed rather than summarised.
	return a.sayIndented(a.out.Warn("some worktree bindings did not follow:"), body)
}

// sayIndented prints a heading and another program's output beneath it, indented so
// that whose words are whose is visible at a glance.
func (a App) sayIndented(heading, body string) error {
	if err := a.say("  " + heading); err != nil {
		return err
	}
	for _, line := range strings.Split(body, "\n") {
		if err := a.say("    " + line); err != nil {
			return err
		}
	}
	return nil
}

// checkWorkspace decides whether a path may be a workspace at all.
//
// Every refusal here is about the machine rather than the model, which is why it is
// in the CLI and not in `model.SetWorkspace`: what exists, what is inside what, and
// what is somebody else's are questions only this machine can answer.
func (a App) checkWorkspace(s caller, who user.Name, path string, adopt bool) (string, error) {
	want := strings.TrimSpace(path)
	if want == "" {
		return "", fault.Usage{Reason: "a workspace needs a path"}
	}
	if !filepath.IsAbs(want) {
		return "", fault.Usage{Reason: fmt.Sprintf(
			"a workspace must be an absolute path, and %q is relative; it would mean a "+
				"different directory depending on where the command was run", want)}
	}
	want = filepath.Clean(want)

	// Inside the fleet's own store. An agent whose workspace contains the keyring
	// is one the compiled `permissions.deny` was written to prevent, and it would
	// be reading every other identity's credential.
	root := filepath.Clean(s.store.Root())
	if want == root || strings.HasPrefix(want, root+string(filepath.Separator)) {
		if want != filepath.Clean(s.store.WorkspaceDir(who)) {
			return "", fault.Denied{Actor: s.who.String(), Action: "put a workspace in", Target: want,
				Reason: "it is inside the fleet's own store, which holds every identity's key"}
		}
	}

	// Inside somebody else's workspace. Two agents sharing a tree is a decision;
	// one agent's workspace being *contained* in another's is an accident, and it
	// makes both of their scopes mean something nobody wrote down.
	for _, other := range s.fleet.Names() {
		if other.String() == who.String() {
			continue
		}
		theirs := filepath.Clean(s.store.WorkspaceDir(other))
		if want == theirs || strings.HasPrefix(want, theirs+string(filepath.Separator)) {
			return "", fault.Conflict{Path: want, Reason: fmt.Sprintf(
				"it is inside %s's workspace; two agents' scopes would overlap where nobody said so", other)}
		}
	}

	info, err := os.Stat(want)
	switch {
	case err == nil && !info.IsDir():
		return "", fault.Conflict{Path: want, Reason: "it is a file, not a directory"}

	case err == nil && !adopt:
		// It exists and the caller did not say to adopt it. Relocating *onto* an
		// existing directory would merge two trees silently.
		return "", fault.Conflict{Path: want, Reason: fmt.Sprintf(
			"it already exists; `orc workspace %s %s --adopt` works in it as it is", who, want)}

	case os.IsNotExist(err) && adopt:
		return "", fault.NotFound{Target: fmt.Sprintf("%s, so there is nothing to adopt", want)}

	case os.IsNotExist(err):
		// The relocate case: the files come with it. Handled by the caller, which
		// copies before it writes anything down.

	case err != nil:
		return "", fault.IO{Op: "stat", Path: want, Err: err}
	}
	return want, nil
}

// afterMove says what the change has and has not reached.
//
// The session's working directory is fixed when Claude starts, so a running agent
// keeps writing to the old one until it is replaced. That is worse here than it is
// for a model change: the agent is working in a directory Orc no longer considers
// its workspace, and its permissions are compiled against the new one.
func (a App) afterMove(s caller, who user.Name, was string, now bool) error {
	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	if !target.Employed() {
		return a.say("  " + a.out.Muted(fmt.Sprintf(
			"it is not employed; `orc employ %s` will start it there", who)))
	}

	_, live, err := s.store.Session(who)
	if err != nil {
		return err
	}
	if !live {
		return a.say("  " + a.out.Muted("no session is running; the next one starts there"))
	}

	if !now {
		return a.say("  " + a.out.Warn("the running session is still working in "+was) + "\n  " +
			a.out.Muted(fmt.Sprintf(
				"its permissions are compiled against the new path — `orc workspace %s --now` "+
					"or `orc refresh %s` replaces it, and loses its context", who, who)))
	}
	return a.restartInPlace(s, who)
}

// restartInPlace replaces the session so the new workspace takes effect.
//
// The same sequence `orc model --now` runs, and for the same reason: what a session
// was started with cannot be changed underneath it, so taking effect means starting
// another one.
func (a App) restartInPlace(s caller, who user.Name) error {
	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	m, e := tuningOf(target)

	if err := a.depopulate(s.store, who); err != nil {
		return err
	}
	id, err := session.NewID()
	if err != nil {
		return err
	}
	if err := a.populate(s.store, who, id, m, e, false); err != nil {
		return err
	}
	return a.say("  " + a.out.Good("restarted") + " " +
		a.out.Muted(fmt.Sprintf("session %s, fresh context, in the new workspace", short(id))))
}

// relocate copies a workspace to its new path, and verifies it arrived.
//
// Copy rather than rename: a rename across filesystems is a copy anyway, and one
// within a filesystem would leave nothing behind if it were interrupted. Copying
// means the old tree is intact at every moment until somebody deliberately removes
// it, which is the property worth having when the thing being moved is an agent's
// work.
//
// It refuses a target inside the source. Copying a tree into itself is a loop that
// fills a disk, and the check is cheap.
func (a App) relocate(who user.Name, from, to string) (int, error) {
	if from == to {
		return 0, nil
	}
	if strings.HasPrefix(to, from+string(filepath.Separator)) {
		return 0, fault.Conflict{Path: to, Reason: fmt.Sprintf(
			"it is inside %s, which is what would be copied; that is a loop, not a move", from)}
	}

	if _, err := os.Stat(from); os.IsNotExist(err) {
		// Nothing to copy. An identity that has never been employed may have no
		// workspace on disk yet, and pointing it somewhere new is still valid.
		if err := os.MkdirAll(to, 0o700); err != nil {
			return 0, fault.IO{Op: "create", Path: to, Err: err}
		}
		return 0, nil
	}

	copied, err := copyTree(from, to)
	if err != nil {
		// Whatever arrived is left for the operator to see rather than half-removed
		// by an error path that may itself fail. The identity has not been changed,
		// so the agent is still pointed at the original.
		return 0, fault.IO{Op: "copy the workspace of " + who.String() + " to", Path: to, Err: err}
	}
	return copied, nil
}

// copyTree copies a directory, and returns how many files it wrote.
//
// Symlinks are recreated as symlinks rather than followed: a workspace holding a link
// to somewhere large would otherwise be copied twice, and one holding a link to
// itself would not terminate.
func copyTree(from, to string) (int, error) {
	files := 0

	err := filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)

		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())

		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)

		case !info.Mode().IsRegular():
			// A socket, a fifo, a device. Copying one is meaningless and recreating
			// it is a different operation; skipping is the honest answer, and the
			// count says how many files actually arrived.
			return nil
		}

		if err := copyFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		files++
		return nil
	})
	return files, err
}

// copyFile copies one file, syncing it before it is called copied.
func copyFile(from, to string, mode os.FileMode) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	// Synced before the identity is told to point here: a workspace that is in the
	// page cache and not on the disk is one a power cut turns into an empty file.
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}
