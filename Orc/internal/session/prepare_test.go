package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
)

// prepared builds a fleet with one identity that holds one clause, and prepares a
// session for it.
//
// It is separate from this package's other rig because that one is about a *running*
// supervisor and this is about the two files written before one starts.
func prepared(t *testing.T) (*store.Store, user.Name, string) {
	t.Helper()

	s, err := store.Create(filepath.Join(t.TempDir(), "fleet"), clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	boss := mustName(t, "boss")
	if _, err := s.CreateIdentity(boss, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("operator: %v", err)
	}
	ember := mustName(t, "ember")
	if _, err := s.CreateIdentity(ember, "0000000b-00000002", boss); err != nil {
		t.Fatalf("identity: %v", err)
	}

	patterns, err := model.ParsePatterns([]string{"read(Anno/**)", "write(Anno/internal/**)", "spawn(24)"})
	if err != nil {
		t.Fatalf("patterns: %v", err)
	}
	floor, err := model.NewAuthority(40)
	if err != nil {
		t.Fatalf("authority: %v", err)
	}
	perm, err := model.ParseName("edit-anno")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if _, err := s.CreatePermission(perm, floor, patterns); err != nil {
		t.Fatalf("permission: %v", err)
	}
	level, err := model.NewAuthority(60)
	if err != nil {
		t.Fatalf("authority: %v", err)
	}
	role, err := model.ParseName("engineer")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if _, err := s.CreateRole(role, level, "writes the code"); err != nil {
		t.Fatalf("role: %v", err)
	}
	if _, err := s.ApplyRole(role, func(model.Role) (model.RoleEvent, error) {
		return model.Permit(boss, epoch, perm)
	}); err != nil {
		t.Fatalf("permit: %v", err)
	}
	if _, err := s.ApplyIdentity(ember, func(model.Identity) (model.IdentityEvent, error) {
		return model.AssignRole(boss, epoch, role)
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	id, err := session.NewID()
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	return s, ember, id
}

func mustName(t *testing.T, raw string) user.Name {
	t.Helper()
	n, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("name %q: %v", raw, err)
	}
	return n
}

// TestPrepareWritesBothFiles: the snapshot the hook falls back to, and the settings
// Claude reads. Both are what the derivation says, not what a caller passed in.
func TestPrepareWritesBothFiles(t *testing.T) {
	s, who, id := prepared(t)

	if err := session.Prepare(s, who, id); err != nil {
		t.Fatalf("preparing: %v", err)
	}

	snapshot, found, err := s.ReadAuthz(who)
	if err != nil || !found {
		t.Fatalf("no snapshot: %v (found %v)", err, found)
	}
	if snapshot.Session != id {
		t.Errorf("the snapshot is for session %q, want %q", snapshot.Session, id)
	}
	if snapshot.Budget != 24 {
		t.Errorf("the snapshot's budget is %d, want 24 from the spawn clause", snapshot.Budget)
	}
	patterns, dropped := snapshot.Patterns()
	if dropped != 0 || len(patterns) != 3 {
		t.Errorf("the snapshot holds %d clauses (%d unreadable), want 3", len(patterns), dropped)
	}

	data, err := os.ReadFile(filepath.Join(s.ClaudeDir(who), "settings.json"))
	if err != nil {
		t.Fatalf("no settings: %v", err)
	}
	text := string(data)
	for _, want := range []string{"bypassPermissions", "orc-hook", "PreToolUse", `"Agent"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the settings are missing %s:\n%s", want, text)
		}
	}
	// The workspace is what a clause is relative to, so the compiled rules are
	// absolute — a relative rule would mean something different per cwd.
	if !strings.Contains(text, s.WorkspaceDir(who)) {
		t.Errorf("the compiled rules are not anchored to the workspace:\n%s", text)
	}
}

// TestPrepareIsIdempotent: preparing twice is what a supervisor restarting inside one
// session would do if the call ever moved, and it must not produce two different
// answers.
func TestPrepareIsIdempotent(t *testing.T) {
	s, who, id := prepared(t)

	if err := session.Prepare(s, who, id); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	first, err := os.ReadFile(s.AuthzPath(who))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if err := session.Prepare(s, who, id); err != nil {
		t.Fatalf("preparing again: %v", err)
	}
	second, err := os.ReadFile(s.AuthzPath(who))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	// The timestamp is the store's clock, which advances, so the two are compared on
	// the part that decides anything.
	if strip(string(first)) != strip(string(second)) {
		t.Errorf("preparing twice changed the snapshot:\n%s\n%s", first, second)
	}
}

func strip(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, `"at"`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestPrepareRefusesNonsense: an identity with no id, or no identity, is a caller
// mistake rather than something to write a file about.
func TestPrepareRefusesNonsense(t *testing.T) {
	s, who, id := prepared(t)

	if err := session.Prepare(nil, who, id); err == nil {
		t.Errorf("preparing with no store succeeded")
	}
	if err := session.Prepare(s, user.Name{}, id); err == nil {
		t.Errorf("preparing with no identity succeeded")
	}
	if err := session.Prepare(s, who, ""); err == nil {
		t.Errorf("preparing with no session id succeeded")
	}
}
