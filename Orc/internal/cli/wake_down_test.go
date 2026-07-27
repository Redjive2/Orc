package cli_test

import (
	"strings"
	"testing"
)

// An agent that is not running at all.
//
// The wake cycle exists to notice that a fleet has quietly stopped, and the loudest
// version of that is an agent with no session — employed, costing budget on the
// worklist, and doing nothing. It used to be skipped in silence on the grounds that
// starting it is `tend`'s job. Starting it is; *saying* so is not, and a cycle that
// reported "all working" over a fleet where nobody was running was answering a
// question nobody asked.

// downed employs an agent and then takes its session away, which is what a machine
// that slept, a killed supervisor, or a crash out of restarts leaves behind.
func downed(t *testing.T) *rig {
	t.Helper()
	r := wakeable(t)
	r.depopulate(mustStore(t, r), mustName(t, "ember"))
	return r
}

func TestWakeSaysWhenAnAgentIsNotRunningAtAll(t *testing.T) {
	r := downed(t)

	got := r.ok("boss", "wake")
	if !strings.Contains(got.stdout, "not running") {
		t.Errorf("an employed agent with no session was passed over:\n%s%s", got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "ember") {
		t.Errorf("the report does not name it:\n%s", got.stdout)
	}
	// And it says what to do about it, because "not running" with no next step is
	// half a diagnosis.
	if !strings.Contains(got.stdout, "tend") {
		t.Errorf("the report does not say what starts it:\n%s", got.stdout)
	}
}

// The summary has to agree with the lines above it. A fleet with one agent down is
// not "all working".
func TestWakeDoesNotReportADownFleetAsWorking(t *testing.T) {
	r := downed(t)

	got := r.ok("boss", "wake")
	if strings.Contains(got.stdout, "all working") {
		t.Errorf("a fleet with nothing running reported itself healthy:\n%s", got.stdout)
	}
}

// With --tend the cycle brings it up itself: the machine where a cron entry runs
// `orc wake` and nothing else is the machine that most needs a session started.
func TestWakeTendStartsWhatIsDown(t *testing.T) {
	r := downed(t)

	got := r.ok("boss", "wake", "--tend")
	if !strings.Contains(got.stdout, "started") {
		t.Errorf("--tend did not start a downed agent:\n%s%s", got.stdout, got.stderr)
	}
	if r.populated["ember"] == "" {
		t.Error("no session was started")
	}
}

// Without it, nothing is started. A cycle that quietly reconciled would be a second
// thing deciding what runs, and two reconcilers with one fleet is how a fleet gets
// two answers.
func TestWakeWithoutTendStartsNothing(t *testing.T) {
	r := downed(t)

	r.ok("boss", "wake")
	if r.populated["ember"] != "" {
		t.Error("a plain wake started a session")
	}
}

// A dry run is still a dry run.
func TestWakeTendDryRunStartsNothing(t *testing.T) {
	r := downed(t)

	got := r.ok("boss", "wake", "--tend", "--dry-run")
	if !strings.Contains(got.stdout, "would start") {
		t.Errorf("a dry run did not say what it would do:\n%s", got.stdout)
	}
	if r.populated["ember"] != "" {
		t.Error("a dry run started a session")
	}
}
