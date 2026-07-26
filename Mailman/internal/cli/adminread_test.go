package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"orc/mailman/internal/cli"
)

// The whole-store view is the one command that shows an account mail it was not
// sent, so most of what is worth testing about it is who is refused.

// TestAStoreWithNoOwnerRefusesTheWholeView is the fail-closed default.
//
// A store written by an older Mailman has no owner record, and the safe reading
// of "nobody is named" is "nobody may", not "anybody may".
func TestAStoreWithNoOwnerRefusesTheWholeView(t *testing.T) {
	r := newRig(t, "boss", "alice")

	got := r.run("boss", "admin", "mail")
	if got.code != cli.CodeDenied {
		t.Fatalf("exit %d, want a denial\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "no owner") {
		t.Errorf("the refusal should say why: %q", got.stderr)
	}
	// And it should say what to do about it, since the reader is the person who
	// can fix it.
	if !strings.Contains(got.stderr, "admin owner") {
		t.Errorf("the refusal should say how to fix it: %q", got.stderr)
	}
}

// TestOnlyTheOwnerMayReadTheStoreWhole is the point of the owner record.
//
// Agents run as the same operating-system user as everything else on the agent
// machine, so file permissions separate nothing. If this check is ever lost,
// every agent can read the operator's mail.
func TestOnlyTheOwnerMayReadTheStoreWhole(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("", "admin", "owner", "boss")

	if got := r.run("alice", "admin", "mail"); got.code != cli.CodeDenied {
		t.Errorf("alice read the whole store: exit %d\n%s", got.code, got.stdout)
	}
	if got := r.run("boss", "admin", "mail"); got.code != cli.CodeOK {
		t.Errorf("the owner was refused: exit %d\n%s", got.code, got.stderr)
	}
}

// TestOwnershipCannotBeTakenOnceHeld: with an owner named, changing it needs
// that owner's key. Otherwise the record would be a lock whose key is running
// the command again.
func TestOwnershipCannotBeTakenOnceHeld(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("", "admin", "owner", "boss")

	if got := r.run("alice", "admin", "owner", "alice"); got.code != cli.CodeDenied {
		t.Fatalf("alice took ownership: exit %d\n%s", got.code, got.stdout)
	}
	if got := r.ok("", "admin", "owner"); !strings.Contains(got.stdout, "boss") {
		t.Errorf("owner = %q, want boss", got.stdout)
	}
}

// The owner may hand it over, which is the one legitimate change.
func TestTheOwnerMayHandOwnershipOver(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("", "admin", "owner", "boss")
	r.ok("boss", "admin", "owner", "alice")

	if got := r.ok("", "admin", "owner"); !strings.Contains(got.stdout, "alice") {
		t.Errorf("owner = %q, want alice", got.stdout)
	}
	// And the former owner is now an ordinary account.
	if got := r.run("boss", "admin", "mail"); got.code != cli.CodeDenied {
		t.Errorf("the former owner still reads everything: exit %d", got.code)
	}
}

// TestTheOwnerMustBeAnAccountThatExists stops a typo from locking the whole
// view behind a name nobody holds a key for.
func TestTheOwnerMustBeAnAccountThatExists(t *testing.T) {
	r := newRig(t, "boss")

	if got := r.run("", "admin", "owner", "nobody"); got.code != cli.CodeNotFound {
		t.Errorf("exit %d, want not found\n%s", got.code, got.stderr)
	}
	if got := r.ok("", "admin", "owner"); !strings.Contains(got.stdout, "no owner") {
		t.Errorf("a failed naming should leave the store unowned: %q", got.stdout)
	}
}

// TestTheOwnerCannotBeRemoved closes a lockout.
//
// The whole-store view needs the owner's key, and so does naming a new owner.
// Remove that account and neither is possible again: the store is owned by a
// name nobody holds a key for, permanently. Refusing is the only outcome that
// leaves a way forward.
func TestTheOwnerCannotBeRemoved(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("", "admin", "owner", "boss")

	got := r.run("", "admin", "user", "remove", "boss")
	if got.code != cli.CodeConflict {
		t.Fatalf("exit %d, want a conflict\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "admin owner") {
		t.Errorf("the refusal should say how to proceed: %q", got.stderr)
	}

	// Still there, and still able to read the store.
	if got := r.run("boss", "admin", "mail"); got.code != cli.CodeOK {
		t.Errorf("the owner was removed anyway: exit %d", got.code)
	}
}

// And once ownership is handed over, the former owner is an ordinary account
// that can be removed like any other.
func TestAFormerOwnerCanBeRemoved(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("", "admin", "owner", "boss")
	r.ok("boss", "admin", "owner", "alice")
	r.ok("", "admin", "user", "remove", "boss")
}

// TestTheWholeViewShowsEveryMailboxAMessageIsIn is what the view is for: not
// "what mail exists" but "who has it, and have they read it".
func TestTheWholeViewShowsEveryMailboxAMessageIsIn(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("", "admin", "owner", "boss")
	r.ok("alice", "send", "a shared subject", "boss", "carol", "the body")
	r.ok("carol", "read", `from="alice"`)

	var whole []struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
		Holders []struct {
			User string `json:"user"`
			Read bool   `json:"read"`
			Mine bool   `json:"mine"`
		} `json:"holders"`
	}
	got := r.ok("boss", "admin", "mail", "--json")
	if err := json.Unmarshal([]byte(got.stdout), &whole); err != nil {
		t.Fatalf("output is not json: %v\n%s", err, got.stdout)
	}

	var found bool
	for _, w := range whole {
		if w.Subject != "a shared subject" {
			continue
		}
		found = true

		read := map[string]bool{}
		sender := ""
		for _, h := range w.Holders {
			read[h.User] = h.Read
			if h.Mine {
				sender = h.User
			}
		}
		// Both recipients and the sender's own copy.
		for _, who := range []string{"boss", "carol", "alice"} {
			if _, ok := read[who]; !ok {
				t.Errorf("%s does not appear as a holder: %+v", who, w.Holders)
			}
		}
		if sender != "alice" {
			t.Errorf("the sender's own copy is not marked: %+v", w.Holders)
		}
		if !read["carol"] {
			t.Error("carol read it, and the view should say so")
		}
		if read["boss"] {
			t.Error("boss has not read it, and the view should say so")
		}
	}
	if !found {
		t.Fatalf("the message is missing from the whole-store view: %s", got.stdout)
	}
}

// TestBodiesAreWithheldWhenAsked: an admin view is metadata, and a caller who
// wants the contents of everyone's mail should have to say so.
func TestBodiesAreWithheldWhenAsked(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("", "admin", "owner", "boss")
	r.ok("alice", "send", "subject", "boss", "a secret body")

	with := r.ok("boss", "admin", "mail", "--json")
	if !strings.Contains(with.stdout, "a secret body") {
		t.Error("bodies should be present by default")
	}
	without := r.ok("boss", "admin", "mail", "--json", "--no-bodies")
	if strings.Contains(without.stdout, "a secret body") {
		t.Errorf("a body survived --no-bodies:\n%s", without.stdout)
	}
	if !strings.Contains(without.stdout, "subject") {
		t.Error("withholding bodies should not withhold the metadata")
	}
}

// TestTheWholeViewTakesNoArguments: it is deliberately not query-driven. A
// query would invite `admin mail from="someone"`, which reads like a filter and
// is really a search of other people's mail.
func TestTheWholeViewTakesNoArguments(t *testing.T) {
	r := newRig(t, "boss")
	r.ok("", "admin", "owner", "boss")

	if got := r.run("boss", "admin", "mail", `from="boss"`); got.code != cli.CodeUsage {
		t.Errorf("exit %d, want a usage error", got.code)
	}
}
