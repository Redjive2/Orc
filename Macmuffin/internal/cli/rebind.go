package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"orc/common/fault"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/repo"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// rebind follows a directory that moved.
//
// A binding is keyed by the resolved path of a worktree, and the hook looks the
// session's directory up in it to decide which task is in force. Move the
// directory — `orc workspace <agent> <path>` does exactly that — and every binding
// under it addresses somewhere nothing is. The hook then finds no task, concludes
// nothing is in force, and enforces nothing.
//
// That is the worst way for an enforcement mechanism to fail: silently, looking
// exactly like an agent that never opted in. So this exists, and the migration that
// moves a directory is expected to run it.
//
// It is a `muff` command rather than something Orc does, because Orc reaching into
// another tool's store to rewrite its records is how two tools come to disagree
// about a file's format. Orc knows a directory moved; only Macmuffin knows what a
// binding is.
func (a App) rebind(args []string) error {
	dry := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--dry-run" {
			dry = true
			continue
		}
		rest = append(rest, arg)
	}
	if err := exactly(rest, 2, "rebind takes the old directory and the new one"); err != nil {
		return err
	}

	s, err := a.begin()
	if err != nil {
		return err
	}

	// Both resolved the same way bindings are, so that a move between /tmp and
	// /private/tmp — the same directory under two spellings — is recognised as the
	// no-op it is rather than rewriting every binding to an identical path.
	from, to := store.Canonical(rest[0]), store.Canonical(rest[1])
	if from == to {
		return a.say(a.out.Muted("those are the same directory; nothing to rebind"))
	}

	bindings, damaged, err := s.store.Bindings()
	if err != nil {
		return err
	}

	var moved []rebound
	for _, b := range bindings {
		rel, under := beneath(from, b.Path)
		if !under {
			continue
		}
		moved = append(moved, rebound{binding: b, want: filepath.Join(to, rel)})
	}

	if len(moved) == 0 {
		if err := a.say(a.out.Muted(fmt.Sprintf("no task is bound to a worktree under %s", from))); err != nil {
			return err
		}
		return a.sayDamaged(damaged)
	}

	if dry {
		for _, m := range moved {
			if err := a.say(fmt.Sprintf("%s  %s → %s",
				a.out.Task(m.binding.Task.String()), a.out.Path(m.binding.Path), a.out.Path(m.want))); err != nil {
				return err
			}
		}
		if err := a.say(a.out.Muted(fmt.Sprintf(
			"%s would be rebound; nothing has changed", count(len(moved), "binding")))); err != nil {
			return err
		}
		return a.sayDamaged(damaged)
	}

	var stuck []stranded
	for _, m := range moved {
		if err := a.rebindOne(s, m); err != nil {
			stuck = append(stuck, stranded{binding: m.binding, want: m.want, why: err})
			continue
		}
		if err := a.say(fmt.Sprintf("%s is now bound to %s",
			a.out.Task(m.binding.Task.String()), a.out.Path(m.want))); err != nil {
			return err
		}
	}

	if err := a.sayDamaged(damaged); err != nil {
		return err
	}
	if len(stuck) == 0 {
		return nil
	}

	// What could not be rebound is the whole point of the command's output. Each
	// one is a task whose scope is no longer enforced anywhere, and each is named
	// with the command that restores it — because the alternative is an operator
	// reconstructing paths by hand from a list of failures.
	if err := a.say(""); err != nil {
		return err
	}
	if err := a.say(a.out.Warn(fmt.Sprintf(
		"%s could not be rebound, and %s scope is no longer enforced anywhere:",
		count(len(stuck), "binding"), theirOrIts(len(stuck))))); err != nil {
		return err
	}
	for _, st := range stuck {
		if err := a.say(fmt.Sprintf("  %s  %s",
			a.out.Task(st.binding.Task.String()), a.out.Muted(reason(st.why)))); err != nil {
			return err
		}
		if err := a.say("    " + a.out.Muted(fmt.Sprintf("muff worktree %s %s",
			st.binding.Task, st.want))); err != nil {
			return err
		}
	}
	// A non-zero exit, because a caller scripting a migration needs to know the
	// enforcement it was relying on did not survive it.
	return fault.Conflict{
		Reason: fmt.Sprintf("%s did not follow the move", count(len(stuck), "binding")),
	}
}

type rebound struct {
	binding store.Binding
	want    string
}

type stranded struct {
	binding store.Binding
	want    string
	why     error
}

// rebindOne moves one binding, new first.
//
// Bind before Unbind: a crash between them leaves a task bound to both the old and
// the new directory, which is a duplicate the next rebind cleans up. The other order
// leaves it bound to neither, which is the silent failure this command exists to
// prevent.
func (a App) rebindOne(s session, m rebound) error {
	wt, err := repo.At(m.want)
	if err != nil {
		return err
	}

	// Same authority as making the binding in the first place: rebinding is that
	// operation, aimed at where the directory went.
	if _, err := s.store.Apply(m.binding.Task, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Worktree); err != nil {
			return task.Event{}, err
		}
		return task.BindWorktree(s.who, s.store.Now(), wt.Root())
	}); err != nil {
		return err
	}
	if err := s.store.Bind(m.binding.Task, wt.Root(), wt.Main()); err != nil {
		return err
	}
	return s.store.Unbind(m.binding.Path)
}

// beneath reports whether path is at or under dir, and by how much.
//
// String prefixes are not enough: `/a/bc` is not under `/a/b`, and a comparison
// that thought it was would rewrite a binding belonging to a different tree.
func beneath(dir, path string) (string, bool) {
	if path == dir {
		return "", true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func (a App) sayDamaged(damaged []string) error {
	if len(damaged) == 0 {
		return nil
	}
	return a.say(a.out.Warn(fmt.Sprintf(
		"%s could not be read and was left alone: %s",
		count(len(damaged), "binding"), strings.Join(damaged, ", "))))
}

// reason is why one binding did not move, in the words the operator needs.
func reason(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

func theirOrIts(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}
