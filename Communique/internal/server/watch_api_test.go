package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"orc/cq/internal/protocol"
)

// Watching a session, over the wire.
//
// The store's own tests cover the merge and the clock. What is worth proving here
// is the join: that a request marked as a narrow round takes the narrow path, that
// an unmarked one does not, and that the lease reaches the machine on the response
// — which is the only moment the two are ever in contact.

func watchCall(t *testing.T, h *harness, verb, name, body string) *http.Response {
	t.Helper()
	cookie, csrf := h.login(t)
	res := h.do(t, "POST", "/api/v1/fleet/identities/"+name+"/"+verb, body,
		h.withCookie(cookie), func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	return res.Result()
}

// narrowBody is what a watched machine posts: one session, and nothing else.
func narrowBody(t *testing.T, machine protocol.MachineID, identity string, turn int) string {
	t.Helper()
	req := protocol.SyncRequest{
		Protocol: protocol.Version, Agent: "cq/test", SentAt: at,
		Watch: identity,
		Snapshot: protocol.Snapshot{
			Machine: machine, User: "redjive", TakenAt: at,
			Fleet: &protocol.Fleet{Sessions: []protocol.FleetSession{
				{Identity: identity, Live: true, Turn: turn},
			}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func syncResponse(t *testing.T, h *harness, body string) protocol.SyncResponse {
	t.Helper()
	w := h.do(t, "POST", "/api/v1/sync", body, h.withToken())
	if w.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	var got protocol.SyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	return got
}

func TestANarrowRoundDoesNotReplaceTheMirror(t *testing.T) {
	// The failure this exists to stop is silent: a narrow snapshot taking the
	// ordinary path would erase a machine's mail every three seconds, and nothing
	// on the screen would say so — the mailbox would simply be empty.
	h := newHarness(t)
	putSnapshot(t, h, "studio")

	syncResponse(t, h, narrowBody(t, "studio", "ember", 7))

	cookie, _ := h.login(t)
	res := h.do(t, "GET", "/api/v1/inbox", "", h.withCookie(cookie))
	if res.Code != http.StatusOK {
		t.Fatalf("reading the inbox back: %d %s", res.Code, res.Body)
	}
	var inbox struct {
		Messages []protocol.Message `json:"messages"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &inbox); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(inbox.Messages) == 0 {
		t.Errorf("a narrow round emptied the mailbox: %s", res.Body)
	}
}

func TestTakingAWatchReachesTheMachineOnTheNextResponse(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")

	if res := watchCall(t, h, "watch", "ember", `{"machine":"studio"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("taking the watch: %d", res.StatusCode)
	}

	got := syncResponse(t, h, syncBody(t, "studio"))
	if got.Watching == nil {
		t.Fatal("the response says nobody is being watched")
	}
	if got.Watching.Identity != "ember" {
		t.Errorf("watching %q, want ember", got.Watching.Identity)
	}
	if got.Watching.Every == "" {
		t.Error("the watch says nothing about how often to come back")
	}
}

func TestDroppingAWatchStopsTheNarrowRounds(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")

	watchCall(t, h, "watch", "ember", `{"machine":"studio"}`)
	if res := watchCall(t, h, "unwatch", "ember", `{"machine":"studio"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("dropping the watch: %d", res.StatusCode)
	}

	if got := syncResponse(t, h, syncBody(t, "studio")); got.Watching != nil {
		t.Errorf("a dropped watch is still on the response: %+v", *got.Watching)
	}
}

func TestAMachineNobodyIsReadingIsToldNothing(t *testing.T) {
	// Absent rather than an empty object: a watcher reads nothing as "stop the
	// narrow rounds", and an empty one would be a lease for an agent with no name.
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	if got := syncResponse(t, h, syncBody(t, "studio")); got.Watching != nil {
		t.Errorf("an unwatched machine is told %+v", *got.Watching)
	}
}

func TestANarrowRoundStillCarriesQueuedActions(t *testing.T) {
	// Not incidental. A narrow round that only *read* would make the pane current
	// and leave answering an agent as slow as it ever was — which is half the
	// point of watching one.
	h := newHarness(t)
	putSnapshot(t, h, "studio")

	if res := watchCall(t, h, "poke", "ember", `{"machine":"studio","message":"carry on"}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("queueing a poke: %d", res.StatusCode)
	}

	got := syncResponse(t, h, narrowBody(t, "studio", "ember", 7))
	if len(got.Actions) != 1 {
		t.Fatalf("a narrow round was handed %d actions, want 1", len(got.Actions))
	}
	if got.Actions[0].Op != protocol.OpOrcPoke {
		t.Errorf("it was handed %s", got.Actions[0].Op)
	}
}
