package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/source"
)

// These are the only actions in cq that write somebody's files, so almost all of
// what is worth testing is what they refuse: a path that leaves the checkout, an
// edit made against a file that has since changed, and a delete of something the
// operator has not actually seen.

func checkout(t *testing.T) (*source.CLI, string) {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "Docs", "Vision.md"), "# §1 Thing\n\nprose\n")
	write(t, filepath.Join(root, "app.go"), "package app\n")

	c := source.NewCLI("redjive")
	c.LibraryRoot = root
	c.Look = func(string) (string, bool) { return "redjive", true }
	return c, root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

func act(op protocol.Op, args protocol.Args) protocol.Action {
	return protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1,
		Machine: "studio", Op: op, Args: args, Queued: time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC),
	}
}

func TestAWriteReplacesAFileItWasShown(t *testing.T) {
	c, root := checkout(t)
	path := filepath.Join(root, "Docs", "Vision.md")
	base := source.Digest(read(t, path))

	err := c.Apply(t.Context(), act(protocol.OpWrite,
		protocol.Args{Path: "Docs/Vision.md", Text: "# §1 Thing\n\nedited\n", Base: base}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := read(t, path); got != "# §1 Thing\n\nedited\n" {
		t.Errorf("file = %q", got)
	}
}

// TestAWriteRefusesAFileThatChanged is the rule that makes a mirror safe to edit
// from. A snapshot is minutes old by the time somebody acts on it, and without
// this an edit made from a phone silently discards whatever arrived in between.
func TestAWriteRefusesAFileThatChanged(t *testing.T) {
	c, root := checkout(t)
	path := filepath.Join(root, "Docs", "Vision.md")
	stale := source.Digest("what the operator was looking at")

	err := c.Apply(t.Context(), act(protocol.OpWrite,
		protocol.Args{Path: "Docs/Vision.md", Text: "clobbered\n", Base: stale}))
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "redo the change") {
		t.Errorf("the refusal should say what to do: %v", err)
	}
	if read(t, path) != "# §1 Thing\n\nprose\n" {
		t.Error("the file was written anyway")
	}
}

// The same precondition makes a write self-guarding against being applied twice:
// after it lands, the file no longer matches what the action expected.
func TestAWriteAppliedTwiceRefusesTheSecondTime(t *testing.T) {
	c, root := checkout(t)
	base := source.Digest(read(t, filepath.Join(root, "app.go")))
	action := act(protocol.OpWrite, protocol.Args{Path: "app.go", Text: "package edited\n", Base: base})

	if err := c.Apply(t.Context(), action); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(t.Context(), action); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("a repeated write should refuse: %v", err)
	}
}

// TestNothingEscapesTheCheckout is the guard that matters most: these actions
// arrive over a network, and a path that leaves the mirrored tree is either a
// bug or an attempt.
func TestNothingEscapesTheCheckout(t *testing.T) {
	c, root := checkout(t)
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	write(t, outside, "untouched\n")

	for _, path := range []string{
		"../outside.txt",
		"Docs/../../outside.txt",
		"/etc/passwd",
		"",
	} {
		err := c.Apply(t.Context(), act(protocol.OpWrite,
			protocol.Args{Path: path, Text: "x", Base: source.Digest("untouched\n")}))
		if err == nil {
			t.Errorf("%q was accepted", path)
		}
	}
	if read(t, outside) != "untouched\n" {
		t.Error("something outside the checkout was written")
	}
}

// A symlink is exactly how a path that looks contained stops being contained, so
// containment is decided after following it rather than before.
func TestASymlinkOutOfTheCheckoutIsRefused(t *testing.T) {
	c, root := checkout(t)
	outside := filepath.Join(t.TempDir(), "secrets.txt")
	write(t, outside, "secret\n")

	link := filepath.Join(root, "Docs", "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("this platform will not make a symlink: %v", err)
	}

	err := c.Apply(t.Context(), act(protocol.OpWrite,
		protocol.Args{Path: "Docs/escape.txt", Text: "owned\n", Base: source.Digest("secret\n")}))
	if err == nil {
		t.Fatal("a link out of the checkout was followed")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("the refusal should say why: %v", err)
	}
	if read(t, outside) != "secret\n" {
		t.Error("the file the link pointed at was written")
	}
}

func TestCreateMakesAFileAndItsDirectory(t *testing.T) {
	c, root := checkout(t)

	err := c.Apply(t.Context(), act(protocol.OpCreate,
		protocol.Args{Path: "Docs/Ideas/New.md", Text: "# §1 New\n"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := read(t, filepath.Join(root, "Docs", "Ideas", "New.md")); got != "# §1 New\n" {
		t.Errorf("file = %q", got)
	}
}

// A create that would replace something is refused: "create" and "overwrite" are
// different intentions and only one of them was expressed.
func TestCreateRefusesSomethingThatExists(t *testing.T) {
	c, root := checkout(t)
	err := c.Apply(t.Context(), act(protocol.OpCreate,
		protocol.Args{Path: "Docs/Vision.md", Text: "replaced\n"}))

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if read(t, filepath.Join(root, "Docs", "Vision.md")) != "# §1 Thing\n\nprose\n" {
		t.Error("the existing file was replaced")
	}
}

// An empty file is a real file, and refusing to create one would be refusing a
// perfectly ordinary thing to want.
func TestAnEmptyFileCanBeCreated(t *testing.T) {
	c, root := checkout(t)
	if err := c.Apply(t.Context(), act(protocol.OpCreate, protocol.Args{Path: "empty.txt"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := read(t, filepath.Join(root, "empty.txt")); got != "" {
		t.Errorf("file = %q", got)
	}
}

func TestDeleteRemovesAFileTheOperatorSaw(t *testing.T) {
	c, root := checkout(t)
	path := filepath.Join(root, "app.go")
	base := source.Digest(read(t, path))

	if err := c.Apply(t.Context(), act(protocol.OpDelete,
		protocol.Args{Path: "app.go", Base: base})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
}

// The one action nobody can undo gets the same precondition as a write: a delete
// of a file that changed since it was queued is a delete of something the
// operator never saw.
func TestDeleteRefusesAFileThatChanged(t *testing.T) {
	c, root := checkout(t)
	err := c.Apply(t.Context(), act(protocol.OpDelete,
		protocol.Args{Path: "app.go", Base: source.Digest("something else")}))

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.go")); err != nil {
		t.Error("the file was deleted anyway")
	}
}

func TestDirectoriesAreMadeAndRemoved(t *testing.T) {
	c, root := checkout(t)

	if err := c.Apply(t.Context(), act(protocol.OpMakeDir, protocol.Args{Path: "Docs/Ideas"})); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "Docs", "Ideas")); err != nil || !info.IsDir() {
		t.Fatalf("directory = %v, %v", info, err)
	}
	if err := c.Apply(t.Context(), act(protocol.OpRemoveDir, protocol.Args{Path: "Docs/Ideas"})); err != nil {
		t.Fatalf("rmdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Docs", "Ideas")); !os.IsNotExist(err) {
		t.Error("the directory is still there")
	}
}

// TestRemovingAFullDirectoryIsRefused: a recursive delete is the one action
// nobody can undo and nobody can preview from a phone.
func TestRemovingAFullDirectoryIsRefused(t *testing.T) {
	c, root := checkout(t)
	err := c.Apply(t.Context(), act(protocol.OpRemoveDir, protocol.Args{Path: "Docs"}))

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("the refusal should say why: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Docs", "Vision.md")); err != nil {
		t.Error("something inside was removed")
	}
}

// TestRemoveDirWillNotTakeAFile is a hole rather than a nicety.
//
// os.Remove deletes files as happily as it does empty directories, and rmdir
// carries no Base. Without this check, "remove this folder" aimed at a file
// would delete it while skipping the digest precondition delete requires —
// which is the only thing standing between a minutes-old mirror and an agent's
// work.
func TestRemoveDirWillNotTakeAFile(t *testing.T) {
	c, root := checkout(t)
	err := c.Apply(t.Context(), act(protocol.OpRemoveDir, protocol.Args{Path: "Docs/Vision.md"}))

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "delete it instead") {
		t.Errorf("the refusal should name the verb that does check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Docs", "Vision.md")); err != nil {
		t.Error("the file was removed anyway")
	}
}

// Removing what is not there is a refusal, not a quiet success: rmdir is not
// idempotent, and reporting "done" for a directory somebody else deleted would
// tell them their tap did something it did not.
func TestRemovingSomethingAlreadyGone(t *testing.T) {
	c, _ := checkout(t)
	err := c.Apply(t.Context(), act(protocol.OpRemoveDir, protocol.Args{Path: "Docs/NotThere"}))

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "already gone") {
		t.Errorf("the refusal should say what it found: %v", err)
	}
}

// A machine that mirrors nothing has nothing to edit, and says so rather than
// writing somewhere it was never pointed at.
func TestAMachineWithNoCheckoutRefusesEveryEdit(t *testing.T) {
	c := source.NewCLI("redjive")
	c.Look = func(string) (string, bool) { return "redjive", true }

	for _, op := range protocol.LibraryOps {
		args := protocol.Args{Path: "a.md"}
		switch op {
		case protocol.OpWrite:
			args.Text, args.Base = "x", source.Digest("y")
		case protocol.OpCreate:
			args.Text = "x"
		case protocol.OpDelete:
			args.Base = source.Digest("y")
		}
		if err := c.Apply(t.Context(), act(op, args)); err == nil {
			t.Errorf("%s was applied with no checkout", op)
		}
	}
}

// A failed write leaves nothing behind: the temporary file it works through is
// removed on every path out, so a refused edit does not litter a repository.
func TestAFailedWriteLeavesNoTemporaryFile(t *testing.T) {
	c, root := checkout(t)
	_ = c.Apply(t.Context(), act(protocol.OpWrite,
		protocol.Args{Path: "Docs/Vision.md", Text: "x", Base: source.Digest("stale")}))

	entries, err := os.ReadDir(filepath.Join(root, "Docs"))
	if err != nil {
		t.Fatal(err)
	}
	// Any dotted temporary, whatever the writer names them: the point is that a
	// refused write leaves no litter in somebody's repository, not that the
	// litter would have had a particular prefix.
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// The digest the browser computes and the one the agent checks have to be the
// same function, or every edit would be refused as stale.
func TestTheDigestIsPlainSHA256(t *testing.T) {
	// echo -n "hello" | shasum -a 256
	const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := source.Digest("hello"); got != hello {
		t.Errorf("Digest(hello) = %q, want %q", got, hello)
	}
	if got := source.Digest(""); len(got) != 64 {
		t.Errorf("an empty file has no digest: %q", got)
	}
}
