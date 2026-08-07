package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"orc/common/fault"
)

// The rosters. `orc status` is the tree and the cards; these are the flat lists,
// and what they have to get right is what they leave *out* — an identity outside
// the caller's branch, and the roles and permissions that only exist there.

// TestListRostersName: each roster names the things it is a roster of.
func TestListRostersName(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "grant", "permission", "ember", "lead", "--until", "2h")

	for _, tc := range []struct {
		kind string
		want []string
	}{
		{"identities", []string{"boss", "atlas", "ember", "quill", "architect", "engineer"}},
		{"roles", []string{"architect", "engineer", "leads the design", "atlas"}},
		{"permissions", []string{"edit-anno", "lead", "read(Anno/**)", "spawn(24)"}},
		{"grants", []string{"ember", "lead", "live"}},
	} {
		got := r.ok("boss", "list", tc.kind)
		for _, want := range tc.want {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("orc list %s does not mention %q:\n%s", tc.kind, want, got.stdout)
			}
		}
	}
}

// TestListTakesSingularAndPlural: `orc list role` is not a mistake worth refusing,
// and neither is `orc list perms`.
func TestListTakesSingularAndPlural(t *testing.T) {
	r := fullFleet(t)
	for _, word := range []string{"identity", "identities", "role", "roles",
		"permission", "permissions", "perms", "perm", "grant", "grants"} {
		if got := r.run("boss", "list", word); got.code != fault.CodeOK {
			t.Errorf("orc list %s exited %d\n%s", word, got.code, got.stderr)
		}
	}

	// And a word that is neither gets the same treatment an unknown verb gets.
	got := r.run("boss", "list", "rolls")
	if got.code != fault.CodeUsage {
		t.Errorf("orc list rolls exited %d, want %d", got.code, fault.CodeUsage)
	}
	if !strings.Contains(got.stderr, "orc list roles") {
		t.Errorf("a near miss should be guessed:\n%s", got.stderr)
	}
}

// The roster is the whole fleet, and the other three listings are not.
//
// This asserted the opposite, under a rule worth stating because it was a
// deliberate one: a roster is a disclosure, and what is not below you is not yours
// to read. It was overturned on purpose. The reason the command exists is a name
// in a task or a mailbox that nobody recognises, and such a name is almost never
// *below* the reader — an agent looks up and sideways far more often than down, so
// the scope that made the answer safe also made it useless. Every agent saw one
// row: itself.
//
// What it discloses is a name, a role, a boss and a date. Mail and tasks already
// carry those names to everybody who works with them, and none of the columns is a
// capability. What an identity may *do* stays scoped, which the rest of this test
// holds to.
func TestTheRosterIsTheWholeFleet(t *testing.T) {
	r := fullFleet(t)
	// quill works for atlas; ember still works for boss.
	r.ok("boss", "move", "quill", "atlas")

	got := r.ok("atlas", "list", "identities")
	for _, want := range []string{"atlas", "quill", "ember", "boss"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("atlas cannot see %q, and a roster that hides names answers nothing:\n%s",
				want, got.stdout)
		}
	}
	// An agent with nobody under it is the case this was written for.
	alone := r.ok("ember", "list", "identities")
	for _, want := range []string{"ember", "atlas", "quill"} {
		if !strings.Contains(alone.stdout, want) {
			t.Errorf("a leaf agent cannot see %q:\n%s", want, alone.stdout)
		}
	}
}

// And the listings that are about *capability* keep the branch rule. Widening the
// roster and quietly widening these with it is the failure worth writing down.
func TestWhatAnIdentityMayDoIsStillYourBranch(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "move", "quill", "atlas")

	// Roles follow their holders. `architect` is atlas's own, so it stays; a role
	// only somebody above holds does not appear.
	r.ok("boss", "new", "role", "auditor", "70", "reads", "everything")
	r.ok("boss", "assign", "role", "ember", "auditor")
	roles := r.ok("atlas", "list", "roles")
	if !strings.Contains(roles.stdout, "architect") {
		t.Errorf("atlas cannot see its own role:\n%s", roles.stdout)
	}
	if strings.Contains(roles.stdout, "auditor") {
		t.Errorf("atlas can see a role only ember holds:\n%s", roles.stdout)
	}

	// And grants: ember's is not atlas's business.
	r.ok("boss", "grant", "permission", "ember", "lead", "--until", "2h")
	grants := r.ok("atlas", "list", "grants")
	if strings.Contains(grants.stdout, "ember") {
		t.Errorf("atlas can see ember's grant:\n%s", grants.stdout)
	}
}

// TestListPermissionsMarksTheUnused: the column that earns this command. A
// permission nothing holds is one somebody made and then took another route, and
// there was no other way to notice one.
func TestListPermissionsMarksTheUnused(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "new", "permission", "orphan", "10", "read(Docs/**)")

	got := r.ok("boss", "list", "permissions")
	line := ""
	for _, l := range strings.Split(got.stdout, "\n") {
		if strings.Contains(l, "orphan") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("the unused permission is not listed at all:\n%s", got.stdout)
	}
	if !strings.Contains(line, "nothing") {
		t.Errorf("the unused permission is not marked as unheld: %q", line)
	}
}

// TestListJSONIsAList: every roster answers --json with an array, so a caller can
// pipe one into anything without a special case per kind.
func TestListJSONIsAList(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "grant", "permission", "ember", "lead", "--until", "2h")

	for _, kind := range []string{"identities", "roles", "permissions", "grants"} {
		got := r.ok("boss", "list", kind, "--json")
		var out []map[string]any
		if err := json.Unmarshal([]byte(got.stdout), &out); err != nil {
			t.Errorf("orc list %s --json is not an array: %v\n%s", kind, err, got.stdout)
			continue
		}
		if len(out) == 0 {
			t.Errorf("orc list %s --json is empty", kind)
		}
		// No credential reaches a roster, the same rule the fleet JSON follows.
		for _, forbidden := range []string{`"key"`, `"secret"`, "ORC_KEY"} {
			if strings.Contains(got.stdout, forbidden) {
				t.Errorf("orc list %s --json mentions %s", kind, forbidden)
			}
		}
	}

	// A grant row says whose it is, which the identity shape does not carry.
	got := r.ok("boss", "list", "grants", "--json")
	if !strings.Contains(got.stdout, `"identity": "ember"`) {
		t.Errorf("a grant row does not name its holder:\n%s", got.stdout)
	}
}

// TestListIsAColourLayer: stripped of escapes, the coloured roster is the plain
// one byte for byte. Tables are where this breaks, because padding a painted
// string lays a column out one way with colour and another way without.
func TestListIsAColourLayer(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "grant", "permission", "ember", "lead", "--until", "2h")

	for _, kind := range []string{"identities", "roles", "permissions", "grants"} {
		plain := r.ok("boss", "list", kind, "--no-color")
		coloured := r.ok("boss", "list", kind, "--color")
		if stripped := strip(coloured.stdout); stripped != plain.stdout {
			t.Errorf("orc list %s differs once colour is stripped:\nplain:\n%s\nstripped:\n%s",
				kind, plain.stdout, stripped)
		}
	}
}

// TestTheToolkitIsVisibleAndItsAbsencesAreNamed. A fleet made before a toolkit
// permission existed simply does not have it — `orc bootstrap` installs it and is
// safe to run again — but until something says so the only symptom is a list missing
// rows nobody knew to expect. That is not a failure anybody diagnoses.
func TestListPermissionsNamesTheMissingToolkit(t *testing.T) {
	r := newRig(t)
	r.bootstrap("boss")

	// A bootstrapped fleet has the whole toolkit, and says which rows are its.
	got := r.ok("boss", "list", "permissions")
	if !strings.Contains(got.stdout, "toolkit") {
		t.Errorf("the toolkit rows are not marked:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "does not have") {
		t.Errorf("a fresh fleet reported a missing toolkit:\n%s", got.stdout)
	}

	// Remove one, as a fleet that predates it would never have had.
	r.ok("boss", "remove", "permission", "instruct")

	got = r.ok("boss", "list", "permissions")
	if !strings.Contains(got.stdout, "instruct") {
		t.Errorf("the missing permission is not named:\n%s", got.stdout)
	}
	if !strings.Contains(squeezed(got.stdout), "orc bootstrap") {
		t.Errorf("it does not say how to get it back:\n%s", got.stdout)
	}

	// A permission the fleet invented is not claimed by the toolkit.
	r.ok("boss", "new", "permission", "mine", "40", "read(**)")
	if got := r.ok("boss", "list", "permissions"); !strings.Contains(got.stdout, "yours") {
		t.Errorf("a fleet's own permission is not distinguished:\n%s", got.stdout)
	}
}

// The same two facts through the JSON, because that is what cq mirrors — and a
// browser that had to keep its own copy of the toolkit would be one that goes stale
// silently.
func TestFleetJSONCarriesTheToolkit(t *testing.T) {
	r := newRig(t)
	r.bootstrap("boss")
	r.ok("boss", "remove", "permission", "instruct")

	var fleet struct {
		Toolkit []struct {
			Name     string   `json:"name"`
			Floor    int      `json:"floor"`
			Patterns []string `json:"patterns"`
			Why      string   `json:"why"`
			Have     bool     `json:"have"`
		} `json:"toolkit"`
	}
	if err := json.Unmarshal([]byte(r.ok("boss", "status", "--json").stdout), &fleet); err != nil {
		t.Fatal(err)
	}
	if len(fleet.Toolkit) == 0 {
		t.Fatal("the fleet reported no toolkit at all")
	}

	var missing []string
	for _, e := range fleet.Toolkit {
		if !e.Have {
			missing = append(missing, e.Name)
		}
		// Every entry carries what it would be, so a browser can show a permission
		// the fleet does not have without inventing its definition.
		if e.Name == "" || e.Floor == 0 || len(e.Patterns) == 0 || e.Why == "" {
			t.Errorf("a toolkit entry is not describable: %+v", e)
		}
	}
	if len(missing) != 1 || missing[0] != "instruct" {
		t.Errorf("the absent permission is %v, want [instruct]", missing)
	}
}
