package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// A description is prose in a file, with the journal recording only that it changed.
// Most of what is worth checking here is that split: the text round-trips through the
// file, and the *record* — folded on every command — never grows a copy of it.

func TestDescriptionRoundTrip(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	// Nothing written is not an error. Most tasks have none.
	if _, found, err := r.Description(name); err != nil || found {
		t.Fatalf("a fresh task had a description: found=%v err=%v", found, err)
	}

	const text = "# the parser\n\nIt drops the last token when the input has no trailing newline.\n"
	if err := r.WriteDescription(name, r.agent("alice"), text); err != nil {
		t.Fatal(err)
	}

	got, found, err := r.Description(name)
	if err != nil || !found {
		t.Fatalf("reading it back: found=%v err=%v", found, err)
	}
	if got != text {
		t.Errorf("read back %q, want %q", got, text)
	}
}

// It lives inside the task's own directory, so removing the task takes the
// description with it rather than leaving an orphan nobody notices.
func TestADescriptionLivesInsideTheTask(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	if err := r.WriteDescription(name, r.agent("alice"), "what to do"); err != nil {
		t.Fatal(err)
	}
	path, err := r.DescriptionPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(r.root, "tasks", "fix-the-parser", "description.md"); path != want {
		t.Errorf("the description is at %s, want %s", path, want)
	}

	if err := os.RemoveAll(filepath.Join(r.root, "tasks", "fix-the-parser")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := r.Description(name); found {
		t.Error("the description outlived the task it belonged to")
	}
}

// TestTheRecordKnowsThereIsOneAndNotWhatItSays. The journal is replayed on every
// command that touches the task; a record carrying 32 KiB of markdown would be
// re-read in full to answer "who owns this".
func TestTheRecordCarriesOnlyThatItChanged(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	const text = "the lexer eats the last token"
	if err := r.WriteDescription(name, r.agent("alice"), text); err != nil {
		t.Fatal(err)
	}

	got, err := r.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Described() {
		t.Error("the record does not know the task has a description")
	}
	if got.DescribedBy().String() != "alice" {
		t.Errorf("described by %q, want alice", got.DescribedBy())
	}
	if got.DescribedAt().IsZero() {
		t.Error("the record does not say when it was described")
	}

	// And the text is nowhere in the journal.
	raw, err := os.ReadFile(filepath.Join(r.root, "tasks", "fix-the-parser", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), text) {
		t.Errorf("the description was written into the journal:\n%s", raw)
	}
}

// Clearing removes the file and says so in the record — including who removed it,
// which is what somebody looking for a description that used to be there needs.
func TestClearingADescription(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	if err := r.WriteDescription(name, r.agent("alice"), "what to do"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClearDescription(name, r.agent("bob")); err != nil {
		t.Fatal(err)
	}

	if _, found, err := r.Description(name); err != nil || found {
		t.Errorf("it survived clearing: found=%v err=%v", found, err)
	}
	path, _ := r.DescriptionPath(name)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file was left behind: %v", err)
	}

	got, err := r.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Described() {
		t.Error("the record still claims a description")
	}
	if got.DescribedBy().String() != "bob" {
		t.Errorf("the removal is attributed to %q, want bob", got.DescribedBy())
	}
}

// Writing nothing is clearing. "No description" and "a description that says
// nothing" read identically, and two spellings of one state is a state somebody
// eventually disagrees about.
func TestWritingNothingClearsTheDescription(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	if err := r.WriteDescription(name, r.agent("alice"), "something"); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteDescription(name, r.agent("alice"), "   \n\n "); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := r.Description(name); found {
		t.Error("whitespace was stored as a description")
	}
}

// Clearing one that is not there satisfies the caller either way.
func TestClearingNothingIsNotAnError(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")

	if err := r.ClearDescription(r.name("fix-the-parser"), r.agent("alice")); err != nil {
		t.Errorf("clearing an absent description: %v", err)
	}
}

// A task that does not exist gets no description. Otherwise a directory appears with
// prose in it and nothing else — a task the pool cannot show and `delete` cannot
// remove.
func TestDescribingATaskThatIsNotThere(t *testing.T) {
	r := newRig(t)

	if err := r.WriteDescription(r.name("no-such-task"), r.agent("alice"), "hello"); err == nil {
		t.Error("a description was written for a task that does not exist")
	}
	if _, err := os.Stat(filepath.Join(r.root, "tasks", "no-such-task", "description.md")); err == nil {
		t.Error("an orphan description was left on disk")
	}
}

// Over the bound it is refused rather than cut: half a specification looks like a
// whole one, which is worse than none.
func TestAnOversizedDescriptionIsRefused(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	err := r.WriteDescription(name, r.agent("alice"), strings.Repeat("x", store.MaxDescription+1))
	if err == nil {
		t.Fatal("an oversized description was written")
	}
	if !strings.Contains(err.Error(), "KiB") {
		t.Errorf("the refusal should give the arithmetic: %v", err)
	}
	if _, found, _ := r.Description(name); found {
		t.Error("the refused description was written anyway")
	}
}

// It is a plain file somebody is expected to edit by hand, so what would be refused
// on the way in is refused on the way out too.
func TestAHandEditedDescriptionIsCheckedWhenRead(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	path, _ := r.DescriptionPath(name)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", store.MaxDescription+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Description(name); err == nil {
		t.Error("a description too large to have been written was read back without complaint")
	}
}

func TestCheckDescriptionRefusesWhatCannotBePrinted(t *testing.T) {
	// A description goes to a terminal through `muff describe` and `muff info`.
	if err := store.CheckDescription("before\x1b[31m after"); err == nil {
		t.Error("an escape sequence was accepted")
	}
	if err := store.CheckDescription("a\x00b"); err == nil {
		t.Error("a NUL was accepted")
	}
	if err := store.CheckDescription(string([]byte{0xff, 0xfe})); err == nil {
		t.Error("invalid UTF-8 was accepted")
	}
	// Markdown is written with newlines and tabs in it.
	if err := store.CheckDescription("# a heading\n\n- a point\n\tindented\n"); err != nil {
		t.Errorf("ordinary markdown was refused: %v", err)
	}
}

// The two operations are in the journal's vocabulary, so a stored one replays.
func TestDescribeEventsRoundTripThroughTheJournal(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	if err := r.WriteDescription(name, r.agent("alice"), "one"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClearDescription(name, r.agent("alice")); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteDescription(name, r.agent("alice"), "two"); err != nil {
		t.Fatal(err)
	}

	got, err := r.Load(name)
	if err != nil {
		t.Fatalf("the journal did not replay: %v", err)
	}
	if !got.Described() {
		t.Error("the last write was lost in the replay")
	}
	for _, op := range []task.Op{task.OpDescribe, task.OpUndescribe} {
		if !op.Valid() {
			t.Errorf("%q is not a known operation", op)
		}
	}
}

// TestAnEnormousDescriptionIsNotReadIntoMemory. These are plain files somebody edits
// by hand, and a mistaken redirect can make one arbitrarily large. Refusing it after
// reading all of it is a way to run a machine out of memory with one wrong path.
func TestAnEnormousDescriptionIsSizedBeforeItIsRead(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")
	path, _ := r.DescriptionPath(name)

	// Sparse, so the test does not write a gigabyte: the file *reports* a huge size
	// and reading it would allocate that much.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		_ = f.Close()
		t.Skipf("this filesystem will not make a sparse file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err = r.Description(name)
	if err == nil {
		t.Fatal("a gigabyte description was read back without complaint")
	}
	if !strings.Contains(err.Error(), "KiB") {
		t.Errorf("the refusal should give the arithmetic: %v", err)
	}
}

// TestADescriptionIsAFileOrItIsNotRead. A symlink here would make `muff describe`
// and, through the cq mirror, a website print whatever it points at. Anything that
// can plant one can already read the target directly — what this stops is
// laundering, where a file only the agent machine can see reaches a browser because
// something called it a task description.
func TestASymlinkedDescriptionIsRefused(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	secret := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(secret, []byte("the operator's key"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := r.DescriptionPath(name)
	if err := os.Symlink(secret, path); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}

	text, found, err := r.Description(name)
	if err == nil {
		t.Fatalf("a symlinked description was read: found=%v text=%q", found, text)
	}
	if strings.Contains(err.Error(), "the operator's key") {
		t.Error("the refusal quoted what it refused to read")
	}
	if !strings.Contains(err.Error(), "link") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// And writing over one replaces it rather than following it, because the write is a
// rename onto the path.
func TestWritingOverASymlinkReplacesIt(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	name := r.name("fix-the-parser")

	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := r.DescriptionPath(name)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}

	if err := r.WriteDescription(name, r.agent("alice"), "what to do"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "untouched" {
		t.Errorf("the write followed the link: %q %v", got, err)
	}
	if got, _, _ := r.Description(name); got != "what to do" {
		t.Errorf("the description did not land: %q", got)
	}
}
