package provision_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
	"orc/orc/internal/provision"
	"orc/orc/internal/store"
)

// settingsEpoch is this file's clock, named for this file rather than shared with
// provision_test.go: two files in one test package share a scope, whatever the file
// ownership says. That is a contention the concurrent plan did not foresee.
var settingsEpoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

func settingsFleet(t *testing.T) (*store.Store, user.Name) {
	t.Helper()

	s, err := store.Create(filepath.Join(t.TempDir(), "fleet"), clock.NewFake(settingsEpoch, time.Second))
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	who, err := user.Parse("ember")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if _, err := s.CreateIdentity(who, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return s, who
}

func settingsClause(t *testing.T, raw string) authz.Clause {
	t.Helper()
	p, err := model.ParsePattern(raw)
	if err != nil {
		t.Fatalf("pattern %q: %v", raw, err)
	}
	return authz.Clause{Pattern: p, Asked: p}
}

// settingsRead reads back what was written, as a map, because the file's shape is
// Claude's rather than Orc's and a typed struct here would be a second guess at it.
func settingsRead(t *testing.T, s *store.Store, who user.Name) map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(s.ClaudeDir(who), "settings.json"))
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("settings are not json: %v\n%s", err, data)
	}
	return out
}

func settingsRules(t *testing.T, settings map[string]any, key string) []string {
	t.Helper()

	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("no permissions in the settings")
	}
	raw, ok := perms[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(string))
	}
	return out
}

// TestCompiledRules: clauses become rules Claude understands, and they are absolute —
// a relative rule would mean something different depending on the session's cwd.
func TestCompiledRules(t *testing.T) {
	s, who := settingsFleet(t)
	ws := s.WorkspaceDir(who)

	if err := provision.WriteSettings(s, who, provision.SettingsSpec{
		Clauses: []authz.Clause{
			settingsClause(t, "read(Anno/**)"),
			settingsClause(t, "write(Anno/internal/**)"),
			settingsClause(t, "spawn(24)"),
		},
		OrcHome:   s.Root(),
		Workspace: ws,
	}); err != nil {
		t.Fatalf("compiling: %v", err)
	}

	settings := settingsRead(t, s, who)
	allow := strings.Join(settingsRules(t, settings, "allow"), " ")
	deny := strings.Join(settingsRules(t, settings, "deny"), " ")

	for _, want := range []string{
		"Read(" + ws + "/Anno/**)",
		"Edit(" + ws + "/Anno/internal/**)",
		"Write(" + ws + "/Anno/internal/**)",
	} {
		if !strings.Contains(allow, want) {
			t.Errorf("the allow list is missing %s:\n%s", want, allow)
		}
	}
	// A write clause becomes Edit *and* Write: Orc's model does not distinguish
	// changing a file from creating one, and a rule set that did would allow half of
	// what a permission says.
	if strings.Contains(allow, "spawn") {
		t.Errorf("a spawn clause became a file rule: %s", allow)
	}

	for _, want := range []string{
		"Agent",
		"Read(" + s.Root() + "/identities/*/key)",
		"Read(" + s.Root() + "/identities/*/session/**)",
		// `Edit`, not `Write`. Claude matches deny rules for file edits against
		// `Edit(path)` only, and an Edit rule covers every file-editing tool — so a
		// `Write(path)` deny rule is not a second fence, it is one that matches
		// nothing, and Claude warns about each one on stderr at every session start.
		"Edit(" + s.Root() + "/roles/**)",
		"Edit(" + s.Root() + "/identities/*/claude/settings.json)",
	} {
		if !strings.Contains(deny, want) {
			t.Errorf("the deny list is missing %s:\n%s", want, deny)
		}
	}

	// The rule that the obvious version of this got wrong: an identity's workspace is
	// *inside* the store, so denying the root would deny an agent its own files. The
	// deny list must not reach it, and must not reach the agent's memories either.
	if strings.Contains(deny, "Read("+s.Root()+"/**)") {
		t.Errorf("the deny list denies the whole store, which contains the workspace:\n%s", deny)
	}
	for _, mine := range []string{ws + "/**", "claude/memory", "claude/CLAUDE.md"} {
		if strings.Contains(deny, mine) {
			t.Errorf("the deny list reaches %s, which is the agent's own:\n%s", mine, deny)
		}
	}

	// The mode is stated in the file as well as on the command line. The flag is the
	// authoritative one; this is so the file describes the session it configures.
	perms := settings["permissions"].(map[string]any)
	if perms["defaultMode"] != "bypassPermissions" {
		t.Errorf("the default mode is %v, want bypassPermissions", perms["defaultMode"])
	}
}

// TestHooksAreWired: the enforcing hook and the feed are both there, and both call the
// bare binary name so the session's PATH decides which one runs.
func TestHooksAreWired(t *testing.T) {
	s, who := settingsFleet(t)

	if err := provision.WriteSettings(s, who, provision.SettingsSpec{
		OrcHome: s.Root(), Workspace: s.WorkspaceDir(who),
	}); err != nil {
		t.Fatalf("compiling: %v", err)
	}

	hooks, ok := settingsRead(t, s, who)["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks in the settings")
	}

	pre, ok := hooks["PreToolUse"].([]any)
	if !ok || len(pre) == 0 {
		t.Fatalf("PreToolUse is not wired")
	}
	entry := pre[0].(map[string]any)
	if matcher, _ := entry["matcher"].(string); !strings.Contains(matcher, "Agent") {
		t.Errorf("the enforcing matcher does not cover the Agent tool: %q", matcher)
	}
	inner := entry["hooks"].([]any)[0].(map[string]any)
	if inner["command"] != provision.HookBinary {
		t.Errorf("the hook runs %v, want the bare name %q", inner["command"], provision.HookBinary)
	}

	for _, name := range provision.FeedEvents {
		if _, ok := hooks[name]; !ok {
			t.Errorf("the feed event %s is not wired", name)
		}
	}
}

// TestUnmanagedKeysSurvive: an operator who added something to an identity's settings
// keeps it. A populate that silently discarded an MCP server would be a populate
// nobody could trust with a configured fleet.
func TestUnmanagedKeysSurvive(t *testing.T) {
	s, who := settingsFleet(t)

	if err := s.WriteClaudeFile(who, "settings.json", []byte(`{
  "model": "opus",
  "mcpServers": {"weather": {"command": "weather-mcp"}},
  "permissions": {"allow": ["Read(/old/**)"], "ask": ["Bash"]}
}`+"\n")); err != nil {
		t.Fatalf("seeding settings: %v", err)
	}

	if err := provision.WriteSettings(s, who, provision.SettingsSpec{
		Clauses: []authz.Clause{settingsClause(t, "read(Anno/**)")},
		OrcHome: s.Root(), Workspace: s.WorkspaceDir(who),
	}); err != nil {
		t.Fatalf("compiling: %v", err)
	}

	settings := settingsRead(t, s, who)
	if settings["model"] != "opus" {
		t.Errorf("an unmanaged key was lost: model is %v", settings["model"])
	}
	if _, ok := settings["mcpServers"]; !ok {
		t.Errorf("mcpServers was lost")
	}

	// Orc's own keys are replaced rather than merged: a stale allow rule from a
	// permission that has since been revoked would be a permission that outlived it.
	allow := strings.Join(settingsRules(t, settings, "allow"), " ")
	if strings.Contains(allow, "/old/**") {
		t.Errorf("a stale allow rule survived: %s", allow)
	}
	// But an unmanaged key *inside* permissions does survive.
	perms := settings["permissions"].(map[string]any)
	if _, ok := perms["ask"]; !ok {
		t.Errorf("permissions.ask was lost; orc manages allow, deny, and defaultMode only")
	}
}

// TestUnparseableSettingsAreLeftAlone: rewriting a file on a guess about its shape is
// how a working configuration becomes a broken one.
func TestUnparseableSettingsAreLeftAlone(t *testing.T) {
	s, who := settingsFleet(t)

	const broken = "{ this is not json at all\n"
	if err := s.WriteClaudeFile(who, "settings.json", []byte(broken)); err != nil {
		t.Fatalf("seeding settings: %v", err)
	}

	err := provision.WriteSettings(s, who, provision.SettingsSpec{
		OrcHome: s.Root(), Workspace: s.WorkspaceDir(who),
	})
	if err == nil {
		t.Fatalf("compiling over unparseable settings succeeded")
	}
	if !strings.Contains(err.Error(), "left the file alone") {
		t.Errorf("the error does not say the file was left alone: %v", err)
	}

	data, readErr := os.ReadFile(filepath.Join(s.ClaudeDir(who), "settings.json"))
	if readErr != nil {
		t.Fatalf("reading back: %v", readErr)
	}
	if string(data) != broken {
		t.Errorf("the file was rewritten:\n%s", data)
	}
}

// TestSnapshotRoundTrip: what the hook's second rung reads is what the first rung
// decided from, expressed in the same patterns.
func TestSnapshotRoundTrip(t *testing.T) {
	s, who := settingsFleet(t)

	clauses := []authz.Clause{
		settingsClause(t, "read(Anno/**)"),
		settingsClause(t, "write(Anno/internal/**)"),
	}
	if err := s.WriteAuthz(who, store.Freeze(who, "0f9a1a6a-0000-4000-8000-000000000000",
		clock.Format(settingsEpoch), clauses, 24)); err != nil {
		t.Fatalf("writing the snapshot: %v", err)
	}

	got, found, err := s.ReadAuthz(who)
	if err != nil || !found {
		t.Fatalf("reading the snapshot: %v (found %v)", err, found)
	}
	if got.Budget != 24 || got.Session == "" {
		t.Errorf("the snapshot lost its budget or its session: %+v", got)
	}

	patterns, dropped := got.Patterns()
	if dropped != 0 {
		t.Errorf("%d clauses could not be rebuilt", dropped)
	}
	var rebuilt []string
	for _, p := range patterns {
		rebuilt = append(rebuilt, p.String())
	}
	if strings.Join(rebuilt, " ") != "read(Anno/**) write(Anno/internal/**)" {
		t.Errorf("the snapshot rebuilt as %v", rebuilt)
	}

	// A missing snapshot is not an error: it is the third rung, and the hook treats it
	// as one rather than as damage.
	other, err := user.Parse("atlas")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if _, found, err := s.ReadAuthz(other); err != nil || found {
		t.Errorf("a missing snapshot reported found=%v err=%v", found, err)
	}
}
