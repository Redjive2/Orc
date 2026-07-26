package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// ownerRig is a fleet whose commands run with **no credential in the environment**,
// which is the state the owner fallback exists for.
//
// It is a separate helper from `rig.run` rather than a flag on it, because the two
// are testing opposite things: everything else in this suite presents a credential
// on purpose, and this presents none on purpose.
func (r *rig) asOwner(args ...string) result {
	r.t.Helper()
	return r.run("", args...) // no ORC_USER, no ORC_KEY
}

// TestOwnerFallbackNeedsNothing: with an empty environment, orc finds the operator's
// credential in the fleet it owns.
//
// This is the whole convenience: an operator on their own machine should not have to
// carry a key in their shell to look at their own fleet.
func TestOwnerFallbackNeedsNothing(t *testing.T) {
	r := fullFleet(t)

	got := r.asOwner("introspect", "--only", "identity")
	if got.code != fault.CodeOK {
		t.Fatalf("orc with no credential exited %d\n%s", got.code, got.stderr)
	}
	if strings.TrimSpace(got.stdout) != "boss" {
		t.Errorf("the fallback resolved to %q, want the operator", strings.TrimSpace(got.stdout))
	}

	// It works for ordinary commands too, not just introspect.
	if got := r.asOwner("status"); got.code != fault.CodeOK {
		t.Errorf("orc status with no credential exited %d\n%s", got.code, got.stderr)
	}

	// And `orc owner` says where the credential came from, because "why does orc
	// believe I am the operator" should have a visible answer.
	shown := r.asOwner("owner")
	if !strings.Contains(shown.stdout, "keyring") {
		t.Errorf("orc owner does not say the credential came from the keyring:\n%s", shown.stdout)
	}
	presented := r.ok("boss", "owner")
	if !strings.Contains(presented.stdout, "environment") {
		t.Errorf("orc owner does not say the credential came from the environment:\n%s", presented.stdout)
	}
}

// TestOwnerFallbackIsNarrow is the security half, and it matters more than the
// convenience: the fallback must not paper over a mistake or promote an agent.
func TestOwnerFallbackIsNarrow(t *testing.T) {
	r := fullFleet(t)

	// A half-set environment is a mistake, not an absence. Falling back here would
	// mean a typo in ORC_USER silently ran as the operator.
	half := r.runEnv(map[string]string{"ORC_USER": "atlas"}, "introspect", "--only", "identity")
	if half.code != fault.CodeAuth {
		t.Errorf("ORC_USER without ORC_KEY exited %d, want %d\n%s", half.code, fault.CodeAuth, half.stderr)
	}
	otherHalf := r.runEnv(map[string]string{"ORC_KEY": r.keys["atlas"]}, "introspect", "--only", "identity")
	if otherHalf.code != fault.CodeAuth {
		t.Errorf("ORC_KEY without ORC_USER exited %d, want %d", otherHalf.code, fault.CodeAuth)
	}

	// A presented credential is still checked. The fallback is not a way past a wrong
	// key: it only applies when nothing was presented at all.
	wrong := r.runEnv(map[string]string{"ORC_USER": "atlas", "ORC_KEY": r.keys["ember"]},
		"introspect", "--only", "identity")
	if wrong.code != fault.CodeAuth {
		t.Errorf("a wrong key exited %d, want %d", wrong.code, fault.CodeAuth)
	}

	// An agent that presents its own credential is itself, never the operator.
	agent := r.ok("ember", "introspect", "--only", "identity")
	if strings.TrimSpace(agent.stdout) != "ember" {
		t.Errorf("an agent resolved to %q", strings.TrimSpace(agent.stdout))
	}
}

// TestOwnerFallbackRefusesASharedStore: the argument for reading the keyring is that
// the caller could read it anyway. On a store that is not private, that argument
// fails — and so must the fallback.
func TestOwnerFallbackRefusesASharedStore(t *testing.T) {
	r := fullFleet(t)

	if err := os.Chmod(r.root, 0o755); err != nil {
		t.Skipf("cannot loosen the store's mode: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.root, 0o700) })

	got := r.asOwner("status")
	if got.code != fault.CodeAuth {
		t.Errorf("a world-readable store still allowed the fallback: exit %d\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "private") {
		t.Errorf("the refusal does not say why:\n%s", got.stderr)
	}
	// A presented credential still works: it is the *fallback* that is refused, not
	// the fleet.
	if got := r.ok("boss", "status"); got.code != fault.CodeOK {
		t.Errorf("a shared store refused a presented credential too")
	}
}

// TestOwnerRename: the operator keeps its key, its memories, its place in the tree,
// and its children — and the mailbox is re-provisioned under the new name.
func TestOwnerRename(t *testing.T) {
	r := fullFleet(t)
	key := r.keys["boss"]

	// Something in the operator's own memory directory, to prove it travels.
	memory := filepath.Join(r.root, "identities", "boss", "claude", "memory", "note.md")
	if err := os.MkdirAll(filepath.Dir(memory), 0o700); err != nil {
		t.Fatalf("memory dir: %v", err)
	}
	if err := os.WriteFile(memory, []byte("remember this\n"), 0o600); err != nil {
		t.Fatalf("memory: %v", err)
	}

	// Without --yes it explains what mail costs rather than doing it.
	if got := r.run("boss", "owner", "rename", "chief"); got.code != fault.CodeUsage {
		t.Fatalf("rename without --yes exited %d, want %d", got.code, fault.CodeUsage)
	} else if !strings.Contains(got.stderr, "mail") {
		t.Errorf("the confirmation does not mention the mailbox:\n%s", got.stderr)
	}

	got := r.ok("boss", "owner", "rename", "chief", "--yes")
	if !strings.Contains(got.stdout, "renamed") {
		t.Errorf("rename said nothing:\n%s", got.stdout)
	}

	// The same key still works, under the new name. That is the point of re-deriving
	// the digest rather than minting a fresh credential.
	r.keys["chief"] = key
	if got := r.ok("chief", "introspect", "--only", "authority"); strings.TrimSpace(got.stdout) != "100" {
		t.Errorf("the renamed operator has authority %q, want 100", strings.TrimSpace(got.stdout))
	}

	// The children came with it: their boss is the new name, and the fleet derives.
	fleet := r.ok("chief", "status")
	for _, want := range []string{"chief", "atlas", "ember", "quill"} {
		if !strings.Contains(fleet.stdout, want) {
			t.Errorf("the fleet lost %s after the rename:\n%s", want, fleet.stdout)
		}
	}
	if strings.Contains(fleet.stdout, "boss") {
		t.Errorf("the old name is still in the fleet:\n%s", fleet.stdout)
	}

	// The memory travelled, and the old directory is gone.
	moved := filepath.Join(r.root, "identities", "chief", "claude", "memory", "note.md")
	if data, err := os.ReadFile(moved); err != nil || !strings.Contains(string(data), "remember this") {
		t.Errorf("the operator's memories did not travel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "identities", "boss")); !os.IsNotExist(err) {
		t.Errorf("the old identity directory survived")
	}

	// The mailbox was re-provisioned under the new name and the old one retired.
	if !r.mailbox["chief"] {
		t.Errorf("no mailbox for the new name: %v", r.mailman)
	}
	if r.mailbox["boss"] {
		t.Errorf("the old mailbox survived: %v", r.mailman)
	}

	// And the owner fallback finds the new name without being told.
	if got := r.asOwner("introspect", "--only", "identity"); strings.TrimSpace(got.stdout) != "chief" {
		t.Errorf("the fallback resolved to %q after the rename", strings.TrimSpace(got.stdout))
	}
}

// TestOwnerRenameRefusals: a name that is taken, a live session, and anybody who is
// not the operator.
func TestOwnerRenameRefusals(t *testing.T) {
	r := fullFleet(t)

	if got := r.run("boss", "owner", "rename", "atlas", "--yes"); got.code != fault.CodeConflict {
		t.Errorf("renaming onto an existing identity exited %d, want %d", got.code, fault.CodeConflict)
	}

	// Authority is not the same as ownership. atlas is an architect at 80 and still
	// must not be able to rename the operator.
	denied := r.run("atlas", "owner", "rename", "chief", "--yes")
	if denied.code != fault.CodeDenied {
		t.Errorf("a non-operator renaming the operator exited %d, want %d", denied.code, fault.CodeDenied)
	}
	if !strings.Contains(denied.stderr, "position") {
		t.Errorf("the refusal does not explain that ownership is not a level:\n%s", denied.stderr)
	}

	// A live session holds the old paths, so the rename waits for it.
	r.ok("boss", "employ", "ember")
	if got := r.run("boss", "owner", "rename", "chief", "--yes"); got.code != fault.CodeOK {
		// ember is live, not the operator, so this must still succeed.
		t.Errorf("a live session elsewhere blocked the rename: %d\n%s", got.code, got.stderr)
	}
}

// TestOwnerReset: everything goes, and a fresh fleet is standing when it returns.
func TestOwnerReset(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	// The confirmation is specific: counts, by kind. "Everything" is not something
	// anybody can weigh.
	refused := r.run("boss", "owner", "reset")
	if refused.code != fault.CodeUsage {
		t.Fatalf("reset without --yes exited %d, want %d", refused.code, fault.CodeUsage)
	}
	for _, want := range []string{"4 identities", "1 employed", "role", "permission"} {
		if !strings.Contains(refused.stderr, want) {
			t.Errorf("the confirmation does not mention %q:\n%s", want, refused.stderr)
		}
	}

	got := r.ok("boss", "owner", "reset", "--yes", "--as", "chief")
	if !strings.Contains(got.stdout, "destroyed") || !strings.Contains(got.stdout, "bootstrapped") {
		t.Errorf("reset did not destroy and then bootstrap:\n%s", got.stdout)
	}

	// The session was stopped before the store went, so nothing is left running.
	if r.populated["ember"] != "" {
		t.Errorf("a session outlived the reset")
	}
	// Every mailbox was retired, which is what lets the name be provisioned again.
	for _, gone := range []string{"boss", "atlas", "ember", "quill"} {
		if r.mailbox[gone] {
			t.Errorf("%s's mailbox outlived the reset: %v", gone, r.mailman)
		}
	}

	// What is standing afterwards is a fleet of exactly one.
	r.keys["chief"] = keyFrom(t, got.stdout)
	fleet := r.ok("chief", "status")
	for _, gone := range []string{"atlas", "ember", "quill"} {
		if strings.Contains(fleet.stdout, gone) {
			t.Errorf("%s survived the reset:\n%s", gone, fleet.stdout)
		}
	}
	if got := r.ok("chief", "introspect", "--only", "authority"); strings.TrimSpace(got.stdout) != "100" {
		t.Errorf("the new operator has authority %q", strings.TrimSpace(got.stdout))
	}
	// And the policy went with it: a reset is not a partial wipe. Checked against the
	// JSON rather than by trying to use a role — assigning a role to *yourself* is
	// refused for an unrelated reason (nobody is their own subordinate), which would
	// make this assertion pass whether or not the role was there.
	shape := r.ok("chief", "status", "--json")
	for _, want := range []string{`"roles"`, `"permissions"`} {
		if strings.Contains(shape.stdout, want) && !strings.Contains(shape.stdout, want+": null") &&
			!strings.Contains(shape.stdout, want+": []") {
			t.Errorf("the reset fleet still has %s:\n%s", want, shape.stdout)
		}
	}
}

// TestOwnerResetNeedsTheOperator: the most destructive command in the tool is the
// one that most needs the narrowest gate.
func TestOwnerResetNeedsTheOperator(t *testing.T) {
	r := fullFleet(t)

	got := r.run("atlas", "owner", "reset", "--yes")
	if got.code != fault.CodeDenied {
		t.Fatalf("a non-operator reset exited %d, want %d", got.code, fault.CodeDenied)
	}
	// And nothing happened.
	if fleet := r.ok("boss", "status"); !strings.Contains(fleet.stdout, "ember") {
		t.Errorf("a refused reset removed something anyway:\n%s", fleet.stdout)
	}
}

// TestOwnerEnv finds the operator without being told who they are, which is what an
// operator wants after a rename or in a fresh shell.
func TestOwnerEnv(t *testing.T) {
	r := fullFleet(t)

	got := r.asOwner("owner", "env")
	if got.code != fault.CodeOK {
		t.Fatalf("owner env exited %d\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "export ORC_USER=boss") {
		t.Errorf("owner env does not name the operator:\n%s", got.stdout)
	}
	if keyFrom(t, got.stdout) != r.keys["boss"] {
		t.Errorf("owner env printed the wrong key")
	}
	if !strings.Contains(got.stderr, "credential") {
		t.Errorf("owner env does not say it printed a secret:\n%s", got.stderr)
	}
}
