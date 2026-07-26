package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/nudge"
)

func spec(dir string) Spec {
	return Spec{
		ProbeID:      "abc-123",
		ProbeName:    "scratch",
		ProbeDir:     dir,
		MailmanDir:   filepath.Join(dir, "state", "mailman"),
		MacmuffinDir: filepath.Join(dir, "state", "macmuffin"),
		CQDir:        filepath.Join(dir, "state", "cq"),
		OrcDir:       filepath.Join(dir, "state", "orc"),
		XDGDir:       filepath.Join(dir, "state", "xdg"),
		BinDir:       filepath.Join(dir, "bin"),
		ClaudeDir:    filepath.Join(dir, "claude"),
		GitConfig:    filepath.Join(dir, "repo", ".probe-gitconfig"),
		BasePath:     "/usr/bin:/bin",
	}
}

func TestComposeRedirectsEveryStore(t *testing.T) {
	vars, err := Compose(spec("/probes/scratch"))
	if err != nil {
		t.Fatal(err)
	}

	// NoNudge must be the name orc/common/nudge actually reads. It was
	// CQ_NO_NUDGE for four milestones — a variable nothing consults, which made
	// one of the four independent stops in front of the network a no-op.
	if NoNudge != nudge.Suppress {
		t.Fatalf("the nudge suppressor is %q; orc/common/nudge reads %q", NoNudge, nudge.Suppress)
	}

	for _, key := range []string{Active, MailmanHome, MacmuffinHome, CQHome, OrcHome, XDGData, XDGState, NoNudge, ClaudeConfig, GitConfig, Path} {
		value, ok := Lookup(vars, key)
		if !ok {
			t.Fatalf("%s is not set; a store that is not redirected is a store that stays real", key)
		}
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is set but empty", key)
		}
	}
	if home, ok := Lookup(vars, Home); ok {
		t.Fatalf("HOME was redirected to %q without --fake-home", home)
	}
}

// TestComposeEnforcesIsolationNotIdentity is the rule the whole shim design
// rests on, so it is pinned here rather than left to the comment.
func TestComposeEnforcesIsolationNotIdentity(t *testing.T) {
	vars, err := Compose(spec("/probes/scratch"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if !v.Enforced {
			t.Fatalf("%s is not enforced; every variable Compose sets is isolation", v.Key)
		}
	}
	for _, v := range Identity("alice", "key") {
		if v.Enforced {
			t.Fatalf("%s is enforced; a shim that restored identity would break `as`", v.Key)
		}
	}
}

func TestPathPutsShimsFirstAndOnlyOnce(t *testing.T) {
	s := spec("/probes/scratch")
	s.BasePath = "/probes/scratch/bin:/usr/bin:/bin"
	vars, err := Compose(s)
	if err != nil {
		t.Fatal(err)
	}

	path, _ := Lookup(vars, Path)
	parts := filepath.SplitList(path)
	if parts[0] != s.BinDir {
		t.Fatalf("PATH starts with %q, want the probe's bin", parts[0])
	}
	count := 0
	for _, p := range parts {
		if p == s.BinDir {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the probe bin appears %d times in PATH; a repeated entry lets a stale one win", count)
	}
}

func TestRenderAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original, err := Compose(spec("/probes/scratch"))
	if err != nil {
		t.Fatal(err)
	}
	// A value with a quote in it is the case the shell quoting has to survive.
	original = append(original, Var{Key: "ODD", Value: `it's "quoted" \ odd`, Enforced: false, Why: "awkward"})

	path := filepath.Join(dir, "env")
	if err := Write(path, original); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(original) {
		t.Fatalf("loaded %d variables, wrote %d", len(loaded), len(original))
	}
	for i, want := range original {
		got := loaded[i]
		if got.Key != want.Key || got.Value != want.Value {
			t.Fatalf("variable %d round-tripped as %s=%q, want %s=%q", i, got.Key, got.Value, want.Key, want.Value)
		}
		if got.Enforced != want.Enforced {
			t.Fatalf("%s round-tripped enforced=%v, want %v — the shim reads this to decide what to restore",
				got.Key, got.Enforced, want.Enforced)
		}
	}
}

func TestLoadRefusesSomethingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := Write(path, []Var{{Key: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := appendLine(t, path, "MAILMAN_HOME=/real/store"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a line it does not write; a line it misreads is a hole in the wall")
	}
}

func TestApplyOverlaysAndSorts(t *testing.T) {
	base := []string{"PATH=/bin", "TERM=xterm", "MAILMAN_HOME=/real/store"}
	out := Apply(base,
		[]Var{{Key: MailmanHome, Value: "/probe/state/mailman", Enforced: true}},
		Identity("alice", "k"))

	found := map[string]string{}
	for _, entry := range out {
		key, value, _ := strings.Cut(entry, "=")
		found[key] = value
	}
	if found[MailmanHome] != "/probe/state/mailman" {
		t.Fatalf("MAILMAN_HOME is %q; the probe's value must win over the ambient one", found[MailmanHome])
	}
	if found["TERM"] != "xterm" {
		t.Fatal("TERM was lost; a probe shell still needs a terminal")
	}
	if found[User] != "alice" {
		t.Fatalf("ORC_USER is %q, want alice", found[User])
	}

	for i := 1; i < len(out); i++ {
		if out[i-1] > out[i] {
			t.Fatal("the environment is not sorted; two runs of one command must produce the same one")
		}
	}
}

func appendLine(t *testing.T, path, line string) error {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, []byte("\n"+line+"\n")...), 0o600)
}
