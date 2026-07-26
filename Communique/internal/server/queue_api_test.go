package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"orc/cq/internal/protocol"
)

// The queue used to be read-only, so an action the agent refused sat in it for
// ever, counted in the status bar, with nothing anywhere that could clear it or
// make it happen. These are the two ways out.

// failOne queues an action, lets a sync collect it, and reports a result.
func failOne(t *testing.T, h *harness, cookie, csrf string, result protocol.Result) string {
	t.Helper()

	w := h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusAccepted {
		t.Fatalf("queueing failed: %d %s", w.Code, w.Body)
	}
	var queued struct {
		ActionID string `json:"action_id"`
	}
	decodeInto(t, w.Body.Bytes(), &queued)

	// A sync has to collect it before a result means anything.
	if w := h.do(t, "POST", "/api/v1/sync", syncBody(t, "studio"), h.withToken()); w.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	result.ActionID = protocol.ActionID(queued.ActionID)
	if w := h.do(t, "POST", "/api/v1/sync", resultsBody(t, "studio", result), h.withToken()); w.Code != http.StatusOK {
		t.Fatalf("reporting: %d %s", w.Code, w.Body)
	}
	return queued.ActionID
}

// resultsBody is a sync that reports outcomes and no snapshot.
func resultsBody(t *testing.T, machine protocol.MachineID, results ...protocol.Result) string {
	t.Helper()
	req := protocol.SyncRequest{
		Protocol: protocol.Version, Agent: "cq/test", SentAt: at,
		Snapshot: sampleSnapshot(machine), Results: results,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func refused() protocol.Result {
	return protocol.Result{OK: false, Error: "mailman said no", At: at}
}

func TestARefusedActionCanBeRetried(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)
	original := failOne(t, h, cookie, csrf, refused())

	w := h.do(t, "POST", "/api/v1/queue/"+original+"/retry", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}

	// The response names the *new* action, because that is the one that is going
	// to happen. The identifier has to change or the agent recognises it and
	// skips it as already applied.
	var again struct {
		ActionID string `json:"action_id"`
		State    string `json:"state"`
	}
	decodeInto(t, w.Body.Bytes(), &again)
	if again.ActionID == original {
		t.Error("the retry reused the id, so the agent would skip it")
	}
	if again.State != "queued" {
		t.Errorf("state = %q, want queued", again.State)
	}

	// And a sync really is handed it.
	w = h.do(t, "POST", "/api/v1/sync", syncBody(t, "studio"), h.withToken())
	var handed protocol.SyncResponse
	decodeInto(t, w.Body.Bytes(), &handed)
	if len(handed.Actions) != 1 || string(handed.Actions[0].ID) != again.ActionID {
		t.Errorf("the agent was handed %+v", handed.Actions)
	}
}

// TestAnInterruptedSendIsNotRetriedOverTheAPI: the store refuses it and the API
// must pass that refusal on rather than finding a way round it. A retry here
// means a real person reads the same message twice.
func TestAnInterruptedSendIsNotRetriedOverTheAPI(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	w := h.do(t, "POST", "/api/v1/messages",
		`{"to":["bob"],"subject":"s","body":"b"}`,
		h.withCookie(cookie), func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusAccepted {
		t.Fatalf("queueing a send failed: %d %s", w.Code, w.Body)
	}
	var queued struct {
		ActionID string `json:"action_id"`
	}
	decodeInto(t, w.Body.Bytes(), &queued)

	if w := h.do(t, "POST", "/api/v1/sync", syncBody(t, "studio"), h.withToken()); w.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	doubt := protocol.Result{
		ActionID: protocol.ActionID(queued.ActionID), OK: false, InDoubt: true,
		Error: "interrupted before its outcome was known; it may or may not have been applied", At: at,
	}
	if w := h.do(t, "POST", "/api/v1/sync", resultsBody(t, "studio", doubt), h.withToken()); w.Code != http.StatusOK {
		t.Fatalf("reporting: %d %s", w.Code, w.Body)
	}

	w = h.do(t, "POST", "/api/v1/queue/"+queued.ActionID+"/retry", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want a conflict — an interrupted send must not be repeated\n%s", w.Code, w.Body)
	}
}

func TestASettledActionCanBeDropped(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)
	gone := failOne(t, h, cookie, csrf, refused())

	del := func() int {
		w := h.do(t, "DELETE", "/api/v1/queue/"+gone, "", h.withCookie(cookie),
			func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
		return w.Code
	}
	if code := del(); code != http.StatusOK {
		t.Fatalf("drop: %d", code)
	}
	// Twice is quiet: two tabs showing the same failed action must not turn the
	// second click into an error about something that already happened.
	if code := del(); code != http.StatusOK {
		t.Errorf("a second drop answered %d", code)
	}

	w := h.do(t, "GET", "/api/v1/queue", "", h.withCookie(cookie))
	var got struct {
		Queue []struct {
			Action protocol.Action `json:"action"`
		} `json:"queue"`
	}
	decodeInto(t, w.Body.Bytes(), &got)
	for _, e := range got.Queue {
		if string(e.Action.ID) == gone {
			t.Errorf("the dropped action is still in the queue")
		}
	}
}

// TestTheQueueRoutesNeedASession: they change state, so they sit behind the same
// default-deny gate as everything else.
func TestTheQueueRoutesNeedASession(t *testing.T) {
	h := newHarness(t)
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/queue/" + id + "/retry"},
		{"DELETE", "/api/v1/queue/" + id},
	} {
		if w := h.do(t, tc.method, tc.path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d without a session", tc.method, tc.path, w.Code)
		}
	}
}

// TestTheQueueRoutesCheckCSRF: a DELETE is as much a state change as a POST, and
// the gate treats every unsafe method alike.
func TestTheQueueRoutesCheckCSRF(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)
	gone := failOne(t, h, cookie, csrf, refused())

	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/queue/" + gone + "/retry"},
		{"DELETE", "/api/v1/queue/" + gone},
	} {
		w := h.do(t, tc.method, tc.path, "", h.withCookie(cookie))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d with no csrf token", tc.method, tc.path, w.Code)
		}
	}
}

func TestTheQueueRoutesRefuseAMalformedID(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)

	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/queue/not-an-id/retry"},
		{"DELETE", "/api/v1/queue/not-an-id"},
	} {
		w := h.do(t, tc.method, tc.path, "", h.withCookie(cookie),
			func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s answered %d for a malformed id\n%s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}
