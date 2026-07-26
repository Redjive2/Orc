package probe

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/source"
)

// World is a synthetic machine: the three stores, a repo, and a Claude
// configuration, in a temporary directory. Every test that needs "the real
// world" builds one of these, so no test ever resolves a real root.
type World struct {
	Home    string
	Mailman string
	Repo    string
	Env     source.Env
}

func newWorld(t *testing.T, tools ...string) World {
	t.Helper()
	home := t.TempDir()

	present := map[string]bool{}
	for _, name := range tools {
		present[name] = true
	}

	mailman := filepath.Join(home, ".mailman")
	if present["mailman"] {
		writeUser(t, mailman, "alice")
		writeUser(t, mailman, "boss")
		write(t, filepath.Join(mailman, "version"), "1\n")
	}
	if present["macmuffin"] {
		muff := filepath.Join(home, ".macmuffin")
		write(t, filepath.Join(muff, "version"), "1\n")
		write(t, filepath.Join(muff, "tasks", "refactor", "task.json"), `{"name":"refactor"}`)
		// A task somebody is holding, a worktree pointing at a real checkout,
		// and mail waiting to go out: the three things that make a copied world
		// look like one agents are working in.
		write(t, filepath.Join(muff, "tasks", "refactor", "journal.jsonl"),
			`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`+"\n"+
				`{"op":"invite","by":"bob","agent":"carol","at":"2026-07-01T09:01:00.000Z"}`+"\n")
		write(t, filepath.Join(muff, "worktrees", "abc123.json"), `{"path":"`+repoPath(home)+`","task":"refactor"}`)
		write(t, filepath.Join(muff, "outbox", "1.json"), `{"to":"carol","subject":"you were invited"}`)
	}
	if present["cq"] {
		write(t, filepath.Join(home, ".cq", "version"), "1\n")
	}
	if present["orc"] {
		orc := filepath.Join(home, ".orc")
		write(t, filepath.Join(orc, "version"), "1\n")
		write(t, filepath.Join(orc, "operator"), "alice\n")
		for _, name := range []string{"alice", "ember"} {
			dir := filepath.Join(orc, "identities", name)
			writeOrcIdentity(t, dir, name)
		}
		// ember is employed: a session claim naming two pids and a socket, plus
		// the log of what it did. The claim must not survive; the log must.
		session := filepath.Join(orc, "identities", "ember", "session")
		write(t, filepath.Join(session, "session.json"),
			`{"identity":"ember","id":"abc","supervisor":4242,"child":4243,"socket":"`+session+`/session.sock"}`)
		write(t, filepath.Join(session, "session.sock"), "")
		write(t, filepath.Join(session, "log.jsonl"), "{\"at\":\"2026-07-01T09:00:00.000Z\"}\n")
		write(t, filepath.Join(orc, "identities", "ember", "workspace", "notes.md"), "работа\n")
		write(t, filepath.Join(orc, "identities", "ember", "claude", "settings.json"), `{"hooks":{}}`)
	}

	repo := filepath.Join(home, "work")
	write(t, filepath.Join(repo, "file.txt"), "hello\n")
	write(t, filepath.Join(repo, ".git", "config"),
		"[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = https://example.invalid/x.git\n[branch \"main\"]\n\tremote = origin\n")

	write(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks":{}}`)

	return World{
		Home:    home,
		Mailman: mailman,
		Repo:    repo,
		// An empty environment: every root resolves under the fake home, and no
		// real variable can leak in.
		Env: source.MapEnv(map[string]string{}),
	}
}

func repoPath(home string) string { return filepath.Join(home, "work") }

// realFleetKey stands in for a credential that opens the real fleet. Orc is the
// only store that keeps one in the clear, so it is the one thing a probe of a
// fleet must never carry.
const realFleetKey = "the-real-key-that-opens-the-real-fleet"

// writeOrcIdentity writes what Orc keeps per identity: the record every tool
// stores, and the plaintext key only Orc has.
func writeOrcIdentity(t *testing.T, dir, name string) {
	t.Helper()
	salt := make([]byte, 32)
	digest := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i + 3)
		digest[i] = byte(i + 5)
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
	write(t, filepath.Join(dir, "user.json"), string(data))
	write(t, filepath.Join(dir, "key"), realFleetKey+"\n")
	write(t, filepath.Join(dir, "identity.json"), `{"name":"`+name+`"}`)
	write(t, filepath.Join(dir, "journal.jsonl"), "{\"op\":\"employ\"}\n")
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

// writeUser writes a Mailman-shaped record, which is what minting rewrites.
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
	write(t, filepath.Join(root, "users", name, "journal.jsonl"), "{\"kind\":\"landed\"}\n")
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "probes"),
		clock.NewFake(time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC), time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func spec(w World, name string) Spec {
	return Spec{
		Name:      name,
		Env:       w.Env,
		Home:      w.Home,
		Cwd:       w.Repo,
		ClaudeDir: filepath.Join(w.Home, ".claude"),
		BasePath:  "/usr/bin:/bin",
	}
}

func TestCheckName(t *testing.T) {
	good := []string{"scratch", "before-migration", "probe.2", "a", "x_1"}
	bad := []string{"", "  ", "-leading", ".hidden", "has space", "UPPER-ok?", "probes", "current", "..",
		strings.Repeat("x", MaxNameLen+1)}

	for _, name := range good {
		if _, err := CheckName(name); err != nil {
			t.Fatalf("CheckName(%q) refused a good name: %v", name, err)
		}
	}
	for _, name := range bad {
		if _, err := CheckName(name); err == nil {
			t.Fatalf("CheckName(%q) accepted a name that is a path element", name)
		}
	}
	// Names normalise the way mailbox names do, so one probe cannot become two
	// on a case-insensitive filesystem.
	if got, _ := CheckName("  Scratch  "); got != "scratch" {
		t.Fatalf("CheckName normalised to %q, want %q", got, "scratch")
	}
}

func TestCreateCopiesTheWorld(t *testing.T) {
	w := newWorld(t, "mailman", "macmuffin", "cq")
	s := newStore(t)

	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	p := report.Probe

	for _, want := range []string{
		RecordFile, ProbeStamp, ManifestFile, EnvFile, IdentitiesFile,
		"state/mailman/users/alice/user.json",
		"state/macmuffin/tasks/refactor/task.json",
		"repo/file.txt",
		"claude/settings.json",
	} {
		if _, err := os.Stat(p.Path(filepath.FromSlash(want))); err != nil {
			t.Fatalf("%s is missing from the probe: %v", want, err)
		}
	}

	// Every copied store carries the stamp the other tools will check.
	for _, dir := range []string{"state/mailman", "state/macmuffin", "state/cq", "repo"} {
		id, err := ReadStamp(p.Path(filepath.FromSlash(dir)))
		if err != nil {
			t.Fatalf("%s has no stamp: %v", dir, err)
		}
		if id != p.ID {
			t.Fatalf("%s is stamped %q, want the probe's own id %q", dir, id, p.ID)
		}
	}

	if report.Identities != 3 {
		t.Fatalf("minted %d identities, want alice, boss, god", report.Identities)
	}
	if len(report.Remotes) != 1 || report.Remotes[0] != "origin" {
		t.Fatalf("removed remotes %v, want [origin]", report.Remotes)
	}
	if len(report.Deferred) == 0 {
		t.Fatal("a probe with unkept promises reported none; that is the one thing it must always say")
	}
}

// TestCreateNeutersLiveness is rule 1 at the level the operator sees it: a
// probe of a busy world is a world where nobody is busy.
func TestCreateNeutersLiveness(t *testing.T) {
	w := newWorld(t, "mailman", "macmuffin", "cq")
	s := newStore(t)

	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Probe.Neutered {
		t.Fatal("the record does not say the probe was neutered")
	}
	if report.Scrub.Collaborators != 1 {
		t.Fatalf("removed %d collaborators, want 1", report.Scrub.Collaborators)
	}
	// An owner cannot be released today (see internal/neuter/macmuffin.go), so
	// the probe must report the task as still owned rather than claim a clean
	// scrub. A probe that is neutered only in part says "partial".
	if len(report.Scrub.Unreleased) != 1 {
		t.Fatalf("reported %v as still owned, want the one claimed task", report.Scrub.Unreleased)
	}
	if got := report.Probe.Liveness(); got != "partial" {
		t.Fatalf("liveness reads %q, want partial", got)
	}
	if report.Scrub.Worktrees != 1 || report.Scrub.Outbox != 1 {
		t.Fatalf("dropped %d worktrees and %d notifications, want 1 and 1",
			report.Scrub.Worktrees, report.Scrub.Outbox)
	}

	journal := report.Probe.Path(filepath.FromSlash("state/macmuffin/tasks/refactor/journal.jsonl"))
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"op":"leave"`) {
		t.Fatalf("the journal does not carry the collaborator's leave:\n%s", text)
	}
	if strings.Contains(text, `"op":"release"`) {
		t.Fatalf("the journal carries a release; macmuffin refuses that op and the task would be unreadable:\n%s", text)
	}
	// History is appended to, never rewritten: the claim is still there.
	if !strings.Contains(text, `"op":"claim","by":"bob"`) {
		t.Fatalf("the original claim was edited out:\n%s", text)
	}

	// And the real world still has all of it.
	real, err := os.ReadFile(filepath.Join(w.Home, ".macmuffin", "tasks", "refactor", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(real), "leave") {
		t.Fatal("the scrub reached the real store")
	}
}

// TestLiveStateKeepsEverything is the opt-out: reproducing a real situation
// needs the real ownership, and the flag says so loudly rather than quietly.
func TestLiveStateKeepsEverything(t *testing.T) {
	w := newWorld(t, "mailman", "macmuffin", "cq")
	s := newStore(t)

	sp := spec(w, "live")
	sp.LiveState = true
	report, err := s.Create(sp)
	if err != nil {
		t.Fatal(err)
	}
	if report.Probe.Neutered {
		t.Fatal("--live-state produced a probe that claims to be neutered")
	}
	if len(report.Scrub.Released) != 0 {
		t.Fatal("--live-state released a claim anyway")
	}

	journal := report.Probe.Path(filepath.FromSlash("state/macmuffin/tasks/refactor/journal.jsonl"))
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "release") {
		t.Fatal("--live-state appended a release")
	}
	if _, err := os.Stat(report.Probe.Path(filepath.FromSlash("state/macmuffin/outbox/1.json"))); err != nil {
		t.Fatal("--live-state dropped a pending notification")
	}

	var said bool
	for _, note := range report.Deferred {
		if strings.Contains(note, "live-state") {
			said = true
		}
	}
	if !said {
		t.Fatal("a probe that kept its liveness did not say so")
	}
}

func TestCreateLeavesTheWorldAlone(t *testing.T) {
	w := newWorld(t, "mailman", "macmuffin", "cq")
	before := fingerprint(t, w.Home)

	s := newStore(t)
	if _, err := s.Create(spec(w, "scratch")); err != nil {
		t.Fatal(err)
	}

	if after := fingerprint(t, w.Home); after != before {
		t.Fatal("creating a probe changed the world it was taken from")
	}
}

func TestCreateSkipsWhatIsNotThere(t *testing.T) {
	w := newWorld(t, "mailman") // no macmuffin, no cq
	s := newStore(t)

	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatalf("a machine where a tool has never run should still be probeable: %v", err)
	}
	for _, src := range report.Probe.Sources {
		if src.Tool == "Mailman" && !src.Present {
			t.Fatal("mailman state was there and was not copied")
		}
		if src.Tool == "Macmuffin" && src.Present {
			t.Fatal("macmuffin state was invented")
		}
	}

	// A tool that has never run still gets an empty *stamped* store, or the
	// probe is a place that tool refuses to work in — which reads as a bug in
	// the tool rather than a hole in the probe. Minting creates the god mailbox
	// in the mail store whether or not one came across, so an unstamped
	// directory would appear there anyway.
	for _, dir := range []string{"state/mailman", "state/macmuffin", "state/cq"} {
		path := report.Probe.Path(filepath.FromSlash(dir))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was not created: %v", dir, err)
		}
		id, err := ReadStamp(path)
		if err != nil {
			t.Fatalf("%s has no stamp, so every tool would refuse it: %v", dir, err)
		}
		if id != report.Probe.ID {
			t.Fatalf("%s is stamped %q, want %q", dir, id, report.Probe.ID)
		}
	}
}

func TestCreateRefusesADuplicate(t *testing.T) {
	w := newWorld(t, "mailman")
	s := newStore(t)
	if _, err := s.Create(spec(w, "scratch")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(spec(w, "scratch")); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second create returned %v, want a conflict", err)
	}
}

// TestCreateRefusesAProbeInsideTheRealStore is the loop that would make destroy
// reach real state.
func TestCreateRefusesAProbeInsideTheRealStore(t *testing.T) {
	w := newWorld(t, "mailman")
	s, err := Open(filepath.Join(w.Mailman, "probes"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(spec(w, "scratch"))
	if !errors.Is(err, fault.ErrEscape) {
		t.Fatalf("creating a probe inside the real store returned %v, want an escape refusal", err)
	}
}

func TestCreateCleansUpAfterItself(t *testing.T) {
	w := newWorld(t, "mailman")
	s := newStore(t)

	// A repo path that does not exist fails the copy partway through.
	sp := spec(w, "scratch")
	sp.Repo = filepath.Join(w.Home, "nothing-here")
	if _, err := s.Create(sp); err == nil {
		t.Fatal("create succeeded with a repo that is not there")
	}
	if _, err := os.Stat(s.dirFor("scratch")); !os.IsNotExist(err) {
		t.Fatal("a failed creation left a directory behind; a half-made probe looks usable")
	}
}

func TestUnfinishedProbeIsRefusedAndNamed(t *testing.T) {
	w := newWorld(t, "mailman")
	s := newStore(t)
	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}

	// Removing the record is exactly what a crash before the last write leaves.
	if err := os.Remove(report.Probe.Path(RecordFile)); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get("scratch")
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("an unfinished probe returned %v, want a conflict that says how to clean it up", err)
	}
	if !strings.Contains(err.Error(), "destroy") {
		t.Fatalf("the message does not say how to clean it up: %v", err)
	}
	// It must still be removable, or a crashed creation is permanent litter.
	if err := s.Destroy("scratch"); err != nil {
		t.Fatalf("an unfinished probe could not be destroyed: %v", err)
	}
}

func TestDestroyRefusesWhatItDidNotMake(t *testing.T) {
	s := newStore(t)
	dir := s.dirFor("planted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Destroy("planted"); err == nil {
		t.Fatal("destroy removed a directory with no probe stamp")
	}
}

func TestCurrentFollowsTheProbes(t *testing.T) {
	w := newWorld(t, "mailman")
	s := newStore(t)
	if _, err := s.Create(spec(w, "one")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(spec(w, "two")); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Resolve(""); err == nil {
		t.Fatal("Resolve picked a probe with no default set")
	}
	if err := s.SetCurrent("two"); err != nil {
		t.Fatal(err)
	}
	p, err := s.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "two" {
		t.Fatalf("resolved %q, want two", p.Name)
	}

	if err := s.Destroy("two"); err != nil {
		t.Fatal(err)
	}
	current, err := s.Current()
	if err != nil {
		t.Fatal(err)
	}
	if current != "" {
		t.Fatalf("the default is still %q after it was destroyed", current)
	}
}

func TestListOrdersByAgeAndNamesTheUnfinished(t *testing.T) {
	w := newWorld(t, "mailman")
	s := newStore(t)
	for _, name := range []string{"first", "second"} {
		if _, err := s.Create(spec(w, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(s.dirFor("broken"), 0o700); err != nil {
		t.Fatal(err)
	}

	probes, unfinished, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 2 || probes[0].Name != "first" {
		t.Fatalf("listed %d probes, first is %v", len(probes), probes)
	}
	if len(unfinished) != 1 || unfinished[0] != "broken" {
		t.Fatalf("unfinished probes reported as %v; a list that quietly hides one is worse", unfinished)
	}
}

func TestManifestSurvivesAnInterruptedAppend(t *testing.T) {
	dir := t.TempDir()
	m := OpenManifest(dir, clock.Real{})
	for _, act := range []string{ActCopy, ActDrop, ActMint} {
		if err := m.Add(act, "thing", "detail"); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(dir, ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Chop the last line in half, which is what a crash mid-append leaves.
	if err := os.WriteFile(path, data[:len(data)-12], 0o600); err != nil {
		t.Fatal(err)
	}

	entries, skipped, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("a torn final line failed the read: %v", err)
	}
	if len(entries) != 2 || skipped == 0 {
		t.Fatalf("read %d entries with %d bytes skipped, want 2 and a non-zero tail", len(entries), skipped)
	}

	// Corruption anywhere but the tail is a hard error, not a shrug.
	if err := os.WriteFile(path, []byte("not json\n{\"at\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadManifest(path); err == nil {
		t.Fatal("a corrupt line in the middle was accepted")
	}
}

func TestRecordRefusesAFutureFormat(t *testing.T) {
	w := newWorld(t, "mailman")
	s := newStore(t)
	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}

	path := report.Probe.Path(RecordFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"version": 1`, `"version": 99`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("scratch"); !errors.Is(err, fault.ErrParse) {
		t.Fatalf("a newer probe format returned %v, want a parse refusal", err)
	}
}

// fingerprint digests a whole tree, for the "nothing was touched" assertions.
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

// TestAProbeOfAFleetHoldsNoRealKey is the row that mattered most in Finish.md's
// stream D, and the reason Orc needed treating differently from every other
// store.
//
// Mailman, Macmuffin, and cq keep digests: copying them leaks nothing a probe
// key can open. Orc keeps the key itself, in the clear, because Orc is the thing
// that issues credentials. A probe that carried those across would be a scratch
// directory — made to be broken, thrown away, maybe pasted somewhere — holding
// the keys to the real fleet.
func TestAProbeOfAFleetHoldsNoRealKey(t *testing.T) {
	w := newWorld(t, "mailman", "macmuffin", "cq", "orc")
	s := newStore(t)

	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}

	// Nothing anywhere in the probe — not the keyring, not a journal, not a
	// stray copy — still holds the credential that opens the real fleet.
	if grep(t, report.Probe.Dir(), realFleetKey) {
		t.Fatal("the real fleet key is somewhere inside the probe")
	}

	// And every identity still works *in* the probe: a keyring that was merely
	// emptied would lock the fleet out of its own copy.
	for _, name := range []string{"alice", "ember"} {
		key := strings.TrimSpace(readFile(t, report.Probe.Path(
			filepath.FromSlash("state/orc/identities/"+name+"/key"))))
		if key == "" {
			t.Fatalf("%s has no key in the probe", name)
		}
		if key == realFleetKey {
			t.Fatalf("%s kept the real key", name)
		}
	}

	// The real fleet is untouched, keyring included.
	realKey := strings.TrimSpace(readFile(t, filepath.Join(w.Home, ".orc", "identities", "ember", "key")))
	if realKey != realFleetKey {
		t.Fatal("orcprobe rewrote the real fleet's keyring")
	}
}

// TestNoProbeClaimsALiveSession covers the other half of the row: session.json
// names two pids and a socket, and in a probe all three are lies.
func TestNoProbeClaimsALiveSession(t *testing.T) {
	w := newWorld(t, "mailman", "orc")
	s := newStore(t)

	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	session := report.Probe.Path(filepath.FromSlash("state/orc/identities/ember/session"))

	if _, err := os.Stat(filepath.Join(session, "session.json")); !os.IsNotExist(err) {
		t.Fatal("the probe still claims ember has a live session")
	}
	if _, err := os.Stat(filepath.Join(session, "session.sock")); !os.IsNotExist(err) {
		t.Fatal("the probe still holds a socket path that looks connectable")
	}
	// What the session *did* is history, and history is what a probe is for.
	if _, err := os.Stat(filepath.Join(session, "log.jsonl")); err != nil {
		t.Fatalf("the session log was thrown away with the claim: %v", err)
	}
	if report.Scrub.Sessions != 1 {
		t.Fatalf("cut %d session claims, want 1", report.Scrub.Sessions)
	}

	// The real fleet still has its session: a probe never reaches back.
	if _, err := os.Stat(filepath.Join(w.Home, ".orc", "identities", "ember", "session", "session.json")); err != nil {
		t.Fatal("orcprobe removed the real fleet's session")
	}
}

// TestTheFleetsWorkspacesAndConfigComeAcross is the copy half: a probe is for
// looking at what a fleet was doing, and an identity's workspace and its Claude
// configuration are most of that.
func TestTheFleetsWorkspacesAndConfigComeAcross(t *testing.T) {
	w := newWorld(t, "mailman", "orc")
	s := newStore(t)

	report, err := s.Create(spec(w, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"state/orc/identities/ember/workspace/notes.md",
		"state/orc/identities/ember/claude/settings.json",
		"state/orc/identities/ember/journal.jsonl",
		"state/orc/operator",
	} {
		if _, err := os.Stat(report.Probe.Path(filepath.FromSlash(want))); err != nil {
			t.Fatalf("%s did not come across: %v", want, err)
		}
	}
}

// TestTheWorldSurvivesAFleetProbe is the inertness claim, with an Orc store in
// the world. It is the same assertion the suite makes everywhere else, repeated
// here because this is the first source orcprobe *rewrites* rather than reads.
func TestTheWorldSurvivesAFleetProbe(t *testing.T) {
	w := newWorld(t, "mailman", "macmuffin", "cq", "orc")
	before := fingerprint(t, w.Home)

	s := newStore(t)
	if _, err := s.Create(spec(w, "scratch")); err != nil {
		t.Fatal(err)
	}

	if after := fingerprint(t, w.Home); after != before {
		t.Fatal("probing a fleet changed the fleet")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// grep reports whether any file under root contains the needle.
func grep(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
