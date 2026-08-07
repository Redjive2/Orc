package store_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/task"
)

// Ordering exists because nothing else in Macmuffin could express it. A checklist
// orders steps inside one task under one owner; two tasks under two owners had no
// relation at all, so the only way to sequence them was to ask both owners and
// hope. These tests are about the difference between asking and enforcing.

// pooled makes a task somebody owns, ready to be blocked or to block.
func (r *rig) pooled(name, owner string) task.Task {
	r.t.Helper()
	who := r.agent(owner)
	r.create(name, "alice")
	r.apply(name, func(task.Task) (task.Event, error) {
		return task.Scope(r.agent("alice"), r.Now(), []string{"internal/"})
	})
	r.apply(name, func(task.Task) (task.Event, error) { return task.Push(r.agent("alice"), r.Now()) })
	return r.apply(name, func(task.Task) (task.Event, error) { return task.Claim(who, r.Now()) })
}

func (r *rig) block(name string, until ...string) task.Task {
	r.t.Helper()
	var names []task.Name
	for _, u := range until {
		names = append(names, r.name(u))
	}
	return r.apply(name, func(task.Task) (task.Event, error) {
		return task.Block(r.agent("alice"), r.Now(), names)
	})
}

// The whole point. A held task refuses the operations that start and finish it,
// and it refuses them at the store rather than by asking nicely.
func TestAHeldTaskCannotBeCompleted(t *testing.T) {
	r := newRig(t)
	r.pooled("first", "bob")
	r.pooled("second", "carol")
	r.block("second", "first")

	_, err := r.Apply(r.name("second"), func(task.Task) (task.Event, error) {
		return task.Complete(r.agent("carol"), r.Now(), false, nil)
	})
	if err == nil {
		t.Fatal("second completed while it was still waiting for first")
	}
	if !errors.Is(err, fault.ErrConflict) {
		t.Errorf("refusal is %v, want a conflict", err)
	}
	// The refusal has to name what is outstanding and who holds it. An agent
	// told only "blocked" has to go and find out; an agent told "first (bob)"
	// can go and ask bob.
	for _, want := range []string{"first", "bob"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// And a held task cannot be picked up in the first place, which is what "hold
// this until that is done" means to somebody reading the pool.
func TestAHeldTaskCannotBeClaimed(t *testing.T) {
	r := newRig(t)
	r.pooled("first", "bob")
	r.create("second", "alice")
	r.apply("second", func(task.Task) (task.Event, error) {
		return task.Scope(r.agent("alice"), r.Now(), []string{"internal/"})
	})
	r.apply("second", func(task.Task) (task.Event, error) { return task.Push(r.agent("alice"), r.Now()) })
	r.block("second", "first")

	if _, err := r.Apply(r.name("second"), func(task.Task) (task.Event, error) {
		return task.Claim(r.agent("carol"), r.Now())
	}); err == nil {
		t.Fatal("carol claimed a task that was waiting for first")
	}
}

// Either kind of finished releases it. The two are kept apart everywhere else —
// a task can report done and still be incomplete — and a gate is the one place
// they are read together: both are an owner saying the work is finished, which
// is the fact the waiting task needs.
func TestEitherDoneOrCompleteReleasesTheHold(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish func(r *rig)
	}{
		{"status done", func(r *rig) {
			r.apply("first", func(task.Task) (task.Event, error) {
				return task.SetStatus(r.agent("bob"), r.Now(), task.StatusDone)
			})
		}},
		{"completed", func(r *rig) {
			r.apply("first", func(task.Task) (task.Event, error) {
				return task.Complete(r.agent("bob"), r.Now(), false, nil)
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.pooled("first", "bob")
			r.pooled("second", "carol")
			r.block("second", "first")
			tc.finish(r)

			if _, err := r.Apply(r.name("second"), func(task.Task) (task.Event, error) {
				return task.Complete(r.agent("carol"), r.Now(), false, nil)
			}); err != nil {
				t.Errorf("second is still held after first was %s: %v", tc.name, err)
			}
		})
	}
}

// Status is not gated, and that is deliberate. It is a report on how the work is
// going, and a tool that refused to let an agent state the truth about a task
// would be teaching agents that the record is fiction.
func TestAHeldTaskMayStillReportItsStatus(t *testing.T) {
	r := newRig(t)
	r.pooled("first", "bob")
	r.pooled("second", "carol")
	r.block("second", "first")

	if _, err := r.Apply(r.name("second"), func(task.Task) (task.Event, error) {
		return task.SetStatus(r.agent("carol"), r.Now(), task.StatusBroken)
	}); err != nil {
		t.Errorf("a held task could not say it was broken: %v", err)
	}
}

// A ring is refused where it is declared. Caught at the gate instead, the board
// would accept the statement and then refuse every task in the ring, with
// nothing to say which declaration caused it.
func TestACycleIsRefusedWhenItIsDeclared(t *testing.T) {
	r := newRig(t)
	r.pooled("first", "bob")
	r.pooled("second", "carol")
	r.pooled("third", "dave")

	r.block("second", "first")
	r.block("third", "second")

	// first waiting for third would close the ring first → second → third.
	_, err := r.Apply(r.name("first"), func(task.Task) (task.Event, error) {
		return task.Block(r.agent("alice"), r.Now(), []task.Name{r.name("third")})
	})
	if err == nil {
		t.Fatal("a cycle was accepted")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the refusal does not say it is a cycle: %v", err)
	}
}

// A task deleted while something waited on it must not hold that thing forever.
// Nobody could release it: the prerequisite is gone, so no owner remains to
// finish it, and the waiting task would sit behind a name that never clears.
func TestBlockingOnAMissingTaskClears(t *testing.T) {
	r := newRig(t)
	r.pooled("first", "bob")
	r.pooled("second", "carol")
	r.block("second", "first")

	// Remove the prerequisite behind the store's back, which is what a delete
	// leaves the waiting task looking at.
	if err := r.Delete(r.name("first"), r.agent("bob")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Apply(r.name("second"), func(task.Task) (task.Event, error) {
		return task.Complete(r.agent("carol"), r.Now(), false, nil)
	}); err != nil {
		t.Errorf("second is held behind a task that no longer exists: %v", err)
	}
}

// The ordering has to survive a reload, or it is a rule that lasts until the next
// command and no longer.
func TestOrderingSurvivesAReplay(t *testing.T) {
	r := newRig(t)
	r.pooled("first", "bob")
	r.pooled("second", "carol")
	r.block("second", "first")

	got, err := r.Load(r.name("second"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Blocked() {
		t.Fatal("the ordering did not survive being folded back from the journal")
	}
	if on := got.BlockedOn(); len(on) != 1 || on[0].String() != "first" {
		t.Errorf("waits for %v, want [first]", on)
	}
}
