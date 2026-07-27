package server_test

import (
	"net/http"
	"strings"
	"testing"

	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// withCSRF is spelled out here rather than reused because the rest of this suite
// writes the header inline; one helper for the thirty calls below is worth it.
func withCSRF(csrf string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) }
}

// queueOf reads the queue the way the browser does, so what these tests assert on
// is what an operator would see rather than the store's own shape.
func queueOf(t *testing.T, h *harness, cookie string) []struct {
	Action protocol.Action `json:"action"`
	State  store.State     `json:"state"`
} {
	t.Helper()
	w := h.do(t, "GET", "/api/v1/queue", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("queue: status %d: %s", w.Code, w.Body)
	}
	var out struct {
		Queue []struct {
			Action protocol.Action `json:"action"`
			State  store.State     `json:"state"`
		} `json:"queue"`
	}
	decodeInto(t, w.Body.Bytes(), &out)
	return out.Queue
}

// The task write endpoints. Every one queues rather than acting: the server
// cannot reach the agent machine, so `202` and a place in the queue is the whole
// of what it can honestly promise.

// TestEveryTaskVerbQueues walks protocol.TaskOps rather than a hand-written list,
// so a verb added to the protocol without a route fails here rather than being
// quietly unreachable from the browser.
func TestEveryTaskVerbQueues(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	routes := map[protocol.Op]struct {
		method, path, body string
	}{
		protocol.OpTaskCreate: {"POST", "/api/v1/tasks",
			`{"machine":"studio","name":"parser","priority":4,"difficulty":3}`},
		protocol.OpTaskPush:  {"POST", "/api/v1/tasks/parser/push", `{"machine":"studio"}`},
		protocol.OpTaskClaim: {"POST", "/api/v1/tasks/parser/claim", `{"machine":"studio"}`},
		protocol.OpTaskAssign: {"POST", "/api/v1/tasks/parser/assign",
			`{"machine":"studio","user":"bob"}`},
		protocol.OpTaskInvite: {"POST", "/api/v1/tasks/parser/invite",
			`{"machine":"studio","user":"bob"}`},
		protocol.OpTaskKick: {"POST", "/api/v1/tasks/parser/kick",
			`{"machine":"studio","user":"bob"}`},
		protocol.OpTaskLeave: {"POST", "/api/v1/tasks/parser/leave", `{"machine":"studio"}`},
		protocol.OpTaskDescribe: {"PUT", "/api/v1/tasks/parser/description",
			`{"machine":"studio","text":"# what to do\n"}`},
		protocol.OpTaskDescribeClear: {"DELETE", "/api/v1/tasks/parser/description",
			`{"machine":"studio"}`},
		protocol.OpTaskScope: {"POST", "/api/v1/tasks/parser/scope",
			`{"machine":"studio","paths":["internal/tree"]}`},
		protocol.OpTaskWorktree: {"POST", "/api/v1/tasks/parser/worktree",
			`{"machine":"studio","path":"work/parser"}`},
		protocol.OpTaskStatus: {"POST", "/api/v1/tasks/parser/status",
			`{"machine":"studio","status":2}`},
		protocol.OpTaskSubtask: {"POST", "/api/v1/tasks/parser/subtasks",
			`{"machine":"studio","sub":"tests"}`},
		protocol.OpTaskComplete: {"POST", "/api/v1/tasks/parser/complete", `{"machine":"studio"}`},
		protocol.OpTaskDelete:   {"DELETE", "/api/v1/tasks/parser", `{"machine":"studio"}`},
	}

	seen := map[protocol.Op]bool{}
	for _, op := range protocol.TaskOps {
		route, ok := routes[op]
		if !ok {
			t.Errorf("%s has no route in this test, so the browser cannot reach it", op)
			continue
		}
		t.Run(string(op), func(t *testing.T) {
			w := h.do(t, route.method, route.path, route.body,
				h.withCookie(cookie), withCSRF(csrf))
			if w.Code != http.StatusAccepted {
				t.Fatalf("status %d, want 202: %s", w.Code, w.Body)
			}
			seen[op] = true
		})
	}

	// And the queue holds one of each, which is the other half: a 202 that queued
	// the wrong operation would pass every check above.
	entries := queueOf(t, h, cookie)
	got := map[protocol.Op]int{}
	for _, e := range entries {
		got[e.Action.Op]++
	}
	for _, op := range protocol.TaskOps {
		if seen[op] && got[op] != 1 {
			t.Errorf("the queue holds %d of %s, want 1", got[op], op)
		}
	}
}

// TestTaskOperandsReachTheQueueIntact: the browser's operands are what the agent
// will run, so a field dropped between the body and the action is a command that
// does something else.
func TestTaskOperandsReachTheQueueIntact(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	post := func(path, body string) {
		t.Helper()
		if w := h.do(t, "POST", path, body, h.withCookie(cookie), withCSRF(csrf)); w.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d: %s", path, w.Code, w.Body)
		}
	}
	post("/api/v1/tasks", `{"machine":"studio","name":"parser","priority":5,"difficulty":2}`)
	post("/api/v1/tasks/parser/scope", `{"machine":"studio","paths":["internal/tree","internal/lex"]}`)
	post("/api/v1/tasks/parser/complete", `{"machine":"studio","sub":"tests","force":true}`)

	by := map[protocol.Op]protocol.Args{}
	for _, e := range queueOf(t, h, cookie) {
		by[e.Action.Op] = e.Action.Args
	}

	if a := by[protocol.OpTaskCreate]; a.Task != "parser" || a.Priority != 5 || a.Difficulty != 2 {
		t.Errorf("create queued as %+v", a)
	}
	if a := by[protocol.OpTaskScope]; len(a.Paths) != 2 || a.Paths[1] != "internal/lex" {
		t.Errorf("scope queued as %+v", a)
	}
	if a := by[protocol.OpTaskComplete]; a.Sub != "tests" || !a.Force {
		t.Errorf("complete queued as %+v", a)
	}
}

// TestTaskRoutesRefuseWhatMacmuffinWould: the operand rules live in the protocol
// and are enforced before anything is queued, so a value the pool would reject
// never becomes an action that fails hours later on a machine nobody is watching.
func TestTaskRoutesRefuseWhatMacmuffinWould(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"a priority off the scale", "POST", "/api/v1/tasks",
			`{"machine":"studio","name":"parser","priority":9,"difficulty":3}`},
		{"a status off the scale", "POST", "/api/v1/tasks/parser/status",
			`{"machine":"studio","status":9}`},
		{"a task with no name", "POST", "/api/v1/tasks",
			`{"machine":"studio","priority":3,"difficulty":3}`},
		{"a name that is not a name", "POST", "/api/v1/tasks/parser/assign",
			`{"machine":"studio","user":"not a name"}`},
		{"a scope with no paths", "POST", "/api/v1/tasks/parser/scope",
			`{"machine":"studio","paths":[]}`},
		{"a scope path climbing out of the checkout", "POST", "/api/v1/tasks/parser/scope",
			`{"machine":"studio","paths":["../../etc"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, tc.method, tc.path, tc.body, h.withCookie(cookie), withCSRF(csrf))
			if w.Code == http.StatusAccepted {
				t.Errorf("accepted %s: %s", tc.name, w.Body)
			}
		})
	}

	// Nothing was queued by any of them.
	if entries := queueOf(t, h, cookie); len(entries) != 0 {
		t.Errorf("a refused request still queued something: %d entries", len(entries))
	}
}

// TestABadOperandIsRefusedAsTheCallersOwn. Creating a task called
// `%invalid-character` answered 500 with the word "internal" and nothing else.
//
// Nothing was broken on the server. The name was refused, correctly, by
// Action.Validate — but that runs inside Enqueue, several layers below the
// request, and every failure from the store passes through serverSide, which
// flattens what it does not recognise so that a path on the server's disk cannot
// reach a browser. A caller's own operand went through the same reduction, and
// the one person who could fix it was told nothing at all.
func TestABadOperandIsRefusedAsTheCallersOwn(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	for _, tc := range []struct{ name, path, body, mentions string }{
		{"a new task", "/api/v1/tasks",
			`{"machine":"studio","name":"%invalid-character","priority":3,"difficulty":3}`, "%"},
		{"a new step", "/api/v1/tasks/parser/subtasks",
			`{"machine":"studio","sub":"one two"}`, "a space"},
		{"an agent to assign to", "/api/v1/tasks/parser/assign",
			`{"machine":"studio","user":"not a name"}`, "a space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, "POST", tc.path, tc.body, h.withCookie(cookie), withCSRF(csrf))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", w.Code, w.Body)
			}
			// The character it objected to, so the person who typed it knows which
			// one to take out. "internal error" named nothing, and a message that
			// says only "invalid name" about a name somebody has now read four
			// times is barely better.
			if body := w.Body.String(); !strings.Contains(body, tc.mentions) {
				t.Errorf("the refusal does not say what was wrong (%q): %s", tc.mentions, body)
			}
		})
	}
}
