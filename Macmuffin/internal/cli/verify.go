package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"orc/common/fault"
	"orc/macmuffin/internal/render"
	"orc/macmuffin/internal/repo"
	"orc/macmuffin/internal/scope"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/style"
	"orc/macmuffin/internal/task"
)

// verify walks the store and reports what is wrong, without changing anything.
//
// A store several unsupervised agents write to needs a way to answer "is this
// healthy?" that is not "read the source". It is additive: nothing depends on
// it, and it never repairs — an automatic repair of damage nobody has understood
// is how one bad file becomes many.
//
// It reads everything it can and reports what it cannot, rather than stopping at
// the first problem. A checker that gives up on the first bad file tells you
// about one problem when you wanted the list.
func (a App) verify(args []string) error {
	if len(args) != 0 {
		return fault.Usage{Reason: fmt.Sprintf("verify takes no arguments, got %d", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// notes are things worth saying that are not damage. They are printed and
	// do not touch the exit code: a store on a machine with no fleet is
	// healthy, and a check that failed there would be one nobody could keep
	// green, which is a check people learn to ignore.
	var notes []string
	note := func(format string, args ...any) {
		notes = append(notes, fmt.Sprintf(format, args...))
	}

	var counted []tally
	count := func(what string, n int, note string) {
		counted = append(counted, tally{what: what, n: n, note: note})
	}

	live, err := s.checkTasks(report, count)
	if err != nil {
		return err
	}
	if err := s.checkBindings(live, report, count); err != nil {
		return err
	}
	if err := s.checkOutbox(report, count); err != nil {
		return err
	}
	if err := s.checkTombstones(live, report, count); err != nil {
		return err
	}
	s.checkIdentity(note)

	table, err := render.Draw(summary(s.store.Root(), counted, len(problems)), s.paint, a.width())
	if err != nil {
		return err
	}
	if err := a.write(table); err != nil {
		return err
	}

	for _, n := range notes {
		if err := a.say("  " + s.paint.Muted("· "+n)); err != nil {
			return err
		}
	}

	if len(problems) == 0 {
		return nil
	}
	if err := a.say(s.paint.Broken(fmt.Sprintf("✗ %s:", problemsWord(len(problems))))); err != nil {
		return err
	}
	for _, p := range problems {
		if err := a.say("  " + s.paint.Warn(p)); err != nil {
			return err
		}
	}
	// A damaged store is a real failure, so the exit code says so and a script
	// can branch on it.
	return fault.Conflict{Path: s.store.Root(), Reason: fmt.Sprintf("%s found", problemsWord(len(problems)))}
}

// report is what each check calls to record a problem.
type report func(format string, args ...any)

// checkTasks folds every journal and returns the tasks that loaded, so the later
// checks can tell a dangling reference from one they simply could not resolve.
func (s session) checkTasks(bad report, count tallier) (map[string]task.Task, error) {
	names, err := s.store.Names()
	if err != nil {
		return nil, err
	}

	live := make(map[string]task.Task, len(names))
	counts := struct{ ok, drafts, done int }{}

	for _, name := range names {
		got, skipped, err := s.store.Inspect(name)
		if err != nil {
			bad("%s: will not load: %v", name, err)
			continue
		}
		live[name.String()] = got
		counts.ok++
		if !got.Pooled() {
			counts.drafts++
		}
		if got.Completed() {
			counts.done++
		}

		// Recovered, not lost — but a store accumulating these is a store
		// something keeps killing, and that is worth saying out loud.
		if skipped > 0 {
			bad("%s: %d bytes at the end of the journal were left by an interrupted write", name, skipped)
		}
		// The name in the record must match the directory it sits in, or a
		// lookup by name and a walk of the store would disagree.
		if !got.Name().Equal(name) {
			bad("%s: the record calls itself %s", name, got.Name())
		}
		if got.Scoped() {
			if _, err := scope.Parse(got.Scope()); err != nil {
				bad("%s: the scope will not parse, so the hook cannot enforce it: %v", name, err)
			}
		}
		if wt, bound := got.Worktree(); bound {
			if _, found, err := s.store.Bound(wt); err != nil {
				bad("%s: its worktree binding will not read: %v", name, err)
			} else if !found {
				bad("%s: says it is bound to %s, but no binding is filed there", name, wt)
			}
		}
	}

	count("tasks", counts.ok, fmt.Sprintf("%d draft · %d completed", counts.drafts, counts.done))
	return live, nil
}

// checkBindings looks at worktree bindings from the other side: a binding
// pointing at a task that is gone would make the hook enforce a scope nobody
// owns.
func (s session) checkBindings(live map[string]task.Task, bad report, count tallier) error {
	bindings, damaged, err := s.store.Bindings()
	if err != nil {
		return err
	}
	for _, name := range damaged {
		bad("worktree binding %s will not decode", name)
	}

	for _, b := range bindings {
		got, known := live[b.Task.String()]
		if !known {
			bad("%s is bound to %s, which is not a task in this store", b.Path, b.Task)
			continue
		}
		// The task and the binding must agree about each other. One direction
		// is checked above; this is the other.
		if wt, bound := got.Worktree(); !bound || wt != b.Path {
			bad("%s is bound to %s, but %s does not say so", b.Path, b.Task, b.Task)
		}
		if _, err := repo.At(b.Path); err != nil {
			bad("%s is bound to %s, but is no longer a worktree: %v", b.Path, b.Task, err)
		}
	}

	count("worktrees", len(bindings), "")
	return nil
}

// checkOutbox reports notices that are waiting, and the ones that have given up.
func (s session) checkOutbox(bad report, count tallier) error {
	pending, err := s.store.Pending()
	if err != nil {
		return err
	}
	damaged, err := s.store.Damaged()
	if err != nil {
		return err
	}
	for _, name := range damaged {
		bad("outbox entry %s will not decode, so it will never be delivered", name)
	}

	stuck := 0
	for _, n := range pending {
		if n.Exhausted() {
			stuck++
			bad("a notice to %s gave up after %d attempts: %s",
				strings.Join(recipients(n), ", "), n.Attempts, oneLine(n.LastErr))
		}
	}

	count("outbox", len(pending), fmt.Sprintf("%d waiting · %d stuck", len(pending)-stuck, stuck))
	return nil
}

// checkIdentity says whether anything confirmed who the caller is.
//
// It is not damage, and it does not fail the check — a store on a machine with
// no fleet is healthy. But every permission in `policy` rests on the caller
// being who they claim, so a health report that never mentioned an unchecked
// claim would be leaving out the load-bearing assumption.
func (s session) checkIdentity(note report) {
	if s.verified {
		return
	}
	note("nobody confirmed you are %s: no orc to ask, so every permission here "+
		"rests on an unchecked claim", s.who)
}

// checkTombstones reads the deletion log. A tombstone for a task that is still
// there is a delete that was interrupted after it was recorded and before the
// directory went — which is exactly the case the log exists to make visible.
func (s session) checkTombstones(live map[string]task.Task, bad report, count tallier) error {
	stones, skipped, err := s.store.Tombstones()
	if err != nil {
		bad("the deletion log will not read: %v", err)
		return nil
	}
	if skipped > 0 {
		bad("%d bytes at the end of the deletion log were left by an interrupted write", skipped)
	}
	for _, stone := range stones {
		if _, still := live[stone.Task.String()]; still {
			bad("%s deleted %s, but it is still in the store; the delete did not finish",
				stone.By, stone.Task)
		}
	}
	count("deletions", len(stones), "")
	return nil
}

// tally is one line of the summary.
type tally struct {
	what string
	n    int
	note string
}

type tallier func(what string, n int, note string)

// summary lays the counts out as a table, with the verdict in the top bar. The
// verdict is a word as well as a colour, because colour is a layer and never
// information.
func summary(root string, counted []tally, problems int) render.Table {
	verdict := "✓ no problems found"
	paint := func(p style.Palette, s string) string { return p.Done(s) }
	if problems > 0 {
		verdict = fmt.Sprintf("✗ %s", problemsWord(problems))
		paint = func(p style.Palette, s string) string { return p.Broken(s) }
	}

	rows := make([][]render.Cell, 0, len(counted))
	for _, t := range counted {
		rows = append(rows, []render.Cell{
			render.Plain(t.what),
			render.Plain(strconv.Itoa(t.n)),
			render.Painted(t.note, func(p style.Palette, s string) string { return p.Muted(s) }),
		})
	}

	return render.Table{
		Title:     "verify · " + root,
		Note:      verdict,
		NotePaint: paint,
		Columns: []render.Column{
			{Header: "what", Align: render.Left, Min: 9},
			{Header: "count", Align: render.Right, Min: 5},
			{Header: "detail", Align: render.Left, Weight: 1, Min: 0},
		},
		Rows:  rows,
		Empty: "an empty store",
	}
}

func recipients(n store.Notice) []string {
	out := make([]string, 0, len(n.To))
	for _, who := range n.To {
		out = append(out, who.String())
	}
	sort.Strings(out)
	return out
}

// problemsWord counts problems in words, so the summary reads as a sentence.
func problemsWord(n int) string {
	if n == 1 {
		return "1 problem"
	}
	return fmt.Sprintf("%d problems", n)
}

// oneLine keeps a recorded error from breaking the report's shape.
func oneLine(s string) string {
	got := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if got == "" {
		return "no reason recorded"
	}
	return got
}
