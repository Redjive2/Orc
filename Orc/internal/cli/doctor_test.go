package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/cli"
)

// doctored runs a command with extra environment the shared rig does not offer.
//
// It builds the App here rather than growing `rig`, because the environment is
// the whole subject of two of these tests and nothing else in the suite needs to
// set one. A local helper is cheaper than a field every other test carries.
func doctored(t *testing.T, r *rig, extra map[string]string, args ...string) result {
	t.Helper()
	env := map[string]string{"ORC_USER": "root", "ORC_KEY": r.keys["root"]}
	for k, v := range extra {
		env[k] = v
	}

	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Env: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
		Root:       r.root,
		Clock:      r.now,
		Width:      100,
		User:       "operator",
		Provision:  r.provision,
		Populate:   r.populate,
		Depopulate: r.depopulate,
	}, args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// withHook puts a stand-in orc-hook on PATH, so the guard that looks for one
// finds it. What it does is irrelevant: doctor asks whether it can be found, and
// enforcement is stream A's to test.
func withHook(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	hook := filepath.Join(dir, "orc-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// withoutHook empties PATH, which is the honest way to say "it is not installed".
func withoutHook(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// squeezed collapses whitespace, so a phrase assertion is about the words rather
// than about where the detail column happened to wrap.
func squeezed(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestDoctorPrintsTheHoles is why the screen exists. §7.5's list is printed on a
// healthy fleet too: an operator reading a screen full of "in force" would
// otherwise conclude the permission model is a fence, and it is a request that
// one hook enforces on one side of one tool layer.
func TestDoctorPrintsTheHoles(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")

	got := doctored(t, r, nil, "doctor")
	for _, hole := range []string{
		"shell reads the keyring", "subagents", "bash writes", "pattern breadth",
	} {
		if !strings.Contains(got.stdout, hole) {
			t.Errorf("doctor did not print the hole %q:\n%s", hole, got.stdout)
		}
	}
	// And each says what it actually costs, not merely that it exists.
	if !strings.Contains(squeezed(got.stdout), "none of this is a kernel boundary") {
		t.Errorf("the keyring hole does not say how weak it is:\n%s", got.stdout)
	}
}

// TestTheHolesAreNotFailures. They are the wall's shape, not its damage: a fleet
// with every buildable guard in force exits zero even though four guards print
// as absent.
func TestTheHolesAreNotFailures(t *testing.T) {
	withHook(t)
	r := newRig(t)
	r.bootstrap("root")

	got := doctored(t, r, nil, "doctor")
	if got.code != fault.CodeOK {
		t.Fatalf("a healthy fleet exited %d:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(squeezed(got.stdout), "every guard that can hold is holding") {
		t.Errorf("a healthy fleet did not say so:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "bash writes") {
		t.Errorf("a healthy fleet hid the holes:\n%s", got.stdout)
	}
}

// TestAMissingHookIsTheLoudestAbsence: without it the compiled deny list is all
// that is left, and §7.2 put Claude in bypassPermissions — so the deny list is a
// request rather than a fence.
func TestAMissingHookIsTheLoudestAbsence(t *testing.T) {
	withoutHook(t)
	r := newRig(t)
	r.bootstrap("root")

	got := doctored(t, r, nil, "doctor")
	if got.code != fault.CodeConflict {
		t.Errorf("code = %d, want %d", got.code, fault.CodeConflict)
	}
	if !strings.Contains(got.stdout, "orc-hook") || !strings.Contains(squeezed(got.stdout), "not on PATH") {
		t.Errorf("the missing hook was not reported:\n%s", got.stdout)
	}
	// A refusal names the way forward.
	if !strings.Contains(squeezed(got.stdout), "go build") {
		t.Errorf("the report does not say how to fix it:\n%s", got.stdout)
	}
}

// TestNotCheckedIsNotAFailure. A guard that could not be measured is unmeasured,
// not broken — counting it would make doctor exit non-zero on every fleet with
// nothing running, which is every fleet at rest.
func TestNotCheckedIsNotAFailure(t *testing.T) {
	withHook(t)
	r := newRig(t)
	r.bootstrap("root")

	got := doctored(t, r, nil, "doctor")
	if !strings.Contains(squeezed(got.stdout), "not checked") {
		t.Fatalf("nothing was unmeasured on a fleet at rest:\n%s", got.stdout)
	}
	if got.code != fault.CodeOK {
		t.Errorf("an unmeasured guard was counted as broken: exit %d\n%s", got.code, got.stdout)
	}
}

// TestDoctorSaysWhereItIs. Believing you are in a probe when you are not is how
// a test wrecks a live fleet, so where you are is on the first line either way.
func TestDoctorSaysWhereItIs(t *testing.T) {
	withHook(t)
	r := newRig(t)
	r.bootstrap("root")

	real := doctored(t, r, nil, "doctor")
	if !strings.Contains(real.stdout, "the real fleet") {
		t.Errorf("doctor did not say it was the real fleet:\n%s", real.stdout)
	}

	probe := doctored(t, r, map[string]string{"ORCPROBE_HOME": t.TempDir()}, "doctor")
	if !strings.Contains(probe.stdout, "a probe") {
		t.Errorf("doctor did not notice the probe stamp:\n%s", probe.stdout)
	}
	// Where it is, is not a guard: a real fleet has no sandbox by design, and
	// listing that as a guard not in force would mean doctor never exits zero.
	if strings.Contains(probe.stdout, "orcprobe stamp   ") {
		t.Errorf("where it is was listed as a guard:\n%s", probe.stdout)
	}
}

// TestALooseKeyringIsReported. The keys are plaintext, so the file modes are the
// only thing between another unix user and the whole fleet.
func TestALooseKeyringIsReported(t *testing.T) {
	withHook(t)
	r := newRig(t)
	r.bootstrap("root")

	key := filepath.Join(r.root, "identities", "root", "key")
	if err := os.Chmod(key, 0o644); err != nil {
		t.Skipf("cannot loosen the mode here: %v", err)
	}

	got := doctored(t, r, nil, "doctor")
	if got.code != fault.CodeConflict {
		t.Errorf("a world-readable key did not fail: exit %d\n%s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, "keyring mode") || !strings.Contains(squeezed(got.stdout), "chmod") {
		t.Errorf("the loose mode was not reported with its fix:\n%s", got.stdout)
	}
}

// TestDoctorChangesNothing. It is a report, and a report that repaired something
// would be a repair nobody asked for.
func TestDoctorChangesNothing(t *testing.T) {
	withHook(t)
	r := newRig(t)
	r.bootstrap("root")

	before := treeOf(t, r.root)
	doctored(t, r, nil, "doctor")
	if after := treeOf(t, r.root); after != before {
		t.Errorf("doctor changed the store")
	}
}

// treeOf is a cheap fingerprint of a directory tree: every path and size.
func treeOf(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		b.WriteString(path)
		if !info.IsDir() {
			b.WriteString(":" + strings.Repeat("x", int(info.Size()%97)))
		}
		b.WriteByte('\n')
		return nil
	})
	return b.String()
}

// TestVerifyReportsAnUnparseableSession. The derivation tolerates it — an
// identity with a broken session file is simply not populated as far as anybody
// can tell — so verify is the only place it is ever visible.
func TestVerifyReportsAnUnparseableSession(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")

	dir := filepath.Join(r.root, "identities", "root", "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := r.run("root", "verify")
	if !strings.Contains(squeezed(got.stdout), "will not parse") {
		t.Errorf("the damaged session file was not reported:\n%s", got.stdout)
	}
	if got.code != fault.CodeConflict {
		t.Errorf("code = %d, want %d", got.code, fault.CodeConflict)
	}
}

// TestVerifyReportsAPidThatIsSomebodyElse.
//
// Liveness answers "is something there", which is the wrong question for a pid
// read out of a file written days ago: the operating system reuses pids, so a
// state file can point at an unrelated process that answers yes. This is the
// case that looks healthy to everything else in the tree.
func TestVerifyReportsAPidThatIsSomebodyElse(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")

	// This test process is certainly alive and certainly not an orc-session.
	writeSession(t, r, `{"identity":"root","id":"x","supervisor":`+itoa(os.Getpid())+
		`,"child":`+itoa(os.Getpid())+`,"socket":"","started":"2026-07-25T12:00:00Z",`+
		`"model":"opus","effort":"high"}`)

	got := r.run("root", "verify")
	if !strings.Contains(squeezed(got.stdout), "the pid was reused") {
		t.Skipf("this platform's ps did not identify the process:\n%s", got.stdout)
	}
	if got.code != fault.CodeConflict {
		t.Errorf("code = %d, want %d", got.code, fault.CodeConflict)
	}
}

// TestVerifyReportsAStaleSocket: a socket left by a killed supervisor is the
// file an operator finds and believes.
func TestVerifyReportsAStaleSocket(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")

	dir := filepath.Join(r.root, "identities", "root", "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Pid 0 is never a live process, which is what makes this a dead session
	// rather than a slow one.
	writeSession(t, r, `{"identity":"root","id":"gone","supervisor":0,"child":0,`+
		`"socket":"`+sock+`","started":"2026-07-25T12:00:00Z","model":"opus","effort":"high"}`)

	got := r.run("root", "verify")
	if !strings.Contains(squeezed(got.stdout), "no session behind it") {
		t.Errorf("the stale socket was not reported:\n%s", got.stdout)
	}
}

// TestACleanFleetVerifiesQuietly guards the false positives: none of the checks
// above may fire on a fleet with nothing running.
func TestACleanFleetVerifiesQuietly(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.hire("root", "ember")

	got := r.run("root", "verify")
	if got.code != fault.CodeOK {
		t.Errorf("a clean fleet reported problems: exit %d\n%s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, "no problems found") {
		t.Errorf("a clean fleet did not say so:\n%s", got.stdout)
	}
}

func writeSession(t *testing.T, r *rig, body string) {
	t.Helper()
	dir := filepath.Join(r.root, "identities", "root", "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// TestWatchRefusesABusyLoop. Reconciling is not free, and a loop tighter than
// MinWatch is a busy-wait dressed as supervision.
func TestWatchRefusesABusyLoop(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")

	for _, tc := range []struct{ arg, want string }{
		{"1s", "shortest useful interval"},
		{"0s", "shortest useful interval"},
		{"nonsense", "takes a duration"},
		{"-5s", "shortest useful interval"},
	} {
		got := r.run("root", "tend", "--watch", tc.arg)
		if got.code != fault.CodeUsage {
			t.Errorf("--watch %s exited %d, want a usage error", tc.arg, got.code)
		}
		if !strings.Contains(squeezed(got.stderr), tc.want) {
			t.Errorf("--watch %s: %q does not mention %q", tc.arg, got.stderr, tc.want)
		}
	}
}

// TestTendWithoutWatchStillRunsOnce: the flag is additive, and the bare verb is
// what every other command calls.
func TestTendWithoutWatchStillRunsOnce(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")

	got := r.run("root", "tend")
	if got.code != fault.CodeOK {
		t.Fatalf("tend exited %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "nothing to do") {
		t.Errorf("a reconciled fleet did not say so:\n%s", got.stdout)
	}
}

// TestDoctorFindsAWorkspaceThatMoved — §2.4.1. `orc workspace <identity>` says this
// for one agent; nobody runs that for every agent, and this is the check somebody
// runs when something is already suspected.
func TestDoctorReportsWorkspaceDrift(t *testing.T) {
	withHook(t)
	r := fullFleet(t)
	first := filepath.Join(t.TempDir(), "one")
	second := filepath.Join(t.TempDir(), "two")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r.ok("boss", "workspace", "ember", first, "--adopt")
	r.ok("boss", "employ", "ember")

	// Nothing has moved: the guard holds, and doctor is quiet about it.
	got := doctored(t, r, asBoss(r), "doctor")
	if !strings.Contains(squeezed(got.stdout), "workspace drift") {
		t.Fatalf("the check is not listed at all:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "is working in") {
		t.Errorf("a fleet in agreement reported drift:\n%s", got.stdout)
	}

	// Moved while it runs: the session keeps the old directory, and its compiled
	// permissions name the old paths.
	r.ok("boss", "workspace", "ember", second, "--adopt")

	got = doctored(t, r, asBoss(r), "doctor")
	if got.code != fault.CodeConflict {
		t.Errorf("a drifted fleet exited %d, want %d:\n%s", got.code, fault.CodeConflict, got.stdout)
	}
	for _, want := range []string{"ember", first, second} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, got.stdout)
		}
	}
	if !strings.Contains(squeezed(got.stdout), "orc refresh") {
		t.Errorf("it does not say how to fix it:\n%s", got.stdout)
	}

	// And a replaced session clears it.
	r.ok("boss", "refresh", "ember")
	if after := doctored(t, r, asBoss(r), "doctor"); strings.Contains(after.stdout, "is working in") {
		t.Errorf("a refreshed session still reads as drifted:\n%s", after.stdout)
	}
}

// asBoss is the environment for a rig bootstrapped as boss rather than root.
func asBoss(r *rig) map[string]string {
	return map[string]string{"ORC_USER": "boss", "ORC_KEY": r.keys["boss"]}
}
