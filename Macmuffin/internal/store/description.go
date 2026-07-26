package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// A task's description: what the work actually is, in prose.
//
// Everything else Macmuffin holds about a task is a fact with a shape — a score, an
// owner, a set of paths, a list of steps. None of them says what to *do*. That was
// left to the task's name and to whatever the agent was told when it was assigned,
// which is how a pool ends up full of tasks called `fix-the-parser` that only one
// person can explain.
//
// It is a file rather than a field for the same reason Orc's standing instructions
// are: prose is edited in an editor, read in a browser, and diffed in git, and none
// of those work on a JSON string. `description.md` sits inside the task's own
// directory, so deleting a task takes its description with it rather than leaving an
// orphan nobody notices.
//
// The journal records *that* it changed, never the text — see task.OpDescribe. A
// record replayed on every command must not carry 32 KiB of markdown.
const descriptionFile = "description.md"

// MaxDescription is what one description may be.
//
// Generous, because this is a specification and specifications have examples in them
// — but bounded, because it travels in a cq snapshot on every sync, and a bound
// nobody set is a bound somebody finds by making a sync time out.
const MaxDescription = 32 << 10

func (s *Store) descriptionPath(name task.Name) string {
	return filepath.Join(s.taskDir(name), descriptionFile)
}

// DescriptionPath is where a task's description lives, for a caller that wants to
// hand the path to an editor rather than the text to a function.
func (s *Store) DescriptionPath(name task.Name) (string, error) {
	if name.Zero() {
		return "", fault.Internal{Where: "store.DescriptionPath", Detail: "no task named"}
	}
	return s.descriptionPath(name), nil
}

// Description reads a task's description. Absent is not an error: most tasks have
// none, and a caller asking is asking whether there is one.
func (s *Store) Description(name task.Name) (string, bool, error) {
	if name.Zero() {
		return "", false, fault.Internal{Where: "store.Description", Detail: "no task named"}
	}
	path := s.descriptionPath(name)

	// A description is a regular file or it is not read.
	//
	// A symlink here would make `muff describe` and — through the mirror — the cq
	// website print whatever it points at. Anything that can plant one can already
	// read the file directly, so this is not a new fence; what it stops is
	// *laundering*, where a file only the agent machine can see ends up on a server
	// and in a browser because something called it a task description.
	//
	// Lstat rather than the ops shim on purpose: the question is about the real
	// filesystem, and asking a stub whether a link is a link has no answer.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", false, fault.Usage{Reason: "the description at " + path +
				" is a symbolic link; it is read as a file or not at all"}
		}
		if !info.Mode().IsRegular() {
			return "", false, fault.Usage{Reason: "the description at " + path + " is not a regular file"}
		}
		// Sized before it is read. These are files an operator edits by hand, and one
		// that has become enormous — a mistaken redirect, a runaway generator — must
		// not be pulled into memory in full just to be told it is too large. The
		// check below still runs: a file under the bound can be wrong in other ways.
		if info.Size() > MaxDescription {
			return "", false, fault.Usage{Reason: sizeRefusal(int(min(info.Size(), 1<<40)))}
		}
	}

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fault.IO{Op: "read", Path: path, Err: err}
	}

	// Checked on the way out as well as in. It is a plain file an operator is
	// expected to edit by hand, and one that would be refused on write must not be
	// delivered because it arrived another way.
	if err := CheckDescription(string(data)); err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// WriteDescription replaces a task's description, and records that it changed.
//
// Empty text removes it rather than writing an empty file: "no description" and "a
// description that says nothing" read identically, and two spellings of one state is
// a state somebody eventually disagrees about.
//
// The file lands before the event. A crash between them leaves a description on disk
// that the journal does not mention — visible, readable, and attributed to nobody,
// which is a smaller wrong than a journal claiming a description that is not there.
func (s *Store) WriteDescription(name task.Name, by user.Name, text string) error {
	if strings.TrimSpace(text) == "" {
		return s.ClearDescription(name, by)
	}
	if err := CheckDescription(text); err != nil {
		return err
	}
	return s.describe(name, by, func() error {
		return s.writeFile(s.descriptionPath(name), []byte(text))
	}, task.Describe)
}

// ClearDescription removes it. Clearing what is not there satisfies the caller's
// intent either way, and still records the intent — an operator who deleted a
// description twice asked for it twice.
func (s *Store) ClearDescription(name task.Name, by user.Name) error {
	return s.describe(name, by, func() error {
		if err := s.ops.remove(s.descriptionPath(name)); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: s.descriptionPath(name), Err: err}
		}
		return nil
	}, task.Undescribe)
}

// describe is the shared half: under the task's lock, touch the file, then append
// the event that says so.
//
// It goes through the same lock every other write takes, so a description written
// while somebody claims the task cannot interleave with the claim's journal append.
func (s *Store) describe(name task.Name, by user.Name, write func() error,
	event func(user.Name, time.Time) (task.Event, error)) error {
	if name.Zero() {
		return fault.Internal{Where: "store.describe", Detail: "no task named"}
	}
	if by.Zero() {
		return fault.Internal{Where: "store.describe", Detail: "nobody named as the author"}
	}

	return s.withLock(name, func() error {
		// The task has to exist. Writing a description for a task that does not is
		// how a directory appears with prose in it and nothing else — a task the
		// pool cannot show and `delete` cannot remove.
		if _, err := s.Load(name); err != nil {
			return err
		}
		if err := write(); err != nil {
			return err
		}

		ev, err := event(by, s.Now())
		if err != nil {
			return err
		}
		line, err := encodeEvent(ev)
		if err != nil {
			return err
		}
		return s.appendLine(s.journalPath(name), line)
	})
}

// CheckDescription refuses what must not be stored.
//
// Over the bound it is refused rather than cut. Silently truncating a specification
// is how an agent ends up implementing the first half of a rule and none of the rest
// — which is worse than having none of it, because it looks like the whole thing.
//
// Control characters go too. A description is printed to a terminal by `muff
// describe` and `muff info`, where an escape sequence repaints a screen and a NUL
// ends a string early. Tabs and newlines are how prose is written.
func CheckDescription(text string) error {
	if len(text) > MaxDescription {
		return fault.Usage{Reason: sizeRefusal(len(text))}
	}
	if !utf8.ValidString(text) {
		return fault.Usage{Reason: "a description must be valid UTF-8"}
	}
	for _, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return fault.Usage{Reason: "a description may not contain control characters; " +
				"it is printed to a terminal, where an escape sequence does what nobody asked for"}
		}
	}
	return nil
}

func sizeRefusal(size int) string {
	return "that description is " + kib(size) + "; the most one may be is " + kib(MaxDescription)
}

func kib(n int) string {
	if n < 1024 {
		return itoa(n) + " B"
	}
	return itoa((n+512)/1024) + " KiB"
}
