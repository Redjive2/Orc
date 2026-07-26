package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/mailman/internal/cli"
	"orc/mailman/internal/store"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// rig is a store, a set of provisioned users, and a way to run commands as any
// of them. Every command in the suite goes through cli.Main, so what is tested
// is what a caller actually gets: stdout, stderr, and an exit code together.
type rig struct {
	t    *testing.T
	root string
	keys map[string]string
	now  *clock.Fake
}

type result struct {
	code   int
	stdout string
	stderr string
}

func newRig(t *testing.T, users ...string) *rig {
	t.Helper()
	r := &rig{
		t:    t,
		root: t.TempDir(),
		keys: map[string]string{},
		now:  clock.NewFake(epoch, time.Second),
	}
	for _, name := range users {
		out := r.run("", "admin", "user", "add", name)
		if out.code != cli.CodeOK {
			t.Fatalf("provisioning %s: %d\n%s", name, out.code, out.stderr)
		}
		r.keys[name] = extractKey(t, out.stdout)
	}
	return r
}

func extractKey(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  export ORC_KEY=") {
			return strings.TrimPrefix(line, "  export ORC_KEY=")
		}
	}
	t.Fatalf("no key in:\n%s", out)
	return ""
}

// run executes a command as the named user, or unauthenticated when who is "".
func (r *rig) run(who string, args ...string) result {
	r.t.Helper()
	return r.runWith(who, nil, args...)
}

// runStyled runs a command with extra environment and a chosen terminal
// answer, which is how the colour policy is exercised from a test that writes
// to a buffer and therefore never has a real terminal.
func (r *rig) runStyled(who string, terminal bool, extra map[string]string, args ...string) result {
	r.t.Helper()
	return r.run3(who, nil, terminal, extra, args...)
}

func (r *rig) runWith(who string, stdin []byte, args ...string) result {
	r.t.Helper()
	return r.run3(who, stdin, false, nil, args...)
}

func (r *rig) run3(who string, stdin []byte, terminal bool, extra map[string]string, args ...string) result {
	r.t.Helper()

	env := map[string]string{}
	for k, v := range extra {
		env[k] = v
	}
	if who != "" {
		env["ORC_USER"] = who
		if key, ok := r.keys[who]; ok {
			env["ORC_KEY"] = key
		}
	}

	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  bytes.NewReader(stdin),
		Stdout: &out,
		Stderr: &errOut,
		Env: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
		Home:     r.root + "/home",
		Root:     r.root + "/store",
		Clock:    r.now,
		Width:    90,
		Colour:   true,
		Terminal: terminal,
		// stderr is painted on the same terms as stdout. It matters now that a
		// screen — the short one `mailman` alone prints — goes to stderr.
		ErrTerminal: terminal,
	}, args)

	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// ok runs a command and fails the test if it does not succeed.
func (r *rig) ok(who string, args ...string) result {
	r.t.Helper()
	got := r.run(who, args...)
	if got.code != cli.CodeOK {
		r.t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", args, got.code, got.stdout, got.stderr)
	}
	return got
}

// TestReferenceInvocations runs every example from Docs/Mailman/Reference.md.
// They are the tool's contract, so each one is a test that must reproduce.
func TestReferenceInvocations(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "RE: work", "alice", "the body")
	r.ok("boss", "send", "other", "alice", "another")

	for _, q := range []string{
		`from="boss"`,
		`from="boss" & subject="RE: work"`,
		`id="0"`,
	} {
		t.Run(q, func(t *testing.T) {
			got := r.run("alice", "open", q)
			if got.code != cli.CodeOK {
				t.Fatalf("open %s exited %d\n%s", q, got.code, got.stderr)
			}
			if !strings.Contains(got.stdout, "from") {
				t.Errorf("open %s produced no message card:\n%s", q, got.stdout)
			}
		})
	}

	// `id="0"` must select by puid, so it is the first message alice received.
	first := r.ok("alice", "open", `id="0"`)
	if !strings.Contains(first.stdout, "RE: work") {
		t.Errorf(`open id="0" did not show the first message:\n%s`, first.stdout)
	}
}

func TestExitCodes(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "subject", "alice", "body")

	for _, tc := range []struct {
		name string
		who  string
		args []string
		want int
	}{
		{"success", "alice", []string{"inbox"}, cli.CodeOK},
		{"help without identity", "", []string{"help"}, cli.CodeOK},
		{"no command", "alice", nil, cli.CodeUsage},
		{"unknown command", "alice", []string{"frobnicate"}, cli.CodeUsage},
		{"unknown option", "alice", []string{"inbox", "--sideways"}, cli.CodeUsage},
		{"too many arguments", "alice", []string{"inbox", "extra"}, cli.CodeUsage},
		{"no identity", "", []string{"inbox"}, cli.CodeAuth},
		{"bad query", "alice", []string{"open", "from=boss &"}, cli.CodeParse},
		{"unknown field", "alice", []string{"open", "sender=boss"}, cli.CodeParse},
		{"nothing matches", "alice", []string{"open", `from="nobody"`}, cli.CodeNotFound},
		{"send to a missing user", "alice", []string{"send", "s", "nobody", "b"}, cli.CodeNotFound},
		{"prune without confirmation", "alice", []string{"prune", `from="boss"`}, cli.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.run(tc.who, tc.args...); got.code != tc.want {
				t.Errorf("%v exited %d, want %d\nstderr: %s", tc.args, got.code, tc.want, got.stderr)
			}
		})
	}
}

// TestWrongKeyIsRefusedAndSaysNothingUseful is the security-shaped end-to-end
// check: the visible failure must not distinguish a bad key from a bad user.
func TestWrongKeyIsRefusedAndSaysNothingUseful(t *testing.T) {
	r := newRig(t, "boss", "alice")

	r.keys["alice"] = r.keys["boss"] // alice presenting boss's key
	got := r.run("alice", "inbox")
	if got.code != cli.CodeAuth {
		t.Fatalf("a wrong key exited %d, want %d", got.code, cli.CodeAuth)
	}
	if !strings.Contains(got.stderr, "authentication failed") {
		t.Errorf("stderr = %q", got.stderr)
	}
	for _, leak := range []string{"no such user", "wrong key", "does not match", r.keys["boss"]} {
		if strings.Contains(got.stderr, leak) {
			t.Errorf("stderr discloses %q:\n%s", leak, got.stderr)
		}
	}
}

func TestSendAndReceive(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("boss", "send", "RE: work", "alice", "carol", "Ship it.")

	for _, who := range []string{"alice", "carol"} {
		got := r.ok(who, "inbox")
		if !strings.Contains(got.stdout, "RE: work") {
			t.Errorf("%s did not receive the message:\n%s", who, got.stdout)
		}
		if !strings.Contains(got.stdout, "1 unread") {
			t.Errorf("%s should have one unread message:\n%s", who, got.stdout)
		}
	}

	// The sender's own copy is filed but must not appear in their inbox, or
	// every send would inflate their unread count.
	boss := r.ok("boss", "inbox", "--all")
	if strings.Contains(boss.stdout, "RE: work") {
		t.Errorf("the sender's own mail appeared in their inbox:\n%s", boss.stdout)
	}
	// It is still reachable, which is what makes `check` work.
	if got := r.ok("boss", "check", `subject~work`); !strings.Contains(got.stdout, "alice") {
		t.Errorf("the sender cannot check their own mail:\n%s", got.stdout)
	}
}

func TestBodyFromStdin(t *testing.T) {
	r := newRig(t, "boss", "alice")
	body := "# Heading\n\nA body with\nseveral lines.\n"

	if got := r.runWith("boss", []byte(body), "send", "subject", "alice", "-"); got.code != cli.CodeOK {
		t.Fatalf("send with stdin exited %d: %s", got.code, got.stderr)
	}
	got := r.ok("alice", "open", `from="boss"`)
	if !strings.Contains(got.stdout, body) {
		t.Errorf("the body did not survive:\n%s", got.stdout)
	}
}

func TestReadIsVisibleToEveryRecipient(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("boss", "send", "RE: work", "alice", "carol", "Ship it.")

	before := r.ok("boss", "check", `subject="RE: work"`)
	if !strings.Contains(before.stdout, "0 of 2") {
		t.Errorf("nobody should have read it yet:\n%s", before.stdout)
	}

	r.ok("alice", "read", `unread=true`)

	after := r.ok("boss", "check", `subject="RE: work"`)
	if !strings.Contains(after.stdout, "1 of 2") {
		t.Errorf("alice's read is not visible:\n%s", after.stdout)
	}
	if !strings.Contains(after.stdout, "✓ read") || !strings.Contains(after.stdout, "· unread") {
		t.Errorf("check should show both states:\n%s", after.stdout)
	}
	// Carol sees it too: a read is visible to every recipient, as the
	// reference requires.
	carol := r.ok("carol", "check", `subject="RE: work"`)
	if !strings.Contains(carol.stdout, "1 of 2") {
		t.Errorf("carol cannot see alice's read:\n%s", carol.stdout)
	}
}

func TestReplyRootsAConversation(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "RE: work", "alice", "one")
	r.ok("alice", "reply", `from="boss"`, "RE: work", "two")
	r.ok("boss", "reply", `from="alice"`, "RE: work", "three")

	got := r.ok("boss", "inbox", "--all")
	if !strings.Contains(got.stdout, "#2") {
		t.Errorf("the reply is not threaded:\n%s", got.stdout)
	}

	// A listing abbreviates the conversation identifier to keep the column
	// narrow; the card carries it in full, which is what `convo` needs.
	card := r.ok("boss", "open", `subject~work`)
	convo := findConvo(t, card.stdout)
	thread := r.ok("alice", "convo", convo, "--all")
	for _, want := range []string{"#1", "#2", "#3"} {
		if !strings.Contains(thread.stdout, want) {
			t.Errorf("the thread is missing %s:\n%s", want, thread.stdout)
		}
	}
}

// findConvo digs the conversation identifier out of an open card.
func findConvo(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			if len(field) == 25 && field[16] == '-' {
				return field
			}
		}
	}
	t.Fatalf("no conversation identifier in:\n%s", out)
	return ""
}

func TestCCAddsAUserWithoutBackfilling(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("boss", "send", "RE: work", "alice", "one")
	r.ok("alice", "reply", `from="boss"`, "RE: work", "two")

	r.ok("boss", "cc", `subject~work`, "carol")

	got := r.ok("carol", "inbox")
	if !strings.Contains(got.stdout, "cc: carol") {
		t.Errorf("carol did not get the cc notice:\n%s", got.stdout)
	}
	// Exactly one message: the notice. History is reachable through `convo`,
	// but is not backfilled into the inbox.
	if !strings.Contains(got.stdout, "1 unread") {
		t.Errorf("carol's inbox should hold only the notice:\n%s", got.stdout)
	}

	// The notice is a real message, so `check` works on it, as the reference
	// requires.
	if got := r.ok("boss", "check", `kind=cc`); !strings.Contains(got.stdout, "carol") {
		t.Errorf("check does not work on the cc notice:\n%s", got.stdout)
	}
}

func TestArchiveAndPrune(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "one", "alice", "a")
	r.ok("boss", "send", "two", "alice", "b")

	// Prune refuses mail that is not archived, and says why.
	got := r.run("alice", "prune", `from="boss"`, "--yes")
	if got.code != cli.CodeUsage {
		t.Fatalf("pruning unarchived mail exited %d, want %d", got.code, cli.CodeUsage)
	}
	if !strings.Contains(got.stderr, "archived") {
		t.Errorf("the refusal should explain itself:\n%s", got.stderr)
	}

	r.ok("alice", "archive", `subject="one"`)
	if got := r.ok("alice", "inbox"); strings.Contains(got.stdout, "one") {
		t.Errorf("archived mail is still in the inbox:\n%s", got.stdout)
	}
	if got := r.ok("alice", "archive"); !strings.Contains(got.stdout, "one") {
		t.Errorf("the archive listing is missing it:\n%s", got.stdout)
	}

	// Prune lists what it will delete before asking.
	refused := r.run("alice", "prune", `subject="one"`)
	if refused.code != cli.CodeUsage {
		t.Fatalf("prune without --yes exited %d", refused.code)
	}
	if !strings.Contains(refused.stdout, "one") {
		t.Errorf("prune should list what it would delete:\n%s", refused.stdout)
	}

	r.ok("alice", "prune", `subject="one"`, "--yes")
	if got := r.ok("alice", "archive"); strings.Contains(got.stdout, "one") {
		t.Errorf("the pruned message survived:\n%s", got.stdout)
	}
	// The other message is untouched.
	if got := r.ok("alice", "inbox"); !strings.Contains(got.stdout, "two") {
		t.Errorf("prune took the wrong message:\n%s", got.stdout)
	}
}

// TestPruneRefusesAQueryThatReachesLiveMail is the guard on the only
// irreversible command: a loose query must not quietly delete the archived
// subset of what it matched.
func TestPruneRefusesAQueryThatReachesLiveMail(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "one", "alice", "a")
	r.ok("boss", "send", "two", "alice", "b")
	r.ok("alice", "archive", `subject="one"`)

	got := r.run("alice", "prune", `from="boss"`, "--yes")
	if got.code != cli.CodeUsage {
		t.Fatalf("prune exited %d, want a refusal", got.code)
	}
	// And nothing was deleted.
	if after := r.ok("alice", "archive"); !strings.Contains(after.stdout, "one") {
		t.Errorf("the refused prune deleted something anyway:\n%s", after.stdout)
	}
}

func TestOpenNarrowsButSaysSo(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "one", "alice", "a")
	r.ok("boss", "send", "two", "alice", "b")

	got := r.ok("alice", "open", `from="boss"`)
	if !strings.Contains(got.stderr, "2 messages matched") {
		t.Errorf("open narrowed silently; stderr was %q", got.stderr)
	}
	// It shows the most recent, as the reference says.
	if !strings.Contains(got.stdout, "two") {
		t.Errorf("open did not show the most recent:\n%s", got.stdout)
	}
	// The note goes to stderr, so stdout stays pipeable.
	if strings.Contains(got.stdout, "matched") {
		t.Errorf("the note leaked into stdout:\n%s", got.stdout)
	}
}

func TestVerifyReportsAHealthyStore(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "one", "alice", "a")
	r.ok("alice", "read", "unread=true")

	got := r.ok("", "verify")
	if !strings.Contains(got.stdout, "no problems found") {
		t.Errorf("a healthy store reported problems:\n%s\n%s", got.stdout, got.stderr)
	}
}

func TestOutputIsUncolouredWhenNotATerminal(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "s", "alice", "b")

	for _, args := range [][]string{
		{"inbox"}, {"open", `from="boss"`}, {"check", `from="boss"`}, {"archive"},
	} {
		got := r.ok("alice", args...)
		if strings.Contains(got.stdout, "\x1b[") {
			t.Errorf("%v emitted escape sequences to a buffer:\n%q", args, got.stdout)
		}
	}
}

// TestMarksSurviveGrep is the practical form of "colour is never information":
// the unread marker is a character, so a pipe still finds it.
func TestMarksSurviveGrep(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "s", "alice", "b")

	got := r.ok("alice", "inbox")
	found := false
	for _, line := range strings.Split(got.stdout, "\n") {
		if strings.Contains(line, "│ * │") {
			found = true
		}
	}
	if !found {
		t.Errorf("no unread marker in:\n%s", got.stdout)
	}
}

func TestAdminUserLifecycle(t *testing.T) {
	r := newRig(t)

	added := r.ok("", "admin", "user", "add", "dave")
	if !strings.Contains(added.stdout, "shown once") {
		t.Errorf("the key should be flagged as unrecoverable:\n%s", added.stdout)
	}

	// Adding twice must not silently revoke the first key.
	again := r.run("", "admin", "user", "add", "dave")
	if again.code != cli.CodeConflict {
		t.Errorf("re-adding a user exited %d, want %d", again.code, cli.CodeConflict)
	}

	if got := r.ok("", "admin", "user", "list"); !strings.Contains(got.stdout, "dave") {
		t.Errorf("the list is missing dave:\n%s", got.stdout)
	}
	r.ok("", "admin", "user", "remove", "dave")
	if got := r.ok("", "admin", "user", "list"); strings.Contains(got.stdout, "dave") {
		t.Errorf("dave survived removal:\n%s", got.stdout)
	}
	if got := r.run("", "admin", "user", "remove", "dave"); got.code != cli.CodeNotFound {
		t.Errorf("removing a missing user exited %d, want %d", got.code, cli.CodeNotFound)
	}
}

// TestAdminUserAddWithSuppliedKey covers the door Orc provisions through: the
// caller chooses the key, Mailman stores it, and the mailbox authenticates with
// it afterwards. The key must not be echoed — Orc already has it, and this
// command runs non-interactively where an echoed credential is a logged one.
func TestAdminUserAddWithSuppliedKey(t *testing.T) {
	r := newRig(t)

	// Long enough to clear user.MinKeyLen, which is what makes the digest
	// choice in orc/common/user defensible.
	const key = "0123456789abcdef0123456789abcdefXYZ"

	added := r.runWith("", []byte(key+"\n"), "admin", "user", "add", "erin", "--key", "-")
	if added.code != cli.CodeOK {
		t.Fatalf("add with a supplied key exited %d\n%s", added.code, added.stderr)
	}
	if strings.Contains(added.stdout, key) {
		t.Errorf("a caller-supplied key must never be echoed:\n%s", added.stdout)
	}

	// The real assertion: the key works. A stored digest that the offered key
	// does not open would make every session Orc starts unauthenticated.
	r.keys["erin"] = key
	if got := r.run("erin", "inbox"); got.code != cli.CodeOK {
		t.Errorf("erin could not authenticate with the supplied key: %d\n%s", got.code, got.stderr)
	}

	// A key too short to be a credential is refused rather than stretched.
	short := r.runWith("", []byte("hunter2\n"), "admin", "user", "add", "frank", "--key", "-")
	if short.code != cli.CodeUsage {
		t.Errorf("a short key exited %d, want %d", short.code, cli.CodeUsage)
	}
	if got := r.ok("", "admin", "user", "list"); strings.Contains(got.stdout, "frank") {
		t.Errorf("a refused key must not leave a mailbox behind:\n%s", got.stdout)
	}
}

// TestMainSurvivesBrokenStreams: a command with no output streams must exit
// with a code rather than panicking on a nil writer.
func TestMainSurvivesBrokenStreams(t *testing.T) {
	if got := cli.Main(cli.App{}, []string{"inbox"}); got != cli.CodeInternal {
		t.Errorf("Main without streams exited %d, want %d", got, cli.CodeInternal)
	}
}

func TestHelpNeedsNoIdentity(t *testing.T) {
	r := newRig(t)
	for _, arg := range []string{"help", "-h", "--help"} {
		got := r.run("", arg)
		if got.code != cli.CodeOK {
			t.Errorf("%s exited %d", arg, got.code)
		}
		// The help has to say where identity comes from, since an agent with
		// none has nothing else to go on.
		if !strings.Contains(got.stdout, "ORC_USER") {
			t.Errorf("%s does not explain the credential:\n%s", arg, got.stdout)
		}
	}
}

// TestStoreVersionIsRefusedLoudly: a store from a newer Mailman must stop every
// command rather than being read on a partial understanding.
func TestStoreVersionIsRefusedLoudly(t *testing.T) {
	r := newRig(t, "alice")

	s, err := store.Open(r.root+"/store", clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeVersion(s.Root(), "99"); err != nil {
		t.Fatal(err)
	}

	got := r.run("alice", "inbox")
	if got.code != cli.CodeParse {
		t.Fatalf("a future store exited %d, want %d", got.code, cli.CodeParse)
	}
	if !strings.Contains(got.stderr, "version") {
		t.Errorf("stderr should mention the version:\n%s", got.stderr)
	}
}

func TestUserNamesAreValidatedAtTheEdge(t *testing.T) {
	r := newRig(t, "boss", "alice")
	for _, name := range []string{"../etc", "a b", "", "ALICE!"} {
		got := r.run("boss", "send", "s", name, "b")
		if got.code == cli.CodeOK {
			t.Errorf("send to %q succeeded", name)
		}
	}
	// The valid spelling of a name still works, whatever its case.
	if got := r.run("boss", "send", "s", "ALICE", "b"); got.code != cli.CodeOK {
		t.Errorf("send to ALICE exited %d: %s", got.code, got.stderr)
	}
}

func TestSelfAddressedMailStaysInTheInbox(t *testing.T) {
	r := newRig(t, "alice")
	r.ok("alice", "send", "note to self", "alice", "remember this")

	got := r.ok("alice", "inbox")
	if !strings.Contains(got.stdout, "note to self") {
		t.Errorf("a self-addressed note should be in the inbox:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "1 unread") {
		t.Errorf("it should be unread:\n%s", got.stdout)
	}
}

// writeVersion rewrites the store's format marker, for the version test.
func writeVersion(root, version string) error {
	return os.WriteFile(filepath.Join(root, "version"), []byte(version+"\n"), 0o600)
}

// TestCCGrantsTheWholeThread is the behaviour `cc` exists for. Adding someone
// to a conversation has to let them read it, or the addition is just a notice
// about a thread they cannot open.
func TestCCGrantsTheWholeThread(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("boss", "send", "RE: work", "alice", "one")
	r.ok("alice", "reply", `from="boss"`, "RE: work", "two")
	r.ok("boss", "cc", `subject~work`, "carol")

	convo := findConvo(t, r.ok("carol", "open", `kind=cc`).stdout)
	thread := r.ok("carol", "convo", convo, "--all")

	// All three messages, including the two sent before carol joined.
	for _, want := range []string{"#1", "#2", "#3"} {
		if !strings.Contains(thread.stdout, want) {
			t.Errorf("carol cannot see %s of the thread she was added to:\n%s", want, thread.stdout)
		}
	}
	// History she was never sent has no puid of her own, and says so rather
	// than printing an identifier that would not open.
	if !strings.Contains(thread.stdout, "[—]") {
		t.Errorf("history should be marked as having no local id:\n%s", thread.stdout)
	}
}

// TestCCKeepsTheJoinerInLaterReplies is the other half of the same defect: a
// reply addresses the conversation, not the message it happens to answer.
func TestCCKeepsTheJoinerInLaterReplies(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("boss", "send", "RE: work", "alice", "one")
	r.ok("alice", "reply", `from="boss"`, "RE: work", "two")
	r.ok("boss", "cc", `subject~work`, "carol")

	// Alice replies to the *original* message, not to the cc notice. Carol must
	// still receive it: she is in the conversation.
	r.ok("alice", "reply", `id="0"`, "RE: work", "four")

	got := r.ok("carol", "inbox")
	if !strings.Contains(got.stdout, "2 unread") {
		t.Errorf("carol was dropped from a reply to an older message:\n%s", got.stdout)
	}
}

// TestConvoIsRefusedToNonMembers: membership is the access check, and a refusal
// must not disclose that the conversation exists.
func TestConvoIsRefusedToNonMembers(t *testing.T) {
	r := newRig(t, "boss", "alice", "dave")
	r.ok("boss", "send", "RE: work", "alice", "one")
	r.ok("alice", "reply", `from="boss"`, "RE: work", "two")

	convo := findConvo(t, r.ok("alice", "open", `subject~work`).stdout)

	got := r.run("dave", "convo", convo)
	if got.code != cli.CodeNotFound {
		t.Fatalf("a non-member exited %d, want %d", got.code, cli.CodeNotFound)
	}
	if strings.Contains(got.stderr, "not allowed") || strings.Contains(got.stderr, "permission") {
		t.Errorf("the refusal discloses that the conversation exists:\n%s", got.stderr)
	}
}

// TestCCIsIdempotent: two agents adding the same person at once must both
// succeed rather than one of them failing over a state it asked for.
func TestCCIsIdempotent(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	r.ok("boss", "send", "RE: work", "alice", "one")
	r.ok("alice", "reply", `from="boss"`, "RE: work", "two")

	r.ok("boss", "cc", `subject~work & kind=mail`, "carol")
	again := r.ok("boss", "cc", `subject~work & kind=mail`, "carol")
	if !strings.Contains(again.stdout, "already in this conversation") {
		t.Errorf("a repeated cc should say so:\n%s", again.stdout)
	}
}

func TestInboxSent(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "outgoing", "alice", "a")
	r.ok("alice", "send", "incoming", "boss", "b")

	sent := r.ok("boss", "inbox", "--sent")
	if !strings.Contains(sent.stdout, "outgoing") {
		t.Errorf("--sent should show what boss sent:\n%s", sent.stdout)
	}
	if strings.Contains(sent.stdout, "incoming") {
		t.Errorf("--sent should not show received mail:\n%s", sent.stdout)
	}

	// And the ordinary inbox is still the other way round.
	in := r.ok("boss", "inbox")
	if !strings.Contains(in.stdout, "incoming") || strings.Contains(in.stdout, "outgoing") {
		t.Errorf("the inbox should hold only received mail:\n%s", in.stdout)
	}

	if got := r.run("boss", "inbox", "--all", "--sent"); got.code != cli.CodeUsage {
		t.Errorf("--all with --sent exited %d, want %d", got.code, cli.CodeUsage)
	}
}

// TestColourIsSessionConfigurableAndOffForAgents drives the whole policy
// through the CLI, which is where it actually matters.
func TestColourIsSessionConfigurableAndOffForAgents(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.ok("boss", "send", "RE: work", "alice", "Ship it.")

	// A terminal in the default flavour: macchiato's mauve title.
	got := r.runStyled("alice", true, map[string]string{"COLORTERM": "truecolor"}, "inbox")
	if got.code != cli.CodeOK {
		t.Fatalf("inbox exited %d: %s", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "38;2;198;160;246") {
		t.Errorf("the default flavour is not macchiato:\n%q", got.stdout)
	}

	// A different flavour, same session, no code change.
	latte := r.runStyled("alice", true, map[string]string{
		"COLORTERM": "truecolor", "ORC_THEME": "latte",
	}, "inbox")
	if !strings.Contains(latte.stdout, "38;2;136;57;239") { // latte mauve
		t.Errorf("ORC_THEME=latte did not take effect:\n%q", latte.stdout)
	}

	// An agent gets nothing, whatever else the environment says.
	agent := r.runStyled("alice", true, map[string]string{
		"COLORTERM": "truecolor", "ORC_THEME": "mocha", "CLICOLOR_FORCE": "1", "ORC_AGENT": "1",
	}, "inbox")
	if strings.Contains(agent.stdout, "\x1b[") {
		t.Errorf("an agent was given colour:\n%q", agent.stdout)
	}
	// And its output is byte-identical to the plain rendering, so nothing
	// downstream has to strip anything.
	plain := r.runStyled("alice", false, nil, "inbox")
	if agent.stdout != plain.stdout {
		t.Errorf("an agent's output differs from the plain rendering:\n%q\n%q", agent.stdout, plain.stdout)
	}

	// NO_COLOR still works for a person.
	quiet := r.runStyled("alice", true, map[string]string{"NO_COLOR": ""}, "inbox")
	if strings.Contains(quiet.stdout, "\x1b[") {
		t.Errorf("NO_COLOR was ignored:\n%q", quiet.stdout)
	}

	// A misspelled theme is reported rather than silently ignored.
	bad := r.runStyled("alice", true, map[string]string{"ORC_THEME": "dracula"}, "inbox")
	if bad.code != cli.CodeUsage {
		t.Errorf("a bad theme exited %d, want %d", bad.code, cli.CodeUsage)
	}
	if !strings.Contains(bad.stderr, "dracula") || !strings.Contains(bad.stderr, "macchiato") {
		t.Errorf("the error should name the value and the options:\n%s", bad.stderr)
	}
}

// TestHelpDocumentsTheColourSettings: an operator who wants to change it has to
// be able to find out how.
func TestHelpDocumentsTheColourSettings(t *testing.T) {
	r := newRig(t)
	got := r.ok("", "help")
	for _, want := range []string{"ORC_THEME", "ORC_AGENT", "NO_COLOR", "macchiato"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("help should mention %q:\n%s", want, got.stdout)
		}
	}
}

// TestHelpColourIsALayer: every help screen, stripped of its escape sequences, is
// byte-for-byte the plain rendering.
//
// It is worth a test of its own rather than trusting the eye, because the way this
// breaks is invisible in a terminal: padding a *painted* string lines the columns up
// one way with colour and another way without, since escape sequences have length but
// no display width. The same bug was found this way in `orc help`.
func TestHelpColourIsALayer(t *testing.T) {
	r := newRig(t)

	for _, args := range [][]string{{"help"}, {"admin"}} {
		plain := r.ok("", args...)
		coloured := r.runStyled("", true, map[string]string{"COLORTERM": "truecolor"}, args...)
		if coloured.code != cli.CodeOK {
			t.Fatalf("mailman %s exited %d: %s", strings.Join(args, " "), coloured.code, coloured.stderr)
		}
		if !strings.Contains(coloured.stdout, "\x1b[") {
			t.Errorf("mailman %s was not painted at all", strings.Join(args, " "))
		}
		if got := stripEscapes(coloured.stdout); got != plain.stdout {
			t.Errorf("mailman %s differs once colour is stripped:\nplain:\n%s\nstripped:\n%s",
				strings.Join(args, " "), plain.stdout, got)
		}
	}
}

// TestHelpNamesEveryCommand: the help is the screen an agent reads when it does not
// know what it may do, so a verb missing from it is a verb nobody finds.
func TestHelpNamesEveryCommand(t *testing.T) {
	r := newRig(t)
	got := r.ok("", "help")

	for _, verb := range []string{
		"inbox", "open", "convo", "send", "reply", "archive",
		"prune", "read", "check", "cc", "verify", "admin",
	} {
		if !strings.Contains(got.stdout, "mailman "+verb) {
			t.Errorf("help does not mention %q:\n%s", verb, got.stdout)
		}
	}
	// And the query language, which is the half of this tool nobody guesses.
	for _, want := range []string{"from=", "operators:", "fields:"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("help does not explain queries (%q):\n%s", want, got.stdout)
		}
	}
}

func stripEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
