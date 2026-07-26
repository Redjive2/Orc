package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
)

// supervisorFor builds one without running it. Args() is a pure question about a
// composed session, so nothing here needs a pty, a socket, or a child.
func supervisorFor(t *testing.T, s *store.Store, who user.Name) *session.Supervisor {
	t.Helper()

	sup, err := session.New(s, session.Spec{
		Identity: who, ID: "0000000a-00000001",
		Model: model.ModelSonnet, Effort: model.EffortMedium,
	}, nil, "claude")
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	return sup
}

// writeRaw puts a file in the store behind its own API, for the cases that are
// about a file somebody edited by hand.
func writeRaw(t *testing.T, s *store.Store, rel, body string) {
	t.Helper()

	path := filepath.Join(s.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Delivery: the composed prompt reaches the session as `--append-system-prompt`.
//
// The flag was checked against the real `claude` before this was built
// (Instruct.md §5, §14): 2.1.220 documents it, and an unknown flag errors, so
// acceptance means it is parsed. What is tested here is that Orc passes it, with
// what, and — the case that matters more — that a fleet which has set no prompts
// passes nothing at all.

func TestNoPromptsMeansNoFlag(t *testing.T) {
	s, who := fleet(t, "ember")
	sup := supervisorFor(t, s, who)

	for _, arg := range sup.Args() {
		if arg == "--append-system-prompt" {
			t.Errorf("a fleet with no prompts still passed one: %v", sup.Args())
		}
	}
}

// TestTheComposedPromptIsPassed, layers and all.
func TestTheComposedPromptReachesTheSession(t *testing.T) {
	s, who := fleet(t, "ember")

	if err := s.WritePrompt(store.FleetPrompt(false), who, "ask before you guess"); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePrompt(store.IdentityPrompt(who, false), who, "you are covering the parser"); err != nil {
		t.Fatal(err)
	}

	args := supervisorFor(t, s, who).Args()

	got := ""
	for i, arg := range args {
		if arg == "--append-system-prompt" && i+1 < len(args) {
			got = args[i+1]
		}
	}
	if got == "" {
		t.Fatalf("the prompt was not passed: %v", args)
	}
	for _, want := range []string{"ask before you guess", "you are covering the parser", "# the fleet"} {
		if !strings.Contains(got, want) {
			t.Errorf("the composed prompt lacks %q:\n%s", want, got)
		}
	}
}

// The prompt is composed once for the session's life, so a restart continues under
// the instructions the conversation has been following rather than under whatever
// somebody has edited since.
func TestThePromptDoesNotChangeUnderARunningSession(t *testing.T) {
	s, who := fleet(t, "ember")
	if err := s.WritePrompt(store.FleetPrompt(false), who, "the original"); err != nil {
		t.Fatal(err)
	}

	sup := supervisorFor(t, s, who)
	before := strings.Join(sup.Args(), " ")

	if err := s.WritePrompt(store.FleetPrompt(false), who, "edited while it was running"); err != nil {
		t.Fatal(err)
	}
	if after := strings.Join(sup.Args(), " "); after != before {
		t.Errorf("the prompt changed underneath a live session:\n%s", after)
	}
	if strings.Contains(before, "edited while it was running") {
		t.Error("the edit reached a session that was already started")
	}
}

// A prompt file too large to have been written is refused when it is read, and the
// session still starts — an agent that cannot think is worse than one missing a
// layer somebody added.
func TestABrokenPromptDoesNotStopTheSession(t *testing.T) {
	s, who := fleet(t, "ember")
	writeRaw(t, s, "prompts/system.md", strings.Repeat("x", instruct.MaxLayer+1))

	sup := supervisorFor(t, s, who)
	args := sup.Args()

	for _, arg := range args {
		if arg == "--append-system-prompt" {
			t.Errorf("an unreadable prompt was passed anyway: %v", args)
		}
	}
	// And the session is still a session.
	if len(args) == 0 || args[0] != "--session-id" {
		t.Errorf("the session lost its arguments with its prompt: %v", args)
	}
}
