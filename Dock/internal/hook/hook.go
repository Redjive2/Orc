// Package hook implements Dock's Claude Code integration.
//
// One job, on PostToolUse over Read: when an agent reads a document carrying §
// headings, hand back its index, so the next thing the agent does can be
// `dock read guide.md§1.2` instead of re-reading the whole document.
//
// This is where most of Dock's saving actually lands, because it applies without
// the agent knowing Dock exists. It is also the riskiest place to put anything,
// since it fires on every read in every session — so the rules are strict:
//
//   - **It never blocks.** The hook is PostToolUse on Read, and there is no such
//     thing as a read that should have been refused. Unlike Anno's guard and
//     Macmuffin's scope hook, this one has no failure mode that stops work.
//   - **Silence is the default.** A document with no § headings produces
//     nothing. A hook that fired on every read and spent tokens on files that
//     are not documents would invert the tool's whole purpose.
//   - **Nothing unexpected ever misbehaves.** Unparseable JSON, an unhandled
//     event, a tool Dock does not care about, a missing path, a deleted file, a
//     binary, a wrong field type: all exit 0 in silence.
//   - **Output is never coloured.** It is read by a model, not a terminal.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"orc/common/source"
	"orc/dock/internal/doc"
	"orc/dock/internal/scan"
	"orc/dock/internal/style"
)

// CodeOK is the only status this hook ever exits with. It is named rather than
// written as 0 so that the one-value-ness is visible: Claude Code reads 2 as
// "block the action", and nothing here can produce it.
const CodeOK = 0

// MaxSections bounds how much of an index the hook will hand back.
//
// The context it emits is spent on every read of every document, so a
// hundred-section reference would cost more than the agent saved. Past the bound
// the hook says how many there are and how to see them, which is cheaper and
// just as actionable.
const MaxSections = 40

// Main runs the hook end to end and returns the process exit code.
//
// It recovers from a panic rather than letting one escape. A hook fires on every
// matching tool call, and a handler that crashed a session would be far worse
// than one that occasionally says nothing.
func Main(stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(stderr, "dock-hook: recovered from %v\n", r)
			code = CodeOK
		}
	}()

	input, err := io.ReadAll(stdin)
	if err != nil {
		return CodeOK
	}
	out := Run(input)
	if len(out) > 0 {
		if _, err := stdout.Write(out); err != nil {
			return CodeOK
		}
	}
	return CodeOK
}

// payload is the part of a hook event Dock reads. Unknown fields are ignored, so
// a future addition to the event schema cannot break the hook.
type payload struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	ToolInput     struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// path returns the file the tool read, resolved against the session's working
// directory when the tool reported a relative one.
func (p payload) path() string {
	name := p.ToolInput.FilePath
	if name == "" || filepath.IsAbs(name) || p.CWD == "" {
		return name
	}
	return filepath.Join(p.CWD, name)
}

// response is the JSON a hook writes on stdout.
type response struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Run decides what to say about one hook event, and returns the bytes to write.
//
// It never returns an error and never returns a non-zero status: a hook that
// cannot make sense of its input has nothing useful to say, and saying nothing
// is always safe.
func Run(input []byte) []byte {
	var p payload
	if err := json.Unmarshal(input, &p); err != nil {
		return nil
	}
	if p.HookEventName != "PostToolUse" || p.ToolName != "Read" {
		return nil
	}
	path := p.path()
	if path == "" {
		return nil
	}

	context := describe(path)
	if context == "" {
		return nil
	}

	var r response
	r.HookSpecificOutput.HookEventName = "PostToolUse"
	r.HookSpecificOutput.AdditionalContext = context
	out, err := json.Marshal(r)
	if err != nil {
		// Marshalling a struct of strings cannot fail, but a hook is the wrong
		// place to assume anything: nothing to say is always safe.
		return nil
	}
	return append(out, '\n')
}

// describe builds the context for one document, or "" when there is nothing
// worth saying.
//
// A file Dock cannot load — deleted, binary, not UTF-8, too large — is none of
// the hook's business. Neither is a document whose numbering is broken: the
// agent just read it and can see the headings, and a hook is not the place to
// report a fault nobody asked about. `dock check` is.
func describe(path string) string {
	f, err := source.Load(path)
	if err != nil {
		return ""
	}
	text := string(f.Bytes())
	if !strings.Contains(text, doc.Sigil) {
		// The cheap test first: most files in a project are not documents, and
		// this is the one that runs on every read.
		return ""
	}

	r := scan.Scan(text)
	d, err := doc.Build(path, r)
	if err != nil || d.Len() == 0 {
		return ""
	}

	name := filepath.Base(path)
	var b strings.Builder
	fmt.Fprintf(&b, "%s carries dock sections. Its structure is:\n\n", name)

	shown := d.Sections()
	if len(shown) > MaxSections {
		shown = shown[:MaxSections]
	}
	// style.Pad rather than a %-*s width: § is two bytes and one column, so
	// padding by byte count would shear the number column.
	w := widest(shown)
	for _, s := range shown {
		fmt.Fprintf(&b, "  %s %s   %s\n",
			style.Pad(doc.Sigil+s.Number(), w),
			style.Pad(lines(d.Content(r.Lines(), s.Tree()).Len()), 9),
			s.Name())
	}
	if n := d.Len() - len(shown); n > 0 {
		fmt.Fprintf(&b, "  … and %d more; see `dock index %s`\n", n, name)
	}

	fmt.Fprintf(&b, "\nRead one instead of the whole document:\n"+
		"  dock read %s%s1.2            its own prose\n"+
		"  dock read %s%s1.2 --tree     and everything under it\n"+
		"  dock links %s%s1.2           what it cites, and what cites it\n",
		name, doc.Sigil, name, doc.Sigil, name, doc.Sigil)
	return b.String()
}

// widest measures the number column so the names line up, which is what makes
// the list scannable rather than a wall.
func widest(sections []doc.Section) int {
	n := 0
	for _, s := range sections {
		n = max(n, style.Width(doc.Sigil+s.Number()))
	}
	return n
}

func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}
