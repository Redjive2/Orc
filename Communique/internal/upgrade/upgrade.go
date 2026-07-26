// Package upgrade pulls the tree, rebuilds every tool, and says what changed.
//
// It is the same work on both machines, which is why it is a package rather than
// two copies. The *server* runs it on itself directly; the *agent* runs it because
// an action came down the queue. Neither machine can reach the other — that is the
// whole architecture — so "upgrade everything" is one local upgrade plus one queued
// action per machine, and the queue is what makes it survive the restart in the
// middle.
//
// What it deliberately does not do is decide *whether* to upgrade. Authority for
// that is Orc's, checked before the request is made; this is the hands.
package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"orc/cq/internal/fault"
)

// Timeout bounds the whole thing. A pull that hangs on a network prompt and a
// build that hangs on a lock are the two ways this stops without stopping, and a
// server that never comes back is worse than one that failed loudly.
const Timeout = 10 * time.Minute

// Options say where the source is and where the binaries go.
type Options struct {
	// Source is the checkout to pull. Empty means this machine does not build
	// from source and cannot be upgraded, which is a refusal rather than a
	// silent success.
	Source string
	// Target is where the built binaries are installed. Empty means the directory
	// the running executable is in, which is almost always right: the thing being
	// replaced is the thing that is running.
	Target string
	// Run executes a command; tests replace it.
	Run func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// Report is what happened, in the order it happened.
type Report struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Before string   `json:"before"`
	After  string   `json:"after"`
	Steps  []Step   `json:"steps"`
	Built  []string `json:"built,omitempty"`
	// Changed is false when the pull found nothing new. The build still runs — a
	// working tree can be dirty, or a previous build can have failed halfway — but
	// a caller that restarts only on a change has the fact it needs.
	Changed bool `json:"changed"`
}

// Step is one command and what it came to.
type Step struct {
	What   string `json:"what"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Run pulls and rebuilds.
//
// The order matters and is not negotiable: pull, then build, then — and only then —
// does the caller restart anything. A restart before the build would bring the old
// binary back up; a restart after a *failed* build would bring nothing up at all.
// So this returns a report and never restarts, and the two callers decide.
//
// Replacing a running binary is safe on unix: the kernel holds the inode, so the
// process keeps running from the image it started with and picks up the new one
// when it next execs. That is what makes "rebuild myself, then restart" work at
// all, and it is why the build can happen before the restart rather than after.
func (o Options) Upgrade(ctx context.Context) (Report, error) {
	source := strings.TrimSpace(o.Source)
	if source == "" {
		return Report{}, fault.Usage{Reason: "no source checkout to build from; set $CQ_SOURCE to the repository"}
	}
	if _, err := os.Stat(filepath.Join(source, ".git")); err != nil {
		return Report{}, fault.Usage{Reason: fmt.Sprintf("%s is not a git checkout, so there is nothing to pull", source)}
	}

	target := strings.TrimSpace(o.Target)
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return Report{}, fault.IO{Op: "find", Subject: "this executable", Err: err}
		}
		target = filepath.Dir(exe)
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	report := Report{Source: source, Target: target}
	step := func(what, dir, name string, args ...string) ([]byte, error) {
		out, err := o.run(ctx, dir, name, args...)
		entry := Step{What: what, Output: trim(string(out))}
		if err != nil {
			entry.Error = trim(err.Error())
		}
		report.Steps = append(report.Steps, entry)
		return out, err
	}

	// The revision before and after, so the report says what actually moved rather
	// than only that a pull ran.
	if out, err := step("the revision before", source, "git", "rev-parse", "--short", "HEAD"); err == nil {
		report.Before = trim(string(out))
	}

	// `--ff-only` on purpose. A merge commit made by a server nobody is watching,
	// on a machine nobody is logged into, is a repository state somebody has to
	// come and untangle by hand. Refusing is the kinder failure.
	if _, err := step("pull", source, "git", "pull", "--ff-only"); err != nil {
		return report, fault.IO{Op: "pull", Subject: source,
			Err: fmt.Errorf("%v — the checkout may have local commits or changes", err)}
	}

	if out, err := step("the revision after", source, "git", "rev-parse", "--short", "HEAD"); err == nil {
		report.After = trim(string(out))
	}
	report.Changed = report.Before != "" && report.After != "" && report.Before != report.After

	// The tree's own build script, rather than a `go build` per module written out
	// again here. It already knows that every module builds from inside its own
	// directory — the whole point of the replace directives — and a second copy of
	// that knowledge would be the one that goes stale.
	script := filepath.Join(source, "sh", "build")
	if _, err := os.Stat(script); err != nil {
		return report, fault.IO{Op: "find", Subject: script,
			Err: fmt.Errorf("the tree has no build script, so there is nothing to run")}
	}
	out, err := step("build", source, script, "--to", target)
	if err != nil {
		return report, fault.IO{Op: "build", Subject: source, Err: fmt.Errorf("%s", trim(string(out)))}
	}
	report.Built = built(string(out))
	return report, nil
}

// built picks the tool names out of the build script's output, so the report can
// say what was replaced rather than only that something was.
func built(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// The script prints one line per tool, the name first. Anything that does
		// not look like that is a heading or a note and is not a tool.
		if len(fields) >= 2 && isToolName(fields[0]) {
			names = append(names, fields[0])
		}
	}
	return names
}

func isToolName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

func (o Options) run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if o.Run != nil {
		return o.Run(ctx, dir, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// A pull that wants a password must fail rather than wait: there is no
	// terminal here, and a prompt nobody can answer is a hang that looks like a
	// slow network.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.Bytes(), fmt.Errorf("timed out after %s", Timeout)
	}
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s: %s", err, trim(out.String()))
	}
	return out.Bytes(), nil
}

// trim keeps a step's output to something a queue entry can carry.
func trim(s string) string {
	s = strings.TrimSpace(s)
	const most = 2000
	if len(s) > most {
		return s[:most] + "…"
	}
	return s
}
