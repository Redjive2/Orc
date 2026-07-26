package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/fault"
)

// mirrorOrc writes a fake `orc` and points the harness at it.
//
// A script rather than a function seam, because the thing under test is a
// boundary between two programs: the format `orc owner env` prints and the exit
// codes it uses are the contract, and a stub that returned Go values would test
// cq's idea of that contract instead of the contract.
func mirrorOrc(t *testing.T, h *harness, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "orc")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("fake orc: %v", err)
	}
	h.env["ORC"] = path
}

// mirrorFleet is a fake orc for a fleet whose operator is `boss`: it answers
// `introspect --only operator`, and hands over the pair for `owner env` only when
// no credential is presented, exactly as the real one does.
const mirrorFleet = `
case "$1 $2" in
"introspect --only") echo boss ;;
"owner env")
  if [ -n "$ORC_USER" ]; then
    echo "orc: orc owner env needs the operator" >&2; exit 8
  fi
  echo "export ORC_USER=boss"
  echo "export ORC_KEY=k-boss"
  echo "export ORC_HOME=/tmp/fleet"
  ;;
*) echo "orc: unexpected $*" >&2; exit 1 ;;
esac`

// TestMirrorFindsTheOperator is the whole point of the ladder: a machine that has
// been bootstrapped and nothing else syncs, without $CQ_USER being set.
func TestMirrorFindsTheOperator(t *testing.T) {
	h := newHarness(t)
	mirrorOrc(t, h, mirrorFleet)

	// Rung 3: nothing presented at all, so orc hands over its own keyring.
	got := h.run(t, "", "status").mustSucceed(t)
	if !strings.Contains(got.stdout, "boss") || !strings.Contains(got.stdout, "keyring") {
		t.Errorf("an empty environment did not resolve to the operator:\n%s", got.stdout)
	}

	// Rung 2: the operator's own shell, which is what `orc bootstrap` tells them
	// to set up. orc refuses `owner env` here, and cq must not need it.
	h.env["ORC_USER"], h.env["ORC_KEY"] = "boss", "k-boss"
	got = h.run(t, "", "status").mustSucceed(t)
	if !strings.Contains(got.stdout, "boss") || !strings.Contains(got.stdout, "ORC_USER") {
		t.Errorf("the operator's own shell did not resolve:\n%s", got.stdout)
	}
}

// TestMirrorPrefersWhatItWasTold: an explicit setting is never second-guessed, and
// a machine may legitimately mirror somebody who is not the operator.
func TestMirrorPrefersWhatItWasTold(t *testing.T) {
	h := newHarness(t)
	mirrorOrc(t, h, `echo "orc: should not have been asked" >&2; exit 1`)
	h.env["CQ_USER"], h.env["CQ_KEY"] = "quill", "k-quill"

	got := h.run(t, "", "status").mustSucceed(t)
	if !strings.Contains(got.stdout, "quill") {
		t.Errorf("$CQ_USER was not honoured:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "CQ_USER") {
		t.Errorf("status does not say where the account came from:\n%s", got.stdout)
	}
}

// TestMirrorRefusesAnAgent is the security half. An agent's `mailman send` spawns
// a nudge carrying the agent's credential; resolving that to the agent would
// publish its mailbox as the machine's, which is the one thing a mirror must not
// do.
func TestMirrorRefusesAnAgent(t *testing.T) {
	h := newHarness(t)
	mirrorOrc(t, h, mirrorFleet)
	h.env["ORC_USER"], h.env["ORC_KEY"] = "ember", "k-ember"

	got := h.run(t, "", "sync", "--dry-run").mustFail(t, fault.ExitConflict)
	for _, want := range []string{"boss", "ember", "CQ_USER=boss"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got.stderr)
		}
	}
	// And it must not have gone on to read anybody's mail.
	if strings.Contains(got.stdout, "in the inbox") {
		t.Errorf("it collected a snapshot anyway:\n%s", got.stdout)
	}
}

// TestMirrorSaysWhatToDoWithoutOrc: the failure that used to read `no user is
// configured for this machine` and name neither the setting nor the way out.
func TestMirrorSaysWhatToDoWithoutOrc(t *testing.T) {
	h := newHarness(t)
	mirrorOrc(t, h, `echo "orc: no fleet at /nowhere" >&2; exit 2`)

	got := h.run(t, "", "sync", "--dry-run").mustFail(t, fault.ExitUsage)
	for _, want := range []string{"CQ_USER", "orc owner env", "orc bootstrap", "no fleet"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the diagnosis should mention %q:\n%s", want, got.stderr)
		}
	}
	// A refusal that already says what to do should not be buried under the
	// overview. Something has gone wrong with the message if it is.
	if strings.Contains(got.stderr, "commands") {
		t.Errorf("the whole overview was printed after a full diagnosis:\n%s", got.stderr)
	}
}

// TestMirrorChecksSettingsFirst: a machine with no server is told about the
// server, not sent off to run a tool it was not asked about.
func TestMirrorChecksSettingsFirst(t *testing.T) {
	h := newHarness(t)
	mirrorOrc(t, h, `echo "orc: should not have been asked" >&2; exit 1`)

	got := h.run(t, "", "sync").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "server") {
		t.Errorf("the missing server should come first:\n%s", got.stderr)
	}
}
