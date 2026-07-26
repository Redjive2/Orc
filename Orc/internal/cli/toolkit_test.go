package cli_test

import (
	"strconv"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/store"
)

// The toolkit: the permissions every fleet is made with.
//
// They are ordinary permissions and the derivation does not know they exist, so
// what is worth testing is not that they work — that is the derivation's own
// suite — but that the *set* is coherent: every one is reachable, the floors mean
// what they say, and the one capability that is a marker cannot be reached by
// holding something broader.

// TestBootstrapMakesTheToolkit: a fresh fleet has a vocabulary on day one.
func TestBootstrapMakesTheToolkit(t *testing.T) {
	r := newRig(t)
	r.bootstrap("boss")

	want, err := store.Toolkit()
	if err != nil {
		t.Fatalf("the toolkit table does not parse: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("the toolkit is empty")
	}

	got := r.ok("boss", "list", "permissions")
	for _, p := range want {
		if !strings.Contains(got.stdout, p.Name.String()) {
			t.Errorf("a fresh fleet has no %s:\n%s", p.Name, got.stdout)
		}
	}
}

// TestTheToolkitIsOrdinary. Nothing about these is a special case: they can be
// assigned, granted, and removed like anything else. A permission the fleet
// treats specially is one nobody can reason about.
func TestTheToolkitIsOrdinary(t *testing.T) {
	r := fullFleet(t)

	r.ok("boss", "new", "role", "writer", "40", "writes", "the", "docs")
	r.ok("boss", "assign", "permission", "writer", "write-docs")
	r.ok("boss", "assign", "role", "ember", "writer")

	// It does what it says.
	got := r.ok("ember", "introspect", "--only", "permissions")
	for _, want := range []string{"read(Docs/**)", "write(Docs/**)"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("write-docs did not give %s:\n%s", want, got.stdout)
		}
	}
	// And nothing more. A battery that quietly widened would be worse than none.
	if strings.Contains(got.stdout, "write(**)") {
		t.Errorf("write-docs gave write(**):\n%s", got.stdout)
	}

	// Removable, so a fleet that wants its own vocabulary is not stuck with this.
	r.ok("boss", "remove", "permission", "write-docs", "--from", "writer")
	if got := r.run("boss", "remove", "permission", "read-docs"); got.code != fault.CodeOK {
		t.Errorf("a builtin nobody holds could not be removed: %s", got.stderr)
	}
}

// TestTheFloorsAreThePolicy: a floor says who may hold a thing at all, and that
// is the whole of what these encode.
func TestTheFloorsAreThePolicy(t *testing.T) {
	r := fullFleet(t)

	for _, tc := range []struct {
		permission string
		below      int // an authority that must be refused
		at         int // one that must be allowed
	}{
		{"write-all", store.FloorWriteAll - 10, store.FloorWriteAll},
		{"orc-agents", store.FloorAgents - 10, store.FloorAgents},
		{"orc-policy", store.FloorPolicy - 10, store.FloorPolicy},
		{"upgrade", store.FloorUpgrade - 10, store.FloorUpgrade},
	} {
		t.Run(tc.permission, func(t *testing.T) {
			low := tc.permission + "-low"
			high := tc.permission + "-high"
			r.ok("boss", "new", "role", low, strconv.Itoa(tc.below), "below", "the", "floor")
			r.ok("boss", "new", "role", high, strconv.Itoa(tc.at), "at", "the", "floor")

			if got := r.run("boss", "assign", "permission", low, tc.permission); got.code == fault.CodeOK {
				t.Errorf("a role at %d was given %s, whose floor is higher", tc.below, tc.permission)
			}
			if got := r.run("boss", "assign", "permission", high, tc.permission); got.code != fault.CodeOK {
				t.Errorf("a role at %d could not hold %s: %s", tc.at, tc.permission, got.stderr)
			}
		})
	}
}

// TestAMarkerIsNotReachableByBreadth is the property `tool(...)` exists for.
//
// Containment is by clause — an identity that may write everything may hand on a
// permission to write one directory — and that is right for paths and wrong for a
// capability whose meaning is "may run this privileged action". With a path clause
// `upgrade` would be reachable by anybody holding `write-all`, whose floor is 20
// lower, and the floor would mean nothing.
func TestAMarkerIsNotReachableByBreadth(t *testing.T) {
	r := fullFleet(t)

	r.ok("boss", "new", "role", "broad", strconv.Itoa(store.FloorWriteAll+5), "writes", "everything")
	r.ok("boss", "assign", "permission", "broad", "write-all")
	r.ok("boss", "assign", "role", "atlas", "broad")

	// It really does hold the broadest write there is.
	if got := r.ok("atlas", "introspect", "--only", "permissions"); !strings.Contains(got.stdout, "write(**)") {
		t.Fatalf("the setup is wrong; atlas cannot write everything:\n%s", got.stdout)
	}
	// And still not this.
	if got := r.run("atlas", "check-permission", "upgrade"); got.code != fault.CodeDenied {
		t.Errorf("write(**) conferred upgrade: exit %d — the floor of %d means nothing",
			got.code, store.FloorUpgrade)
	}

	// Given explicitly, to a role senior enough, it works.
	r.ok("boss", "new", "role", "exec", strconv.Itoa(store.FloorUpgrade+5), "runs", "the", "machines")
	r.ok("boss", "assign", "permission", "exec", "upgrade")
	r.ok("boss", "assign", "role", "quill", "exec")
	if got := r.run("quill", "check-permission", "upgrade"); got.code != fault.CodeOK {
		t.Errorf("an executive agent given upgrade cannot use it: %s", got.stderr)
	}
}

// TestOrcReadNarrows: the orc-kind batteries confine rather than enable, which is
// the one thing about them somebody handing one out has to understand.
func TestOrcReadNarrows(t *testing.T) {
	r := fullFleet(t)

	r.ok("boss", "new", "role", "watcher", "30", "looks", "but", "does", "not", "touch")
	r.ok("boss", "assign", "permission", "watcher", "orc-read")
	r.ok("boss", "assign", "permission", "watcher", "read-all")
	r.ok("boss", "assign", "role", "ember", "watcher")

	// It may look.
	if got := r.run("ember", "list", "identities"); got.code != fault.CodeOK {
		t.Errorf("orc-read does not allow looking: %s", got.stderr)
	}
	if got := r.run("ember", "status"); got.code != fault.CodeOK {
		t.Errorf("orc-read does not allow status: %s", got.stderr)
	}
	// And not touch. Every verb here is one it would have had by structure alone,
	// which is what "narrows" means: the permission takes them away rather than
	// giving anything.
	for _, verb := range [][]string{
		{"new", "identity", "sneaky"},
		{"employ", "ember"},
		{"poke", "ember"},
		{"grant", "permission", "ember", "read-all"},
	} {
		got := r.run("ember", verb...)
		if got.code == fault.CodeOK {
			t.Errorf("orc-read allowed `orc %s`", strings.Join(verb, " "))
		}
	}
}

// TestEveryOrcClauseIsAVerbThatIsChecked.
//
// Only the verbs that change something consult the orc-clause gate. A toolkit
// permission naming an unchecked one would read like a control and be nothing —
// the worst kind of entry in a list people trust.
//
// The list is written out because a Go switch is not introspectable; it is the
// same list as the `mayRunVerb` calls across this package.
func TestEveryOrcClauseIsAVerbThatIsChecked(t *testing.T) {
	checked := map[string]bool{
		"new": true, "assign": true, "remove": true, "grant": true, "revoke": true,
		"move": true, "employ": true, "fire": true, "attach": true, "poke": true,
		"refresh": true,
		// `orc-read`'s clause is the exception, and deliberate: its effect is the
		// narrowing, not the allowance. See store/builtin.go.
		"introspect": true,
	}

	got, err := store.Toolkit()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		for _, clause := range p.Patterns {
			if clause.Kind().String() != "orc" {
				continue
			}
			if !checked[clause.Arg()] {
				t.Errorf("%s names orc(%s), which no command checks — it controls nothing",
					p.Name, clause.Arg())
			}
		}
	}
}

// TestTheToolkitTableIsWellFormed. It is data, and data with a typo in it is a
// fleet that cannot be bootstrapped — so it is checked without needing a store.
func TestTheToolkitTableIsWellFormed(t *testing.T) {
	got, err := store.Toolkit()
	if err != nil {
		t.Fatalf("the toolkit does not parse: %v", err)
	}

	seen := map[string]bool{}
	for _, p := range got {
		if seen[p.Name.String()] {
			t.Errorf("%s appears twice", p.Name)
		}
		seen[p.Name.String()] = true

		if len(p.Patterns) == 0 {
			t.Errorf("%s has no clauses, so it grants nothing", p.Name)
		}
		if strings.TrimSpace(p.Why) == "" {
			t.Errorf("%s does not say what it is for, and the help screen prints that", p.Name)
		}
		if n := p.Floor.Int(); n < 1 || n > 100 {
			t.Errorf("%s has a floor of %d, which is off the scale", p.Name, n)
		}
	}
}
