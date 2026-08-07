package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Whether an upgrade would work, asked without doing one.
//
// The button that rebuilds the fleet is the only control in cq that takes the site
// down, and until now the only way to find out whether it *could* was to press it
// and read the wreckage afterwards. Every reason a build fails is knowable in
// advance — a detached head, a branch with no upstream, a checkout that has diverged
// from the remote, no toolchain on the supervisor's PATH — and none of them is
// discoverable from a browser.
//
// Read-only, and that is a constraint rather than an observation. This is a GET the
// page polls, so it runs no fetch: `git fetch` is a network call and a write to the
// local refs, and doing either on a poll would make looking at a screen change the
// thing being looked at. What that costs is that `behind` is as fresh as the last
// fetch, which the page says. It costs nothing that matters — being behind is the
// *reason* to rebuild, never a reason not to — and every condition that actually
// blocks a build is answerable from local refs.

// The three verdicts, worst first. Strings rather than an integer because they cross
// the wire to a browser, and a number would have to be decoded on both sides.
const (
	// Stop: the build or the pull will fail. Pressing it wastes ten minutes.
	Stop = "stop"
	// Caution: it will probably work, and what comes out may not be what is wanted
	// — unpushed commits, uncommitted edits, a server that cannot restart itself.
	Caution = "caution"
	// Go: nothing in the way.
	Go = "go"
)

// Status is what the checkout is, and what that means for rebuilding.
type Status struct {
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Upstream is the remote-tracking branch `git pull` would merge, when there is
	// one. Its absence is the commonest reason a server cannot upgrade itself.
	Upstream string `json:"upstream,omitempty"`
	Head     string `json:"head,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Dirty is how many paths `git status --porcelain` reports.
	Dirty int `json:"dirty"`
	// Ahead and Behind are against the remote-tracking ref, so both are as of the
	// last fetch. See the note above about why this does not fetch.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Toolchain and Script are the two things the build needs that have nothing to
	// do with git, and both fail in ways that read as something else.
	Toolchain bool `json:"toolchain"`
	Script    bool `json:"script"`
	// Unknown is set when a probe was cut short rather than answered. It is the
	// difference between "this machine has no go" and "nobody asked it in time", and
	// the two must not print the same word: one is a reason not to press the button
	// and the other is a reason the screen cannot say.
	Unknown bool `json:"unknown,omitempty"`

	// Verdict is the worst of the reasons: `stop`, `caution`, or `go`.
	Verdict string   `json:"verdict"`
	Reasons []Reason `json:"reasons,omitempty"`
}

// Reason is one thing worth knowing before pressing the button, and how bad it is.
//
// The fix travels with it. A reason on a screen that says what is wrong and not what
// to do about it sends somebody to a terminal to work out a command this code
// already knows.
type Reason struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"`
}

// CheckTimeout bounds **one** command, and CheckBudget the whole inspection.
//
// Seconds, not the ten minutes an upgrade gets, and the difference is the point: an
// upgrade is work somebody asked for and waits on, and this is a question a page
// asks on a timer. The ways git takes forever are all real — an index lock held by
// another process, a checkout on a filesystem that has gone away — and every one of
// them would otherwise hold a request open until the browser gave up, with the
// subprocess still running and the next poll starting another.
//
// Two numbers rather than one, because one was the bug. Every probe shared a single
// budget and `go version` ran last, so a slow git spent the whole of it and the
// toolchain probe was cancelled before it started. The panel then said "no working
// go toolchain on this machine's PATH" — a hard stop, blaming a thing that was
// installed and working — and the button, which has ten minutes, rebuilt the fleet
// perfectly. A screen that says a build is impossible while the build succeeds is
// worse than no screen.
//
// So a slow command now spends its own time and nobody else's.
// The whole inspection keeps the ten seconds it always had: this is a GET a page
// polls, and a request held open past that is one the browser abandons with the
// subprocess still running and the next poll starting another. Widening it to fit
// four commands would have traded a wrong answer for a hung page.
//
// What changed is that one command may no longer spend all of it.
const (
	CheckTimeout = 4 * time.Second
	CheckBudget  = 10 * time.Second
)

// Check reports whether this machine could rebuild itself.
//
// It never returns an error. Every way of failing here is a fact about the checkout
// and belongs in the answer — a route that returned 500 because git was not
// installed would be reporting the diagnosis as a fault of the diagnosis.
func (o Options) Check(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, CheckBudget)
	defer cancel()

	source := strings.TrimSpace(o.Source)
	got := Status{Source: source, Verdict: Go}

	if source == "" {
		got.Verdict = Stop
		got.Reasons = []Reason{{Level: Stop,
			Text: "this machine has no checkout to build from",
			Fix:  "set $CQ_SOURCE to the repository and restart cq"}}
		return got
	}
	if _, err := os.Stat(filepath.Join(source, ".git")); err != nil {
		got.Verdict = Stop
		got.Reasons = []Reason{{Level: Stop,
			Text: source + " is not a git checkout, so there is nothing to pull"}}
		return got
	}
	if target, err := installDir(o.Target); err == nil {
		got.Target = target
	} else {
		got.Reasons = append(got.Reasons, Reason{Level: Stop, Text: err.Error()})
	}

	// The toolchain first, and deliberately.
	//
	// It is the cheapest question here — `go version` prints a string — and it was
	// asked last, so every slow git ahead of it came out of its budget. It is also
	// the one whose false negative was worst: "no working go toolchain" reads as a
	// broken machine rather than as a slow moment. Asking it first costs the git
	// calls a few milliseconds they have to spare.
	_, cut, err := o.probe(ctx, source, "go", "version")
	got.Toolchain = err == nil
	got.Unknown = cut

	got.Head = o.ask(ctx, &got, source, "rev-parse", "--short", "HEAD")
	got.Branch = o.ask(ctx, &got, source, "rev-parse", "--abbrev-ref", "HEAD")
	got.Detached = got.Branch == "" || got.Branch == "HEAD"
	got.Upstream = o.ask(ctx, &got, source, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")

	// The same diagnosis the upgrade itself performs, so a screen that says the
	// build will fail and a build that fails cannot disagree about why.
	if why := o.checkUpstream(ctx, source); why != "" {
		text, fix, _ := strings.Cut(why, "; ")
		// Probe-derived, so it follows the same rule as the rest: a `rev-parse` that
		// was cut short reads as a detached head, and a detached head is a stop.
		level := Stop
		if got.Unknown {
			level = Caution
			text = "could not read the checkout in time — " + text
		}
		got.Reasons = append(got.Reasons, Reason{Level: level, Text: text, Fix: fix})
	}

	// The same call the upgrade itself makes, so a screen that says a build will be
	// refused and a build that refuses cannot disagree about whether the tree is
	// clean. One line per changed path, and a checkout can hold a great many — a
	// stray `node_modules`, a build directory nobody ignored — which is what the cap
	// in `run` is for. A count that stops at the cap still reports the thing that
	// matters, which is that there is uncommitted work.
	if what := o.dirty(ctx, source); what != "" {
		got.Dirty = len(strings.Split(what, "\n"))
	}
	if got.Upstream != "" {
		// `--left-right` against the merge base: left is what the upstream has and
		// this does not, right is the reverse. One command for both, so the two
		// numbers cannot be counted against different bases.
		fields := strings.Fields(o.ask(ctx, &got, source, "rev-list", "--left-right", "--count", "@{u}...HEAD"))
		if len(fields) == 2 {
			got.Behind, _ = strconv.Atoi(fields[0])
			got.Ahead, _ = strconv.Atoi(fields[1])
		}
	}

	_, statErr := os.Stat(filepath.Join(source, "sh", "build"))
	got.Script = statErr == nil

	got.Reasons = append(got.Reasons, o.judge(got)...)
	for _, r := range got.Reasons {
		if r.Level == Stop {
			got.Verdict = Stop
			break
		}
		if r.Level == Caution {
			got.Verdict = Caution
		}
	}
	return got
}

// judge turns the facts into the things worth saying about them.
//
// Separate from the gathering so the rules are readable in one place: what makes a
// build impossible against what makes it unwise is the judgement this whole screen
// is, and it should not be scattered through the commands that establish the facts.
func (o Options) judge(got Status) []Reason {
	var out []Reason

	// When the inspection was cut short, nothing it *inferred* may be a hard stop.
	//
	// A probe that did not run says nothing about the checkout, and every fact
	// below this line comes from one. The three that do not — no source, not a
	// checkout, no build script — are `os.Stat` calls that answer instantly and
	// return before this is reached, so they keep their stop.
	//
	// This is the rule the whole file turns on: "cannot rebuild" is a claim about
	// the machine, and a screen may only make it when it asked and was told.
	worst := Stop
	if got.Unknown {
		worst = Caution
	}

	switch {
	case got.Unknown:
		// Not a fact about the machine. The probe was cut short — a loaded machine,
		// a cold cache, a git call ahead of it that took its time — and reporting
		// that as "no toolchain" is how this panel came to say a build was
		// impossible while the build worked.
		out = append(out, Reason{Level: Caution,
			Text: "could not check the go toolchain in time, so this may be better than it looks",
			Fix:  "press it and watch, or run `go version` on the serving machine"})
	case !got.Toolchain:
		out = append(out, Reason{Level: worst,
			Text: "no working go toolchain on this machine's PATH",
			Fix: "a supervised process does not inherit a login shell's environment; " +
				"go has to be on the PATH the supervisor gives it"})
	}
	if !got.Script {
		out = append(out, Reason{Level: Stop,
			Text: "the checkout has no sh/build, so there is nothing to run"})
	}

	// Diverged. `--ff-only` refuses this and is right to: a merge made by a server
	// nobody is watching is a repository somebody has to come and untangle.
	if got.Ahead > 0 && got.Behind > 0 {
		out = append(out, Reason{Level: worst,
			Text: plural(got.Ahead, "commit") + " here the remote does not have, and " +
				plural(got.Behind, "commit") + " there this does not — it cannot fast-forward",
			Fix: "rebase or merge in " + got.Source + ", by hand, where somebody can see it"})
	} else if got.Ahead > 0 {
		// Not a blocker: the pull is a no-op and the build succeeds. It is worth
		// saying because what gets built is not what the remote has, which is
		// usually a surprise rather than a plan.
		out = append(out, Reason{Level: Caution,
			Text: plural(got.Ahead, "commit") + " here that the remote does not have — " +
				"the build will include " + itOrThem(got.Ahead)})
	}

	if got.Dirty > 0 {
		// A stop, because `Upgrade` refuses outright: an upgrade installs what it
		// builds on every machine, and doing that from somebody's work in progress is
		// a mistake found later, everywhere at once. This has to follow that rule
		// rather than judge the tree for itself — a light saying "probably shouldn't"
		// over a build that will certainly be refused is the disagreement this whole
		// screen exists to prevent.
		//
		// With `--dirty` it is the caution it reads as: the build runs, and what comes
		// out is not what the remote has.
		out = append(out, Reason{
			Level: map[bool]string{true: Caution, false: worst}[o.Dirty],
			Text: plural(got.Dirty, "uncommitted change") + " in the checkout, and an upgrade " +
				"installs what it builds on every machine",
			Fix: "commit or stash in " + got.Source + ", or pass --dirty to build them anyway",
		})
	}

	if got.Behind == 0 && got.Upstream != "" && !got.Detached {
		// Not a reason against, and worth saying: somebody pressing this expects new
		// code, and "nothing to pull" is the answer to why the version did not move.
		out = append(out, Reason{Level: Go,
			Text: "nothing new to pull — the build would rebuild what is already here"})
	}
	return out
}

func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// ask is probe for the commands whose output is a string, recording on the status
// when one of them was cut short rather than answered.
//
// Every fact below this line is read off a probe, and a probe that did not run says
// nothing about the checkout. Before this, a `rev-parse` that timed out returned ""
// — which reads as a detached head, which is a hard stop — so a slow moment made
// the panel refuse a build that would have worked.
func (o Options) ask(ctx context.Context, got *Status, dir string, args ...string) string {
	out, cut, err := o.probe(ctx, dir, "git", args...)
	if cut {
		got.Unknown = true
	}
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// probe runs one command with a budget of its own, and says whether it was cut off
// rather than answered.
//
// The two facts are different and were being conflated. `o.run` returns an error for
// a command that failed and for one that never ran, and the caller could not tell
// them apart — so a probe that timed out was recorded as a machine that lacks the
// thing being probed.
func (o Options) probe(ctx context.Context, dir, name string, args ...string) ([]byte, bool, error) {
	own, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()

	out, err := o.run(own, dir, name, args...)
	if err == nil {
		return out, false, nil
	}
	// Its own deadline, or the whole inspection's. Either way nobody asked the
	// question, so nobody may report the answer.
	cut := own.Err() != nil || ctx.Err() != nil
	return out, cut, err
}
