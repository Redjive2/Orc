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

// TestListShowsOnlyYourBranch is the half that matters. A roster is a disclosure,
// and the rule it follows is the one `status` already follows: what is not below
// you is not yours to read.
func TestListShowsOnlyYourBranch(t *testing.T) {
	r := fullFleet(t)
	// quill works for atlas; ember still works for boss.
	r.ok("boss", "move", "quill", "atlas")

	got := r.ok("atlas", "list", "identities")
	for _, want := range []string{"atlas", "quill"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("atlas cannot see %q in its own branch:\n%s", want, got.stdout)
		}
	}
	// Two rows, and neither is ember's. `boss` still appears — as atlas's *boss*
	// column, which is a fact atlas already knows and `orc status` already shows;
	// what must not appear is a row for somebody outside the branch.
	if !strings.Contains(got.stdout, "2 identities") {
		t.Errorf("atlas sees more than its own branch:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "ember") {
		t.Errorf("atlas can see ember, who is not below it:\n%s", got.stdout)
	}

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
