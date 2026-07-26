package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// `cq workspace` is the only per-verb command cq has, and the reason it earns that
// exception is that it does different work on each side. So the tests are mostly
// about which side it decided it was on, and about it never deciding silently.

// fakeOrc puts a stand-in orc on $ORC that logs its arguments and answers with the
// given text. What orc does with a workspace is Orc's to test; what cq must get right
// is that it asks, and relays the answer.
func fakeOrc(t *testing.T, h *harness, stdout string, code int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "called")
	bin := filepath.Join(dir, "orc")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %s\nprintf '%%s\\n' %s\nexit %d\n",
		shellWord(log), shellWord(stdout), code)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h.env["ORC"] = bin
	return log
}

func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func asked(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return ""
	}
	return string(data)
}

// agentSide is a machine configured as an agent: it can reach a server, and has a
// fleet of its own.
func agentSide(h *harness) {
	h.env["CQ_SERVER"] = "https://cq.example"
	delete(h.env, "CQ_STATE")
}

// mirrored registers a machine as having synced, which is what makes it a fleet the
// server can queue against.
func mirrored(t *testing.T, h *harness, ids ...protocol.MachineID) {
	t.Helper()
	s, err := store.Open(h.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		snap := protocol.Snapshot{
			Machine: id, User: "redjive", TakenAt: when,
			Fleet: &protocol.Fleet{
				Operator:   "redjive",
				Identities: []protocol.FleetID{{Name: "ember", Workspace: "/old"}},
			},
		}
		if err := s.PutSnapshot(snap, "cq/test", when); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspaceOnTheAgentMachineRunsOrc(t *testing.T) {
	h := newHarness(t)
	agentSide(h)
	log := fakeOrc(t, h, "moved ember   /old → /new", 0)

	got := h.run(t, "", "workspace", "ember", "/new").mustSucceed(t)

	if call := asked(t, log); !strings.Contains(call, "workspace ember /new") {
		t.Errorf("orc was not asked to move it: %q", call)
	}
	// Orc's own words: it says what was copied, what was left behind, and which
	// worktree bindings followed. There is nothing cq could add to that.
	if !strings.Contains(got.stdout, "moved ember") {
		t.Errorf("orc's answer was not relayed:\n%s", got.stdout)
	}
	// And nothing about a queue, because there is nothing to wait for.
	if strings.Contains(got.stdout, "queued") {
		t.Errorf("a direct change was reported as queued:\n%s", got.stdout)
	}
}

func TestWorkspaceAdoptReachesOrc(t *testing.T) {
	h := newHarness(t)
	agentSide(h)
	log := fakeOrc(t, h, "ember now works in /new", 0)

	h.run(t, "", "workspace", "--adopt", "ember", "/new").mustSucceed(t)

	if call := asked(t, log); !strings.Contains(call, "--adopt") {
		t.Errorf("--adopt did not reach orc: %q", call)
	}
}

// Orc's refusal is the answer, and it comes back as a failure rather than as text on
// a successful exit.
func TestWorkspaceRelaysOrcsRefusal(t *testing.T) {
	h := newHarness(t)
	agentSide(h)
	fakeOrc(t, h, "orc: ember: no such identity", 2)

	got := h.run(t, "", "workspace", "ember", "/new")
	if got.code == fault.ExitOK {
		t.Fatalf("a refused move exited 0:\n%s", got.stdout)
	}
}

// TestOnTheServerItQueues — the same action the browser makes, and it says when it
// will happen rather than pretending it has.
func TestWorkspaceOnTheServerQueues(t *testing.T) {
	h := newHarness(t)
	mirrored(t, h, "studio")

	got := h.run(t, "", "workspace", "ember", "/new").mustSucceed(t)

	if !strings.Contains(got.stdout, "queued") {
		t.Errorf("it does not say the action was queued:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "next sync") {
		t.Errorf("it does not say when it happens:\n%s", got.stdout)
	}

	s, err := store.Open(h.state)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.Queue()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action.Op != protocol.OpOrcWorkspace {
			continue
		}
		found = true
		if e.Action.Args.Identity != "ember" || e.Action.Args.Workspace != "/new" {
			t.Errorf("the queued action carries %+v", e.Action.Args)
		}
	}
	if !found {
		t.Error("nothing was queued")
	}
}

// TestUnstatedFromComesFromTheMirror. The action is refused on the agent machine if
// the view it was made against has moved on, so it must carry one — and what the
// operator was looking at is the mirror.
func TestWorkspaceCarriesTheMirrorsView(t *testing.T) {
	h := newHarness(t)
	mirrored(t, h, "studio")

	h.run(t, "", "workspace", "ember", "/new").mustSucceed(t)

	s, _ := store.Open(h.state)
	entries, _ := s.Queue()
	for _, e := range entries {
		if e.Action.Op == protocol.OpOrcWorkspace && e.Action.Args.From != "/old" {
			t.Errorf("the queued action does not carry the mirror's view: %+v", e.Action.Args)
		}
	}
}

// An identity the mirror does not know about is a move with nothing to check
// against, and it says so rather than queueing something the agent will refuse.
func TestWorkspaceRefusesAnIdentityTheMirrorDoesNotKnow(t *testing.T) {
	h := newHarness(t)
	mirrored(t, h, "studio")

	got := h.run(t, "", "workspace", "nobody", "/new")
	if got.code != fault.ExitUsage {
		t.Fatalf("exit %d, want %d\n%s", got.code, fault.ExitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "--from") {
		t.Errorf("the refusal does not say how to proceed anyway:\n%s", got.stderr)
	}
}

// --from is what the operator believed, and it travels with the action so the agent
// machine can refuse a move made against a view that has moved on.
func TestWorkspaceQueuesTheViewItWasMadeAgainst(t *testing.T) {
	h := newHarness(t)
	mirrored(t, h, "studio")

	h.run(t, "", "workspace", "--from", "/old", "ember", "/new").mustSucceed(t)

	s, _ := store.Open(h.state)
	entries, _ := s.Queue()
	for _, e := range entries {
		if e.Action.Op == protocol.OpOrcWorkspace && e.Action.Args.From != "/old" {
			t.Errorf("the queued action lost --from: %+v", e.Action.Args)
		}
	}
}

// TestItNeverPicksASideSilently. The two sides differ in *when* the change takes
// effect, which is the part somebody would not notice going wrong.
func TestWorkspaceRefusesWhenItIsBothSides(t *testing.T) {
	h := newHarness(t)
	h.env["CQ_SERVER"] = "https://cq.example"

	got := h.run(t, "", "workspace", "ember", "/new")
	if got.code != fault.ExitUsage {
		t.Fatalf("exit %d, want %d\n%s", got.code, fault.ExitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "both") {
		t.Errorf("the refusal does not say what is ambiguous:\n%s", got.stderr)
	}
}

// And with --state it stops being ambiguous: that is the operator saying which.
func TestWorkspaceStateFlagSettlesIt(t *testing.T) {
	h := newHarness(t)
	h.env["CQ_SERVER"] = "https://cq.example"
	mirrored(t, h, "studio")

	got := h.run(t, "", "workspace", "--state", h.state, "ember", "/new").mustSucceed(t)
	if !strings.Contains(got.stdout, "queued") {
		t.Errorf("--state did not choose the queue:\n%s", got.stdout)
	}
}

// A machine that is neither has nothing to change and nowhere to put the request.
func TestWorkspaceOnANakedMachine(t *testing.T) {
	h := newHarness(t)
	h.env = map[string]string{}

	got := h.run(t, "", "workspace", "ember", "/new")
	if got.code != fault.ExitUsage {
		t.Errorf("exit %d, want %d\n%s", got.code, fault.ExitUsage, got.stderr)
	}
}

// A server mirroring two fleets asks which rather than picking. Changing a fleet the
// operator was not thinking about is worse than one more word to type.
func TestWorkspaceWithTwoMachines(t *testing.T) {
	h := newHarness(t)
	mirrored(t, h, "studio", "laptop")

	got := h.run(t, "", "workspace", "ember", "/new")
	if got.code == fault.ExitOK {
		t.Fatalf("it picked a fleet:\n%s", got.stdout)
	}
	for _, want := range []string{"studio", "laptop"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal does not offer %q:\n%s", want, got.stderr)
		}
	}

	// Named, it queues against that one.
	h.run(t, "", "workspace", "--machine", "laptop", "ember", "/new").mustSucceed(t)
}

func TestWorkspaceRefusals(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		what string
		args []string
	}{
		{"no arguments", []string{"workspace"}},
		{"only an identity", []string{"workspace", "ember"}},
		{"a third word", []string{"workspace", "ember", "/new", "/newer"}},
	} {
		if got := h.run(t, "", tc.args...); got.code != fault.ExitUsage {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, fault.ExitUsage, got.stderr)
		}
	}
}
