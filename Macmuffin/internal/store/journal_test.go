package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// busy builds a task with a long, varied journal, so the recovery tests have
// something with every kind of event in it.
func (r *rig) busy(name string) task.Task {
	r.t.Helper()
	alice, bob, carol := r.agent("alice"), r.agent("bob"), r.agent("carol")

	r.create(name, "alice")
	steps := []func(task.Task) (task.Event, error){
		func(task.Task) (task.Event, error) {
			return task.Scope(alice, r.Now(), []string{"internal/tree/", "cmd/anno/main.go"})
		},
		func(task.Task) (task.Event, error) { return task.AddSub(alice, r.Now(), r.name("one")) },
		func(task.Task) (task.Event, error) { return task.AddSub(alice, r.Now(), r.name("two")) },
		func(task.Task) (task.Event, error) { return task.Push(alice, r.Now()) },
		func(task.Task) (task.Event, error) { return task.Claim(bob, r.Now()) },
		func(task.Task) (task.Event, error) { return task.Invite(bob, r.Now(), carol) },
		func(task.Task) (task.Event, error) { return task.SetStatus(bob, r.Now(), task.StatusNominal) },
		func(task.Task) (task.Event, error) { return task.DoneSub(carol, r.Now(), r.name("one")) },
		func(task.Task) (task.Event, error) { return task.BindWorktree(bob, r.Now(), "/tmp/wt") },
	}
	var got task.Task
	for _, step := range steps {
		got = r.apply(name, step)
	}
	return got
}

func (r *rig) journalPath(name string) string {
	return filepath.Join(r.Root(), "tasks", name, "journal.jsonl")
}

// TestAnyPrefixOfAJournalReplays is milestone 2's crash-consistency property.
// A process killed mid-append leaves exactly a prefix, so every prefix must
// load — and a prefix can only ever lose events, never invent them.
func TestAnyPrefixOfAJournalReplays(t *testing.T) {
	r := newRig(t)
	full := r.busy("fix-the-parser")
	path := r.journalPath("fix-the-parser")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	wantDone, wantTotal := full.Progress()
	for cut := range len(data) + 1 {
		if err := os.WriteFile(path, data[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := r.Load(r.name("fix-the-parser"))
		if err != nil {
			t.Fatalf("a %d byte prefix failed to replay: %v", cut, err)
		}

		// A prefix can only ever have less happen to it.
		done, total := got.Progress()
		if total > wantTotal || done > wantDone {
			t.Fatalf("a %d byte prefix produced %d/%d, past the full %d/%d", cut, done, total, wantDone, wantTotal)
		}
		if len(got.Scope()) > len(full.Scope()) {
			t.Fatalf("a %d byte prefix invented scope", cut)
		}
		if got.Completed() && !full.Completed() {
			t.Fatalf("a %d byte prefix completed a task that is not complete", cut)
		}
	}

	// And the whole file still loads to what it did before.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatal(err)
	}
	if owner, _ := got.Owner(); owner.String() != "bob" {
		t.Errorf("after the round trip the owner is %q", owner)
	}
}

// TestInteriorCorruptionIsRefused is the other half of the rule: only the tail
// may be damaged. A bad line in the middle is corruption, and skipping it would
// silently drop a claim.
func TestInteriorCorruptionIsRefused(t *testing.T) {
	r := newRig(t)
	r.busy("fix-the-parser")
	path := r.journalPath("fix-the-parser")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	for _, tc := range []struct {
		name    string
		replace string
	}{
		{"garbage", "not json at all"},
		{"empty", ""},
		{"unknown op", `{"op":"shred","by":"alice","at":"2026-07-24T12:00:00.000Z"}`},
		{"unknown field", `{"op":"push","by":"alice","at":"2026-07-24T12:00:00.000Z","secret":1}`},
		{"bad actor", `{"op":"push","by":"../etc","at":"2026-07-24T12:00:00.000Z"}`},
		{"bad time", `{"op":"push","by":"alice","at":"yesterday"}`},
		{"claim with a scope", `{"op":"claim","by":"alice","at":"2026-07-24T12:00:00.000Z","paths":["x"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			damaged := append([]string{}, lines...)
			damaged[2] = tc.replace
			if err := os.WriteFile(path, []byte(strings.Join(damaged, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Load(r.name("fix-the-parser")); !errors.Is(err, fault.ErrParse) {
				t.Errorf("interior corruption loaded with %v, want a parse fault", err)
			}
		})
	}
}

// TestAJournalThatFoldsToAnIllegalStateIsCorruption. The events were legal when
// they were appended, so a sequence that no longer applies means something has
// rewritten them.
func TestReorderedJournalIsRefused(t *testing.T) {
	r := newRig(t)
	r.busy("fix-the-parser")
	path := r.journalPath("fix-the-parser")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	// Put the subtask completion before the subtask exists, which could not
	// have happened. (Claim before push would *not* do: the reference lets a
	// scopeless stub be claimed, so that order is legal.)
	reordered := append([]string{lines[7]}, lines[:7]...)
	reordered = append(reordered, lines[8:]...)
	if err := os.WriteFile(path, []byte(strings.Join(reordered, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(r.name("fix-the-parser")); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a reordered journal loaded with %v, want a parse fault", err)
	}
}

// FuzzFold drives the replay rules with arbitrary bytes.
//
// A journal is the file an operator is most likely to open in an editor while
// repairing something, so it must never panic, never hang, and never report a
// failure the CLI cannot turn into an exit code.
func FuzzFold(f *testing.F) {
	at := `"at":"2026-07-24T12:00:00.000Z"`
	for _, seed := range []string{
		"",
		"\n",
		`{"op":"scope","by":"alice","paths":["x"],` + at + "}\n",
		`{"op":"scope","by":"alice","paths":["x"],` + at + "}\n" +
			`{"op":"push","by":"alice",` + at + "}\n" +
			`{"op":"claim","by":"bob",` + at + "}\n",
		`{"op":"push","by":"alice",` + at, // no trailing newline
		`{"op":"claim","by":"bob",` + at + "}\n" + `{"op":"stat`,
		"{}\n", "null\n", "[]\n", "not json\n",
		`{"op":"claim","by":"bob","paths":["x"],` + at + "}\n",
	} {
		f.Add([]byte(seed))
	}

	base := mustBase(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		got, skipped, err := store.Fold("journal.jsonl", base, data)
		if err != nil {
			if !errors.Is(err, fault.ErrParse) && !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("Fold failed with an unclassified error: %v", err)
			}
			return
		}
		if skipped < 0 {
			t.Fatalf("skipped %d bytes", skipped)
		}

		// The creation record is immutable: no journal may rewrite it.
		if !got.Name().Equal(base.Name()) || got.Author().String() != base.Author().String() {
			t.Fatalf("the fold rewrote the creation record: %q by %q", got.Name(), got.Author())
		}
		if !got.Created().Equal(base.Created()) {
			t.Fatalf("the fold moved the creation time")
		}
		if got.Priority().Value() != base.Priority().Value() {
			t.Fatalf("the fold changed a score")
		}
		// And whatever it produced is a task that would pass its own checks,
		// which is what folding through With guarantees.
		if _, err := got.With(mustNoop(t)); err != nil && !errors.Is(err, fault.ErrConflict) {
			t.Fatalf("the folded task is not well formed: %v", err)
		}

		// Folding twice gives the same answer.
		again, _, err := store.Fold("journal.jsonl", base, data)
		if err != nil {
			t.Fatalf("the second fold failed where the first succeeded: %v", err)
		}
		gotDone, gotTotal := got.Progress()
		againDone, againTotal := again.Progress()
		if gotDone != againDone || gotTotal != againTotal {
			t.Fatalf("the fold is not deterministic: %d/%d then %d/%d", gotDone, gotTotal, againDone, againTotal)
		}
	})
}

// mustBase builds the creation record the fuzz target folds onto.
func mustBase(f *testing.F) task.Task {
	f.Helper()
	name, err := task.ParseName("fix-the-parser")
	if err != nil {
		f.Fatal(err)
	}
	author, err := parseAgent("alice")
	if err != nil {
		f.Fatal(err)
	}
	p, err := task.NewPriority(3)
	if err != nil {
		f.Fatal(err)
	}
	d, err := task.NewDifficulty(3)
	if err != nil {
		f.Fatal(err)
	}
	base, err := task.NewDraft(name, author, p, d, epoch)
	if err != nil {
		f.Fatal(err)
	}
	return base
}

// mustNoop is an event that is always shape-valid, used to check that a folded
// task still passes its own invariants.
func mustNoop(t *testing.T) task.Event {
	t.Helper()
	who, err := parseAgent("alice")
	if err != nil {
		t.Fatal(err)
	}
	ev, err := task.SetStatus(who, epoch, task.StatusNominal)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
