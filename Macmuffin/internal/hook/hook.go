// Package hook implements Macmuffin's PreToolUse scope guard.
//
// It is the mechanism behind the vision's hard requirement: a task's scope
// "enforces editing only on files in scope". Before an agent edits a file, this
// asks whether the file is inside the scope of the task the agent is working on,
// and blocks the edit if it is not.
//
// It runs on PreToolUse rather than PostToolUse because a scope violation has to
// be prevented, not reported after the write.
//
// A hook fires on every matching tool call in somebody's live session, so the
// rule that outranks everything else here is Anno's: **only a genuine violation
// may block; everything unexpected exits 0 silently.** Unparseable input, an
// unknown event, a tool Macmuffin does not care about, no task in force, a
// scopeless task, a missing store, a corrupt journal, a store that will not
// answer in time — all pass. The cost is stated rather than hidden: while the
// store is broken, a violation gets through. That is the right trade for a
// bystander, and the opposite of the one `policy` makes, because a permission
// check is asked a question it can always answer.
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/macmuffin/internal/repo"
	"orc/macmuffin/internal/scope"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// Exit codes, as Claude Code reads them: 0 lets the tool call proceed, 2 blocks
// it and feeds stderr back to the agent.
const (
	CodeOK    = 0
	CodeBlock = 2
)

// EnvTask names the task in force, when an agent says so explicitly.
const EnvTask = "MUFF_TASK"

// Deadline bounds the whole check.
//
// A store on a slow disk, behind a stalled lock, or on an unresponsive network
// mount must not freeze an agent's session. Two seconds is far longer than a
// healthy check (which is a stat, a read, and a fold) and far shorter than a
// human notices as a hang.
const Deadline = 2 * time.Second

// Options is everything the hook needs from outside itself. Every field has a
// working default, so a test can set only what it cares about.
type Options struct {
	// Root overrides where the store lives.
	Root string

	// Home is the user's home directory, used to find the default store.
	Home string

	// Env reads the environment.
	Env func(key string) (string, bool)

	// Clock is the store's clock. The hook never writes, so nothing it does
	// depends on the time; the store still wants one.
	Clock clock.Clock

	// Deadline bounds the check. Zero means Deadline.
	Deadline time.Duration
}

func (o Options) env(key string) (string, bool) {
	if o.Env == nil {
		return os.LookupEnv(key)
	}
	return o.Env(key)
}

func (o Options) clock() clock.Clock {
	if o.Clock == nil {
		return clock.Real{}
	}
	return o.Clock
}

func (o Options) deadline() time.Duration {
	if o.Deadline <= 0 {
		return Deadline
	}
	return o.Deadline
}

// Outcome is everything the process should do: what to say, and what to exit
// with. There is no stdout: a PreToolUse hook that says nothing is a hook that
// costs the session nothing.
type Outcome struct {
	Code   int
	Stderr string
}

// pass is the answer to everything the hook does not understand.
var pass = Outcome{Code: CodeOK}

// Editors are the tools whose target path is checked against the scope.
var Editors = []string{"Edit", "Write", "NotebookEdit", "MultiEdit"}

// Main runs the hook end to end and returns the process exit code.
//
// It recovers from a panic rather than letting one escape: a handler that
// crashes an agent's session would be far worse than one that occasionally says
// nothing.
func Main(stdin io.Reader, stderr io.Writer, opts Options) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(stderr, "muff-hook: recovered from %v\n", r)
			code = CodeOK
		}
	}()

	input, err := io.ReadAll(stdin)
	if err != nil {
		return CodeOK
	}

	out := Run(input, opts)
	if out.Stderr != "" {
		if _, err := fmt.Fprintln(stderr, out.Stderr); err != nil {
			return CodeOK
		}
	}
	return out.Code
}

// payload is the part of a hook event Macmuffin reads. Unknown fields are
// ignored, so an addition to the event schema cannot break the hook.
type payload struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	ToolInput     struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
		Edits        []struct {
			FilePath string `json:"file_path"`
		} `json:"edits"`
		Paths []string `json:"paths"`
	} `json:"tool_input"`
}

// targets returns the paths this tool call would write, or nothing when the
// call is none of Macmuffin's business.
func (p payload) targets() []string {
	if p.ToolName == "Bash" {
		return annoWrites(p.ToolInput.Command)
	}
	if !known(p.ToolName, Editors) {
		return nil
	}

	var out []string
	for _, candidate := range append([]string{p.ToolInput.FilePath, p.ToolInput.NotebookPath}, p.ToolInput.Paths...) {
		if strings.TrimSpace(candidate) != "" {
			out = append(out, candidate)
		}
	}
	// MultiEdit carries its targets in a list. They are usually all one file,
	// but nothing promises that, so each is checked.
	for _, e := range p.ToolInput.Edits {
		if strings.TrimSpace(e.FilePath) != "" {
			out = append(out, e.FilePath)
		}
	}
	return out
}

func known(name string, against []string) bool {
	for _, candidate := range against {
		if name == candidate {
			return true
		}
	}
	return false
}

// annoWrites finds `anno write <target>` in a shell command.
//
// Deciding what an arbitrary shell command will write is undecidable, and this
// does not try. It recognises exactly one shape, because that shape is how Anno
// reaches the filesystem and is therefore the one hole in §8.2 worth closing
// from this side. The real fix is `muff check-scope`, which Anno calls itself;
// this is belt-and-braces for the window before that lands, and for an older
// `anno` binary.
//
// Anything it does not recognise yields nothing and the command passes — which
// is the honest answer, and the docs say so rather than implying a guarantee
// that does not hold.
func annoWrites(command string) []string {
	var out []string
	for _, segment := range splitCommands(command) {
		fields := strings.Fields(segment)
		// Skip a leading `cd … &&`, which agents write constantly.
		for len(fields) > 0 && (fields[0] == "cd" || fields[0] == "sudo") {
			if len(fields) < 2 {
				break
			}
			fields = fields[2:]
		}
		if len(fields) < 3 {
			continue
		}
		if filepath.Base(fields[0]) != "anno" || fields[1] != "write" {
			continue
		}
		target := fields[2]
		if strings.HasPrefix(target, "-") {
			continue
		}
		out = append(out, unquote(target))
	}
	return out
}

// splitCommands breaks a command line on the separators that start a new
// command. Quoting is not honoured: a false positive here costs one extra scope
// check on a path that will not resolve, which is harmless.
func splitCommands(command string) []string {
	fields := strings.FieldsFunc(command, func(r rune) bool { return r == ';' || r == '\n' })

	var out []string
	for _, f := range fields {
		for _, part := range strings.Split(strings.ReplaceAll(f, "||", "&&"), "&&") {
			if strings.TrimSpace(part) != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// Run decides what to do about one hook event.
//
// It never returns an error. A hook that cannot make sense of its input has
// nothing useful to say, and saying nothing is always safe.
func Run(input []byte, opts Options) Outcome {
	var p payload
	if err := json.Unmarshal(input, &p); err != nil {
		return pass
	}
	if p.HookEventName != "PreToolUse" {
		return pass
	}
	targets := p.targets()
	if len(targets) == 0 {
		return pass
	}

	// The check runs on its own goroutine so a store that never answers costs
	// the session a deadline rather than a session. The goroutine is abandoned
	// rather than cancelled: it holds no lock, writes nothing, and will end when
	// its I/O does.
	done := make(chan Outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- Outcome{Code: CodeOK, Stderr: fmt.Sprintf("muff-hook: recovered from %v", r)}
			}
		}()
		done <- check(p, targets, opts)
	}()

	select {
	case out := <-done:
		return out
	case <-time.After(opts.deadline()):
		return Outcome{Code: CodeOK, Stderr: fmt.Sprintf(
			"muff-hook: the task store did not answer within %s, so this edit was not checked", opts.deadline())}
	}
}

// check does the work: find the task in force, and match every target against
// its scope.
func check(p payload, targets []string, opts Options) Outcome {
	s, err := open(opts)
	if err != nil {
		// No store, an unreadable one, or one written by a newer Macmuffin. An
		// agent who never used the tool must not be blocked by it.
		return pass
	}

	current, root, ok := inForce(s, p, opts)
	if !ok || !current.Scoped() {
		return pass
	}

	set, err := scope.Parse(current.Scope())
	if err != nil {
		return note("the scope of %s will not parse, so this edit was not checked: %v", current.Name(), err)
	}

	for _, target := range targets {
		rel, err := scope.Resolve(root, absolute(target, p.CWD))
		if err != nil {
			// A path that escapes the worktree is a containment failure, and it
			// is the one error in here that still blocks: it is exactly the case
			// a scope exists to prevent.
			if errors.Is(err, fault.ErrEscape) {
				return Outcome{Code: CodeBlock, Stderr: escaped(target, current, root)}
			}
			return pass
		}
		inside, err := set.Matches(rel)
		if err != nil {
			return pass
		}
		if !inside {
			return Outcome{Code: CodeBlock, Stderr: outside(rel, current)}
		}
	}
	return pass
}

// open reads the store without touching it. Rule 3 of §8.4: the hook never
// writes. A hook that journalled on every tool call would turn the journal into
// a log of keystrokes and put a lock in the path of every edit.
func open(opts Options) (*store.Store, error) {
	root := opts.Root
	if root == "" {
		home := opts.Home
		if home == "" {
			if h, err := os.UserHomeDir(); err == nil {
				home = h
			}
		}
		got, err := store.DefaultRoot(opts.env, home)
		if err != nil {
			return nil, err
		}
		root = got
	}
	return store.Read(root, opts.clock())
}

// inForce answers "which task is this agent working on?", in the order §8.1
// sets out: an explicit environment variable, then the worktree the session's
// working directory sits in, then nothing at all.
//
// Nothing at all is the common case and the safe one: an agent that never opted
// in is never blocked.
func inForce(s *store.Store, p payload, opts Options) (task.Task, string, bool) {
	cwd := p.CWD
	if cwd == "" {
		if got, err := os.Getwd(); err == nil {
			cwd = got
		}
	}

	if raw, set := opts.env(EnvTask); set && strings.TrimSpace(raw) != "" {
		name, err := task.ParseName(raw)
		if err != nil {
			return task.Task{}, "", false
		}
		got, err := s.Load(name)
		if err != nil {
			return task.Task{}, "", false
		}
		root, _ := got.Worktree()
		if root == "" {
			// Without a binding the paths are relative to wherever the session
			// is, which is the best available answer and no worse than what the
			// agent itself would compute.
			root = cwd
		}
		return got, root, root != ""
	}

	if cwd == "" {
		return task.Task{}, "", false
	}
	wt, ok, err := repo.Find(cwd)
	if err != nil || !ok {
		return task.Task{}, "", false
	}
	bound, found, err := s.Bound(wt.Root())
	if err != nil || !found {
		return task.Task{}, "", false
	}
	got, err := s.Load(bound.Task)
	if err != nil {
		return task.Task{}, "", false
	}
	return got, wt.Root(), true
}

func absolute(target, cwd string) string {
	if filepath.IsAbs(target) || cwd == "" {
		return target
	}
	return filepath.Join(cwd, target)
}

// note reports something the hook could not do, without blocking. It goes to
// stderr, which on a passing hook Claude does not show the agent, so it costs
// nothing but is there when somebody looks.
func note(format string, args ...any) Outcome {
	return Outcome{Code: CodeOK, Stderr: "muff-hook: " + fmt.Sprintf(format, args...)}
}

// outside is the message an agent reads when its edit is refused. It says what
// was refused, what is allowed, and the two ways forward — a refusal that does
// not say how to proceed just gets worked around.
func outside(rel string, t task.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "muff: %s is outside the scope of %s.\n\n", rel, t.Name())
	fmt.Fprintf(&b, "  in scope:  %s\n\n", strings.Join(t.Scope(), "  "))
	fmt.Fprintf(&b, "Add it with `muff scope %s %s`, or work on a task that covers it.", t.Name(), rel)
	return b.String()
}

func escaped(target string, t task.Task, root string) string {
	return fmt.Sprintf(
		"muff: %s resolves outside %s, which is the worktree %s is being done in.\n\n"+
			"A scope cannot cover a path outside the tree it is scoped to. If this is where the work "+
			"belongs, bind %s to that worktree instead.",
		target, root, t.Name(), t.Name())
}
