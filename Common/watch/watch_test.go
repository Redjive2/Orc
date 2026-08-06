package watch_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"orc/common/watch"
)

// What these guard is one promise: after an upgrade, the loops that keep the
// fleet alive are still running, and still running the build that was just
// installed. Everything below is one half of that or the other.

func registry(t *testing.T) watch.Registry {
	t.Helper()
	return watch.Registry{Dir: filepath.Join(t.TempDir(), "watchers")}
}

func TestARegisteredWatcherIsFound(t *testing.T) {
	reg := registry(t)

	release, err := reg.Register(watch.Record{Kind: watch.Sync, Period: watch.Duration(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	running, err := reg.Running(watch.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Error("a watcher that just registered was not found running")
	}
}

// The question an upgrade actually asks is about a kind, not about any watcher at
// all: a fleet being tended says nothing about whether its mirror is being pushed.
func TestOneKindOfWatcherIsNotAnother(t *testing.T) {
	reg := registry(t)
	release, err := reg.Register(watch.Record{Kind: watch.Tend})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	running, err := reg.Running(watch.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Error("a tend watcher was mistaken for a sync watcher, so no mirror would be started")
	}
}

func TestAReleasedWatcherIsGone(t *testing.T) {
	reg := registry(t)
	release, err := reg.Register(watch.Record{Kind: watch.Sync})
	if err != nil {
		t.Fatal(err)
	}
	release()

	running, err := reg.Running(watch.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Error("a watcher that stopped is still reported as running")
	}
}

// Releasing twice happens: the loop removes its record before exec'ing, and the
// deferred release runs as well when the exec fails.
func TestReleasingTwiceIsHarmless(t *testing.T) {
	reg := registry(t)
	release, err := reg.Register(watch.Record{Kind: watch.Sync})
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
}

// The one that matters most. A record is a claim, and a watcher killed with
// SIGKILL leaves one behind — so a registry that believed its own files would
// report a mirror that stopped hours ago as running, and an upgrade would decline
// to start the watcher that was the whole point.
func TestARecordForADeadProcessIsNotAWatcher(t *testing.T) {
	reg := registry(t)

	dead := exec.Command(sleeper(), "0")
	if err := dead.Start(); err != nil {
		t.Skipf("cannot start a child here: %v", err)
	}
	pid := dead.Process.Pid
	_ = dead.Wait()

	if _, err := reg.Register(watch.Record{Kind: watch.Sync, Pid: pid}); err != nil {
		t.Fatal(err)
	}

	running, err := reg.Running(watch.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Error("a record left behind by a killed watcher was believed, so nothing would restart the mirror")
	}
}

// And the stale record is cleared away rather than left to be re-read for ever.
func TestReadingClearsAwayWhatIsDead(t *testing.T) {
	reg := registry(t)
	if _, err := reg.Register(watch.Record{Kind: watch.Sync, Pid: 999999}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Live(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(reg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a stale record survived a read: %d files left", len(entries))
	}
}

// A machine that has never run a watcher is the ordinary case, and asking about
// it must be an answer rather than a failure.
func TestNoRegistryIsNoWatchers(t *testing.T) {
	reg := watch.Registry{Dir: filepath.Join(t.TempDir(), "never-made")}
	running, err := reg.Running(watch.Sync)
	if err != nil {
		t.Fatalf("asking about a machine with no watchers failed: %v", err)
	}
	if running {
		t.Error("a directory that does not exist reported a watcher")
	}
}

// Corrupt records get the same treatment as stale ones. A half-written file must
// not be able to convince an upgrade that a mirror is running.
func TestAnUnreadableRecordIsNotAWatcher(t *testing.T) {
	reg := registry(t)
	if err := os.MkdirAll(reg.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reg.Dir, "sync-1.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	running, err := reg.Running(watch.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Error("a corrupt record was read as a running watcher")
	}
}

// --- what is written -------------------------------------------------------

// The file exists to be opened and read by a person, which a count of nanoseconds
// defeats.
func TestAPeriodIsWrittenAsSomethingReadable(t *testing.T) {
	reg := registry(t)
	release, err := reg.Register(watch.Record{
		Kind: watch.Sync, Period: watch.Duration(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	live, err := reg.Live()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("expected one watcher, got %d", len(live))
	}
	if got := time.Duration(live[0].Period); got != 5*time.Minute {
		t.Errorf("period came back as %s, want 5m0s", got)
	}

	body, err := os.ReadFile(filepath.Join(reg.Dir, "sync-"+itoa(live[0].Pid)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["period"] != "5m0s" {
		t.Errorf("period is on disk as %v, which nobody can read; want \"5m0s\"", raw["period"])
	}
}

// --- the time to live ------------------------------------------------------

// The default watcher an upgrade starts has one; the one an operator started by
// hand does not, and must not be given one by accident.
func TestNoLifetimeMeansNoExpiry(t *testing.T) {
	if at := watch.Until(time.Now(), 0); at != nil {
		t.Errorf("a watcher with no --for was given an expiry of %s", at)
	}
	// Negative is the same case, and must not read as "already expired": a watcher
	// that stopped the instant it started would be a mirror that silently never
	// updated, reported as a success.
	if at := watch.Until(time.Now(), -time.Hour); at != nil {
		t.Errorf("a negative lifetime produced an expiry of %s", at)
	}
}

func TestALifetimeExpiresWhenItShould(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	at := watch.Until(started, time.Hour)
	if at == nil {
		t.Fatal("an hour-long watch was given no expiry")
	}
	if want := started.Add(time.Hour); !at.Equal(want) {
		t.Errorf("expires at %s, want %s", at, want)
	}
}

// --- noticing a new build --------------------------------------------------

func TestAnUntouchedBinaryIsNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cq")
	if err := os.WriteFile(path, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, err := watch.Look(path)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Replaced(path, stamp) {
		t.Error("a binary nobody touched was reported as a new build, so the watcher would restart in a loop")
	}
}

func TestARebuiltBinaryIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cq")
	if err := os.WriteFile(path, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, err := watch.Look(path)
	if err != nil {
		t.Fatal(err)
	}

	// A different size is the unambiguous case, and the one an upgrade produces.
	if err := os.WriteFile(path, []byte("a new build, longer than the old one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !watch.Replaced(path, stamp) {
		t.Error("a rebuilt binary went unnoticed, so the watcher would run the old build for ever")
	}
}

// A same-sized rebuild is the case a naive size check misses, and it is not
// hypothetical: a one-character fix produces a binary of exactly the same length
// more often than one would like.
func TestASameSizedRebuildIsStillReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cq")
	if err := os.WriteFile(path, []byte("build one"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, err := watch.Look(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("build two"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Written far enough after to be distinguishable on any filesystem this runs
	// on: some record modification times only to the second.
	later := stamp.Mod.Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	if !watch.Replaced(path, stamp) {
		t.Error("a rebuild of the same size went unnoticed")
	}
}

// Mid-build the file can be absent for a moment. Treating that as a new build
// would have the watcher exec a file that is not there, fail, and say so every
// round of a build that is going perfectly well.
func TestAMissingBinaryIsNotANewBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cq")
	if err := os.WriteFile(path, []byte("build"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, err := watch.Look(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if watch.Replaced(path, stamp) {
		t.Error("a binary mid-write was reported as a new build")
	}
}

// --- starting one ----------------------------------------------------------

// The upgrade's fallback: when nothing is watching, one is started that outlives
// the process starting it.
func TestSpawnStartsSomethingThatOutlivesTheCaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sleeper and its signals differ enough on windows to be a separate test")
	}
	if err := watch.Spawn(sleeper(), []string{"1"}); err != nil {
		t.Fatalf("could not start a detached watcher: %v", err)
	}
	// Nothing to wait on by design — the point is that this process does not. That
	// it started at all is what is being checked; that it is detached is checked by
	// the absence of a zombie, which Go's test runner would not report anyway.
}

func TestSpawningSomethingThatIsNotThereSaysSo(t *testing.T) {
	err := watch.Spawn(filepath.Join(t.TempDir(), "no-such-binary"), nil)
	if err == nil {
		t.Fatal("starting a watcher that does not exist reported success")
	}
}

// --- restarting ------------------------------------------------------------

// Restart resolves the path the way the platform would, so that the Windows case
// this guards — a binary with no extension, which that platform will not run even
// when handed its own absolute path — is a message rather than a mystery.
func TestRestartingIntoSomethingUnrunnableSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(path, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	handedOff, err := watch.Restart(path, nil)
	if err == nil {
		t.Fatal("restarting into a file that cannot be run reported success")
	}
	// And it did not claim a replacement is running. A caller that believed that
	// would stop watching, leaving nothing behind it.
	if handedOff {
		t.Error("a failed restart said it had handed off")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// sleeper is a program that exists on every platform this runs on and does
// nothing for a while.
func sleeper() string {
	if runtime.GOOS == "windows" {
		return "timeout"
	}
	return "/bin/sleep"
}
