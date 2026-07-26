package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/watch"
)

// The promise: after an upgrade, this machine is still being mirrored.
//
// It is kept in two different ways depending on how the upgrade arrived, and the
// tests below are one for each — plus the defaults, which are the numbers an
// operator will find running on a machine they did not configure and should be
// able to recognise.

func TestAMachineWithNothingWatchingNeedsAWatch(t *testing.T) {
	home := t.TempDir()
	needed, err := watchNeeded(home)
	if err != nil {
		t.Fatal(err)
	}
	if !needed {
		t.Error("a machine with nothing mirroring it was left with nothing mirroring it")
	}
}

// The ordinary case: the upgrade came down the queue during a `cq sync --watch`,
// and that watcher is still there and restarts itself. A second one would double
// this machine's sync traffic for nothing.
func TestAMachineAlreadyWatchedNeedsNoSecondWatch(t *testing.T) {
	home := t.TempDir()
	release, err := watch.Registry{Dir: watchers(home)}.Register(watch.Record{
		Kind: watch.Sync, Period: watch.Duration(30 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	needed, err := watchNeeded(home)
	if err != nil {
		t.Fatal(err)
	}
	if needed {
		t.Error("a second watcher would have been started beside the one already running")
	}
}

// A watcher of another kind is not a mirror. Orc's sweeps register in their own
// store, but nothing stops a record of another kind appearing here, and reading
// one as a sync would leave the website quietly stale.
func TestAWatcherOfAnotherKindDoesNotCount(t *testing.T) {
	home := t.TempDir()
	release, err := watch.Registry{Dir: watchers(home)}.Register(watch.Record{Kind: watch.Tend})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	needed, err := watchNeeded(home)
	if err != nil {
		t.Fatal(err)
	}
	if !needed {
		t.Error("a tend watcher was counted as a mirror, so none would be started")
	}
}

// The record of a watcher that was killed is a claim, not a fact. Believing one
// is how a machine ends up with nothing mirroring it and no sign of trouble.
func TestAStaleRecordDoesNotSatisfyThePromise(t *testing.T) {
	home := t.TempDir()
	dir := watchers(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pid far beyond anything this machine will have allocated.
	body := `{"kind":"sync","pid":999999,"period":"5m0s","started":"2026-07-26T12:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "sync-999999.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	needed, err := watchNeeded(home)
	if err != nil {
		t.Fatal(err)
	}
	if !needed {
		t.Error("a record left behind by a dead watcher was believed, so the mirror would stay stopped")
	}
}

// --- what gets started -----------------------------------------------------

func TestTheDefaultWatchIsFiveMinutesForAnHour(t *testing.T) {
	if DefaultWatch != 5*time.Minute {
		t.Errorf("the default watch is %s, want 5m", DefaultWatch)
	}
	if DefaultWatchFor != time.Hour {
		t.Errorf("the default watch runs for %s, want 1h", DefaultWatchFor)
	}
}

func TestTheDefaultWatchNamesItsPeriodItsLifetimeAndItsHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "agent")
	args := watchPlan{Home: home}.args()

	if args[0] != "sync" {
		t.Errorf("the watch an upgrade starts is %q, not a sync", args[0])
	}
	got := strings.Join(args, " ")
	for _, want := range []string{"--watch 5m0s", "--for 1h0m0s", "--home " + home} {
		if !strings.Contains(got, want) {
			t.Errorf("the default watch is %q, which is missing %q", got, want)
		}
	}
}

// The lifetime is the part that is easy to lose in a refactor and impossible to
// notice: a watcher started with no `--for` runs until the machine reboots, on
// every machine that was ever upgraded from the browser.
func TestTheDefaultWatchIsNotImmortal(t *testing.T) {
	for _, arg := range (watchPlan{Home: t.TempDir()}).args() {
		if arg == "--for" {
			return
		}
	}
	t.Error("the watcher an upgrade starts has no lifetime, so it will outlive the reason it was started")
}

// And a watch somebody asked for by hand keeps running until they stop it. The
// lifetime is for the one nobody asked for.
func TestAWatchWithNoLifetimeNeverExpires(t *testing.T) {
	if at := watch.Until(time.Now(), 0); at != nil {
		t.Error("a `cq sync --watch` with no --for was given an expiry")
	}
}

// --- what the started watch is given ---------------------------------------

// The bug this pins: a sync configured by flags spawned a watcher configured by
// nothing. The child inherits variables, not flags, so `cq sync --server https://…
// --machine studio` started a watch with neither — which failed before its first
// round and left the machine unmirrored, having just announced that it was fine.
func TestTheStartedWatchIsGivenWhatTheSyncWasGiven(t *testing.T) {
	p := watchPlan{
		Home: "/var/lib/cq", Server: "https://cq.example", Machine: "studio",
		User: "boss", Library: "/srv/Orc",
	}
	got := strings.Join(p.args(), " ")

	for _, want := range []string{
		"--home /var/lib/cq",
		"--server https://cq.example",
		"--machine studio",
		"--user boss",
		"--library /srv/Orc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the started watch is missing %q, so it cannot sync: %s", want, got)
		}
	}
}

// An empty value is left out rather than passed as an empty string. `--user ""`
// is a name that is nobody; leaving it out lets the mirror ladder ask Orc who the
// operator is, which is what an unconfigured sync relies on.
func TestUnsetSettingsAreLeftOutRatherThanPassedEmpty(t *testing.T) {
	got := strings.Join(watchPlan{Home: "/var/lib/cq"}.args(), " ")
	for _, unwanted := range []string{"--server", "--machine", "--user", "--library"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s was passed with nothing behind it: %s", unwanted, got)
		}
	}
	if !strings.Contains(got, "--home /var/lib/cq") {
		t.Errorf("the home is always needed and was left out: %s", got)
	}
}

// --- and that the claim is true --------------------------------------------

// The other half of the same bug. Spawn returns as soon as the child is started,
// which says nothing about whether it stayed up — so the promise is checked
// against the registry rather than assumed, and a watcher that died is reported
// instead of announced.
func TestAWatchThatDidNotStayUpIsReportedRatherThanAnnounced(t *testing.T) {
	var out strings.Builder
	app := App{Stdout: &out, Stderr: &out, Env: func(string) (string, bool) { return "", false }}

	// Nothing ever registers here, which is what a watcher exiting immediately
	// looks like from the outside.
	err := app.awaitWatcher(t.TempDir())
	if err == nil {
		t.Fatal("a watch that never started was reported as started")
	}
	for _, want := range []string{"nothing is mirroring", "cq sync --watch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, err)
		}
	}
	if strings.Contains(out.String(), "was started") {
		t.Errorf("it announced a watch it could not find: %s", out.String())
	}
}

// And the ordinary case returns promptly rather than waiting out the whole
// window, since this runs inside an upgrade somebody is waiting on.
func TestAWatchThatIsUpIsAcceptedAtOnce(t *testing.T) {
	home := t.TempDir()
	release, err := watch.Registry{Dir: watchers(home)}.Register(watch.Record{Kind: watch.Sync})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	app := App{Stdout: io.Discard, Stderr: io.Discard, Env: func(string) (string, bool) { return "", false }}
	start := time.Now()
	if err := app.awaitWatcher(home); err != nil {
		t.Fatalf("a running watch was not recognised: %v", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("waited %s for a watcher that was already there", waited)
	}
}
