package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/source"
)

// harness is a whole machine in a temporary directory: a synthetic world to
// probe, a probe store to put probes in, and an App wired to buffers. No test
// in this package ever resolves a real root or writes outside t.TempDir.
type harness struct {
	t     *testing.T
	home  string
	root  string
	world string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()

	write(t, filepath.Join(home, ".mailman", "version"), "1\n")
	writeUser(t, filepath.Join(home, ".mailman"), "alice")
	writeUser(t, filepath.Join(home, ".mailman"), "boss")
	writeMessage(t, filepath.Join(home, ".mailman"), "ab111111-0001", "boss", "alice", "the plan")
	write(t, filepath.Join(home, ".mailman", "users", "alice", "journal.jsonl"),
		`{"op":"deliver","mid":"ab111111-0001","puid":0,"at":"2026-07-01T09:00:01.000Z"}`+"\n")
	write(t, filepath.Join(home, ".macmuffin", "version"), "1\n")
	write(t, filepath.Join(home, ".macmuffin", "tasks", "refactor", "journal.jsonl"),
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`+"\n")
	write(t, filepath.Join(home, "work", "file.txt"), "hello\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks":{}}`)

	return &harness{t: t, home: home, root: filepath.Join(t.TempDir(), "probes"), world: home}
}

// run executes one command line and returns its exit code and streams.
func (h *harness) run(args ...string) (int, string, string) {
	h.t.Helper()
	return h.runWith(nil, false, args...)
}

// runColour runs with both streams pretending to be terminals, or neither.
func (h *harness) runColour(colour bool, args ...string) (int, string, string) {
	h.t.Helper()
	return h.runWith(nil, colour, args...)
}

// runWith runs one command line with an injected environment and a choice about
// whether the streams look like terminals.
func (h *harness) runWith(extra map[string]string, terminal bool, args ...string) (int, string, string) {
	h.t.Helper()
	return h.runFull(extra, terminal, terminal, args...)
}

// runStreams asks about each stream separately, which is what the two palettes
// exist for.
func (h *harness) runStreams(stdout, stderr bool, args ...string) (int, string, string) {
	h.t.Helper()
	return h.runFull(nil, stdout, stderr, args...)
}

func (h *harness) runFull(extra map[string]string, stdout, stderr bool, args ...string) (int, string, string) {
	h.t.Helper()
	var out, errs bytes.Buffer

	// An empty environment by default: every root resolves under the fake home,
	// and no real colour setting can leak into a test.
	environment := map[string]string{}
	for k, v := range extra {
		environment[k] = v
	}

	code := Main(App{
		Stdin:       strings.NewReader(""),
		Stdout:      &out,
		Stderr:      &errs,
		Env:         source.MapEnv(environment),
		Environ:     []string{"TERM=dumb"},
		Home:        h.home,
		Cwd:         filepath.Join(h.home, "work"),
		Path:        "/usr/bin:/bin",
		Root:        h.root,
		Clock:       clock.NewFake(time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC), time.Second),
		Width:       100,
		Colour:      true,
		Terminal:    stdout,
		ErrTerminal: stderr,
	}, args)
	return code, out.String(), errs.String()
}

// plantUnfinished leaves a directory that looks like an interrupted creation,
// so `list` has something to warn about on stderr.
func plantUnfinished(t *testing.T, h *harness) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, "probes", "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUser(t *testing.T, root, name string) {
	t.Helper()
	salt := make([]byte, 32)
	digest := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i + 1)
		digest[i] = byte(i + 2)
	}
	rec := map[string]any{
		"version": 1, "name": name, "algo": "hmac-sha256",
		"salt":    base64.StdEncoding.EncodeToString(salt),
		"digest":  base64.StdEncoding.EncodeToString(digest),
		"created": clock.Format(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "users", name, "user.json"), string(data))
}

// writeMessage writes one stored message in Mailman's format, so the views have
// something real to decode.
func writeMessage(t *testing.T, root, mid, from, to, subject string) {
	t.Helper()
	body := "read it\n"
	header := strings.Join([]string{
		"mailman/1",
		"id: " + mid,
		"kind: mail",
		"from: " + from,
		"to: " + to,
		"subject: " + subject,
		"sent: 2026-07-01T09:00:00.000Z",
		fmt.Sprintf("bytes: %d", len(body)),
		"",
	}, "\n")
	write(t, filepath.Join(root, "messages", mid[:2], mid+".msg"), header+"\n"+body)
}

func TestCreateThenList(t *testing.T) {
	h := newHarness(t)

	code, out, _ := h.run("create", "scratch")
	if code != CodeOK {
		t.Fatalf("create exited %d\n%s", code, out)
	}
	for _, want := range []string{"probe scratch", "Mailman", "mailboxes have probe keys", "orcprobe shell"} {
		if !strings.Contains(out, want) {
			t.Fatalf("create did not mention %q:\n%s", want, out)
		}
	}
	// What was scrubbed, and what is still not guaranteed, are both shown at
	// creation rather than buried in a manifest.
	if !strings.Contains(out, "neutered:") {
		t.Fatalf("create did not say what it took the life out of:\n%s", out)
	}
	// An owner it could not release is named, not glossed over.
	if !strings.Contains(out, "still shows bob as its owner") {
		t.Fatalf("create did not name the task it could not release:\n%s", out)
	}
	// The stamp guard is in force now, so it is no longer a deferred promise —
	// it is a manifest note saying what the other tools will refuse.
	_, manifest, _ := h.run("manifest")
	if !strings.Contains(manifest, "stamp guard") {
		t.Fatalf("the manifest does not record the stamp guard:\n%s", manifest)
	}

	code, out, _ = h.run("list")
	if code != CodeOK {
		t.Fatalf("list exited %d", code)
	}
	// The world had one claimed task, and orcprobe cannot release an owner, so
	// this probe is scrubbed only in part and must say "partial" rather than
	// borrow the word for a clean one.
	if !strings.Contains(out, "scratch") || !strings.Contains(out, "partial") {
		t.Fatalf("list does not show the probe and its liveness:\n%s", out)
	}
	// A first probe becomes the default, or every new user's second command
	// fails with "no default probe".
	if !strings.Contains(out, "●") {
		t.Fatalf("the first probe did not become the default:\n%s", out)
	}
}

func TestManifestRecordsWhatWasDeferred(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatalf("create exited %d\n%s", code, out)
	}

	code, out, _ := h.run("manifest")
	if code != CodeOK {
		t.Fatalf("manifest exited %d\n%s", code, out)
	}
	for _, want := range []string{"copy", "mint", "defer"} {
		if !strings.Contains(out, want) {
			t.Fatalf("manifest has no %q entries:\n%s", want, out)
		}
	}
}

// TestLiveStateSaysSo pins the one thing a probe that kept its liveness must
// never do: look like a neutered one.
func TestLiveStateSaysSo(t *testing.T) {
	h := newHarness(t)

	code, out, _ := h.run("create", "live", "--live-state")
	if code != CodeOK {
		t.Fatalf("create --live-state exited %d\n%s", code, out)
	}
	if strings.Contains(out, "neutered:") {
		t.Fatalf("--live-state claimed a scrub it did not do:\n%s", out)
	}
	if !strings.Contains(out, "live-state") {
		t.Fatalf("--live-state did not warn that liveness came across:\n%s", out)
	}

	_, out, _ = h.run("list")
	if !strings.Contains(out, "verbatim") {
		t.Fatalf("list does not mark the probe as unscrubbed:\n%s", out)
	}
}

func TestDestroyNeedsConfirmation(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}

	// Not a terminal — which, for an agent, is always.
	code, out, _ := h.run("destroy", "scratch")
	if code != CodeUsage {
		t.Fatalf("destroy without --yes exited %d, want %d", code, CodeUsage)
	}
	if !strings.Contains(out, "will remove probe scratch") {
		t.Fatalf("destroy did not say what it would remove:\n%s", out)
	}
	if code, _, _ := h.run("list"); code != CodeOK {
		t.Fatal("the probe should still be there")
	}

	if code, _, _ := h.run("destroy", "scratch", "--yes"); code != CodeOK {
		t.Fatalf("destroy --yes exited %d", code)
	}
	_, out, _ = h.run("list")
	if strings.Contains(out, "scratch") {
		t.Fatalf("the probe survived destroy:\n%s", out)
	}
}

// TestRefusalsExitAsEscapes pins the exit code a hook would branch on: a
// refusal to reach the real world is not a usage error.
func TestRefusalsExitAsEscapes(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}

	for _, command := range [][]string{
		{"as", "god", "--", "cq", "sync"},
		{"as", "god", "--", "git", "push"},
		{"as", "god", "--", "orc", "run", "anything"},
	} {
		code, _, errs := h.run(command...)
		if code != CodeEscape {
			t.Fatalf("%v exited %d, want %d\n%s", command, code, CodeEscape, errs)
		}
		if !strings.Contains(errs, "refused") {
			t.Fatalf("%v did not say it was refused:\n%s", command, errs)
		}
	}
}

// TestEscapeUsesTheSharedExitCode pins the number itself.
//
// Claude/Docs/ExitCodes.md gives 9 to "out of scope" and 11 to "a path resolved
// outside the root it was measured against". Orcprobe used 9 before that table
// existed, which would have made a hook read a containment failure as an
// ordinary scope refusal — the two things a probe most needs to tell apart.
func TestEscapeUsesTheSharedExitCode(t *testing.T) {
	if CodeEscape != 11 {
		t.Fatalf("CodeEscape is %d; the shared table says 11", CodeEscape)
	}
	if CodeEscape == 9 {
		t.Fatal("9 is out-of-scope in every other Orc tool")
	}
}

func TestAsRefusesAnUnknownIdentity(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}
	if code, _, _ := h.run("as", "nobody", "--", "mailman", "inbox"); code != CodeNotFound {
		t.Fatalf("an unknown identity exited %d, want %d", code, CodeNotFound)
	}
}

func TestCommandsWithoutAProbe(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("list"); code != CodeOK {
		t.Fatal("list with no probes should be an empty table, not a failure")
	}
	if code, _, _ := h.run("manifest"); code != CodeNotFound {
		t.Fatal("a command needing a probe should say there is none")
	}
	if code, _, errs := h.run("nonsense"); code != CodeUsage || !strings.Contains(errs, "unknown command") {
		t.Fatalf("an unknown command exited %d:\n%s", code, errs)
	}
	if code, out, _ := h.run("help"); code != CodeOK || !strings.Contains(out, "orcprobe create") {
		t.Fatalf("help exited %d:\n%s", code, out)
	}
}

// TestTheOmniscientViews covers what `shell` cannot do: read the store with no
// identity at all, and show what no single mailbox could.
func TestTheOmniscientViews(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatalf("create exited %d\n%s", code, out)
	}

	t.Run("world", func(t *testing.T) {
		code, out, _ := h.run("world")
		if code != CodeOK {
			t.Fatalf("world exited %d\n%s", code, out)
		}
		for _, want := range []string{"mailboxes", "alice", "boss", "tasks", "refactor", "probe"} {
			if !strings.Contains(out, want) {
				t.Fatalf("the world screen does not mention %q:\n%s", want, out)
			}
		}
		// The task orcprobe could not release is the thing worth noticing here.
		if !strings.Contains(out, "still") {
			t.Fatalf("the world screen does not flag the held task:\n%s", out)
		}
	})

	t.Run("mail across mailboxes", func(t *testing.T) {
		code, out, _ := h.run("mail")
		if code != CodeOK {
			t.Fatalf("mail exited %d\n%s", code, out)
		}
		// One row, showing sender and recipient together — the cross-user view
		// no `mailman inbox` can produce.
		for _, want := range []string{"boss", "alice", "the plan", "read by", "nobody"} {
			if !strings.Contains(out, want) {
				t.Fatalf("the mail view does not show %q:\n%s", want, out)
			}
		}
	})

	t.Run("mail takes mailman's query language", func(t *testing.T) {
		if code, out, _ := h.run("mail", `from="boss"`); code != CodeOK || !strings.Contains(out, "the plan") {
			t.Fatalf("a matching query exited %d:\n%s", code, out)
		}
		if code, out, _ := h.run("mail", `from="nobody"`); code != CodeOK || !strings.Contains(out, "nothing matches") {
			t.Fatalf("a query matching nothing exited %d:\n%s", code, out)
		}
		// A typo is an error, never an empty table.
		code, _, errs := h.run("mail", `sender="boss"`)
		if code != CodeParse {
			t.Fatalf("an unknown field exited %d, want %d\n%s", code, CodeParse, errs)
		}
	})

	t.Run("tasks shows what the pool hides", func(t *testing.T) {
		code, out, _ := h.run("tasks")
		if code != CodeOK {
			t.Fatalf("tasks exited %d\n%s", code, out)
		}
		if !strings.Contains(out, "refactor") || !strings.Contains(out, "bob") {
			t.Fatalf("the task view does not show the task and its owner:\n%s", out)
		}
	})

	t.Run("journal", func(t *testing.T) {
		code, out, _ := h.run("journal", "refactor")
		if code != CodeOK {
			t.Fatalf("journal exited %d\n%s", code, out)
		}
		if !strings.Contains(out, "claim") || !strings.Contains(out, "bob") {
			t.Fatalf("the journal does not show the claim:\n%s", out)
		}

		code, out, _ = h.run("journal", "alice")
		if code != CodeOK || !strings.Contains(out, "mailbox alice") {
			t.Fatalf("a mailbox journal exited %d:\n%s", code, out)
		}
		if code, _, _ := h.run("journal", "nothing-by-that-name"); code != CodeNotFound {
			t.Fatalf("an unknown journal exited %d, want %d", code, CodeNotFound)
		}
	})

	t.Run("timeline merges tools", func(t *testing.T) {
		code, out, _ := h.run("timeline")
		if code != CodeOK {
			t.Fatalf("timeline exited %d\n%s", code, out)
		}
		if !strings.Contains(out, "mailman") || !strings.Contains(out, "muff") {
			t.Fatalf("the timeline does not merge both tools:\n%s", out)
		}
		if code, out, _ := h.run("timeline", "--tool", "muff"); code != CodeOK || strings.Contains(out, "the plan") {
			t.Fatalf("--tool did not narrow the timeline:\n%s", out)
		}
		if code, _, _ := h.run("timeline", "--since", "not-a-time"); code != CodeUsage {
			t.Fatalf("a bad --since exited %d, want %d", code, CodeUsage)
		}
	})
}

// TestViewsWorkWithoutTheTools is the property that makes these the right thing
// to reach for when a tool is what you are debugging: nothing here runs mailman
// or muff, so a probe whose tools are not installed still reads.
func TestViewsWorkWithoutTheTools(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}
	// The harness has no binaries on its PATH at all, and every view above
	// already ran against it. This test states the reason out loud so a future
	// change that shells out to `muff pool` fails here with an explanation.
	for _, command := range [][]string{{"world"}, {"mail"}, {"tasks"}, {"timeline"}} {
		if code, out, errs := h.run(command...); code != CodeOK {
			t.Fatalf("%v exited %d without the tools installed\n%s%s", command, code, out, errs)
		}
	}
}

func TestCheckpointsRewindAProbe(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatalf("create exited %d\n%s", code, out)
	}

	code, out, _ := h.run("save", "before")
	if code != CodeOK {
		t.Fatalf("save exited %d\n%s", code, out)
	}
	if !strings.Contains(out, "orcprobe restore before") {
		t.Fatalf("save does not say how to rewind:\n%s", out)
	}

	// Change something inside the probe, the way an experiment would.
	live := filepath.Join(h.root, "probes", "scratch", "state", "mailman", "wrecked.txt")
	write(t, live, "an experiment happened here")

	// A rewind discards work, so it is confirmed like destroy is.
	if code, _, _ := h.run("restore", "before"); code != CodeUsage {
		t.Fatalf("restore without --yes exited %d, want %d", code, CodeUsage)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("the unconfirmed restore rewound the probe anyway")
	}

	if code, out, _ := h.run("restore", "before", "--yes"); code != CodeOK {
		t.Fatalf("restore exited %d\n%s", code, out)
	}
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Fatal("the rewind did not discard work done since the checkpoint")
	}

	// The probe is still a probe afterwards: a rewind restores contents, never
	// the identity that makes the probe usable.
	if code, out, errs := h.run("world"); code != CodeOK {
		t.Fatalf("the probe was unusable after a rewind: exited %d\n%s%s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(h.root, "probes", "scratch", "identities.json")); err != nil {
		t.Fatalf("the rewind removed the probe's keys: %v", err)
	}

	_, out, _ = h.run("list")
	if !strings.Contains(out, "before") {
		t.Fatalf("list does not show the checkpoint:\n%s", out)
	}
}

func TestDiffComparesProbesAndSources(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"one", "two"} {
		if code, out, _ := h.run("create", name); code != CodeOK {
			t.Fatalf("create %s exited %d\n%s", name, code, out)
		}
	}

	// Two probes of the same unchanged world differ in nothing but their own
	// minted keys, which live outside the compared parts.
	code, out, _ := h.run("diff", "one", "two")
	if code != CodeOK {
		t.Fatalf("diff exited %d\n%s", code, out)
	}
	if !strings.Contains(out, "identical") {
		t.Fatalf("two probes of one world are not reported identical:\n%s", out)
	}

	// Change one probe, and the diff must find it.
	write(t, filepath.Join(h.root, "probes", "two", "state", "mailman", "new.txt"), "x")
	_, out, _ = h.run("diff", "one", "two")
	if !strings.Contains(out, "difference") {
		t.Fatalf("a changed probe still reports as identical:\n%s", out)
	}

	// Drift: the world has not moved, so the probe still matches it.
	code, out, _ = h.run("diff", "--source", "one")
	if code != CodeOK {
		t.Fatalf("diff --source exited %d\n%s", code, out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Fatalf("an untouched world reads as drifted:\n%s", out)
	}

	// Move the world, and drift must say so.
	write(t, filepath.Join(h.home, ".mailman", "users", "alice", "journal.jsonl"),
		`{"op":"read","mid":"ab111111-0001","at":"2026-07-02T09:00:00.000Z"}`+"\n")
	_, out, _ = h.run("diff", "--source", "one")
	if !strings.Contains(out, "moved on") {
		t.Fatalf("a world that changed does not read as drifted:\n%s", out)
	}
}

// TestDoctorReportsRatherThanReassures is the point of the command: with no
// tools installed the stamp guard cannot be measured, and the summary has to
// say that rather than call the probe sound.
func TestDoctorReportsRatherThanReassures(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatalf("create exited %d\n%s", code, out)
	}

	code, out, _ := h.run("doctor")
	if code != CodeOK {
		t.Fatalf("doctor exited %d\n%s", code, out)
	}
	for _, want := range []string{"stamp", "redirection", "MAILMAN_HOME", "in force", "not checked"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the doctor report does not mention %q:\n%s", want, out)
		}
	}
	// The harness installs no shims and no tools, so this probe genuinely is
	// not fully contained — and the report has to say so rather than let a
	// wall of "in force" rows imply otherwise.
	if !strings.Contains(out, "not fully contained") {
		t.Fatalf("doctor passed a probe with no shims:\n%s", out)
	}
	if !strings.Contains(out, "not checked") {
		t.Fatalf("doctor did not distinguish an unmeasured guard from a working one:\n%s", out)
	}

	// --strict turns an unmeasured or absent guard into a non-zero exit, so a
	// script can gate on containment.
	if code, _, _ := h.run("doctor", "--strict"); code != CodeEscape {
		t.Fatalf("doctor --strict exited %d with unmeasured guards, want %d", code, CodeEscape)
	}
}

func TestDoctorNoticesABrokenProbe(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}
	// Remove a store's stamp: the tools would refuse it, and doctor must say so
	// before the operator finds out the hard way.
	if err := os.Remove(filepath.Join(h.root, "probes", "scratch", "state", "mailman", ".orcprobe-stamp")); err != nil {
		t.Fatal(err)
	}

	code, out, _ := h.run("doctor")
	if code != CodeOK {
		t.Fatalf("doctor exited %d\n%s", code, out)
	}
	if !strings.Contains(out, "absent") || !strings.Contains(out, "not fully contained") {
		t.Fatalf("doctor did not report the missing stamp:\n%s", out)
	}
}

// TestNothingReachesTheRealWorld is this tool's central claim, so it is a test
// rather than a review item: run the whole command surface against a synthetic
// world, then assert the world is byte-for-byte what it was.
func TestNothingReachesTheRealWorld(t *testing.T) {
	h := newHarness(t)
	before := fingerprint(t, h.world)

	battery := [][]string{
		{"create", "one"},
		{"create", "two"},
		{"list"},
		{"use", "two"},
		{"manifest"},
		{"manifest", "--probe", "one"},
		{"as", "god", "--", "cq", "sync"},
		{"as", "god", "--", "git", "push"},
		{"as", "alice", "--", "mailman", "inbox"},
		{"world"},
		{"mail"},
		{"mail", `from="boss"`},
		{"tasks"},
		{"journal", "refactor"},
		{"journal", "alice"},
		{"timeline"},
		{"save", "point"},
		{"restore", "point", "--yes"},
		{"diff", "one", "two"},
		{"diff", "--source", "one"},
		{"doctor"},
		{"destroy", "one", "--yes"},
		{"destroy", "two", "--yes"},
		{"list"},
	}
	for _, args := range battery {
		h.run(args...) // exit codes are pinned by the tests above; this is about side effects
	}

	if after := fingerprint(t, h.world); after != before {
		t.Fatal("the world changed while orcprobe was working on copies of it")
	}
}

func fingerprint(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b.WriteString(rel + "|" + info.Mode().String() + "|")
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.Write(data)
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
