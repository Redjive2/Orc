package store_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/store"
)

// FuzzReplay drives the journal reader with arbitrary bytes.
//
// A journal is the one file in the store an operator is likely to open in an
// editor while repairing something, so it has to survive anything at all: it
// must never panic, never hang, and never report a failure the CLI cannot
// classify into an exit code.
func FuzzReplay(f *testing.F) {
	// Seeds: a well-formed journal, the shapes an interrupted write leaves, and
	// the shapes a bad hand-edit leaves.
	at := `"at":"2026-07-24T12:00:00.000Z"`
	id := strings.Repeat("0", 16) + "-00000001"
	other := strings.Repeat("0", 16) + "-00000002"

	for _, seed := range []string{
		"",
		"\n",
		`{"op":"deliver","mid":"` + id + `","puid":0,` + at + "}\n",
		`{"op":"deliver","mid":"` + id + `","puid":0,` + at + "}\n" +
			`{"op":"read","mid":"` + id + `",` + at + "}\n",
		`{"op":"deliver","mid":"` + id + `","puid":0,` + at + "}\n" +
			`{"op":"deliver","mid":"` + other + `","puid":1,` + at + "}\n",
		`{"op":"deliver","mid":"` + id + `","puid":0,` + at, // no trailing newline
		`{"op":"deliver","mid":"` + id + `","puid":0,` + at + "}\n" + `{"op":"rea`,
		"{}\n",
		"null\n",
		"[]\n",
		`{"op":"deliver"}` + "\n",
		"not json at all\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := store.Fold("journal.jsonl", data)
		if err != nil {
			// Every refusal must classify, or the command would exit 70 and read
			// as a crash rather than as a damaged file.
			if !errors.Is(err, fault.ErrParse) && !errors.Is(err, fault.ErrIO) && !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("Replay failed with an unclassified error: %v", err)
			}
			return
		}

		// Anything that replays must be self-consistent: puids unique, entries
		// and order agreeing, and the next puid past every one assigned.
		seen := make(map[int]string, st.Len())
		for _, e := range st.Entries() {
			if other, dup := seen[e.PUID]; dup {
				t.Fatalf("puid %d belongs to both %s and %s", e.PUID, other, e.MID)
			}
			seen[e.PUID] = e.MID.String()

			if e.PUID >= st.NextPUID() {
				t.Fatalf("entry %s has puid %d but the next is %d", e.MID, e.PUID, st.NextPUID())
			}
			if e.Pruned {
				t.Fatalf("Entries returned a pruned message %s", e.MID)
			}
			if !e.Unread() && e.ReadAt.Before(e.Delivered) {
				t.Fatalf("message %s was read at %s, before it was delivered at %s", e.MID, e.ReadAt, e.Delivered)
			}
		}
		if st.Skipped() < 0 {
			t.Fatalf("Skipped() = %d", st.Skipped())
		}

		// Folding twice must give the same answer; a fold that depended on
		// anything but its input would be unreproducible in the field.
		again, err := store.Fold("journal.jsonl", data)
		if err != nil {
			t.Fatalf("the second fold failed where the first succeeded: %v", err)
		}
		if again.Len() != st.Len() || again.NextPUID() != st.NextPUID() {
			t.Fatalf("the fold is not deterministic: %d/%d then %d/%d",
				st.Len(), st.NextPUID(), again.Len(), again.NextPUID())
		}
	})
}

// TestReplayIsOrderIndependentForCommutingEvents.
//
// Marks on *different* messages do not interact, so whatever order two racing
// agents happen to append them in, the folded mailbox must be the same. Without
// this, a mailbox could depend on scheduling — the sort of defect that only
// shows up on a loaded machine.
func TestReplayIsOrderIndependentForCommutingEvents(t *testing.T) {
	// Every permutation of the marks below is written as a journal by hand, so
	// the test controls the interleaving rather than hoping to provoke one.
	const n = 4
	at := "2026-07-24T12:00:00.000Z"

	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("%016x-0000000%d", 1, i+1)
	}

	deliveries := make([]string, n)
	for i, id := range ids {
		deliveries[i] = fmt.Sprintf(`{"op":"deliver","mid":%q,"puid":%d,"at":%q}`, id, i, at)
	}

	// The commuting part: a read on each of the first two, an archive on each of
	// the last two. No two touch the same message.
	marks := []string{
		fmt.Sprintf(`{"op":"read","mid":%q,"at":%q}`, ids[0], at),
		fmt.Sprintf(`{"op":"read","mid":%q,"at":%q}`, ids[1], at),
		fmt.Sprintf(`{"op":"archive","mid":%q,"at":%q}`, ids[2], at),
		fmt.Sprintf(`{"op":"archive","mid":%q,"at":%q}`, ids[3], at),
	}

	h := newHarness(t, "alice")
	alice := h.name("alice")
	path := h.JournalPathFor("alice")

	var want string
	for _, order := range permutations(len(marks)) {
		lines := slices.Clone(deliveries)
		for _, i := range order {
			lines = append(lines, marks[i])
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		st, err := h.Replay(alice)
		if err != nil {
			t.Fatalf("order %v failed to replay: %v", order, err)
		}
		got := describe(t, st, ids)
		if want == "" {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("order %v folded to\n  %s\nbut another order gave\n  %s", order, got, want)
		}
	}

	// And the fold is the one intended, not merely a consistent wrong answer.
	if !strings.Contains(want, "0:read") || !strings.Contains(want, "2:archived") {
		t.Errorf("the folded state is not what the events describe: %s", want)
	}
}

// describe renders a mailbox's state as a comparable string.
func describe(t *testing.T, st store.State, ids []string) string {
	t.Helper()
	var parts []string
	for i, raw := range ids {
		id, err := mail.ParseID(raw)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := st.Lookup(id)
		if !ok {
			parts = append(parts, fmt.Sprintf("%d:absent", i))
			continue
		}
		state := "unread"
		if !e.Unread() {
			state = "read"
		}
		if e.Archived {
			state = "archived"
		}
		parts = append(parts, fmt.Sprintf("%d:%s:puid%d", i, state, e.PUID))
	}
	return strings.Join(parts, " ")
}

// permutations returns every ordering of 0..n-1.
func permutations(n int) [][]int {
	if n <= 1 {
		return [][]int{{0}}
	}
	var out [][]int
	for _, rest := range permutations(n - 1) {
		for at := range n {
			perm := make([]int, 0, n)
			perm = append(perm, rest[:at]...)
			perm = append(perm, n-1)
			perm = append(perm, rest[at:]...)
			out = append(out, perm)
		}
	}
	return out
}
