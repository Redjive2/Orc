package server_test

import (
	"net/http"
	"testing"

	"orc/cq/internal/protocol"
)

// The fleet endpoints. Reading is one route because a fleet is one derived thing;
// the verbs are one route each, and every one queues.

// TestEveryFleetVerbQueues walks protocol.FleetOps rather than a hand-written
// list, so a verb added to the protocol without a route fails here rather than
// being quietly unreachable from the browser.
func TestEveryFleetVerbQueues(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	const m = `"machine":"studio"`
	routes := map[protocol.Op]struct{ method, path, body string }{
		protocol.OpOrcNewIdentity: {"POST", "/api/v1/fleet/identities", `{` + m + `,"name":"atlas"}`},
		protocol.OpOrcNewRole: {"POST", "/api/v1/fleet/roles",
			`{` + m + `,"name":"engineer","authority":60,"description":"writes the code"}`},
		protocol.OpOrcNewPermission: {"POST", "/api/v1/fleet/permissions",
			`{` + m + `,"name":"edit","floor":40,"patterns":["read(A/**)"]}`},
		protocol.OpOrcAssignRole: {"POST", "/api/v1/fleet/identities/atlas/role",
			`{` + m + `,"role":"engineer"}`},
		protocol.OpOrcMove:    {"POST", "/api/v1/fleet/identities/atlas/move", `{` + m + `,"boss":"boss"}`},
		protocol.OpOrcEmploy:  {"POST", "/api/v1/fleet/identities/atlas/employ", `{` + m + `}`},
		protocol.OpOrcFire:    {"POST", "/api/v1/fleet/identities/atlas/fire", `{` + m + `}`},
		protocol.OpOrcPoke:    {"POST", "/api/v1/fleet/identities/atlas/poke", `{` + m + `}`},
		protocol.OpOrcRefresh: {"POST", "/api/v1/fleet/identities/atlas/refresh", `{` + m + `}`},
		protocol.OpOrcGrant: {"POST", "/api/v1/fleet/identities/atlas/grant",
			`{` + m + `,"permission":"edit"}`},
		protocol.OpOrcRevoke: {"POST", "/api/v1/fleet/identities/atlas/revoke",
			`{` + m + `,"permission":"edit"}`},
		protocol.OpOrcRemoveIdentity: {"DELETE", "/api/v1/fleet/identities/atlas", `{` + m + `}`},
		protocol.OpOrcAssignAuthority: {"POST", "/api/v1/fleet/roles/engineer/authority",
			`{` + m + `,"authority":55}`},
		protocol.OpOrcEditPermission: {"PATCH", "/api/v1/fleet/permissions/edit-anno",
			`{` + m + `,"floor":40,"patterns":["read(Anno/**)"]}`},
		protocol.OpOrcAssignPerm: {"POST", "/api/v1/fleet/roles/engineer/permissions",
			`{` + m + `,"permission":"edit"}`},
		protocol.OpOrcBudget:     {"POST", "/api/v1/fleet/roles/engineer/budget", `{` + m + `,"load":24}`},
		protocol.OpOrcRemoveRole: {"DELETE", "/api/v1/fleet/roles/engineer", `{` + m + `}`},
		protocol.OpOrcRemovePerm: {"DELETE", "/api/v1/fleet/permissions/edit", `{` + m + `}`},
		protocol.OpOrcTend:       {"POST", "/api/v1/fleet/tend", `{` + m + `}`},
		// `from` is part of the request rather than optional: the route refuses a
		// move that cannot say what the browser was looking at.
		protocol.OpOrcToolkit: {"POST", "/api/v1/fleet/toolkit", `{` + m + `,"name":"boss"}`},
		protocol.OpOrcInstructSet: {"PUT", "/api/v1/instruct/identities/atlas",
			`{` + m + `,"text":"you write the parser"}`},
		protocol.OpOrcInstructClear: {"DELETE", "/api/v1/instruct/system", `{` + m + `}`},
		protocol.OpOrcWorkspace: {"POST", "/api/v1/fleet/identities/atlas/workspace",
			`{` + m + `,"workspace":"/trees/parser","from":"/old/workspace","adopt":true}`},
	}

	seen := map[protocol.Op]bool{}
	for _, op := range protocol.FleetOps {
		route, ok := routes[op]
		if !ok {
			t.Errorf("%s has no route in this test, so the browser cannot reach it", op)
			continue
		}
		t.Run(string(op), func(t *testing.T) {
			w := h.do(t, route.method, route.path, route.body, h.withCookie(cookie), withCSRF(csrf))
			if w.Code != http.StatusAccepted {
				t.Fatalf("status %d, want 202: %s", w.Code, w.Body)
			}
			seen[op] = true
		})
	}

	got := map[protocol.Op]int{}
	for _, e := range queueOf(t, h, cookie) {
		got[e.Action.Op]++
	}
	for _, op := range protocol.FleetOps {
		if seen[op] && got[op] != 1 {
			t.Errorf("the queue holds %d of %s, want 1", got[op], op)
		}
	}
}

// TestFleetOperandsReachTheQueueIntact: what the browser sent is what the agent
// will run, so a field dropped in between is a command that does something else —
// and these commands are about authority.
func TestFleetOperandsReachTheQueueIntact(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	post := func(method, path, body string) {
		t.Helper()
		if w := h.do(t, method, path, body, h.withCookie(cookie), withCSRF(csrf)); w.Code != http.StatusAccepted {
			t.Fatalf("%s %s: status %d: %s", method, path, w.Code, w.Body)
		}
	}
	post("POST", "/api/v1/fleet/roles",
		`{"machine":"studio","name":"engineer","authority":60,"description":"writes the code"}`)
	post("POST", "/api/v1/fleet/identities/atlas/employ",
		`{"machine":"studio","model":"opus","effort":"high"}`)
	post("POST", "/api/v1/fleet/identities/atlas/grant",
		`{"machine":"studio","permission":"edit","until":"2h"}`)
	post("DELETE", "/api/v1/fleet/permissions/edit", `{"machine":"studio","role":"engineer"}`)

	by := map[protocol.Op]protocol.Args{}
	for _, e := range queueOf(t, h, cookie) {
		by[e.Action.Op] = e.Action.Args
	}
	if a := by[protocol.OpOrcNewRole]; a.Role != "engineer" || a.Authority != 60 || a.Description == "" {
		t.Errorf("the role queued as %+v", a)
	}
	if a := by[protocol.OpOrcEmploy]; a.Model != "opus" || a.Effort != "high" {
		t.Errorf("employ queued as %+v", a)
	}
	if a := by[protocol.OpOrcGrant]; a.Until != "2h" {
		t.Errorf("the grant queued as %+v", a)
	}
	// The one that narrows a role rather than deleting the permission.
	if a := by[protocol.OpOrcRemovePerm]; a.Role != "engineer" {
		t.Errorf("removing from a role queued as %+v", a)
	}
}

// TestABudgetOfNothingIsNotAMissingBudget: zero is a real budget — it refuses
// every employ — so the two have to stay apart all the way to the queue.
func TestABudgetOfNothingIsNotAMissingBudget(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	w := h.do(t, "POST", "/api/v1/fleet/roles/engineer/budget",
		`{"machine":"studio","load":0}`, h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusAccepted {
		t.Fatalf("a budget of zero was refused: %d %s", w.Code, w.Body)
	}

	// And a body with no load at all is a mistake, said as one.
	w = h.do(t, "POST", "/api/v1/fleet/roles/engineer/budget",
		`{"machine":"studio"}`, h.withCookie(cookie), withCSRF(csrf))
	if w.Code == http.StatusAccepted {
		t.Errorf("a budget with no load was accepted")
	}

	for _, e := range queueOf(t, h, cookie) {
		if e.Action.Op == protocol.OpOrcBudget && e.Action.Args.Load != 0 {
			t.Errorf("a budget of zero queued as %d", e.Action.Args.Load)
		}
	}
}

// TestFleetRoutesRefuseWhatOrcWould: the operand rules are the protocol's and are
// enforced before anything is queued, so a value the fleet would reject never
// becomes an action that fails hours later on a machine nobody is watching.
func TestFleetRoutesRefuseWhatOrcWould(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	for _, tc := range []struct{ name, method, path, body string }{
		{"a role at the operator's level", "POST", "/api/v1/fleet/roles",
			`{"machine":"studio","name":"boss-like","authority":100,"description":"no"}`},
		{"a role with no description", "POST", "/api/v1/fleet/roles",
			`{"machine":"studio","name":"engineer","authority":60}`},
		{"a permission with no clauses", "POST", "/api/v1/fleet/permissions",
			`{"machine":"studio","name":"empty","floor":40}`},
		{"a move with no boss", "POST", "/api/v1/fleet/identities/atlas/move",
			`{"machine":"studio"}`},
		{"an identity name that is not a name", "POST", "/api/v1/fleet/identities",
			`{"machine":"studio","name":"not a name"}`},
		{"a budget above what orc will hold", "POST", "/api/v1/fleet/roles/engineer/budget",
			`{"machine":"studio","load":99999}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, tc.method, tc.path, tc.body, h.withCookie(cookie), withCSRF(csrf))
			if w.Code == http.StatusAccepted {
				t.Errorf("accepted %s: %s", tc.name, w.Body)
			}
		})
	}
	if entries := queueOf(t, h, cookie); len(entries) != 0 {
		t.Errorf("a refused request still queued something: %d entries", len(entries))
	}
}

// TestFleetIsServedPerMachine: a machine that runs no agents carries no fleet,
// and is left out rather than listed as empty — an empty fleet reads as one that
// has lost its identities.
func TestFleetIsServedPerMachine(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)
	putSnapshot(t, h, "studio")

	w := h.do(t, "GET", "/api/v1/fleet", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out struct {
		Fleets []struct {
			Machine  string `json:"machine"`
			Operator string `json:"operator"`
		} `json:"fleets"`
	}
	decodeInto(t, w.Body.Bytes(), &out)
	if len(out.Fleets) != 0 {
		t.Errorf("a machine with no fleet was listed: %+v", out.Fleets)
	}
}
