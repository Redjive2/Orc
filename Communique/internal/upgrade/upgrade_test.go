package upgrade_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		"sh/build":  "orc      /bin/orc\nmailman  /bin/mailman\n",
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
	// which tools moved rather than only that something did.
	if len(report.Built) != 2 || report.Built[0] != "orc" {
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
	if !strings.Contains(err.Error(), "local commits or changes") {
		t.Errorf("the failure does not say what --ff-only refuses: %v", err)
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
