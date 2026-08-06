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
	"runtime"
	"sort"
	"strings"
	"time"

	"orc/common/watch"

	"orc/cq/internal/fault"
)

// Timeout bounds the whole thing. A pull that hangs on a network prompt and a
// build that hangs on a lock are the two ways this stops without stopping, and a
// server that never comes back is worse than one that failed loudly.
const Timeout = 10 * time.Minute

// Options say where the source is and where the binaries go.
type Options struct {
	// Dirty allows a build from a checkout with uncommitted changes in it. Off by
	// default: an upgrade installs what it builds on every machine, and doing that
	// from somebody's work in progress is the kind of mistake that is only found
	// later, everywhere at once.
	Dirty bool

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
	// Replaced names the files in Target whose contents actually moved, measured
	// rather than inferred.
	//
	// `Built` says what the build script *reported* building. This says what is on
	// the disk now that was not there before, which is a different question and the
	// one that matters: a build can report nine tools and install them somewhere
	// nobody runs from, and every layer above this then reports a successful
	// upgrade that changed nothing.
	Replaced []string `json:"replaced,omitempty"`
}

// Untouched reports whether a path in the install directory came through the build
// unchanged.
//
// The question a caller asks before restarting: is the binary I am about to exec
// the new one? Answering it needed a fact nobody was collecting.
func (r Report) Untouched(path string) bool {
	for _, got := range r.Replaced {
		if got == path {
			return false
		}
	}
	return true
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

	target, err := installDir(o.Target)
	if err != nil {
		return Report{}, err
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

	// A branch with no upstream cannot be pulled, and the failure git gives for it
	// is four lines of advice about `--set-upstream-to` that arrive inside a server
	// log. Asked first, so the refusal names the actual condition — a checkout on a
	// branch the remote does not have is the usual cause, and it is nothing like
	// "local commits".
	// A checkout with uncommitted work in it builds that work and installs it
	// across the fleet. `sh/pull` refuses this outright and the upgrade did not —
	// it only consulted the working tree *after* a failed pull, to explain the
	// failure. A tree that fast-forwards cleanly with edits in it went straight
	// through.
	if !o.Dirty {
		if what := o.dirty(ctx, source); what != "" {
			report.Steps = append(report.Steps, Step{What: "status", Output: trim(what)})
			return report, fault.Conflict{Subject: source, Reason: "the checkout has uncommitted " +
				"changes, and an upgrade installs what it builds on every machine; commit or stash " +
				"them, or pass --dirty to build them anyway"}
		}
	}
	if why := o.checkUpstream(ctx, source); why != "" {
		report.Steps = append(report.Steps, Step{What: "upstream", Error: why})
		return report, fault.IO{Op: "pull", Subject: source, Err: fmt.Errorf("%s", why)}
	}

	// `--ff-only` on purpose. A merge commit made by a server nobody is watching,
	// on a machine nobody is logged into, is a repository state somebody has to
	// come and untangle by hand. Refusing is the kinder failure.
	if out, err := step("pull", source, "git", "pull", "--ff-only"); err != nil {
		return report, fault.IO{Op: "pull", Subject: source,
			Err: fmt.Errorf("%v — %s", err, o.whyPullFailed(ctx, source, string(out)))}
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
	// Before the script rather than inside it: the script reports one failure per
	// module, so a missing toolchain arrives as nine identical build failures and
	// reads as a broken tree.
	if err := o.toolchain(ctx, source); err != nil {
		report.Steps = append(report.Steps, Step{What: "go version", Error: trim(err.Error())})
		return report, err
	}
	name, args, err := runScript(script, "--to", target)
	if err != nil {
		report.Steps = append(report.Steps, Step{What: "build", Error: trim(err.Error())})
		return report, err
	}
	// What is in the install directory before the build, so the report can say what
	// the build actually moved rather than what it said it would.
	before := stampAll(target)

	out, err := step("build", source, name, args...)
	if err != nil {
		return report, fault.IO{Op: "build", Subject: source, Err: fmt.Errorf("%s", trim(string(out)))}
	}
	report.Built = built(string(out))
	report.Replaced = movedSince(target, before)
	if len(report.Replaced) == 0 {
		report.Steps = append(report.Steps, Step{What: "install",
			Error: "the build reported success and no file in " + target + " changed"})
	}
	return report, nil
}

// installDir works out where the binaries go, and tolerates being handed the
// binary instead of the directory.
//
// `$CQ_BIN` names two different things in this tree and always has. `Common/nudge`
// reads it as *the cq command to run*, and this reads it as *the directory to
// install into* — so a machine that set it the way nudge documents had every upgrade
// die inside the build script at `mkdir -p /usr/local/bin/cq`, which is a file. The
// server logged that it was still on the old build and the browser said it was
// building, which is the worst possible pair.
//
// Both readings are now accepted, because both are already out there in shell
// profiles and neither is wrong: a path that is an existing file is the cq binary,
// and its directory is where the tools go. What is refused is the third case — a
// path that exists and is neither — because guessing there would install a fleet's
// tools somewhere nobody asked for.
func installDir(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fault.IO{Op: "find", Subject: "this executable", Err: err}
		}
		return filepath.Dir(exe), nil
	}

	// Absolute, always. The build script never changes to the repository root, so a
	// relative target resolves against two different directories inside one run:
	// `mkdir -p "$target"` against the script's own working directory, and
	// `go build -o "$target/cq"` against the *module* directory it built from. The
	// binaries then land inside the checkout, in a directory nothing is on the path
	// of, and the upgrade reports success.
	//
	// `$CQ_BIN` is documented as taking the same spelling a nudge does, which is a
	// bare command name — that is, exactly a relative path.
	if !filepath.IsAbs(target) {
		return "", fault.Usage{Reason: fmt.Sprintf(
			"the install directory %q is relative; give an absolute path, because the build "+
				"script resolves it against a different directory from the one you are in", target)}
	}

	got, err := os.Stat(target)
	switch {
	case err != nil:
		// Not there yet. The build script creates it, which is right for a fresh
		// machine and is what `--to` has always done.
		return target, nil
	case got.IsDir():
		return target, nil
	case got.Mode().IsRegular():
		return filepath.Dir(target), nil
	default:
		return "", fault.Usage{Reason: fmt.Sprintf(
			"$CQ_BIN is %s, which is neither a directory to install into nor a binary to "+
				"install beside", target)}
	}
}

// Toolchain is the first thing checked before a build, and the failure it catches is
// the commonest one on a server.
//
// A supervised process inherits the supervisor's environment, not a login shell's,
// and Go is very often installed somewhere only a login shell knows about. The
// symptom without this check is a build script that fails per module with
// `go: command not found` buried in a few hundred lines of step output — a message
// about the tree when the problem is the PATH of the process reading it.
func (o Options) toolchain(ctx context.Context, source string) error {
	if _, err := o.run(ctx, source, "go", "version"); err != nil {
		return fault.Usage{Reason: fmt.Sprintf(
			"no working go toolchain on this server's PATH (%s) — a supervised process does "+
				"not inherit a login shell's environment, so go has to be on the PATH the "+
				"supervisor gives it", os.Getenv("PATH"))}
	}
	return nil
}

// built picks the tool names out of the build script's output, so the report can
// say what was replaced rather than only that something was.
//
// The format is one line per **module**, and the module name comes first:
//
//	Anno         ok anno-hook anno
//	Common       ok (library)
//	Communique   ok cq
//
// This used to read the first field as the tool name and require it to be lower
// case. Every real line begins with a capital, so every real line was rejected and
// the report was empty — while the note the script prints when the install
// directory is off `$PATH`,
//
//	export PATH="/tmp/x:$PATH"
//
// began with a lower-case word and two fields, and was accepted. On the output the
// script actually produces the answer was `["export"]`, on every upgrade, and the
// test that passed fed it lines the script cannot print.
//
// So it reads the shape instead: a module, the word `ok`, and the commands after
// it. A module with no commands says `(library)`, which is not a tool.
func built(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "ok" {
			continue
		}
		for _, name := range fields[2:] {
			if isToolName(name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// isToolName reports whether a field is a command's name rather than a note.
//
// Commands in this tree are lower case with hyphens — `anno-hook`, `orc-session`.
// `(library)` is the one other thing that appears in that position, and the
// parentheses are what rule it out.
func isToolName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// dirty reports what is uncommitted in the checkout, or "" when it is clean.
//
// Unreadable answers "clean". This gates a build rather than reporting damage, and
// a `git status` that could not run must not be able to stop an upgrade — the pull
// that follows is the step that actually refuses a tree it cannot fast-forward.
func (o Options) dirty(ctx context.Context, source string) string {
	out, err := o.run(ctx, source, "git", "status", "--porcelain")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkUpstream reports why this checkout cannot be pulled, or "" if it can.
//
// `git pull --ff-only` with no arguments pulls the current branch's upstream, and a
// branch that has none fails with advice written for somebody at a terminal. On a
// server that advice lands in a log, hours later, under a message about the upgrade
// failing — so the condition is diagnosed here instead, in the words of what to do
// about it.
//
// It does not fix anything. Setting an upstream or switching branches on a machine
// nobody is logged into is the same class of thing `--ff-only` exists to refuse: a
// repository state somebody has to come and untangle. Saying exactly what is wrong,
// and the one command that resolves it, is the useful half.
func (o Options) checkUpstream(ctx context.Context, source string) string {
	branch := o.git(ctx, source, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" || branch == "HEAD" {
		// Detached, or not on a branch at all. Nothing to pull into.
		return "the checkout is not on a branch, so there is nothing to pull into; " +
			"`git switch <branch>` in " + source
	}
	if up := o.git(ctx, source, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); up != "" {
		return ""
	}

	// No upstream. Which of the two shapes is it — a branch the remote has under
	// the same name, or a branch the remote has never heard of?
	if o.git(ctx, source, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch) != "" {
		return fmt.Sprintf("the branch %s has no upstream, so `git pull` does not know what to merge; "+
			"`git branch --set-upstream-to=origin/%s %s` in %s", branch, branch, branch, source)
	}

	where := "origin"
	if head := o.git(ctx, source, "rev-parse", "--abbrev-ref", "origin/HEAD"); head != "" {
		where = head
	} else if o.git(ctx, source, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main") != "" {
		where = "origin/main"
	}
	return fmt.Sprintf("the checkout is on %s, which %s does not have — so there is nothing to pull; "+
		"`git switch %s` in %s puts it on the branch the remote does have",
		branch, where, strings.TrimPrefix(where, "origin/"), source)
}

// whyPullFailed turns a fast-forward refusal into the reason for it.
//
// The old text blamed "local commits or changes" whatever had happened, which sent
// somebody looking through a clean checkout for edits that were not there.
func (o Options) whyPullFailed(ctx context.Context, source, output string) string {
	if strings.Contains(output, "Not possible to fast-forward") ||
		strings.Contains(output, "not possible to fast-forward") {
		return "the checkout has commits the remote does not; it cannot fast-forward"
	}
	if dirty := o.git(ctx, source, "status", "--porcelain"); dirty != "" {
		return "the checkout has uncommitted changes"
	}
	return "the pull was refused; the output above is git's own"
}

// git runs one read-only git command and returns its trimmed output, or "" if it
// failed. Every caller here is asking a question whose failure *is* an answer.
func (o Options) git(ctx context.Context, dir string, args ...string) string {
	out, err := o.run(ctx, dir, "git", args...)
	if err != nil {
		return ""
	}
	return trim(string(out))
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
	// Bounded, because none of this output is trusted to be small. `git status
	// --porcelain` is one line per changed path and a checkout can hold a stray
	// `node_modules`; a build that loops prints until it is stopped. Everything read
	// here is either counted or trimmed to two thousand characters, so nothing is
	// lost that anybody would have seen — and without the cap a poll could put a
	// gigabyte in this process's heap for a number.
	out := &capped{most: MaxOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.Bytes(), fmt.Errorf("timed out after %s", Timeout)
	}
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s: %s", err, trim(out.String()))
	}
	return out.Bytes(), nil
}

// MaxOutput is how much of a command's output is kept.
//
// A megabyte, which is far more than any of these produce and far less than the
// worst of them could. Everything read is counted or trimmed to two thousand
// characters before anybody sees it, so the cap costs nothing real.
const MaxOutput = 1 << 20

// capped is a writer that stops at a limit rather than growing without one.
//
// A subprocess writing into a bytes.Buffer is that process choosing how much memory
// this one uses, which is only ever acceptable when the amount is known. It is not
// known here: this reads the output of `git status` in somebody else's checkout.
type capped struct {
	buf  bytes.Buffer
	most int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.most - c.buf.Len(); room > 0 {
		if len(p) > room {
			p, c.over = p[:room], true
		}
		c.buf.Write(p)
	} else if len(p) > 0 {
		c.over = true
	}
	// The full length, always: a short write is an error to the process writing it,
	// and killing a build because its output was long would be the cap deciding
	// something it has no business deciding.
	return len(p), nil
}

func (c *capped) Bytes() []byte { return c.buf.Bytes() }

func (c *capped) String() string { return c.buf.String() }

// trim keeps a step's output to something a queue entry can carry.
func trim(s string) string {
	s = strings.TrimSpace(s)
	const most = 2000
	if len(s) > most {
		return s[:most] + "…"
	}
	return s
}

// runScript is how the build script is started on this platform.
//
// On unix the script is executable and has a shebang, so it runs as itself. On
// Windows it is neither: `sh/build` has no extension, so it matches no PATHEXT
// entry, and a text file with a `#!` line is not a PE image. `CreateProcess`
// refuses it, and the upgrade failed there with an error about a file that plainly
// exists — which is the whole of why Windows has never been able to rebuild.
//
// So Windows runs it through bash. Git for Windows ships one and nearly every
// machine with a checkout on it has Git; WSL and MSYS2 provide one too. Where none
// is found the refusal names what to install rather than repeating the exec error.
func runScript(script string, args ...string) (string, []string, error) {
	if runtime.GOOS != "windows" {
		return script, args, nil
	}
	shell, err := findBash()
	if err != nil {
		return "", nil, err
	}
	// A Windows path reaches bash as it is; Git for Windows accepts one.
	return shell, append([]string{script}, args...), nil
}

// bashPlaces are where Git for Windows puts bash, in the order they are tried.
//
// PATH first, so a machine that has made a choice keeps it.
var bashPlaces = []string{
	`C:\Program Files\Git\bin\bash.exe`,
	`C:\Program Files (x86)\Git\bin\bash.exe`,
	`C:\Program Files\Git\usr\bin\bash.exe`,
}

func findBash() (string, error) {
	if got, err := exec.LookPath("bash"); err == nil {
		return got, nil
	}
	for _, place := range bashPlaces {
		if info, err := os.Stat(place); err == nil && info.Mode().IsRegular() {
			return place, nil
		}
	}
	return "", fault.Usage{Reason: "the build script needs bash, and none was found on PATH or " +
		"in the usual Git for Windows places; install Git for Windows, or run `sh/build` by hand " +
		"from a shell that has one"}
}

// stampAll records every file in a directory, so a later look can say what moved.
//
// Size and modification time, which is what watch.Look measures and what the
// watchers already restart on. A hash would be surer and is not worth reading every
// binary in the tree twice for — a `go build` that writes a file always moves its
// mtime, and the failure this catches is a file that was **not written at all**.
//
// An unreadable directory stamps as empty, which reports every file as new. That is
// the direction to fail in: an upgrade wrongly reported as having changed something
// costs a restart, and one wrongly reported as having changed nothing costs the
// upgrade.
func stampAll(dir string) map[string]watch.Stamp {
	out := map[string]watch.Stamp{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if got, err := watch.Look(path); err == nil {
			out[path] = got
		}
	}
	return out
}

// movedSince names the files that are new or changed, in a stable order.
func movedSince(dir string, before map[string]watch.Stamp) []string {
	var moved []string
	for path, now := range stampAll(dir) {
		was, seen := before[path]
		if !seen || now.Size != was.Size || !now.Mod.Equal(was.Mod) {
			moved = append(moved, path)
		}
	}
	sort.Strings(moved)
	return moved
}
