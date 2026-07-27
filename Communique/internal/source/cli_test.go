package source_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"orc/common/nudge"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/source"
)

// fakeRun answers each command with canned output, and records what it was
// asked. It stands in for Mailman and Macmuffin until their --json modes land.
type fakeRun struct {
	out   map[string]string
	err   map[string]error
	calls [][]string
	// before runs just before a call is answered, while whatever the command was
	// given still exists — a temporary file passed to `orc instruct --set` is gone
	// by the time Apply returns, which is the point of it.
	before func(args []string)
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.before != nil {
		f.before(append([]string{name}, args...))
	}
	if err, ok := f.err[key]; ok {
		return nil, err
	}
	return []byte(f.out[key]), nil
}

// The canned output is the real shape Mailman and Macmuffin emit, captured
// from the tools themselves rather than invented here — the point of the
// adapter is that it reads what they actually print.
func newFakeRun() *fakeRun {
	return &fakeRun{
		out: map[string]string{
			"mailman inbox --all --json": `[
				{"puid":0,"id":"000657671b272088-e1a36f21","sent":"2026-07-25T03:30:09.061Z",
				 "from":"bob","to":["redjive"],"subject":"the parser",
				 "convo":{"id":"000657671b272088-e1a36f21","title":"the parser","index":1},
				 "read":false,"archived":false,"mine":false,"filed":true,
				 "body":"It needs a rewrite."}]`,
			// `orc workspace <identity>` is the read the workspace guard makes
			// before it moves anything: it says where the identity works now, and
			// the queued action's `from` is compared against it.
			"orc workspace atlas": "atlas works in /old/workspace\n",
			"mailman archive --json": `[
				{"puid":1,"id":"000657671b2795b8-a141b6a5","sent":"2026-07-25T03:30:09.091Z",
				 "from":"bob","to":["redjive"],"subject":"old news",
				 "read":false,"archived":true,"mine":false,"filed":true,
				 "body":"Archived soon."}]`,
			"mailman inbox --sent --json": `[
				{"puid":2,"id":"000657671b27fb48-885b63a4","sent":"2026-07-25T03:30:09.117Z",
				 "from":"redjive","to":["bob"],"subject":"my own note",
				 "read":true,"archived":false,"mine":true,"filed":true,"body":"Sent by me."},
				{"puid":3,"id":"000657671be7b550-6d352dc6","sent":"2026-07-25T03:30:21.682Z",
				 "from":"redjive","to":["bob"],"subject":"RE: the parser",
				 "convo":{"id":"000657671b272088-e1a36f21","title":"the parser","index":2},
				 "read":true,"archived":false,"mine":true,"filed":true,
				 "body":"Agreed, let us rewrite it."}]`,
			"mailman admin mail --json --no-bodies": `[
				{"id":"000657671b272088-e1a36f21","sent":"2026-07-25T03:30:09.061Z",
				 "from":"bob","to":["redjive"],"subject":"the parser",
				 "holders":[{"user":"redjive","puid":0,"read":false,"archived":false,"mine":false}]}]`,
			"mailman convo 000657671b272088-e1a36f21 --all --json": `{
				"id":"000657671b272088-e1a36f21","title":"the parser",
				"members":["bob","redjive","carol"],"count":5,"messages":[]}`,
			"mailman admin user list --json": `[{"name":"bob"},{"name":"redjive"}]`,
			// The whole-store view, as `mailman admin mail --json` prints it.
			"mailman admin mail --json": `[
				{"id":"000657671b272088-e1a36f21","sent":"2026-07-25T03:30:09.061Z",
				 "from":"bob","to":["redjive"],"subject":"the parser",
				 "convo":{"id":"000657671b272088-e1a36f21","title":"the parser","index":1},
				 "holders":[
				   {"user":"bob","puid":4,"read":true,"archived":false,"mine":true},
				   {"user":"redjive","puid":0,"read":false,"archived":false,"mine":false}],
				 "body":"It needs a rewrite."},
				{"id":"000657671be7b550-6d352dc6","sent":"2026-07-25T03:30:21.682Z",
				 "from":"redjive","to":["bob","carol"],"subject":"RE: the parser",
				 "convo":{"id":"000657671b272088-e1a36f21","title":"the parser","index":2},
				 "holders":[
				   {"user":"bob","puid":5,"read":true,"archived":false,"mine":false},
				   {"user":"carol","puid":1,"read":false,"archived":false,"mine":false},
				   {"user":"redjive","puid":3,"read":true,"archived":false,"mine":true}],
				 "receipts":[{"user":"bob","at":"2026-07-25T03:31:00Z"}],
				 "body":"Agreed, let us rewrite it."}]`,
			"muff pool --all --json": `[
				{"name":"fix-the-parser","author":"redjive","created":"2026-07-25T03:30:41.451Z",
				 "owner":"redjive","priority":4,"difficulty":3,"status":3,"status_word":"nominal",
				 "done":0,"total":0,"draft":true,"completed":false}]`,
		},
		err: map[string]error{},
	}
}

func newCLI(f *fakeRun) *source.CLI {
	c := source.NewCLI("redjive")
	c.Run = f.run
	// Hermetic by default: the adapter checks the ambient identity, and a test
	// must not pass or fail on whatever ORC_USER the developer has exported.
	c.Look = func(string) (string, bool) { return "redjive", true }
	return c
}

func TestSnapshotCollectsEverything(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if snap.Machine != "studio" || snap.User != "redjive" {
		t.Errorf("snapshot = %+v", snap)
	}
	if len(snap.Inbox) != 1 || snap.Inbox[0].Subject != "the parser" {
		t.Errorf("inbox = %+v", snap.Inbox)
	}
	// Mailman calls it `id`; cq calls it `mid`. The names differ, so a mapping
	// that quietly dropped it would leave every message unaddressable.
	if snap.Inbox[0].MID == "" || snap.Inbox[0].Convo.UID == "" {
		t.Errorf("the message id did not survive the mapping: %+v", snap.Inbox[0])
	}
	if len(snap.Archive) != 1 || snap.Archive[0].Subject != "old news" {
		t.Errorf("archive = %+v", snap.Archive)
	}
	if len(snap.Sent) != 2 {
		t.Errorf("sent = %+v", snap.Sent)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].Owner != "redjive" {
		t.Errorf("tasks = %+v", snap.Tasks)
	}

	// Mailman's own answer, not cq's arithmetic: the thread has five messages
	// and three members, of which cq holds two and can see two.
	if len(snap.Convos) != 1 {
		t.Fatalf("convos = %+v", snap.Convos)
	}
	if c := snap.Convos[0]; c.Title != "the parser" || c.Count != 5 ||
		!slices.Equal(c.Members, []string{"bob", "carol", "redjive"}) {
		t.Errorf("convo = %+v", c)
	}
	if snap.Admin != nil {
		t.Errorf("the admin block should be absent unless asked for")
	}
	if snap.TakenAt.IsZero() {
		t.Errorf("the snapshot carries no capture time")
	}
}

// TestArchivedMailAppearsOnce checks the two lists do not both carry it, which
// would show every archived message twice.
func TestArchivedMailAppearsOnce(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snap.Inbox {
		if m.Archived {
			t.Errorf("archived message %d is in the inbox", m.PUID)
		}
	}
	for _, m := range snap.Archive {
		if !m.Archived {
			t.Errorf("unarchived message %d is in the archive", m.PUID)
		}
	}
}

// TestTheThreeListsStaySeparate: Mailman keeps sent, filed, and received mail
// apart, and so does the mirror. A message in two of cq's lists would show up
// twice on the site.
func TestTheThreeListsStaySeparate(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, list := range []struct {
		name string
		msgs []protocol.Message
	}{{"inbox", snap.Inbox}, {"archive", snap.Archive}, {"sent", snap.Sent}} {
		for _, m := range list.msgs {
			if where, dup := seen[m.MID]; dup {
				t.Errorf("%s is in both %s and %s", m.MID, where, list.name)
			}
			seen[m.MID] = list.name
		}
	}
	for _, m := range snap.Inbox {
		if m.Archived {
			t.Errorf("archived message %d is in the inbox", m.PUID)
		}
	}
}

// TestAThreadIsAskedAboutRatherThanInferred: a conversation cq holds two
// messages of may have five, and counting its own view would be cq mistaking
// what it has for what exists.
func TestAThreadIsAskedAboutRatherThanInferred(t *testing.T) {
	f := newFakeRun()
	if _, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"}); err != nil {
		t.Fatal(err)
	}
	asked := false
	for _, call := range f.calls {
		if len(call) > 2 && call[1] == "convo" && call[2] == "000657671b272088-e1a36f21" {
			asked = true
		}
	}
	if !asked {
		t.Errorf("cq never asked about the conversation: %v", f.calls)
	}
}

// TestAnUnaskableThreadFallsBackRatherThanVanishing: the title is what the
// mailbox shows beside every message in the thread, so losing the conversation
// is worse than undercounting it.
func TestAnUnaskableThreadFallsBackRatherThanVanishing(t *testing.T) {
	f := newFakeRun()
	f.err["mailman convo 000657671b272088-e1a36f21 --all --json"] = errors.New("no such conversation")

	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatalf("one unanswerable conversation should not fail the sync: %v", err)
	}
	if len(snap.Convos) != 1 {
		t.Fatalf("convos = %+v", snap.Convos)
	}
	// Derived from the two messages cq holds, which is all it can say.
	if c := snap.Convos[0]; c.Title != "the parser" || c.Count != 2 {
		t.Errorf("convo = %+v", c)
	}
}

// TestSentMailIsMirroredWithoutTheAdminPanel: it is the user's own mail, not an
// administrative curiosity, and half a conversation is not a conversation.
func TestSentMailIsMirroredWithoutTheAdminPanel(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Admin != nil {
		t.Fatal("the admin block should be absent")
	}
	if len(snap.Sent) != 2 {
		t.Errorf("sent = %+v", snap.Sent)
	}
	if len(snap.Convos) != 1 {
		t.Errorf("convos = %+v", snap.Convos)
	}
}

// TestUnknownFieldsAreTolerated: the far side of this boundary has its own
// release cycle, and a field Mailman adds must not break the mirror.
func TestUnknownFieldsAreTolerated(t *testing.T) {
	f := newFakeRun()
	f.out["mailman inbox --all --json"] = `[{"puid":1,"id":"m","sent":"2026-07-24T18:31:04Z",
		"from":"boss","to":["redjive"],"subject":"s","read":false,"archived":false,
		"invented_by_a_newer_mailman":true}]`

	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatalf("a field cq does not know broke the mirror: %v", err)
	}
	if len(snap.Inbox) != 1 {
		t.Errorf("inbox = %+v", snap.Inbox)
	}
}

func TestAdminBodiesAreWithheldWhenAsked(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio", Admin: true})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Admin == nil {
		t.Fatal("the admin block is missing")
	}
	if !snap.Admin.MetadataOnly {
		t.Errorf("the block should be marked metadata-only")
	}
	for _, m := range snap.Admin.Messages {
		if m.Body != "" {
			t.Errorf("a body survived the withholding: %q", m.Body)
		}
	}
	if len(snap.Admin.Messages) == 0 {
		t.Error("withholding bodies should not withhold the messages")
	}
}

// TestAdminBodiesAreStrippedEvenIfTheToolIgnoresTheFlag: the guarantee is cq's.
// An operator who has not enabled bodies must not see one because a flag was
// spelled differently upstream.
func TestAdminBodiesAreStrippedEvenIfTheToolIgnoresTheFlag(t *testing.T) {
	f := newFakeRun()
	f.out["mailman admin mail --json --no-bodies"] = f.out["mailman admin mail --json"]

	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio", Admin: true})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, m := range snap.Admin.Messages {
		if m.Body != "" {
			t.Errorf("a body reached the snapshot despite --no-bodies: %q", m.Body)
		}
	}
}

func TestAdminBodiesAreIncludedWhenAllowed(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(),
		source.Options{Machine: "studio", Admin: true, AdminBodies: true})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Admin.MetadataOnly {
		t.Errorf("the block should not be marked metadata-only")
	}
	if len(snap.Admin.Messages) != 2 {
		t.Fatalf("messages = %+v", snap.Admin.Messages)
	}
	for _, m := range snap.Admin.Messages {
		if m.Body == "" {
			t.Errorf("a message lost its body: %+v", m)
		}
	}
}

// TestThePanelSeesMailTheOperatorNeverGot is why this is one Mailman command
// rather than cq assembling the panel from its own mailbox.
//
// carol holds a message the mirrored account was never sent. The old panel,
// built from what redjive could see plus a `check` per sent message, could not
// have known she had it.
func TestThePanelSeesMailTheOperatorNeverGot(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(),
		source.Options{Machine: "studio", Admin: true, AdminBodies: true})
	if err != nil {
		t.Fatal(err)
	}
	var carol bool
	for _, r := range snap.Admin.Receipts {
		if r.Recipient == "carol" {
			carol = true
		}
	}
	if !carol {
		t.Errorf("carol holds mail the panel does not report: %+v", snap.Admin.Receipts)
	}
}

// TestAHolderWhoHasNotReadItIsStillAnAnswer: the panel asks "who has this and
// have they seen it", so an unread holder is a row, not an absence.
func TestAHolderWhoHasNotReadItIsStillAnAnswer(t *testing.T) {
	f := newFakeRun()
	snap, err := newCLI(f).Snapshot(t.Context(),
		source.Options{Machine: "studio", Admin: true, AdminBodies: true})
	if err != nil {
		t.Fatal(err)
	}

	byWho := map[string]protocol.Receipt{}
	for _, r := range snap.Admin.Receipts {
		if r.MID == "000657671be7b550-6d352dc6" {
			byWho[r.Recipient] = r
		}
	}
	if got, ok := byWho["bob"]; !ok || !got.Read || got.At == nil {
		t.Errorf("bob read it and the panel should carry when: %+v", byWho["bob"])
	}
	if got, ok := byWho["carol"]; !ok || got.Read || got.At != nil {
		t.Errorf("carol has not read it, and must not carry a read time: %+v", byWho["carol"])
	}
	// The sender's own copy is not a delivery, so it is not a receipt.
	if _, ok := byWho["redjive"]; ok {
		t.Error("the sender's own copy was reported as a delivery")
	}
}

// TestMirroringRefusesAnotherAccountsMail is the guard against the worst thing
// this adapter could do.
//
// Mailman answers as whoever ORC_USER names; cq only labels the snapshot with
// the account it was told to mirror. Nothing connects the two, so an environment
// where they disagree produced a snapshot that said "redjive" and held somebody
// else's mail — which the server then served as the operator's own inbox.
//
// It is reachable in the ordinary setup: a nudge inherits the environment of
// whichever tool changed something, and on the agent machine that is an agent
// running under its own name.
func TestMirroringRefusesAnotherAccountsMail(t *testing.T) {
	f := newFakeRun()
	c := newCLI(f)
	c.User = "redjive"
	c.Look = func(k string) (string, bool) {
		if k == source.OrcUser {
			return "bob", true
		}
		return "", false
	}

	_, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	for _, want := range []string{"bob", "redjive", source.OrcUser} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should name %q: %v", want, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("it should refuse before running anything: %v", f.calls)
	}
}

func TestMirroringAcceptsTheAccountItMirrors(t *testing.T) {
	for _, who := range []string{"redjive", "REDJIVE", " redjive "} {
		f := newFakeRun()
		c := newCLI(f)
		c.User = "redjive"
		c.Look = func(string) (string, bool) { return who, true }

		if _, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"}); err != nil {
			t.Errorf("%s=%q should be accepted: %v", source.OrcUser, who, err)
		}
	}
}

// An unset identity is left to Mailman, whose message about missing credentials
// is the better one.
func TestAnUnsetIdentityIsLeftToMailman(t *testing.T) {
	for _, v := range []string{"", "unset"} {
		f := newFakeRun()
		c := newCLI(f)
		c.User = "redjive"
		c.Look = func(string) (string, bool) {
			return "", v != "unset" // "" present, or absent entirely
		}
		if _, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"}); err != nil {
			t.Errorf("%s=%q should not be refused here: %v", source.OrcUser, v, err)
		}
	}
}

// TestItsOwnCredentialOverridesTheAmbientOne is the other half of the fix.
//
// With a credential of its own, cq mirrors the account it was configured for no
// matter who triggered the sync — which is what lets an agent's `mailman send`
// bring the operator's new mail to the website.
func TestItsOwnCredentialOverridesTheAmbientOne(t *testing.T) {
	f := newFakeRun()
	c := newCLI(f)
	c.User = "redjive"
	c.Key = "the-operators-orc-key"
	c.Look = func(string) (string, bool) { return "some-agent", true }

	if _, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"}); err != nil {
		t.Fatalf("its own credential should not be overruled by the environment: %v", err)
	}
}

// TestTheChildIsRunAsTheMirroredAccount checks the environment actually handed
// to Mailman, since the whole guarantee is that cq reads the operator's mailbox
// rather than the caller's.
func TestTheChildIsRunAsTheMirroredAccount(t *testing.T) {
	c := source.NewCLI("redjive")
	c.Key = "the-operators-orc-key"

	got := map[string]string{}
	for _, entry := range source.ChildEnv(c) {
		if k, v, ok := strings.Cut(entry, "="); ok {
			// Last value wins for a duplicate key, so this is what the child
			// would resolve, not merely what was appended.
			got[k] = v
		}
	}
	if got[source.OrcUser] != "redjive" || got[source.OrcKey] != "the-operators-orc-key" {
		t.Errorf("child would run as %q, want redjive", got[source.OrcUser])
	}
	if got[nudge.Suppress] == "" {
		t.Error("the child should be told not to nudge in turn")
	}
}

// Without a credential the ambient identity is left alone: it is the only one
// there is, and the mismatch check has already confirmed it is the right one.
func TestWithoutACredentialTheEnvironmentIsLeftAlone(t *testing.T) {
	c := source.NewCLI("redjive")

	for _, entry := range source.ChildEnv(c) {
		if strings.HasPrefix(entry, source.OrcKey+"=") {
			t.Errorf("cq invented a credential it does not have: %q", entry)
		}
	}
}

// TestARefusedPanelDoesNotFailTheSync is the state every machine is in until
// somebody runs `mailman admin owner`.
//
// Mailman refuses the whole-store view to anyone but the store's owner. cq asks
// for it by default, so before that command is run the answer is always no —
// and failing the whole mirror over an extra would break mirroring for exactly
// the setups that never opted into the extra.
func TestARefusedPanelDoesNotFailTheSync(t *testing.T) {
	f := newFakeRun()
	f.err["mailman admin mail --json"] = &exec.ExitError{
		ProcessState: exitedWith(t, source.ExitDenied),
	}

	var warned []string
	c := newCLI(f)
	c.Warn = func(format string, args ...any) {
		warned = append(warned, fmt.Sprintf(format, args...))
	}

	snap, err := c.Snapshot(t.Context(), source.Options{Machine: "studio", Admin: true, AdminBodies: true})
	if err != nil {
		t.Fatalf("a refused panel should not fail the sync: %v", err)
	}
	if snap.Admin != nil {
		t.Errorf("the panel should be absent, not partial: %+v", snap.Admin)
	}
	// The mailbox — the thing cq is actually for — is still mirrored.
	if len(snap.Inbox) != 1 {
		t.Errorf("the mailbox was lost with the panel: %+v", snap.Inbox)
	}

	// And the operator is told why, since an empty panel with no explanation is
	// someone wondering whether cq is broken.
	if len(warned) != 1 || !strings.Contains(warned[0], "admin owner") {
		t.Errorf("warnings = %v, want one naming the fix", warned)
	}
}

// A panel that fails for any other reason is a real failure and still stops the
// sync: cq cannot tell a broken Mailman from a half-read one.
func TestAPanelThatBreaksStillFailsTheSync(t *testing.T) {
	f := newFakeRun()
	f.err["mailman admin mail --json"] = errors.New("the store is on fire")

	_, err := newCLI(f).Snapshot(t.Context(),
		source.Options{Machine: "studio", Admin: true, AdminBodies: true})
	if err == nil {
		t.Fatal("a broken panel should fail the sync")
	}
}

func TestSnapshotRefusesPartialResults(t *testing.T) {
	for _, failing := range []string{
		"mailman inbox --all --json",
		"muff pool --all --json",
	} {
		t.Run(failing, func(t *testing.T) {
			f := newFakeRun()
			f.err[failing] = errors.New("tool exploded")
			if _, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"}); err == nil {
				t.Errorf("a partial snapshot was returned")
			}
		})
	}

	for _, failing := range []string{
		"mailman admin user list --json",
	} {
		t.Run(failing, func(t *testing.T) {
			f := newFakeRun()
			f.err[failing] = errors.New("tool exploded")
			_, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio", Admin: true})
			if err == nil {
				t.Errorf("a partial snapshot was returned")
			}
		})
	}
}

func TestSnapshotRefusesUnusableOutput(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"not json", "this is not json"},
		{"two documents", `[] []`},
		{"a message cq cannot represent", `[{"puid":1,"id":"m","sent":"2026-07-24T18:31:04Z","from":"Not A Name","subject":"s"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRun()
			f.out["mailman inbox --all --json"] = tc.out
			_, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
			if !errors.Is(err, fault.ErrParse) {
				t.Errorf("error = %v, want a parse fault", err)
			}
		})
	}
}

func TestEmptyOutputIsNotAnError(t *testing.T) {
	f := newFakeRun()
	for key := range f.out {
		f.out[key] = ""
	}
	snap, err := newCLI(f).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatalf("a machine with nothing to report should still snapshot: %v", err)
	}
	if len(snap.Inbox) != 0 || len(snap.Tasks) != 0 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestSnapshotValidatesItsOptions(t *testing.T) {
	f := newFakeRun()
	if _, err := newCLI(f).Snapshot(t.Context(), source.Options{}); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault for the missing machine", err)
	}

	c := source.NewCLI("")
	c.Run = f.run
	if _, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"}); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("error = %v, want a usage fault for the missing user", err)
	}
}

// TestApplyRunsTheRightCommand pins each operation to exactly one Mailman verb.
func TestApplyRunsTheRightCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   protocol.Op
		args protocol.Args
		want []string
	}{
		{"send", protocol.OpSend,
			protocol.Args{To: []string{"bob", "carol"}, Subject: "hello", Body: "hi"},
			[]string{"mailman", "send", "hello", "bob", "carol", "hi"}},
		{"reply", protocol.OpReply,
			protocol.Args{PUID: 41, Subject: "RE: work", Body: "yes"},
			[]string{"mailman", "reply", `id="41"`, "RE: work", "yes"}},
		{"read", protocol.OpRead, protocol.Args{PUID: 41},
			[]string{"mailman", "read", `id="41"`}},
		{"archive", protocol.OpArchive, protocol.Args{PUID: 41},
			[]string{"mailman", "archive", `id="41"`}},
		{"cc", protocol.OpCC, protocol.Args{ConvoUID: "c-1", User: "carol"},
			[]string{"mailman", "cc", `convo="c-1"`, "carol"}},
		// --yes because there is nobody at this end to ask: the confirmation
		// happened in the browser before the action was queued. Without it Mailman
		// refuses and the queue fills with actions that will never apply.
		{"prune", protocol.OpPrune, protocol.Args{PUID: 41},
			[]string{"mailman", "prune", `id="41"`, "--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRun()
			action := protocol.Action{
				ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
				Op: tc.op, Args: tc.args, Queued: at(),
			}
			if err := newCLI(f).Apply(t.Context(), action); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("ran %d commands, want 1: %v", len(f.calls), f.calls)
			}
			if strings.Join(f.calls[0], "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("ran %v, want %v", f.calls[0], tc.want)
			}
		})
	}
}

// TestApplyPassesArgumentsWithoutAShell: a subject line is data, not a second
// command, however it is punctuated.
func TestApplyPassesArgumentsWithoutAShell(t *testing.T) {
	f := newFakeRun()
	nasty := `hello"; rm -rf / ;#`
	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpSend, Args: protocol.Args{To: []string{"bob"}, Subject: nasty, Body: "hi"},
		Queued: at(),
	}
	if err := newCLI(f).Apply(t.Context(), action); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0][2] != nasty {
		t.Errorf("the subject was not passed intact: %v", f.calls)
	}
}

func TestApplyRefusesAnInvalidAction(t *testing.T) {
	f := newFakeRun()
	err := newCLI(f).Apply(t.Context(), protocol.Action{})
	if !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("an invalid action reached the tool: %v", f.calls)
	}
}

func TestApplyReportsAToolFailure(t *testing.T) {
	f := newFakeRun()
	f.err[`mailman read id="41"`] = errors.New("no such message")
	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpRead, Args: protocol.Args{PUID: 41}, Queued: at(),
	}
	if err := newCLI(f).Apply(t.Context(), action); err == nil {
		t.Errorf("a failing tool was reported as success")
	}
}

func TestDefaultCommandNames(t *testing.T) {
	c := &source.CLI{User: "redjive"}
	f := newFakeRun()
	c.Run = f.run
	if _, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(f.calls) == 0 || f.calls[0][0] != "mailman" {
		t.Errorf("the default mailman command is wrong: %v", f.calls)
	}
	sawMuff := false
	for _, call := range f.calls {
		if call[0] == "muff" {
			sawMuff = true
		}
	}
	if !sawMuff {
		t.Errorf("the default muff command was never used: %v", f.calls)
	}
}

func TestOptionsValidation(t *testing.T) {
	if err := (source.Options{Machine: "studio"}).Validate(); err != nil {
		t.Errorf("a sound option set was rejected: %v", err)
	}
	if err := (source.Options{}).Validate(); err == nil {
		t.Errorf("a missing machine was accepted")
	}
}

func at() time.Time { return time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC) }

// exitedWith produces a real ProcessState carrying one exit code, by running a
// command that exits with it. Go offers no way to construct one directly, and a
// fake would not exercise the same errors.As path the adapter uses.
func exitedWith(t *testing.T, code int) *os.ProcessState {
	t.Helper()
	cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code))
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("could not produce an exit status: %v", err)
	}
	return exit.ProcessState
}
