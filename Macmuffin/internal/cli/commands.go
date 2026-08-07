package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/control"
	"orc/macmuffin/internal/notify"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/render"
	"orc/macmuffin/internal/repo"
	"orc/macmuffin/internal/scope"
	"orc/macmuffin/internal/task"
	"orc/macmuffin/internal/view"
)

// create makes a new draft task.
//
// Creation does not publish: a draft is visible only to its author and can be
// shaped freely, which is what makes `create` cheap. `push` is what exposes it.
func (a App) create(args []string) error {
	var sub string
	rest, err := flagged(args, options{values: map[string]*string{"--sub": &sub}})
	if err != nil {
		return err
	}
	// `create <task> --sub <name>` is the documented way to add a subtask, so
	// the two spellings share a command rather than a name.
	if sub != "" {
		return a.sub(rest, sub)
	}
	args = rest

	if err := exactly(args, 3, "create takes a task name, a priority, and a difficulty"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}
	priority, err := task.ParsePriority(args[1])
	if err != nil {
		return err
	}
	difficulty, err := task.ParseDifficulty(args[2])
	if err != nil {
		return err
	}

	got, err := s.store.Create(name, s.who, priority, difficulty)
	if err != nil {
		return err
	}

	if err := a.say(fmt.Sprintf("created draft %s  %s", a.out.Task(got.Name().String()),
		a.out.Muted(fmt.Sprintf("(priority %s, difficulty %s)",
			got.Priority().Label(), got.Difficulty().Label())))); err != nil {
		return err
	}
	// The next step is not obvious, and a task without a scope can do almost
	// nothing, so the command that unblocks it is printed rather than left to
	// be discovered.
	return a.say("give it a scope before pushing it:\n  " + a.out.Command(fmt.Sprintf("muff scope %s <paths...>", got.Name())))
}

// push publishes a draft to the pool. One-way.
func (a App) push(args []string) error {
	if err := exactly(args, 1, "push takes one task name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Push); err != nil {
			return task.Event{}, err
		}
		return task.Push(s.who, s.store.Now())
	})
	if err != nil {
		return err
	}

	return a.say(fmt.Sprintf("pushed %s to the pool — anyone can claim it now", a.out.Task(got.Name().String())))
}

// claim takes an unowned task.
//
// This is the compare-and-set the whole tool rests on: two agents scanning the
// same pool will claim within microseconds of each other, and the second must
// lose loudly, naming the winner. The decision runs under the store's lock, so
// it can never be made against a task that has already moved.
func (a App) claim(args []string) error {
	if err := exactly(args, 1, "claim takes one task name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	alreadyMine := false
	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Claim); err != nil {
			return task.Event{}, err
		}
		// Claiming a task you already own is a no-op, reported as one rather
		// than as a conflict: the caller asked for a state the task is already
		// in, and telling them off for that helps nobody.
		if owner, owned := current.Owner(); owned && owner.String() == s.who.String() {
			alreadyMine = true
			return task.Event{}, nil
		}
		return task.Claim(s.who, s.store.Now())
	})
	if err != nil {
		return err
	}

	if alreadyMine {
		return a.say(fmt.Sprintf("%s is already yours", a.out.Task(got.Name().String())))
	}
	if err := a.say(fmt.Sprintf("claimed %s", a.out.Task(got.Name().String()))); err != nil {
		return err
	}
	if !got.Scoped() {
		return a.say("it has no scope yet:\n  " + a.out.Command(fmt.Sprintf("muff scope %s <paths...>", got.Name())))
	}
	return nil
}

// pool shows the board: every task the caller can see, in board order.
func (a App) pool(args []string) error {
	all, asJSON := false, false
	rest, err := flagged(args, switches(map[string]*bool{"--all": &all, "--json": &asJSON}))
	if err != nil {
		return err
	}
	if err := exactly(rest, 0, "pool takes no arguments"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	scope := view.Active
	if all {
		scope = view.All
	}
	p, err := view.Load(s.store, s.who, scope)
	if err != nil {
		return err
	}
	// Damage is reported and stepped over. One unreadable task must not hide
	// the rest of the pool, but a board that quietly shows nine of ten is worse
	// than one that shows nine and says so.
	for _, d := range p.Damaged() {
		a.note("task %s could not be read and is not shown: %v", a.err.Task(d.Name.String()), d.Err)
	}

	if asJSON {
		return a.emitJSON(tasksJSON(p.Tasks()))
	}
	out, err := render.Board(p, scope, s.palette(), a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// info shows one task in full.
func (a App) info(args []string) error {
	asJSON := false
	rest, err := flagged(args, switches(map[string]*bool{"--json": &asJSON}))
	if err != nil {
		return err
	}
	args = rest
	if err := exactly(args, 1, "info takes one task name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	got, err := view.Find(s.store, s.who, name)
	if err != nil {
		return err
	}
	if asJSON {
		shape := taskJSON(got, true)
		// The prose travels with `info` and not with the board. A description that
		// cannot be read costs the text and not the answer: the rest of the card is
		// still what the caller asked for, and `muff describe` is where a broken
		// one is diagnosed.
		if got.Described() {
			if text, found, err := s.store.Description(name); err == nil && found {
				shape.Description = text
			} else if err != nil {
				a.note("the description of %s could not be read: %v", name, err)
			}
		}
		return a.emitJSON(shape)
	}
	out, err := render.Card(got, s.palette(), a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// scope declares the editable surface, and is what turns a stub into a task
// that can be worked on.
//
// It replaces any previous scope rather than adding to it, so `muff scope`
// always states the whole surface — a reader should never have to accumulate a
// history to know what a task may touch.
func (a App) scope(args []string) error {
	if len(args) < 2 {
		return fault.Usage{Reason: "scope takes a task name and at least one path"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	set, err := scope.Parse(args[1:])
	if err != nil {
		return err
	}
	// A bare directory name is normalised to a directory entry when it really
	// is one, because a caller writing `muff scope x internal/tree` means the
	// directory, and making them remember the slash is a papercut with no
	// safety value.
	entries := s.app.directoriesAsPrefixes(set.Entries())
	// A scope is a set of paths, and nothing here can know which tree they will be
	// measured against: the person who runs the task may be working somewhere else
	// entirely. So an entry that is not there is a caution and not a refusal.
	//
	// Refusing would be wrong for the case that produced this. An architect with a
	// freshly provisioned workspace wrote three tasks scoped to a repository it was
	// not standing in — every path correct, none of them present — and a refusal
	// would have stopped the tasks being written at all. Saying nothing was also
	// wrong: the scopes only mean something once the owner has a workspace at that
	// repository, and nobody was told.
	a.cautionAbsent(entries)

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Scope); err != nil {
			return task.Event{}, err
		}
		return task.Scope(s.who, s.store.Now(), entries)
	})
	if err != nil {
		return err
	}

	if err := a.say(fmt.Sprintf("%s may now edit:", a.out.Task(got.Name().String()))); err != nil {
		return err
	}
	for _, entry := range got.Scope() {
		if err := a.say("  " + a.out.Path(entry)); err != nil {
			return err
		}
	}
	return nil
}

// cautionAbsent says which scope entries are not in the caller's own tree.
//
// On stderr, so a script reading the scope back off stdout is unaffected, and
// worded as a fact about *here* rather than a verdict about the paths: they may
// be exactly right for the machine the work will be done on.
func (a App) cautionAbsent(entries []string) {
	var missing []string
	for _, entry := range entries {
		if !a.exists(strings.TrimSuffix(entry, "/")) {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return
	}
	is := "is"
	if len(missing) > 1 {
		is = "are"
	}
	a.note("%s %s not in %s.", a.err.Path(strings.Join(missing, " ")), is, a.here())
	a.note("  a scope is measured from whoever runs the task, so this may be right —")
	a.note("  but it means nothing until its owner has a workspace where these exist.")
}

// exists reports whether a path is in the caller's tree at all, directory or not.
//
// isDirectory answers a narrower question — it decides whether to add a trailing
// slash — and reading a false from it as "absent" would report every ordinary
// file in a scope as missing.
func (a App) exists(rel string) bool {
	root := a.Cwd
	if root == "" {
		got, err := os.Getwd()
		if err != nil {
			return true // nothing to compare against; say nothing rather than guess
		}
		root = got
	}
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

// here names the directory the caution was measured in, because "not found" with
// no root is a sentence somebody has to go and work out.
func (a App) here() string {
	if a.Cwd != "" {
		return a.Cwd
	}
	if got, err := os.Getwd(); err == nil {
		return got
	}
	return "this directory"
}

// directoriesAsPrefixes turns an entry that names an existing directory into a
// directory entry, so the trailing slash is optional at the command line while
// staying required in the matcher.
func (a App) directoriesAsPrefixes(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry, "/") && a.isDirectory(entry) {
			entry += "/"
		}
		out = append(out, entry)
	}
	return out
}

func (a App) isDirectory(rel string) bool {
	root := a.Cwd
	if root == "" {
		got, err := os.Getwd()
		if err != nil {
			return false
		}
		root = got
	}
	info, err := os.Stat(filepath.Join(root, rel))
	return err == nil && info.IsDir()
}

// worktree binds a task to a git worktree of the main repository.
//
// The binding is what lets the hook answer "which task is in force?" from the
// working directory alone, without an environment variable each agent has to
// remember to set.
func (a App) worktree(args []string) error {
	if err := exactly(args, 2, "worktree takes a task name and a path"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	wt, err := repo.At(args[1])
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Worktree); err != nil {
			return task.Event{}, err
		}
		return task.BindWorktree(s.who, s.store.Now(), wt.Root())
	})
	if err != nil {
		return err
	}
	if err := s.store.Bind(name, wt.Root(), wt.Main()); err != nil {
		return err
	}

	kind := "worktree"
	if wt.Linked() {
		kind = "linked worktree"
	}
	return a.say(fmt.Sprintf("%s is bound to the %s at %s",
		a.out.Task(got.Name().String()), kind, a.out.Path(wt.Root())))
}

// checkScope is the contract Anno calls: exit 0 in scope, 9 outside, and
// nothing on stdout either way.
//
// It prints its reasoning to stderr, where a human debugging a refusal can see
// it and a program checking the status can ignore it. When no task is in force
// nothing is enforced and it exits 0 — an agent that never opted in is never
// blocked.
func (a App) checkScope(args []string) error {
	if len(args) < 1 {
		return fault.Usage{Reason: "check-scope takes at least one path"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	current, wt, ok, err := s.inForce()
	if err != nil {
		return err
	}
	if !ok {
		a.note("no task is in force, so nothing is enforced")
		return nil
	}
	if !current.Scoped() {
		a.note("%s has no scope, so nothing is enforced", a.err.Task(current.Name().String()))
		return nil
	}

	set, err := scope.Parse(current.Scope())
	if err != nil {
		return err
	}
	for _, target := range args {
		rel, err := scope.Resolve(wt, target)
		if err != nil {
			return err
		}
		inside, err := set.Matches(rel)
		if err != nil {
			return err
		}
		if !inside {
			return fault.Scope{Path: target, Task: current.Name().String(), InScope: current.Scope()}
		}
	}
	return nil
}

// inForce answers "which task is this agent working on?", in the order §8.1
// sets out: an explicit environment variable, then the worktree the working
// directory sits in, then nothing.
func (s session) inForce() (task.Task, string, bool, error) {
	if raw, set := s.app.Env(EnvTask); set && strings.TrimSpace(raw) != "" {
		name, err := task.ParseName(raw)
		if err != nil {
			return task.Task{}, "", false, err
		}
		got, err := s.store.Load(name)
		if err != nil {
			return task.Task{}, "", false, err
		}
		root, _ := got.Worktree()
		if root == "" {
			root = s.app.cwd()
		}
		return got, root, true, nil
	}

	wt, ok, err := repo.Find(s.app.cwd())
	if err != nil || !ok {
		return task.Task{}, "", false, err
	}
	bound, found, err := s.store.Bound(wt.Root())
	if err != nil || !found {
		return task.Task{}, "", false, err
	}
	got, err := s.store.Load(bound.Task)
	if err != nil {
		return task.Task{}, "", false, err
	}
	return got, wt.Root(), true, nil
}

// status reports how the work is going.
//
// It prints the previous value beside the new one, so a change is visible: an
// agent that set a task to "broken" wants to know it was "nominal" a moment
// ago, and a `status` that printed nothing would leave that invisible.
func (a App) status(args []string) error {
	if err := exactly(args, 2, "status takes a task name and a value"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}
	want, err := task.ParseStatus(args[1])
	if err != nil {
		return err
	}

	var before task.Status
	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Status); err != nil {
			return task.Event{}, err
		}
		before = current.Status()
		if before == want {
			// Already there. Reported as a no-op rather than journaled, so the
			// history stays a record of changes rather than of repetitions.
			return task.Event{}, nil
		}
		return task.SetStatus(s.who, s.store.Now(), want)
	})
	if err != nil {
		return err
	}

	if before == got.Status() {
		return a.say(fmt.Sprintf("%s is already %s",
			a.out.Task(got.Name().String()), paintStatus(a.out, got.Status())))
	}
	return a.say(fmt.Sprintf("%s: %s → %s", a.out.Task(got.Name().String()),
		a.out.Muted(before.Label()), paintStatus(a.out, got.Status())))
}

// sub adds a subtask, which is what `create <task> --sub <name>` reaches.
func (a App) sub(args []string, subName string) error {
	if err := exactly(args, 1, "create --sub takes a task name and a subtask name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}
	sub, err := task.ParseName(subName)
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.SubAdd); err != nil {
			return task.Event{}, err
		}
		return task.AddSub(s.who, s.store.Now(), sub)
	})
	if err != nil {
		return err
	}

	done, total := got.Progress()
	return a.say(fmt.Sprintf("added %s to %s  %s", a.out.Task(sub.String()),
		a.out.Task(got.Name().String()), a.out.Muted(fmt.Sprintf("(%d/%d)", done, total))))
}

// complete marks a task, or one of its subtasks, done.
//
// A task with unfinished subtasks refuses and lists them. `--force` completes
// anyway and journals what was skipped: the point of a tracker is that
// shortcuts stay visible, so the override exists and leaves a mark.
func (a App) complete(args []string) error {
	var sub string
	force := false
	rest, err := flagged(args, options{
		switches: map[string]*bool{"--force": &force},
		values:   map[string]*string{"--sub": &sub},
	})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "complete takes one task name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(rest[0])
	if err != nil {
		return err
	}

	if sub != "" {
		if force {
			return fault.Usage{Reason: "--force applies to a whole task, not to one subtask"}
		}
		return s.completeSub(name, sub)
	}

	var skipped []task.Name
	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Complete); err != nil {
			return task.Event{}, err
		}
		skipped = nil
		for _, outstanding := range current.Unfinished() {
			skipped = append(skipped, outstanding.Name())
		}
		// The skipped list is only meaningful on a forced completion: it is the
		// mark the override leaves. An unforced one carries none, and the fold
		// refuses it with a conflict naming the count — not with a bug report.
		if !force {
			return task.Complete(s.who, s.store.Now(), false, nil)
		}
		return task.Complete(s.who, s.store.Now(), true, skipped)
	})
	if err != nil {
		// The refusal names what is outstanding, so the caller can finish it or
		// decide to override rather than guessing which it was.
		if len(skipped) > 0 && !force {
			a.note("outstanding: %s", joinNames(skipped))
			a.note("finish them, or `%s` to complete anyway",
				a.err.Command(fmt.Sprintf("muff complete %s --force", name)))
		}
		return err
	}

	if force && len(skipped) > 0 {
		if err := a.say(fmt.Sprintf("completed %s, skipping %s",
			a.out.Task(got.Name().String()), a.out.Warn(plural2(len(skipped), "subtask")))); err != nil {
			return err
		}
		return a.say("  " + a.out.Warn(joinNames(skipped)))
	}
	return a.say(fmt.Sprintf("completed %s", a.out.Done(got.Name().String())))
}

// completeSub ticks one item off the checklist.
func (s session) completeSub(name task.Name, subName string) error {
	sub, err := task.ParseName(subName)
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.SubDone); err != nil {
			return task.Event{}, err
		}
		return task.DoneSub(s.who, s.store.Now(), sub)
	})
	if err != nil {
		return err
	}

	done, total := got.Progress()
	if err := s.app.say(fmt.Sprintf("%s: %s done  %s", s.app.out.Task(got.Name().String()),
		s.app.out.Done(sub.String()), s.app.out.Muted(fmt.Sprintf("(%d/%d)", done, total)))); err != nil {
		return err
	}
	if done == total {
		return s.app.say("every subtask is finished — " +
			s.app.out.Command(fmt.Sprintf("muff complete %s", got.Name())))
	}
	return nil
}

// deleteTask removes a task, or one of its subtasks.
//
// A whole-task deletion is the only irreversible operation in the tool, so it
// prints what it will destroy before asking: the collaborators lose the task
// without warning otherwise, and the subtask count is the difference between
// "a stub I made by mistake" and "a week of somebody's checklist".
func (a App) deleteTask(args []string) error {
	var sub string
	yes := false
	rest, err := flagged(args, options{
		switches: map[string]*bool{"--yes": &yes},
		values:   map[string]*string{"--sub": &sub},
	})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "delete takes one task name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(rest[0])
	if err != nil {
		return err
	}

	if sub != "" {
		return s.deleteSub(name, sub)
	}

	current, err := view.Find(s.store, s.who, name)
	if err != nil {
		return err
	}
	if err := s.permit(current, policy.Delete); err != nil {
		return err
	}

	if err := a.describeDeletion(current); err != nil {
		return err
	}
	if !yes {
		return fault.Usage{Reason: fmt.Sprintf(
			"nothing was deleted; pass --yes to confirm\n  muff delete %s --yes", current.Name())}
	}

	if err := s.store.Delete(current.Name(), s.who); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("deleted %s", a.out.Task(current.Name().String())))
}

// describeDeletion prints what is about to be destroyed.
func (a App) describeDeletion(t task.Task) error {
	done, total := t.Progress()
	if err := a.say(fmt.Sprintf("delete %s (%s, %s):", a.out.Alarm(t.Name().String()),
		a.out.Muted(t.Life().String()), paintStatus(a.out, t.Status()))); err != nil {
		return err
	}
	if total > 0 {
		if err := a.say(fmt.Sprintf("  %s, %d finished", plural2(total, "subtask"), done)); err != nil {
			return err
		}
	}
	if with := t.Collaborators(); len(with) > 0 {
		// Named, not counted: these are the agents who lose the task without
		// warning, and a number does not tell the caller who to mail.
		if err := a.say("  collaborators who lose it: " + a.out.Agent(strings.Join(user.Names(with), ", "))); err != nil {
			return err
		}
	}
	if wt, bound := t.Worktree(); bound {
		if err := a.say("  worktree binding: " + a.out.Path(wt)); err != nil {
			return err
		}
	}
	return nil
}

// deleteSub removes one item from the checklist. It is not irreversible in the
// way a task is — the task survives — so it needs no confirmation.
func (s session) deleteSub(name task.Name, subName string) error {
	sub, err := task.ParseName(subName)
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.SubDelete); err != nil {
			return task.Event{}, err
		}
		return task.DeleteSub(s.who, s.store.Now(), sub)
	})
	if err != nil {
		return err
	}

	done, total := got.Progress()
	return s.app.say(fmt.Sprintf("removed %s from %s  %s", s.app.out.Task(sub.String()),
		s.app.out.Task(got.Name().String()), s.app.out.Muted(fmt.Sprintf("(%d/%d)", done, total))))
}

func joinNames(names []task.Name) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n.String()
	}
	return strings.Join(out, ", ")
}

func plural2(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// assign gives a task to an agent the caller controls, and tells them.
//
// Two questions have to be answered, and they belong to different tools. "May
// you direct this agent?" is Orc's — it owns the fleet — and is asked first,
// because being refused for a reason Macmuffin cannot even see should not cost
// a store write. "May this task be given away, and to them?" is Macmuffin's,
// and is the same question `claim` asks.
//
// The control check fails closed. Orc missing, unreachable, or unable to answer
// is not permission: the whole point of the condition is that assignment is
// restricted, and a restriction that evaporates when a peer is down is not one.
func (a App) assign(args []string) error {
	if err := exactly(args, 2, "assign takes an agent and a task name"); err != nil {
		return err
	}
	who, err := user.Parse(args[0])
	if err != nil {
		return err
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	if who.String() == s.who.String() {
		// Not a control failure — Orc says nobody controls themselves — but the
		// caller means `claim`, and saying so is more use than "you do not
		// control yourself".
		return fault.Usage{Reason: fmt.Sprintf(
			"assign gives a task to someone else; take it yourself with `muff claim %s`", args[1])}
	}
	if err := s.controls(who); err != nil {
		return err
	}

	name, err := s.resolve(args[1])
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Assign); err != nil {
			return task.Event{}, err
		}
		return task.Assign(s.who, s.store.Now(), who)
	})
	if err != nil {
		return err
	}

	if err := a.say(fmt.Sprintf("assigned %s to %s", a.out.Task(got.Name().String()), a.out.Agent(who.String()))); err != nil {
		return err
	}
	// The assignment is done; telling them is best-effort, exactly as it is for
	// invite. An agent that owns a task it has not been told about is a smaller
	// problem than an assignment that failed because mail was down.
	return s.announce(got, who, policy.Assign)
}

// controls asks Orc whether the caller may direct the agent.
func (s session) controls(who user.Name) error {
	check := s.app.Control
	if check == nil {
		check = control.Exec
	}
	return check(who)
}

// invite adds a collaborator, and tells them.
func (a App) invite(args []string) error {
	if err := exactly(args, 2, "invite takes an agent and a task name"); err != nil {
		return err
	}
	return a.membership(args[1], args[0], policy.Invite)
}

// kick removes a collaborator, and tells them.
func (a App) kick(args []string) error {
	if err := exactly(args, 2, "kick takes an agent and a task name"); err != nil {
		return err
	}
	return a.membership(args[1], args[0], policy.Kick)
}

// membership is invite and kick, which differ only in the event and the notice.
func (a App) membership(rawTask, rawAgent string, action policy.Action) error {
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(rawTask)
	if err != nil {
		return err
	}
	who, err := user.Parse(rawAgent)
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, action); err != nil {
			return task.Event{}, err
		}
		if action == policy.Invite {
			return task.Invite(s.who, s.store.Now(), who)
		}
		return task.Kick(s.who, s.store.Now(), who)
	})
	if err != nil {
		return err
	}

	verb, prep := "added", "to"
	if action == policy.Kick {
		verb, prep = "removed", "from"
	}
	if err := a.say(fmt.Sprintf("%s %s %s %s", verb, a.out.Agent(who.String()),
		prep, a.out.Task(got.Name().String()))); err != nil {
		return err
	}

	// The membership change is done. Telling them about it is a separate,
	// best-effort step: a notice that cannot be sent is queued and retried by
	// whichever agent next touches the store, and never fails the change.
	return s.announce(got, who, action)
}

// announce queues and attempts the notice, reporting a failure as a warning.
func (s session) announce(t task.Task, who user.Name, action policy.Action) error {
	courier, err := notify.New(s.store, s.app.Notify)
	if err != nil {
		return err
	}

	switch action {
	case policy.Kick:
		err = courier.Removed(t, s.who, who)
	case policy.Assign:
		err = courier.Assigned(t, s.who, who)
	default:
		err = courier.Joined(t, s.who, who)
	}

	var undeliverable notify.Undeliverable
	if errors.As(err, &undeliverable) {
		s.app.note("%v", undeliverable)
		s.app.note("it is queued, and the next muff command will try again")
		return nil
	}
	return err
}

// leave drops collaboration. Nobody is notified: the agent leaving already
// knows, and the owner sees it on the board.
func (a App) leave(args []string) error {
	if err := exactly(args, 1, "leave takes one task name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, policy.Leave); err != nil {
			return task.Event{}, err
		}
		return task.Leave(s.who, s.store.Now())
	})
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("left %s", a.out.Task(got.Name().String())))
}
