package cli

import "testing"

// The supervisor's whole job is keeping the site up, and its whole risk is
// keeping it up when somebody wanted it down. This is the policy that comes to,
// tested as the four sentences it is.
//
// Internal, because the decision is deliberately not exported: it is not
// something a caller chooses, and the loop around it cannot be tested without
// real processes and real signals. Testing the loop would mean a test that sends
// itself SIGTERM and hopes; testing this means asserting the sentences.

func TestACrashComesBack(t *testing.T) {
	if got := decide(false, 1, 1); got != relaunch {
		t.Errorf("a crashed server was not restarted: got %v, want relaunch", got)
	}
}

// The one that used to be missing. A server that died at three in the morning
// left the site down until somebody noticed, because the supervisor handed the
// exit code to a service manager that does not exist anywhere in this tree.
func TestACrashLongAfterStartupStillComesBack(t *testing.T) {
	// A run that stayed up resets the count, so a crash after a week arrives here
	// as the first one.
	if got := decide(false, 2, 1); got != relaunch {
		t.Errorf("a server that had been up for a week and crashed was left down: got %v", got)
	}
}

// And the guard on that: a build which cannot start fails fast, over and over,
// and coming back for ever would bury the reason under its own log.
func TestAServerThatWillNotStartIsEventuallyLeftAlone(t *testing.T) {
	if got := decide(false, 1, CrashGiveUp); got != relaunch {
		t.Errorf("gave up inside its own allowance: got %v at %d crashes", got, CrashGiveUp)
	}
	if got := decide(false, 1, CrashGiveUp+1); got != giveUp {
		t.Errorf("a server that never starts was retried for ever: got %v", got)
	}
}

// The most obvious way an auto-restarting supervisor becomes unusable: ^C brings
// the thing straight back up and there is no way to turn it off.
func TestAskingItToStopStopsIt(t *testing.T) {
	// Whatever the child's exit code. A server signalled mid-shutdown can exit
	// any number of ways and none of them means "start me again".
	for _, code := range []int{0, 1, 2, 130, 143, ExitRestart} {
		if got := decide(true, code, 0); got != stop {
			t.Errorf("a stop request with child exit %d did not stop the server: got %v", code, got)
		}
	}
}

// A clean exit is a decision too: `cq serve` told to shut down should not be
// argued with.
func TestACleanExitStaysDown(t *testing.T) {
	if got := decide(false, 0, 0); got != stop {
		t.Errorf("a clean exit was restarted: got %v", got)
	}
}

// The upgrade path is not a crash and must not be counted as one, or a machine
// upgraded six times would stop coming back.
func TestARestartRequestIsNotACrash(t *testing.T) {
	if got := decide(false, ExitRestart, 0); got != replace {
		t.Errorf("an upgrade did not restart into the new build: got %v", got)
	}
	if got := decide(false, ExitRestart, CrashGiveUp+10); got != replace {
		t.Errorf("an upgrade was refused over an unrelated crash count: got %v", got)
	}
	if crashed(false, ExitRestart) {
		t.Error("an upgrade counted as a crash, so repeated upgrades would exhaust the allowance")
	}
}

// What gets counted, so the allowance means what its message says.
func TestOnlyCrashesAreCounted(t *testing.T) {
	cases := []struct {
		what     string
		stopping bool
		code     int
		want     bool
	}{
		{"a crash", false, 1, true},
		{"a crash with an odd code", false, 70, true},
		{"a clean exit", false, 0, false},
		{"an upgrade", false, ExitRestart, false},
		{"a signalled shutdown", true, 143, false},
		{"a signalled shutdown that exited cleanly", true, 0, false},
	}
	for _, c := range cases {
		if got := crashed(c.stopping, c.code); got != c.want {
			t.Errorf("%s counted as a crash = %v, want %v", c.what, got, c.want)
		}
	}
}

// A supervisor is only worth having if what it supervises is not another
// supervisor. That has been true since this was written; it is asserted here
// because the crash loop is a new way to get it wrong — one that forked a
// supervisor per crash would be a fork bomb with a backoff.
func TestTheChildIsMarkedSoItDoesNotSuperviseInTurn(t *testing.T) {
	child := App{Env: func(k string) (string, bool) {
		if k == supervisedEnv {
			return "1", true
		}
		return "", false
	}}
	if !child.supervised() {
		t.Error("a supervised child did not know it was one, and would start a supervisor of its own")
	}

	plain := App{Env: func(string) (string, bool) { return "", false }}
	if plain.supervised() {
		t.Error("an unsupervised process believed it was a child, so nothing would supervise it")
	}
}
