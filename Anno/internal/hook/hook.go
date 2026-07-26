// Package hook implements Anno's Claude Code integration.
//
// Two jobs, both driven by PostToolUse:
//
//   - After an agent edits a file, check that its annotations still parse. A
//     broken annotation is blocked and reported back, which is what keeps an
//     edit from quietly destroying the structure the next read depends on.
//   - After an agent reads an annotated file, hand back its index, so the agent
//     can address regions by name instead of re-reading the whole file.
//
// A hook runs on every matching tool call, so the overriding rule here is that
// it must never break a session. Anything unexpected — an unfamiliar payload, a
// path that is not there, a file Anno cannot read — is silent success. The only
// non-zero exit is a file whose annotations genuinely stopped parsing.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"orc/anno/internal/render"
	"orc/anno/internal/style"
	"orc/anno/internal/tree"
	"orc/common/source"
)

// Exit codes, as Claude Code reads them: 0 is success, 2 blocks the action and
// feeds stderr back to the agent.
const (
	CodeOK    = 0
	CodeBlock = 2
)

// Main runs the hook end to end: read the event, decide, report, and return the
// process exit code.
//
// It recovers from a panic rather than letting one escape. A hook fires on every
// matching tool call, and a handler that crashes the session would be far worse
// than one that occasionally says nothing.
func Main(stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(stderr, "anno-hook: recovered from %v\n", r)
			code = CodeOK
		}
	}()

	input, err := io.ReadAll(stdin)
	if err != nil {
		return CodeOK
	}

	out := Run(input)
	if len(out.Stdout) > 0 {
		if _, err := stdout.Write(out.Stdout); err != nil {
			return CodeOK
		}
	}
	if out.Stderr != "" {
		if _, err := fmt.Fprintln(stderr, out.Stderr); err != nil {
			return CodeOK
		}
	}
	return out.Code
}

// payload is the part of a hook event Anno reads. Unknown fields are ignored,
// so a future addition to the event schema cannot break the hook.
type payload struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	ToolInput     struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"tool_input"`
}

// path returns the file the tool acted on, resolved against the session's
// working directory when the tool reported a relative path.
func (p payload) path() string {
	name := p.ToolInput.FilePath
	if name == "" {
		name = p.ToolInput.NotebookPath
	}
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

// Outcome is everything the process should do: what to print where, and what to
// exit with.
type Outcome struct {
	Code   int
	Stdout []byte
	Stderr string
}

// Editors are the tools whose output is checked for broken annotations.
var Editors = []string{"Edit", "Write", "NotebookEdit", "MultiEdit"}

// Readers are the tools whose output is enriched with an annotation index.
var Readers = []string{"Read"}

// Run decides what to do about one hook event.
//
// It never returns an error: a hook that cannot make sense of its input has
// nothing useful to say, and saying nothing is always safe.
func Run(input []byte) Outcome {
	var p payload
	if err := json.Unmarshal(input, &p); err != nil {
		return Outcome{Code: CodeOK}
	}
	if p.HookEventName != "PostToolUse" {
		return Outcome{Code: CodeOK}
	}

	path := p.path()
	if path == "" {
		return Outcome{Code: CodeOK}
	}

	switch {
	case matches(p.ToolName, Editors):
		return guard(path)
	case matches(p.ToolName, Readers):
		return enrich(path)
	default:
		return Outcome{Code: CodeOK}
	}
}

func matches(name string, against []string) bool {
	for _, candidate := range against {
		if name == candidate {
			return true
		}
	}
	return false
}

// guard blocks an edit that left the file's annotations unparseable.
//
// A file Anno cannot load at all — deleted, binary, not UTF-8 — is none of the
// hook's business and passes silently. Neither is a file carrying no annotation
// markers, and that is tested first: the question is whether an edit broke
// annotations meant to be there, not whether every file in the project has them.
func guard(path string) Outcome {
	f, err := source.Load(path)
	if err != nil || !annotated(f) {
		return Outcome{Code: CodeOK}
	}
	err = validate(f)
	if err == nil {
		return Outcome{Code: CodeOK}
	}
	return Outcome{
		Code: CodeBlock,
		Stderr: fmt.Sprintf(
			"anno: this edit left the annotations in %s unparseable, so they can no longer be addressed.\n\n%v\n\n"+
				"Fix the markers, or run `anno index %s` to see what parses.",
			path, err, path),
	}
}

// validate reports whether a file's annotations still form a tree.
func validate(f source.File) error {
	_, err := tree.Build(f)
	return err
}

// enrich returns the file's annotation index as context for the agent.
//
// Files without annotations produce nothing at all: the point of the tool is to
// spend fewer tokens, so a hook that fires on every read must stay silent unless
// it has something worth the space.
func enrich(path string) Outcome {
	f, err := source.Load(path)
	if err != nil {
		return Outcome{Code: CodeOK}
	}
	t, err := tree.Build(f)
	if err != nil || t.Empty() {
		return Outcome{Code: CodeOK}
	}

	// Never coloured: this text is read by a model, not a terminal.
	table, err := render.Index(t, style.Palette{})
	if err != nil {
		return Outcome{Code: CodeOK}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s carries anno annotations. Its structure is:\n\n%s\n", path, table)
	b.WriteString("You can read or replace any one of these regions by name, ")
	b.WriteString("instead of re-reading or rewriting the whole file:\n")
	fmt.Fprintf(&b, "  anno read  %s@section       (also :symbol and ^part)\n", path)
	fmt.Fprintf(&b, "  anno write %s^part -        (content on stdin)\n", path)
	b.WriteString("Chains may be partial or fully qualified; an ambiguous one fails and lists every candidate.")

	var r response
	r.HookSpecificOutput.HookEventName = "PostToolUse"
	r.HookSpecificOutput.AdditionalContext = b.String()

	out, err := json.Marshal(r)
	if err != nil {
		return Outcome{Code: CodeOK}
	}
	return Outcome{Code: CodeOK, Stdout: append(out, '\n')}
}

// annotated reports whether the file carries anything meant to be a marker.
// It looks for the sigils directly rather than for parsed markers, because the
// file being asked about is one that failed to parse.
func annotated(f source.File) bool {
	for _, line := range f.Lines() {
		if strings.Contains(line, "@:>") || strings.Contains(line, "@:<") || strings.Contains(line, "@:;") {
			return true
		}
	}
	return false
}
