package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"orc/common/fault"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// `muff describe` — what the work actually is.
//
// Everything else a task carries is a fact with a shape: a score, an owner, a set of
// paths, a list of steps. None of them says what to *do*, which is how a pool fills
// up with tasks called `fix-the-parser` that one person can explain and nobody else
// can start.
//
// Four forms, because prose arrives four ways and each of them is somebody's normal
// way of working:
//
//	muff describe <task>              print it — nothing else on stdout, so it redirects
//	muff describe <task> --set <file> replace it from a file, or from `-` for stdin
//	muff describe <task> --edit       open $EDITOR on the file itself
//	muff describe <task> --clear      remove it
//
// Printing puts the text alone on stdout so `muff describe x > spec.md` is the whole
// export story and `muff describe x --set spec.md` is the whole import one.
func (a App) describe(args []string) error {
	var (
		set   string
		edit  bool
		clear bool
		rest  []string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--set":
			if i+1 >= len(args) {
				return fault.Usage{Reason: "--set takes a file, or - for stdin"}
			}
			set, i = args[i+1], i+1
		case "--edit":
			edit = true
		case "--clear":
			clear = true
		default:
			rest = append(rest, args[i])
		}
	}
	if err := exactly(rest, 1, "describe takes a task name"); err != nil {
		return err
	}
	// Naming two of them is somebody expecting both to happen. Which one won would
	// be an implementation detail, and the loser would be silent.
	if howMany(set != "", edit, clear) > 1 {
		return fault.Usage{Reason: "--set, --edit, and --clear each replace the description; name one"}
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(rest[0])
	if err != nil {
		return err
	}

	switch {
	case set != "":
		return a.describeSet(s, name, set)
	case edit:
		return a.describeEdit(s, name)
	case clear:
		return a.describeClear(s, name)
	default:
		return a.describePrint(s, name)
	}
}

// howMany counts the flags given, so naming two of them can be refused rather than
// resolved by whichever the code happened to check first.
func howMany(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// describePrint puts the description on stdout and nothing else, so it round-trips.
func (a App) describePrint(s session, name task.Name) error {
	current, err := s.store.Load(name)
	if err != nil {
		return err
	}
	if err := policy.Allows(s.who, current, policy.Info); err != nil {
		return err
	}

	text, found, err := s.store.Description(name)
	if err != nil {
		return err
	}
	if !found {
		// To stderr, so a redirect that captured nothing captures nothing. The
		// note is for the person, and the empty stdout is for the pipe.
		a.note("%s has no description; `muff describe %s --edit` writes one",
			a.err.Task(name.String()), name)
		return nil
	}
	return a.write(ensureNewline(text))
}

func (a App) describeSet(s session, name task.Name, from string) error {
	text, err := a.readDescription(from)
	if err != nil {
		return err
	}
	// Checked before the store is touched, so an oversized file is refused with the
	// arithmetic rather than after a lock and a partial write.
	if err := store.CheckDescription(text); err != nil {
		return err
	}
	if err := a.mayDescribe(s, name); err != nil {
		return err
	}
	if err := s.store.WriteDescription(name, s.who, text); err != nil {
		return err
	}
	return a.sayDescribed(name, len(text))
}

// readDescription is the file, or stdin when it is `-`.
//
// Both are read to one byte past the bound and no further. The check that follows
// would refuse an oversized description either way, but reading the whole of a file
// somebody pointed at by mistake — a core dump, a video, /dev/zero — to then say it
// is too long is a way to run a machine out of memory by typing one wrong path.
func (a App) readDescription(from string) (string, error) {
	if from == "-" {
		data, err := io.ReadAll(io.LimitReader(a.Stdin, store.MaxDescription+1))
		if err != nil {
			return "", fault.IO{Op: "read", Path: "standard input", Err: err}
		}
		return string(data), nil
	}

	f, err := os.Open(from)
	if err != nil {
		return "", fault.IO{Op: "read", Path: from, Err: err}
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, store.MaxDescription+1))
	if err != nil {
		return "", fault.IO{Op: "read", Path: from, Err: err}
	}
	return string(data), nil
}

// describeEdit opens $EDITOR on the file itself.
//
// The real file, not a copy: an editor that writes in place leaves the description
// where it belongs even if this process dies mid-edit. What it saved is then read
// back through the store, which validates — an editor is not a caller that checked
// the bounds.
func (a App) describeEdit(s session, name task.Name) error {
	if err := a.mayDescribe(s, name); err != nil {
		return err
	}

	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		return fault.Usage{Reason: "no $EDITOR is set; `muff describe <task> --set <file>` takes a file instead"}
	}
	path, err := s.store.DescriptionPath(name)
	if err != nil {
		return err
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fault.IO{Op: "run " + editor + " on", Path: path, Err: err}
	}

	text, found, err := s.store.Description(name)
	if err != nil {
		return err
	}
	if !found {
		return a.say(a.out.Muted("nothing was saved, so nothing changed"))
	}
	// Written back through the store even though the bytes are already there, so
	// the journal records the change and the record knows there is one.
	if err := s.store.WriteDescription(name, s.who, text); err != nil {
		return err
	}
	return a.sayDescribed(name, len(text))
}

func (a App) describeClear(s session, name task.Name) error {
	if err := a.mayDescribe(s, name); err != nil {
		return err
	}
	if err := s.store.ClearDescription(name, s.who); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s has no description now", a.out.Task(name.String())))
}

// mayDescribe asks the policy before anything is written.
//
// Separate from the write because three of the four forms need the same answer, and
// because `--edit` has to know *before* it opens an editor: discovering the refusal
// after somebody has typed a page of prose is how work gets lost.
func (a App) mayDescribe(s session, name task.Name) error {
	current, err := s.store.Load(name)
	if err != nil {
		return err
	}
	return s.permit(current, policy.Describe)
}

func (a App) sayDescribed(name task.Name, size int) error {
	return a.say(fmt.Sprintf("%s is described   %s",
		a.out.Task(name.String()), a.out.Muted(sized(size))))
}

// sized is a length a person reads, for the line that confirms a write.
func sized(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
