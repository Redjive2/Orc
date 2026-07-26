package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/store"
)

// Standing instructions are plain files beside the thing they describe, which is
// most of what is worth checking here: that they land where §3 says, that reading
// one back gives what was written, and that a layer nobody set is absent rather than
// empty.

func TestPromptsLandWhereTheyBelong(t *testing.T) {
	s, root := fresh(t)
	ember := mustUser(t, "ember")
	engineer := mustName(t, "engineer")

	for _, tc := range []struct {
		what   string
		target store.Target
		want   string
	}{
		{"the fleet's", store.FleetPrompt(false), filepath.Join("prompts", "system.md")},
		{"the fleet's wake", store.FleetPrompt(true), filepath.Join("prompts", "wake.md")},
		{"a role's", store.RolePrompt(engineer, false), filepath.Join("roles", "engineer", "prompt.md")},
		{"a role's wake", store.RolePrompt(engineer, true), filepath.Join("roles", "engineer", "wake.md")},
		{"an agent's", store.IdentityPrompt(ember, false), filepath.Join("identities", "ember", "prompt.md")},
		{"an agent's wake", store.IdentityPrompt(ember, true), filepath.Join("identities", "ember", "wake.md")},
	} {
		got, err := s.PromptPath(tc.target)
		if err != nil {
			t.Errorf("%s: %v", tc.what, err)
			continue
		}
		if want := filepath.Join(root, tc.want); got != want {
			t.Errorf("%s is at %s, want %s", tc.what, got, want)
		}
	}
}

// Beside the thing they describe, so removing a role takes its prompt with it rather
// than leaving an orphan nobody notices for months.
func TestARolePromptLivesInsideTheRole(t *testing.T) {
	s, root := fresh(t)
	engineer := mustName(t, "engineer")

	if err := s.WritePrompt(store.RolePrompt(engineer, false), editor(t), "you write the parser"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "roles", "engineer")); err != nil {
		t.Fatal(err)
	}

	if _, found, err := s.Prompt(store.RolePrompt(engineer, false)); err != nil || found {
		t.Errorf("the prompt outlived the role it belonged to: found=%v err=%v", found, err)
	}
}

func TestPromptRoundTrip(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")
	target := store.IdentityPrompt(ember, false)

	// Nothing set is not an error: most layers are empty most of the time.
	if _, found, err := s.Prompt(target); err != nil || found {
		t.Fatalf("a fresh store had a prompt: found=%v err=%v", found, err)
	}

	const text = "you are covering the parser this week.\n\nask atlas before you rewrite the lexer.\n"
	if err := s.WritePrompt(target, editor(t), text); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.Prompt(target)
	if err != nil || !found {
		t.Fatalf("reading it back: found=%v err=%v", found, err)
	}
	if got != text {
		t.Errorf("read back %q, want %q", got, text)
	}
}

// Empty text removes the layer rather than writing an empty file: "no layer" and "a
// layer that says nothing" compose identically, and two ways to spell one state is a
// state somebody eventually disagrees about.
func TestWritingNothingClearsIt(t *testing.T) {
	s, _ := fresh(t)
	target := store.FleetPrompt(false)

	if err := s.WritePrompt(target, editor(t), "ask before you guess"); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePrompt(target, editor(t), ""); err != nil {
		t.Fatal(err)
	}

	if _, found, err := s.Prompt(target); err != nil || found {
		t.Errorf("writing nothing left something: found=%v err=%v", found, err)
	}
	path, _ := s.PromptPath(target)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an empty file was left behind: %v", err)
	}
}

// Clearing what is not there satisfies the caller's intent either way.
func TestClearingNothingIsNotAnError(t *testing.T) {
	s, _ := fresh(t)
	if err := s.ClearPrompt(store.FleetPrompt(true), editor(t)); err != nil {
		t.Errorf("clearing an absent prompt: %v", err)
	}
}

// TestTheThreeLayersAreGatheredInOrder — the composition is only meaningful as a
// set, which is why the store hands back all three rather than one at a time.
func TestInstructionsGathersEveryLayer(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")
	engineer := mustName(t, "engineer")

	for target, text := range map[store.Target]string{
		store.FleetPrompt(false):           "ask before you guess",
		store.RolePrompt(engineer, false):  "you write the parser",
		store.IdentityPrompt(ember, false): "you are covering for atlas",
	} {
		if err := s.WritePrompt(target, editor(t), text); err != nil {
			t.Fatal(err)
		}
	}

	layers, err := s.Instructions(ember, engineer)
	if err != nil {
		t.Fatal(err)
	}
	if layers.System == "" || layers.Role == "" || layers.Identity == "" {
		t.Fatalf("a layer was not gathered: %+v", layers)
	}
	// The names travel with the text, because the headings say where each came from.
	if layers.RoleName != "engineer" || layers.IdentityName != "ember" {
		t.Errorf("the layers do not know whose they are: %+v", layers)
	}

	got, err := instruct.Compose(layers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ask before you guess", "the engineer role", "ember"} {
		if !strings.Contains(got, want) {
			t.Errorf("the composed prompt lacks %q:\n%s", want, got)
		}
	}
}

// An identity with no role composes what there is, rather than failing on the layer
// that has nowhere to come from.
func TestInstructionsWithNoRole(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")

	if err := s.WritePrompt(store.FleetPrompt(false), editor(t), "ask before you guess"); err != nil {
		t.Fatal(err)
	}
	layers, err := s.Instructions(ember, mustName(t, "engineer"))
	if err != nil {
		t.Fatal(err)
	}
	if layers.Role != "" {
		t.Errorf("a role with no prompt produced %q", layers.Role)
	}
	if layers.System == "" {
		t.Error("the fleet's layer was lost with the role's")
	}
}

// TestTheWakeChainIsWalkedInTheStore — the override rule, through the files.
func TestWakeMessageOverrides(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")
	engineer := mustName(t, "engineer")

	// Nothing set: the built-in bottom, so a fleet that configures nothing behaves
	// exactly as it did before any of this existed.
	got, from, err := s.WakeMessage(ember, engineer)
	if err != nil {
		t.Fatal(err)
	}
	if got != instruct.DefaultWake || from != "" {
		t.Errorf("an unconfigured fleet said %q from %q", got, from)
	}

	if err := s.WritePrompt(store.FleetPrompt(true), editor(t), "carry on"); err != nil {
		t.Fatal(err)
	}
	if got, from, _ := s.WakeMessage(ember, engineer); got != "carry on" || from != instruct.Wake {
		t.Errorf("the fleet's message: %q from %q", got, from)
	}

	if err := s.WritePrompt(store.RolePrompt(engineer, true), editor(t), "back to the parser"); err != nil {
		t.Fatal(err)
	}
	if got, from, _ := s.WakeMessage(ember, engineer); got != "back to the parser" || from != instruct.Role {
		t.Errorf("the role's message should win over the fleet's: %q from %q", got, from)
	}

	if err := s.WritePrompt(store.IdentityPrompt(ember, true), editor(t), "finish the lexer first"); err != nil {
		t.Fatal(err)
	}
	got, from, _ = s.WakeMessage(ember, engineer)
	if got != "finish the lexer first" || from != instruct.Identity {
		t.Errorf("the agent's own message should win: %q from %q", got, from)
	}
	// And only that one is sent.
	if strings.Contains(got, "parser") || strings.Contains(got, "carry on") {
		t.Errorf("the losing messages came too: %q", got)
	}
}

// A prompt over its bound is refused on the way in.
func TestWritingAnOversizedPromptIsRefused(t *testing.T) {
	s, _ := fresh(t)

	err := s.WritePrompt(store.FleetPrompt(false), editor(t), strings.Repeat("x", instruct.MaxLayer+1))
	if err == nil {
		t.Fatal("an oversized prompt was written")
	}
	if _, found, _ := s.Prompt(store.FleetPrompt(false)); found {
		t.Error("the refused prompt was written anyway")
	}
}

// TestAHandEditedFileIsCheckedOnTheWayOut. These are plain files an operator is
// expected to edit, so a prompt that would be refused on write must not be delivered
// because it arrived another way.
func TestAnOversizedFileOnDiskIsRefusedWhenRead(t *testing.T) {
	s, root := fresh(t)

	path := filepath.Join(root, "prompts", "system.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", instruct.MaxLayer+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Prompt(store.FleetPrompt(false)); err == nil {
		t.Error("a prompt too large to have been written was read back without complaint")
	}
}

// A target that names the wrong thing is a defect in the caller, not a user mistake.
func TestPromptPathRejectsAMisaddressedTarget(t *testing.T) {
	s, _ := fresh(t)

	for _, tc := range []struct {
		what   string
		target store.Target
	}{
		{"a role prompt with no role", store.Target{Kind: instruct.Role}},
		{"an identity prompt with no identity", store.Target{Kind: instruct.Identity}},
		{"the fleet's, named", store.Target{Kind: instruct.System, Identity: mustUser(t, "ember")}},
		{"a kind that does not exist", store.Target{Kind: "nonsense"}},
	} {
		if _, err := s.PromptPath(tc.target); err == nil {
			t.Errorf("%s was accepted", tc.what)
		}
	}
}

// editor is who made a change, for the tests that do not care which.
func editor(t *testing.T) user.Name { return mustUser(t, "boss") }
