package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"orc/common/fault"
	"orc/orc/internal/prose"
)

// `orc prose` — the house writing rule, as something a writer can run.
//
// The rule is in every agent's system prompt, and a rule that only lives in a prompt
// is a rule nobody can check. This is the other half: point it at a file and it says
// which sentences break it and why, in the words somebody would use to fix them.
//
// It reads a file, a directory, or standard input, so it fits both ways of working —
// an agent checking what it has just written, and a person checking a tree.
//
// Every agent may run it. It reads and it prints; it changes nothing, so there is no
// reason to gate it behind a permission and every reason for the agent whose writing
// is being judged to be able to judge it first.

// proseCmd is `orc prose [path…]`.
func (a App) proseCmd(args []string) error {
	var quiet bool
	args, err := flagged(args, options{switches: map[string]*bool{"--quiet": &quiet}})
	if err != nil {
		return err
	}

	var texts []checked
	if len(args) == 0 {
		body, err := io.ReadAll(a.Stdin)
		if err != nil {
			return fault.IO{Op: "read", Path: "stdin", Err: err}
		}
		texts = []checked{{name: "stdin", text: string(body)}}
	} else {
		texts, err = gather(args)
		if err != nil {
			return err
		}
	}

	failed := 0
	for _, got := range texts {
		report := prose.Check(got.text)
		if !report.OK() {
			failed++
		}
		if err := a.sayProse(got.name, report, quiet); err != nil {
			return err
		}
	}

	if failed == 0 {
		return nil
	}
	// Exit 6: the writing and the rule disagree, which is a conflict rather than a
	// malformed request. A hook branching on the code needs it to mean one thing.
	return fault.Conflict{Path: plainly(texts), Reason: fmt.Sprintf(
		"%d of %d did not meet the writing rule", failed, len(texts))}
}

// checked is one text and where it came from.
type checked struct {
	name string
	text string
}

// gather reads the paths given, walking directories for the kinds of file that hold
// prose. Source files are not walked: the rule is about documents, and an agent that
// had to rewrite every comment in a package to land a change would stop using it.
func gather(paths []string) ([]checked, error) {
	var out []checked
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fault.IO{Op: "stat", Path: path, Err: err}
		}
		if !info.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return nil, fault.IO{Op: "read", Path: path, Err: err}
			}
			out = append(out, checked{name: path, text: string(body)})
			continue
		}
		err = filepath.WalkDir(path, func(at string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !prosey(at) {
				return nil
			}
			body, err := os.ReadFile(at)
			if err != nil {
				return nil
			}
			out = append(out, checked{name: at, text: string(body)})
			return nil
		})
		if err != nil {
			return nil, fault.IO{Op: "walk", Path: path, Err: err}
		}
	}
	return out, nil
}

// prosey reports whether a file is the kind this rule is about.
func prosey(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".markdown":
		return true
	}
	return false
}

// sayProse draws one report.
func (a App) sayProse(name string, r prose.Report, quiet bool) error {
	score := fmt.Sprintf("%.0f%%", r.Score()*100)
	head := a.out.Good
	if !r.OK() {
		head = a.out.Alarm
	}

	// A passing file says one line, and only when it was asked for. A tool that
	// printed twenty clean lines to show one failure is one people pipe to grep.
	if r.OK() && quiet {
		return nil
	}
	if err := a.say(fmt.Sprintf("%s   %s   %s",
		head(score), a.out.Value(name),
		a.out.Muted(fmt.Sprintf("%d of %d sentences plain", r.Clean, r.Sentences)))); err != nil {
		return err
	}
	if r.OK() {
		return nil
	}

	for _, f := range r.Findings {
		if err := a.say(fmt.Sprintf("  %s %s   %s",
			a.out.Muted(fmt.Sprintf("%s:%d", name, f.Line)),
			a.out.Warn(string(f.Rule)), a.out.Muted(f.Detail))); err != nil {
			return err
		}
		if err := a.say("    " + a.out.Muted(f.Text)); err != nil {
			return err
		}
	}
	return nil
}

// plainly names what was checked, for a refusal that has to fit on a line.
func plainly(texts []checked) string {
	if len(texts) == 1 {
		return texts[0].name
	}
	return fmt.Sprintf("%d files", len(texts))
}
