package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// What an agent is doing, as `orc status --json` reports it.
//
// The states are a *reading* of things Orc already knew, so what is worth pinning
// is that the reading is the same one the commands make: an employed identity with
// no session is `down` and not `idle`, a live one that has said nothing is
// `waiting` and not `generating`, and the difference between them is the thing the
// browser could not previously see.

// stateOf pulls one identity's activity out of the fleet JSON.
func stateOf(t *testing.T, r *rig, who string) map[string]any {
	t.Helper()

	got := r.ok("boss", "status", "--json")
	var fleet struct {
		Identities []map[string]any `json:"identities"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &fleet); err != nil {
		t.Fatalf("reading the fleet json: %v\n%s", err, got.stdout)
	}
	for _, id := range fleet.Identities {
		if id["name"] == who {
			return id
		}
	}
	t.Fatalf("%s is not in the fleet json", who)
	return nil
}

func TestAnUnemployedIdentityIsIdle(t *testing.T) {
	r := fullFleet(t)
	if got := stateOf(t, r, "ember")["activity"]; got != "idle" {
		t.Errorf("an unemployed identity is %q, want idle", got)
	}
}

func TestAnEmployedIdentityWithNoSessionIsDown(t *testing.T) {
	r := fullFleet(t)
	r.failStart = true
	r.run("boss", "employ", "ember")

	id := stateOf(t, r, "ember")
	if id["activity"] != "down" {
		t.Fatalf("an employed identity with no session is %q, want down", id["activity"])
	}
	// And it says why, which is the whole difference between this and idle.
	if why, _ := id["why"].(string); !strings.Contains(why, "start") {
		t.Errorf("down does not say what went wrong: %q", why)
	}
}

// A live session that has said nothing is waiting to be spoken to, which is what
// `orc wake` reads it as. Anything else would have the tab and the cycle
// disagreeing about the same agent.
func TestALiveSessionWithNoEventsIsWaiting(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	if got := stateOf(t, r, "ember")["activity"]; got != "waiting" {
		t.Errorf("a session that has said nothing is %q, want waiting", got)
	}
}

func TestAMidTurnSessionIsGenerating(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	feed(t, r, "ember", ago(1, "PreToolUse", "Edit", "internal/cli/wake.go"))

	id := stateOf(t, r, "ember")
	if id["activity"] != "generating" {
		t.Fatalf("a session mid-tool-call is %q, want generating", id["activity"])
	}
	rows, _ := id["doing"].([]any)
	if len(rows) != 1 {
		t.Fatalf("it carried %d rows, want 1", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	if row["tool"] != "Edit" || row["detail"] != "internal/cli/wake.go" {
		t.Errorf("the row is %v", row)
	}
}

// The feed travels bounded. A fleet of twenty with a thousand events each must not
// put a megabyte through a sync; anybody who wants the whole feed has `orc attach`.
func TestOnlyTheLastFewEventsTravel(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	var events []string
	for i := 40; i > 0; i-- {
		events = append(events, ago(i, "PreToolUse", "Read", "internal/cli/cli.go"))
	}
	feed(t, r, "ember", events...)

	rows, _ := stateOf(t, r, "ember")["doing"].([]any)
	if len(rows) == 0 || len(rows) > 8 {
		t.Errorf("%d rows travelled; the bound is 8", len(rows))
	}
}

// A refusal is carried as one. It is the row anybody scanning a fleet is looking
// for, and one that arrived as an ordinary tool call would be invisible.
func TestARefusalIsCarriedAsARefusal(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")
	feed(t, r, "ember",
		`{"at":"2026-07-25T11:59:00.000Z","session":"s","event":"PreToolUse","tool":"Write",`+
			`"path":"/etc/passwd","verdict":"block","reason":"outside the workspace"}`)

	rows, _ := stateOf(t, r, "ember")["doing"].([]any)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	if row["blocked"] != true {
		t.Errorf("a blocked call did not travel as blocked: %v", row)
	}
	if reason, _ := row["reason"].(string); !strings.Contains(reason, "workspace") {
		t.Errorf("the reason did not travel: %q", reason)
	}
}
