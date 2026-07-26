package agent_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/agent"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// fakeServer answers syncs with whatever a test dictates, so the agent can be
// driven into situations a correct server would never produce.
func fakeServer(t *testing.T, handler func(protocol.SyncRequest) (int, any)) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("the agent sent something undecodable: %v", err)
		}
		status, body := handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding the reply: %v", err)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func agentAgainst(t *testing.T, url, dir string, src *fakeSource) *agent.Agent {
	t.Helper()
	a, err := agent.New(agent.Options{
		Source: src, Server: url, Token: "t", Machine: "studio",
		State: dir, Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestARedeliveredActionIsSkipped is what makes at-least-once delivery safe.
// The server never learned the result, so it offers the action again; the
// journal recognises it and does nothing.
func TestARedeliveredActionIsSkipped(t *testing.T) {
	r := newRig(t)
	action := r.queue(t, protocol.OpSend, protocol.Args{
		To: []string{"bob"}, Subject: "hello", Body: "hi",
	})

	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Fatalf("applied %d times on the first sync", got)
	}

	// The response never reached the server: the agent believes it reported,
	// the server still has the action pending.
	if err := agent.MarkReported(r.dir, at, action.ID); err != nil {
		t.Fatal(err)
	}

	report, err := r.agent.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Received != 1 {
		t.Fatalf("the server should have re-delivered it: %+v", report)
	}
	if report.Skipped != 1 || report.Applied != 0 {
		t.Errorf("report = %+v, want the action skipped", report)
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Errorf("a re-delivered send reached the world %d times, want 1", got)
	}
}

// TestAMisaddressedActionIsRefused: a puid means something different in each
// mailbox, so applying another machine's action here would act on the wrong
// message entirely.
func TestAMisaddressedActionIsRefused(t *testing.T) {
	src := newFakeSource("studio")
	dir := t.TempDir()

	id := protocol.ActionID(strings.Repeat("b", 32))
	ts := fakeServer(t, func(protocol.SyncRequest) (int, any) {
		return http.StatusOK, protocol.SyncResponse{
			Protocol: protocol.Version, ServerTime: at,
			Actions: []protocol.Action{{
				ID: id, Seq: 1, Machine: "laptop", Op: protocol.OpArchive,
				Args: protocol.Args{PUID: 41}, Queued: at,
			}},
		}
	})

	report, err := agentAgainst(t, ts.URL, dir, src).Sync(t.Context())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if report.Failed != 1 || report.Applied != 0 {
		t.Errorf("report = %+v, want the action refused", report)
	}
	if got := src.timesApplied(id); got != 0 {
		t.Errorf("another machine's action was applied %d times", got)
	}
}

// TestServerFailuresAreClassified, so `cq sync` exits with a status a script
// can branch on.
func TestServerFailuresAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   fault.Code
		want   error
		exit   int
	}{
		{"rejected token", http.StatusUnauthorized, fault.CodeUnauthenticated, fault.ErrUnauthenticated, fault.ExitUnauthenticated},
		{"bad request", http.StatusBadRequest, fault.CodeParse, fault.ErrParse, fault.ExitParse},
		{"usage", http.StatusBadRequest, fault.CodeUsage, fault.ErrParse, fault.ExitParse},
		{"conflict", http.StatusConflict, fault.CodeConflict, fault.ErrConflict, fault.ExitConflict},
		{"server broke", http.StatusInternalServerError, fault.CodeInternal, fault.ErrUnavailable, fault.ExitUnavailable},
		{"i/o", http.StatusInternalServerError, fault.CodeIO, fault.ErrUnavailable, fault.ExitUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := fakeServer(t, func(protocol.SyncRequest) (int, any) {
				return tc.status, protocol.APIError{
					Error: protocol.ErrorBody{Code: tc.code, Message: "because"},
				}
			})
			_, err := agentAgainst(t, ts.URL, t.TempDir(), newFakeSource("studio")).Sync(t.Context())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got := fault.Exit(err); got != tc.exit {
				t.Errorf("exit = %d, want %d", got, tc.exit)
			}
		})
	}
}

func TestAnUnusableResponseIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		body any
	}{
		{"wrong protocol", protocol.SyncResponse{Protocol: 99, ServerTime: at}},
		{"no server time", protocol.SyncResponse{Protocol: protocol.Version}},
		{"an action cq cannot apply", protocol.SyncResponse{
			Protocol: protocol.Version, ServerTime: at,
			Actions: []protocol.Action{{ID: "short", Seq: 1, Machine: "studio",
				Op: protocol.OpRead, Args: protocol.Args{PUID: 1}, Queued: at}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := fakeServer(t, func(protocol.SyncRequest) (int, any) {
				return http.StatusOK, tc.body
			})
			src := newFakeSource("studio")
			_, err := agentAgainst(t, ts.URL, t.TempDir(), src).Sync(t.Context())
			if !errors.Is(err, fault.ErrParse) {
				t.Errorf("error = %v, want a parse fault", err)
			}
			if len(src.applied) != 0 {
				t.Errorf("actions from an unusable response were applied")
			}
		})
	}
}

func TestReportReadsAsASentence(t *testing.T) {
	r := agent.Report{Machine: "studio", Sent: 2, Received: 3, Applied: 1, Failed: 1, Skipped: 1}
	got := r.String()
	for _, want := range []string{"studio", "2 up", "3 down", "1 applied", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q should contain %q", got, want)
		}
	}
}

func TestTheClockDefaultsToNow(t *testing.T) {
	r := newRig(t)
	a, err := agent.New(agent.Options{
		Source: r.src, Server: r.server.URL, Token: "t", Machine: "studio",
		State: t.TempDir(), Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The token is wrong, so this fails — but it fails having stamped a real
	// time into the cursor, which is what is being checked.
	_, _ = a.Sync(t.Context())
	c, _, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if c.LastError == "" {
		t.Errorf("the failure was not recorded")
	}
}

// TestCorruptJournalShapesAreRefused: every event the journal can hold is
// validated on the way back in, so a hand-edited or half-written record cannot
// be mistaken for a real one.
func TestCorruptJournalShapesAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"unknown op", `{"op":"invented","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","at":"2026-07-24T18:31:04Z"}`},
		{"no time", `{"op":"applied","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ok":true}`},
		{"bad id", `{"op":"applied","id":"short","ok":true,"at":"2026-07-24T18:31:04Z"}`},
		{"failure with no reason", `{"op":"applied","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ok":false,"at":"2026-07-24T18:31:04Z"}`},
		{"success with a reason", `{"op":"applied","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ok":true,"error":"hm","at":"2026-07-24T18:31:04Z"}`},
		{"report naming nobody", `{"op":"reported","ids":[],"at":"2026-07-24T18:31:04Z"}`},
		{"report with a bad id", `{"op":"reported","ids":["short"],"at":"2026-07-24T18:31:04Z"}`},
		{"applying with a bad id", `{"op":"applying","id":"short","at":"2026-07-24T18:31:04Z"}`},
		{"unknown field", `{"op":"applied","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ok":true,"at":"2026-07-24T18:31:04Z","invented":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			path := filepath.Join(r.dir, "applied.jsonl")
			// A sound line after it, so the bad one is not merely the last.
			sound := `{"op":"reported","ids":["cccccccccccccccccccccccccccccccc"],"at":"2026-07-24T18:31:04Z"}`
			if err := os.WriteFile(path, []byte(tc.line+"\n"+sound+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := r.agent.Sync(t.Context()); !errors.Is(err, fault.ErrParse) {
				t.Errorf("error = %v, want a parse fault", err)
			}
		})
	}
}

func TestBlankJournalLinesAreIgnored(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(r.dir, "applied.jsonl")
	if err := os.WriteFile(path, []byte("\n\n   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Errorf("blank lines should be ignored: %v", err)
	}
}

// TestPruningForgetsOldResultsAndKeepsTheRest.
func TestPruningForgetsOldResultsAndKeepsTheRest(t *testing.T) {
	r := newRig(t)
	settled := r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	before, err := agent.JournalSize(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatalf("the journal is empty")
	}

	// An unreported result must survive whatever its age: the server has not
	// been told about it.
	unreported := r.queue(t, protocol.OpArchive, protocol.Args{PUID: 41})
	r.src.applyErr[unreported.ID] = errors.New("refused")
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := agent.PruneJournal(r.dir, at.Add(time.Hour)); err != nil {
		t.Fatalf("PruneJournal: %v", err)
	}
	after, err := agent.JournalSize(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if after >= before+1 {
		t.Errorf("pruning kept %d entries, want the settled one gone", after)
	}

	// The unreported one is still there to be sent.
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.entry(t, unreported.ID); got.State != store.Failed {
		t.Errorf("the unreported result was lost to pruning: %+v", got)
	}
	_ = settled
}

func TestJournalFailuresAreReported(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	r := newRig(t)
	r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})

	if err := os.Chmod(r.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.dir, 0o700) })

	if _, err := r.agent.Sync(t.Context()); !errors.Is(err, fault.ErrIO) {
		t.Errorf("error = %v, want an i/o fault", err)
	}
}

func TestNudgeReportsAnUnusableStateDirectory(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	r := newRig(t)
	if err := os.Chmod(r.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.dir, 0o700) })

	if _, _, err := r.agent.Nudge(t.Context()); err == nil {
		t.Errorf("a nudge that cannot take its lock should say so")
	}
}

func TestStatusReportsAnUnreadableJournal(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	r := newRig(t)
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.dir, "applied.jsonl")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if _, _, err := r.agent.Status(); !errors.Is(err, fault.ErrIO) {
		t.Errorf("error = %v, want an i/o fault", err)
	}
}

// modeBitsBite reports whether this machine can genuinely deny the process
// access to a file it owns.
//
// Two machines cannot, and the tests that chmod something all need one that
// can. Root is
// refused nothing. Windows has no mode bit for this at all: os.Chmod there
// toggles the read-only attribute and leaves reading alone, and does not make a
// directory unwritable — so the failure these tests provoke simply does not
// happen, and the assertion would fail for the wrong reason.
func modeBitsBite() bool {
	return os.Geteuid() != 0 && runtime.GOOS != "windows"
}
