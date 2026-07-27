package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

func decodeInto(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("could not decode %s: %v", body, err)
	}
}

func sampleSnapshot(machine protocol.MachineID) protocol.Snapshot {
	return protocol.Snapshot{
		Machine: machine,
		User:    "redjive",
		TakenAt: at,
		Inbox: []protocol.Message{
			{PUID: 41, MID: "019a-1", Sent: at, From: "boss", To: []string{"redjive"},
				Subject: "RE: work", Convo: protocol.ConvoRef{UID: "c-1", Title: "parser", Index: 3},
				Body: "look at the span rules"},
			{PUID: 40, MID: "019a-2", Sent: at.Add(-time.Hour), From: "alice", To: []string{"redjive"},
				Subject: "scope", Read: true, Body: "scope please"},
		},
		Archive: []protocol.Message{
			{PUID: 12, MID: "019a-3", Sent: at.Add(-48 * time.Hour), From: "carol",
				To: []string{"redjive"}, Subject: "old", Archived: true, Body: "old news"},
		},
		Convos: []protocol.Convo{{UID: "c-1", Title: "parser", Members: []string{"boss", "redjive"}, Count: 2}},
		Tasks: []protocol.Task{
			{Name: "fix-the-parser", Owner: "bob", Priority: 4, Difficulty: 3, Status: 3, Done: 5, Total: 8},
			{Name: "write-docs", Priority: 2, Difficulty: 1, Status: 3},
		},
		Admin: &protocol.AdminState{
			Users:    []protocol.AdminUser{{Name: "boss"}, {Name: "redjive"}},
			Messages: []protocol.Message{{PUID: 1, MID: "019a-1", Sent: at, From: "boss", To: []string{"redjive"}, Subject: "RE: work"}},
			Receipts: []protocol.Receipt{{MID: "019a-1", Recipient: "redjive", Read: true, At: &at}},
		},
	}
}

// syncBody builds a valid sync request for a machine.
func syncBody(t *testing.T, machine protocol.MachineID) string {
	t.Helper()
	req := protocol.SyncRequest{
		Protocol: protocol.Version, Agent: "cq/test", SentAt: at,
		Snapshot: sampleSnapshot(machine),
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestASessionCanDriveTheWholeAPI is the milestone gate's other half: every
// endpoint answers correctly to a logged-in caller.
func TestASessionCanDriveTheWholeAPI(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	get := func(path string) []byte {
		t.Helper()
		w := h.do(t, "GET", path, "", h.withCookie(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}

	t.Run("session", func(t *testing.T) {
		var v struct {
			User     string `json:"user"`
			CSRF     string `json:"csrf"`
			Admin    bool   `json:"admin"`
			Machines []struct {
				Machine string `json:"machine"`
				Unread  int    `json:"unread"`
				Total   int    `json:"total"`
			} `json:"machines"`
		}
		decodeInto(t, get("/api/v1/session"), &v)
		if v.User != "redjive" || v.CSRF == "" || !v.Admin {
			t.Errorf("session = %+v", v)
		}
		if len(v.Machines) != 1 || v.Machines[0].Machine != "studio" {
			t.Fatalf("machines = %+v", v.Machines)
		}
		if v.Machines[0].Unread != 1 || v.Machines[0].Total != 2 {
			t.Errorf("counts = %d unread of %d", v.Machines[0].Unread, v.Machines[0].Total)
		}
	})

	t.Run("inbox is newest first", func(t *testing.T) {
		var v struct {
			Messages []struct {
				PUID    int    `json:"puid"`
				Machine string `json:"machine"`
				Subject string `json:"subject"`
			} `json:"messages"`
		}
		decodeInto(t, get("/api/v1/inbox"), &v)
		if len(v.Messages) != 2 {
			t.Fatalf("inbox holds %d messages, want 2", len(v.Messages))
		}
		if v.Messages[0].PUID != 41 {
			t.Errorf("first message is %d, want the newest (41)", v.Messages[0].PUID)
		}
		if v.Messages[0].Machine != "studio" {
			t.Errorf("a message carries no machine: %+v", v.Messages[0])
		}
	})

	t.Run("archive is separate", func(t *testing.T) {
		var v struct {
			Messages []struct {
				PUID int `json:"puid"`
			} `json:"messages"`
		}
		decodeInto(t, get("/api/v1/archive"), &v)
		if len(v.Messages) != 1 || v.Messages[0].PUID != 12 {
			t.Errorf("archive = %+v", v.Messages)
		}
	})

	t.Run("one message and its thread", func(t *testing.T) {
		var v struct {
			Message struct {
				PUID int    `json:"puid"`
				Body string `json:"body"`
			} `json:"message"`
			Thread []struct {
				PUID int `json:"puid"`
			} `json:"thread"`
		}
		decodeInto(t, get("/api/v1/messages/41"), &v)
		if v.Message.PUID != 41 || !strings.Contains(v.Message.Body, "span rules") {
			t.Errorf("message = %+v", v.Message)
		}
		if len(v.Thread) != 1 {
			t.Errorf("thread = %+v", v.Thread)
		}
	})

	t.Run("conversation", func(t *testing.T) {
		var v struct {
			Messages []struct {
				PUID int `json:"puid"`
			} `json:"messages"`
		}
		decodeInto(t, get("/api/v1/convos/c-1"), &v)
		if len(v.Messages) != 1 || v.Messages[0].PUID != 41 {
			t.Errorf("conversation = %+v", v.Messages)
		}
	})

	t.Run("read receipts", func(t *testing.T) {
		var v struct {
			MID      string `json:"mid"`
			Receipts []struct {
				Recipient string `json:"recipient"`
				Read      bool   `json:"read"`
			} `json:"receipts"`
		}
		decodeInto(t, get("/api/v1/messages/41/check"), &v)
		if len(v.Receipts) != 1 || v.Receipts[0].Recipient != "redjive" || !v.Receipts[0].Read {
			t.Errorf("receipts = %+v", v.Receipts)
		}
	})

	t.Run("tasks are ranked", func(t *testing.T) {
		var v struct {
			Tasks []struct {
				Name     string `json:"name"`
				Priority int    `json:"priority"`
			} `json:"tasks"`
		}
		decodeInto(t, get("/api/v1/tasks"), &v)
		if len(v.Tasks) != 2 {
			t.Fatalf("tasks = %+v", v.Tasks)
		}
		if v.Tasks[0].Name != "fix-the-parser" {
			t.Errorf("tasks are not priority-ordered: %+v", v.Tasks)
		}
	})

	t.Run("one task", func(t *testing.T) {
		var v struct {
			Name  string `json:"name"`
			Owner string `json:"owner"`
		}
		decodeInto(t, get("/api/v1/tasks/fix-the-parser"), &v)
		if v.Name != "fix-the-parser" || v.Owner != "bob" {
			t.Errorf("task = %+v", v)
		}
	})

	t.Run("admin state", func(t *testing.T) {
		var v struct {
			Machines []struct {
				Machine string `json:"machine"`
				State   struct {
					Users    []struct{ Name string } `json:"users"`
					Receipts []struct{ MID string }  `json:"receipts"`
				} `json:"state"`
			} `json:"machines"`
		}
		decodeInto(t, get("/api/v1/admin/state"), &v)
		if len(v.Machines) != 1 || len(v.Machines[0].State.Users) != 2 {
			t.Errorf("admin state = %+v", v)
		}
	})

	t.Run("queue starts empty", func(t *testing.T) {
		var v struct {
			Queue []store.Entry `json:"queue"`
		}
		decodeInto(t, get("/api/v1/queue"), &v)
		if len(v.Queue) != 0 {
			t.Errorf("queue = %+v", v.Queue)
		}
	})

	t.Run("every write queues and says so", func(t *testing.T) {
		for _, tc := range []struct{ name, method, path, body string }{
			{"read", "POST", "/api/v1/messages/41/read", ""},
			{"archive", "POST", "/api/v1/messages/41/archive", ""},
			{"reply", "POST", "/api/v1/messages/41/reply", `{"subject":"RE: work","body":"looks good"}`},
			{"send", "POST", "/api/v1/messages", `{"to":["bob"],"subject":"hello","body":"hi"}`},
			{"cc", "POST", "/api/v1/convos/c-1/cc", `{"user":"carol"}`},
			// The one that cannot be undone. It queues like the rest: the
			// confirmation is a conversation with whoever is looking at the mail,
			// and by the time a request arrives that has either happened in the
			// browser or was never going to.
			{"prune", "POST", "/api/v1/messages/41/prune", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := h.do(t, tc.method, tc.path, tc.body, h.withCookie(cookie),
					func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
				if w.Code != http.StatusAccepted {
					t.Fatalf("status %d, want 202 (body %s)", w.Code, w.Body)
				}
				var v struct {
					ActionID string `json:"action_id"`
					State    string `json:"state"`
					Machine  string `json:"machine"`
				}
				decodeInto(t, w.Body.Bytes(), &v)
				if len(v.ActionID) != 32 || v.State != "queued" || v.Machine != "studio" {
					t.Errorf("queued view = %+v", v)
				}
			})
		}

		var v struct {
			Queue []store.Entry `json:"queue"`
		}
		decodeInto(t, get("/api/v1/queue"), &v)
		if len(v.Queue) != 6 {
			t.Errorf("queue holds %d entries, want 6", len(v.Queue))
		}
	})
}

// TestQueuedActionsReachTheAgentAndComeBack is the whole loop: the user acts,
// a sync collects it, the agent reports, and the queue settles.
func TestQueuedActionsReachTheAgentAndComeBack(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	w := h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusAccepted {
		t.Fatalf("queueing failed: %d %s", w.Code, w.Body)
	}
	var queued struct {
		ActionID string `json:"action_id"`
	}
	decodeInto(t, w.Body.Bytes(), &queued)

	// The agent syncs and is handed the action.
	w = h.do(t, "POST", "/api/v1/sync", syncBody(t, "studio"), h.withToken())
	if w.Code != http.StatusOK {
		t.Fatalf("sync: status %d, body %s", w.Code, w.Body)
	}
	// Each response is decoded into a fresh value. `actions` is omitempty, so
	// reusing one would leave the previous batch in place when a later response
	// carries none — and the test would pass or fail for the wrong reason.
	var first protocol.SyncResponse
	decodeInto(t, w.Body.Bytes(), &first)
	if len(first.Actions) != 1 || string(first.Actions[0].ID) != queued.ActionID {
		t.Fatalf("sync handed back %+v", first.Actions)
	}
	if first.Actions[0].Op != protocol.OpRead || first.Actions[0].Args.PUID != 41 {
		t.Errorf("action = %+v", first.Actions[0])
	}

	// A sync that reports nothing gets the same action again: delivery is
	// at-least-once, and the agent is the one that makes it exactly-once.
	w = h.do(t, "POST", "/api/v1/sync", syncBody(t, "studio"), h.withToken())
	var again protocol.SyncResponse
	decodeInto(t, w.Body.Bytes(), &again)
	if len(again.Actions) != 1 {
		t.Errorf("an unreported action was not re-delivered: %+v", again.Actions)
	}

	// Now the agent reports it done.
	req := protocol.SyncRequest{
		Protocol: protocol.Version, Agent: "cq/test", SentAt: at,
		Results:  []protocol.Result{{ActionID: protocol.ActionID(queued.ActionID), OK: true, At: at}},
		Snapshot: sampleSnapshot("studio"),
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w = h.do(t, "POST", "/api/v1/sync", string(body), h.withToken())
	if w.Code != http.StatusOK {
		t.Fatalf("sync: status %d, body %s", w.Code, w.Body)
	}
	var settled protocol.SyncResponse
	decodeInto(t, w.Body.Bytes(), &settled)
	if len(settled.Actions) != 0 {
		t.Errorf("a settled action was re-delivered: %+v", settled.Actions)
	}

	// And the user can see what became of it.
	got := h.do(t, "GET", "/api/v1/queue", "", h.withCookie(cookie))
	var q struct {
		Queue []store.Entry `json:"queue"`
	}
	decodeInto(t, got.Body.Bytes(), &q)
	if len(q.Queue) != 1 || q.Queue[0].State != store.Done {
		t.Errorf("queue = %+v", q.Queue)
	}
}

func TestSyncRejectsAMalformedBody(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"not json", "hello"},
		{"wrong protocol", `{"protocol":99,"agent":"x","sent_at":"2026-07-24T18:31:04Z","snapshot":{"machine":"m","user":"u","taken_at":"2026-07-24T18:31:04Z"}}`},
		{"unknown field", `{"protocol":1,"invented":true}`},
		{"bad machine", `{"protocol":1,"agent":"x","sent_at":"2026-07-24T18:31:04Z","snapshot":{"machine":"../etc","user":"u","taken_at":"2026-07-24T18:31:04Z"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, "POST", "/api/v1/sync", tc.body, h.withToken())
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (body %s)", w.Code, w.Body)
			}
			var e struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeInto(t, w.Body.Bytes(), &e)
			if e.Error.Code != "parse" {
				t.Errorf("code = %q, want parse", e.Error.Code)
			}
		})
	}
}

// TestSeveralMachinesMustBeNamed: with two mailboxes, guessing one would be
// picking at random, so the request is refused and the choices are listed.
func TestSeveralMachinesMustBeNamed(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	putSnapshot(t, h, "laptop")
	cookie, csrf := h.login(t)

	w := h.do(t, "GET", "/api/v1/messages/41", "", h.withCookie(cookie))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "studio") || !strings.Contains(body, "laptop") {
		t.Errorf("the refusal should list the choices: %s", body)
	}

	// Named, it works.
	w = h.do(t, "GET", "/api/v1/messages/41?machine=studio", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Errorf("status %d with the machine named", w.Code)
	}

	// The inbox unions both without being asked.
	w = h.do(t, "GET", "/api/v1/inbox", "", h.withCookie(cookie))
	var v struct {
		Messages []struct {
			Machine string `json:"machine"`
		} `json:"messages"`
	}
	decodeInto(t, w.Body.Bytes(), &v)
	if len(v.Messages) != 4 {
		t.Errorf("inbox holds %d messages, want both machines' 2 each", len(v.Messages))
	}

	// And a write must name its machine too.
	w = h.do(t, "POST", "/api/v1/messages", `{"to":["bob"],"subject":"s","body":"b"}`,
		h.withCookie(cookie), func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusConflict {
		t.Errorf("an unaddressed send was accepted: status %d", w.Code)
	}
	w = h.do(t, "POST", "/api/v1/messages", `{"machine":"laptop","to":["bob"],"subject":"s","body":"b"}`,
		h.withCookie(cookie), func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusAccepted {
		t.Errorf("an addressed send was refused: status %d, body %s", w.Code, w.Body)
	}
}

func TestNotFoundCases(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, _ := h.login(t)

	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"unknown message", "/api/v1/messages/999", http.StatusNotFound},
		{"unknown task", "/api/v1/tasks/nope", http.StatusNotFound},
		{"unknown conversation", "/api/v1/convos/nope", http.StatusNotFound},
		{"message id is not a number", "/api/v1/messages/abc", http.StatusBadRequest},
		{"negative message id", "/api/v1/messages/-1", http.StatusBadRequest},
		{"bad machine name", "/api/v1/inbox?machine=Not%20A%20Machine", http.StatusBadRequest},
		{"unknown page", "/nope", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, "GET", tc.path, "", h.withCookie(cookie))
			if w.Code != tc.want {
				t.Errorf("status %d, want %d (body %s)", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestNoMachineYet(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)

	w := h.do(t, "GET", "/api/v1/inbox", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("an empty mirror should still answer: status %d", w.Code)
	}
	var v struct {
		Messages []any `json:"messages"`
	}
	decodeInto(t, w.Body.Bytes(), &v)
	if len(v.Messages) != 0 {
		t.Errorf("messages = %+v", v.Messages)
	}

	if got := h.do(t, "GET", "/api/v1/messages/1", "", h.withCookie(cookie)); got.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 when nothing has synced", got.Code)
	}
}

func TestAdminPanelCanBeTurnedOff(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, _ := h.login(t)

	// A second server over the same store, with the panel off.
	off := newHarnessWithAdmin(t, h, false)
	w := off.do(t, "GET", "/api/v1/admin/state", "", off.withCookie(cookie))
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 with the admin panel off", w.Code)
	}
}

func TestWriteBodiesAreValidated(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	for _, tc := range []struct{ name, path, body string }{
		{"send without recipients", "/api/v1/messages", `{"subject":"s","body":"b"}`},
		{"send without a subject", "/api/v1/messages", `{"to":["bob"],"body":"b"}`},
		{"send without a body", "/api/v1/messages", `{"to":["bob"],"subject":"s"}`},
		{"reply without a body", "/api/v1/messages/41/reply", `{"subject":"s"}`},
		{"cc without a user", "/api/v1/convos/c-1/cc", `{}`},
		{"unknown field", "/api/v1/messages", `{"to":["bob"],"subject":"s","body":"b","invented":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, "POST", tc.path, tc.body, h.withCookie(cookie),
				func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (body %s)", w.Code, w.Body)
			}
		})
	}
}
