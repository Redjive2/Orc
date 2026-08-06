package upgrade_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/upgrade"
)

// checkout makes a directory that looks enough like the tree to build from: a
// `.git` and an `sh/build`. Both are checked before anything runs, so both have to
// be here for the happy path to be reachable.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{".git", "sh"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sh", "build"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// recorder captures the commands rather than running them.
type recorder struct {
	calls  [][]string
	out    map[string]string
	failAt string
}

func (r *recorder) run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	joined := strings.Join(call, " ")
	if r.failAt != "" && strings.Contains(joined, r.failAt) {
		return []byte("it went wrong"), errors.New("exit status 1")
	}
	for key, out := range r.out {
		if strings.Contains(joined, key) {
			return []byte(out), nil
		}
	}
	// A healthy checkout by default: on a branch, with an upstream. Without these
	// every test would exercise the refusal path for a checkout that cannot be
	// pulled, which is not what any of them is about.
	switch {
	case strings.Contains(joined, "--abbrev-ref --symbolic-full-name @{u}"):
		return []byte("origin/main\n"), nil
	case strings.Contains(joined, "rev-parse --abbrev-ref HEAD"):
		return []byte("main\n"), nil
	}
	return nil, nil
}

// TestUpgradePullsThenBuilds is the order the whole design rests on.
//
// The caller restarts *after* this returns, so a build that has not run yet would
// bring the old binary back up. Pull, build, then hand back — and never a restart
// from in here.
func TestUpgradePullsThenBuilds(t *testing.T) {
	dir := checkout(t)
	r := &recorder{out: map[string]string{
		"rev-parse": "aaaaaaa\n",
		"sh/build":  buildOutput,
	}}

	report, err := upgrade.Options{Source: dir, Target: "/tmp/bin", Run: r.run}.Upgrade(t.Context())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	var order []string
	for _, call := range r.calls {
		order = append(order, strings.Join(call, " "))
	}
	joined := strings.Join(order, " | ")
	pull := strings.Index(joined, "git pull")
	build := strings.Index(joined, "build")
	if pull < 0 || build < 0 || pull > build {
		t.Errorf("the pull did not come before the build: %s", joined)
	}
	if !strings.Contains(joined, "--ff-only") {
		t.Errorf("the pull was not --ff-only, so it could merge on a machine nobody is watching: %s", joined)
	}
	if !strings.Contains(joined, "--to /tmp/bin") {
		t.Errorf("the build was not told where to install: %s", joined)
	}
	// The report names what was replaced, so an operator reading the queue knows
	// which tools moved rather than only that something did. What it should say
	// about this output is pinned by TestBuiltReadsWhatTheScriptPrints; here it is
	// enough that the order of the steps did not cost the report.
	if len(report.Built) == 0 {
		t.Errorf("the report does not say what was built: %+v", report.Built)
	}
}

// TestUpgradeReportsTheRevision, and tells "nothing new" apart from "moved".
func TestUpgradeReportsTheRevision(t *testing.T) {
	dir := checkout(t)

	// Two different revisions: something moved.
	moved := 0
	r := &recorder{}
	r.out = map[string]string{}
	opts := upgrade.Options{Source: dir, Run: func(ctx context.Context, d, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			moved++
			if moved == 1 {
				return []byte("aaaaaaa\n"), nil
			}
			return []byte("bbbbbbb\n"), nil
		}
		return r.run(ctx, d, name, args...)
	}}
	report, err := opts.Upgrade(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Before != "aaaaaaa" || report.After != "bbbbbbb" || !report.Changed {
		t.Errorf("a moved checkout reported %+v", report)
	}

	// The same revision twice: nothing new, and the build still runs. A working
	// tree can be dirty and a previous build can have failed halfway, so "no new
	// commits" is not "no work to do".
	same := &recorder{out: map[string]string{"rev-parse": "aaaaaaa\n"}}
	report, err = upgrade.Options{Source: dir, Run: same.run}.Upgrade(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Changed {
		t.Errorf("an unmoved checkout reported a change: %+v", report)
	}
	if !strings.Contains(strings.Join(flatten(same.calls), " "), "build") {
		t.Errorf("the build was skipped because nothing was pulled")
	}
}

// TestUpgradeRefusesWhatItCannotDo: the two ways a machine is not one that builds.
func TestUpgradeRefusesWhatItCannotDo(t *testing.T) {
	// No source at all — the ordinary case for a machine that installs binaries.
	_, err := upgrade.Options{}.Upgrade(t.Context())
	if !errors.Is(err, fault.ErrUsage) {
		t.Errorf("a machine with no checkout: %v, want a usage fault", err)
	}
	if err == nil || !strings.Contains(err.Error(), "CQ_SOURCE") {
		t.Errorf("the refusal does not say what to set: %v", err)
	}

	// A directory that is not a checkout.
	_, err = upgrade.Options{Source: t.TempDir()}.Upgrade(t.Context())
	if err == nil || !strings.Contains(err.Error(), "git checkout") {
		t.Errorf("a directory that is not a checkout: %v", err)
	}

	// A checkout with no build script. Caught before the pull would have run,
	// so a machine that cannot finish does not start.
	bare := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bare, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	_, err = upgrade.Options{Source: bare, Run: r.run}.Upgrade(t.Context())
	if err == nil || !strings.Contains(err.Error(), "build script") {
		t.Errorf("a tree with no build script: %v", err)
	}
}

// TestAFailedPullDoesNotBuild: half an upgrade is worse than none. A pull that
// could not fast-forward leaves the machine on the build it already had.
func TestAFailedPullDoesNotBuild(t *testing.T) {
	dir := checkout(t)
	r := &recorder{failAt: "git pull"}

	_, err := upgrade.Options{Source: dir, Run: r.run}.Upgrade(t.Context())
	if err == nil {
		t.Fatal("a failed pull was reported as a success")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("the failure does not say the pull was refused: %v", err)
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "sh/build") {
			t.Errorf("it built anyway after the pull failed: %v", r.calls)
		}
	}
}

// TestTheReportCarriesEveryStep, so a queue entry says where it stopped rather
// than only that it did.
func TestTheReportCarriesEveryStep(t *testing.T) {
	dir := checkout(t)
	r := &recorder{failAt: "sh/build", out: map[string]string{"rev-parse": "aaaaaaa\n"}}

	report, err := upgrade.Options{Source: dir, Run: r.run}.Upgrade(t.Context())
	if err == nil {
		t.Fatal("a failed build was reported as a success")
	}
	if len(report.Steps) == 0 {
		t.Fatal("the report carries no steps")
	}
	last := report.Steps[len(report.Steps)-1]
	if last.What != "build" || last.Error == "" {
		t.Errorf("the last step does not say the build failed: %+v", last)
	}
	// And the pull is still in there, so the report says what *did* happen too.
	if !strings.Contains(fmt.Sprint(report.Steps), "pull") {
		t.Errorf("the report lost the steps that worked: %+v", report.Steps)
	}
}

func flatten(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// TestACheckoutThatCannotBePulledSaysWhy.
//
// `git pull --ff-only` with no arguments pulls the current branch's upstream, and a
// branch with none fails with four lines of advice written for somebody at a
// terminal. On a server that advice lands in a log, hours later, under a message
// about the upgrade failing — and the reason cq gave was "the checkout may have
// local commits or changes", which is not what happened and sends somebody looking
// through a clean tree for edits that are not there.
func TestUpgradeNamesWhyItCannotPull(t *testing.T) {
	for _, tc := range []struct {
		what string
		// git answers, by the fragment of the command that asks the question.
		out  map[string]string
		want []string
	}{
		{
			what: "a branch the remote has, with no upstream set",
			out: map[string]string{
				// An empty answer stands for the command failing, which is how git
				// reports that a branch has no upstream.
				"@{u}":                        "",
				"rev-parse --abbrev-ref HEAD": "main\n",
				"rev-parse --verify --quiet refs/remotes/origin/main": "0123456\n",
			},
			want: []string{"no upstream", "--set-upstream-to=origin/main main"},
		},
		{
			what: "a branch the remote has never heard of",
			out: map[string]string{
				"@{u}":                               "",
				"rev-parse --abbrev-ref HEAD":        "master\n",
				"rev-parse --abbrev-ref origin/HEAD": "origin/main\n",
			},
			want: []string{"master", "origin/main does not have", "git switch main"},
		},
		{
			what: "a detached head",
			out:  map[string]string{"rev-parse --abbrev-ref HEAD": "HEAD\n"},
			want: []string{"not on a branch"},
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			r := &recorder{out: tc.out}
			_, err := upgrade.Options{Source: checkout(t), Run: r.run}.Upgrade(t.Context())
			if err == nil {
				t.Fatal("a checkout that cannot be pulled was upgraded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q:\n%v", want, err)
				}
			}
			// And it stopped before doing anything: half an upgrade is worse than
			// none, and nothing here was going to work.
			for _, call := range r.calls {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "git pull") || strings.Contains(joined, "sh/build") {
					t.Errorf("it carried on regardless: %v", r.calls)
				}
			}
		})
	}
}

// A tree with commits of its own is the case the old message described, and it
// still says so — from git's own words rather than from a guess.
func TestUpgradeSaysWhenItCannotFastForward(t *testing.T) {
	r := &recorder{failAt: "git pull", out: map[string]string{
		"pull --ff-only": "fatal: Not possible to fast-forward, aborting.",
	}}
	// The recorder answers `out` before it fails, so the pull returns that text
	// *and* an error, which is what git does here.
	r.failAt = "git pull"

	_, err := upgrade.Options{Source: checkout(t), Run: r.run}.Upgrade(t.Context())
	if err == nil {
		t.Fatal("a refused pull was reported as a success")
	}
	if !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "fast-forward") {
		t.Errorf("the failure does not say why:\n%v", err)
	}
}

// $CQ_BIN names two different things in this tree: `Common/nudge` reads it as the
// cq command to run, and this reads it as the directory to install into. A machine
// that set it the way nudge documents had every upgrade die inside the build script
// at `mkdir -p /usr/local/bin/cq` — which is a file — and the server went on serving
// the old binary while the page said it was building.
func TestTheInstallTargetMayBeTheBinaryItself(t *testing.T) {
	dir := checkout(t)
	bin := t.TempDir()
	exe := filepath.Join(bin, "cq")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &recorder{}
	if _, err := (upgrade.Options{Source: dir, Target: exe, Run: r.run}).Upgrade(t.Context()); err != nil {
		t.Fatalf("upgrading: %v", err)
	}

	var to string
	for _, call := range r.calls {
		for i, arg := range call {
			if arg == "--to" && i+1 < len(call) {
				to = call[i+1]
			}
		}
	}
	if to != bin {
		t.Errorf("the build was told to install into %q, want the binary's directory %q", to, bin)
	}
}

// A directory is still a directory, and one that does not exist yet is still where
// the tools go: the build script creates it, which is right for a fresh machine.
func TestTheInstallTargetIsUsedAsGivenWhenItIsADirectory(t *testing.T) {
	dir := checkout(t)
	for _, target := range []string{t.TempDir(), filepath.Join(t.TempDir(), "not-yet")} {
		r := &recorder{}
		if _, err := (upgrade.Options{Source: dir, Target: target, Run: r.run}).Upgrade(t.Context()); err != nil {
			t.Fatalf("upgrading into %s: %v", target, err)
		}
		var to string
		for _, call := range r.calls {
			for i, arg := range call {
				if arg == "--to" && i+1 < len(call) {
					to = call[i+1]
				}
			}
		}
		if to != target {
			t.Errorf("the build was told to install into %q, want %q", to, target)
		}
	}
}

// A supervised process inherits the supervisor's environment, not a login shell's,
// and Go is usually installed somewhere only a login shell knows about. Without this
// the symptom is one `go: command not found` per module inside a few hundred lines
// of build output — a message about the tree when the problem is the PATH.
func TestAMissingToolchainIsNamedRatherThanLeftToTheBuild(t *testing.T) {
	dir := checkout(t)
	r := &recorder{failAt: "go version"}

	report, err := (upgrade.Options{Source: dir, Target: t.TempDir(), Run: r.run}).Upgrade(t.Context())
	if err == nil {
		t.Fatal("a machine with no go toolchain upgraded anyway")
	}
	if !strings.Contains(err.Error(), "toolchain") {
		t.Errorf("the refusal does not name the toolchain: %v", err)
	}
	// The pull still happened and is still reported: it is what the checkout now is,
	// and a report that dropped it would leave the tree in a state nothing recorded.
	if report.After == "" && len(report.Steps) == 0 {
		t.Error("the report says nothing about what did run")
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "sh/build") {
			t.Error("the build ran despite there being nothing to build with")
		}
	}
}

// buildOutput is what `sh/build --to <dir>` really prints, captured from a run of
// it rather than written by hand.
//
// The difference matters more than it looks. The fixture here used to be two lines
// of `name  /path`, which the script has never printed — so the parser was pinned
// against a format nobody produced, passed, and returned the wrong answer on every
// real upgrade. A fixture that cannot occur is a test of nothing.
//
// The trailing note is part of it on purpose: it is the line that used to be
// mistaken for a tool called `export`.
const buildOutput = `Anno         ok anno-hook anno
Common       ok (library)
Communique   ok cq
Dock         ok dock-hook dock
Macmuffin    ok muff-hook muff
Mailman      ok mailman
Orc          ok orc-hook orc-session orc
Orcprobe     ok orcprobe-shim orcprobe
Theme        ok (library)

✓ installed 13 binaries to /tmp/bin
  /tmp/bin is not on your PATH. add it with:
    export PATH="/tmp/bin:$PATH"
`

// TestBuiltReadsWhatTheScriptPrints, against that output.
func TestBuiltReadsWhatTheScriptPrints(t *testing.T) {
	dir := checkout(t)
	r := &recorder{out: map[string]string{"rev-parse": "aaaaaaa\n", "sh/build": buildOutput}}

	report, err := upgrade.Options{Source: dir, Target: "/tmp/bin", Run: r.run}.Upgrade(t.Context())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	want := []string{
		"anno-hook", "anno", "cq", "dock-hook", "dock", "muff-hook", "muff",
		"mailman", "orc-hook", "orc-session", "orc", "orcprobe-shim", "orcprobe",
	}
	if strings.Join(report.Built, " ") != strings.Join(want, " ") {
		t.Errorf("built %v, want %v", report.Built, want)
	}
	// The two things that used to be got wrong, said as themselves.
	for _, wrong := range []string{"export", "Anno", "Communique", "(library)"} {
		if slices.Contains(report.Built, wrong) {
			t.Errorf("%q was read as a tool", wrong)
		}
	}
	if len(report.Built) == 0 {
		t.Error("the real output produced no tools at all, which is what the bug was")
	}
}
