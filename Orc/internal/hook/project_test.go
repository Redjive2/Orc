package hook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/orc/internal/hook"
	"orc/orc/internal/model"
)

// A path clause is measured from the project, not from the workspace.
//
// It used to be workspace-relative, which made it two things at once: the fence
// around what an agent could touch and the ruler those touches were measured with.
// Taking the fence away left the ruler measuring nothing — every clause was dead,
// and an agent outside its workspace was refused and told to ask for a permission
// that could not exist.
//
// The project is what a clause was always describing. `read(Docs/**)` is the
// repository's `Docs`, which is the same directory whichever agent reads it.

// inRepo moves the agent's workspace into a git checkout of its own and returns
// the two paths.
//
// Outside the fleet's store, deliberately. The default workspace sits under
// `<store>/identities/<name>/workspace`, so its parent is the identity directory —
// and making *that* the project would put every clause inside the one place the
// escape check refuses before any permission is consulted. A real fleet points
// workspaces at a checkout, which is what this builds.
func inRepo(t *testing.T, r *rig) (project, workspace string) {
	t.Helper()
	project = t.TempDir()
	workspace = filepath.Join(project, "agents", "ember")
	for _, at := range []string{filepath.Join(project, ".git"), workspace} {
		if err := os.MkdirAll(at, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.store.ApplyIdentity(r.who, func(model.Identity) (model.IdentityEvent, error) {
		return model.SetWorkspace(r.who, r.store.Now(), workspace)
	}); err != nil {
		t.Fatal(err)
	}
	return project, workspace
}

func TestAClauseReachesIntoTheProject(t *testing.T) {
	r := newRig(t)
	project, _ := inRepo(t, r)
	r.permit("docs", "read(Docs/**)")
	opts := r.as("ember", nil)

	at := filepath.Join(project, "Docs", "Vision.md")
	if out := r.call(opts, tool("Read", at, project)); out.Code != hook.CodeOK {
		t.Errorf("a clause naming Docs did not reach the project's Docs:\n%s", out.Stderr)
	}
	// And it is the clause doing the work, not the project being open.
	other := filepath.Join(project, "Secrets", "keys.txt")
	out := r.call(opts, tool("Read", other, project))
	if out.Code != hook.CodeBlock {
		t.Error("the whole project was readable, so the clause narrowed nothing")
	}
	if !strings.Contains(out.Stderr, "no permission of yours covers") {
		t.Errorf("the refusal does not say a permission is what is missing:\n%s", out.Stderr)
	}
	// The refusal names the clause that would cover it, so the ask is exact.
	if !strings.Contains(out.Stderr, "read(Secrets/keys.txt)") {
		t.Errorf("the refusal does not name a clause to ask for:\n%s", out.Stderr)
	}
}

// The kind still matters: a read clause is not a write clause.
func TestAReadClauseDoesNotGrantAWrite(t *testing.T) {
	r := newRig(t)
	project, _ := inRepo(t, r)
	r.permit("docs", "read(Docs/**)")
	opts := r.as("ember", nil)

	at := filepath.Join(project, "Docs", "Vision.md")
	if out := r.call(opts, tool("Read", at, project)); out.Code != hook.CodeOK {
		t.Fatalf("the read was blocked:\n%s", out.Stderr)
	}
	if out := r.call(opts, tool("Write", at, project)); out.Code != hook.CodeBlock {
		t.Error("a read clause allowed a write")
	}
}

// Outside the project is a different refusal, because it is a different situation:
// no clause reaches there, so it is not a permission to ask for.
func TestOutsideTheProjectIsNotAPermissionAway(t *testing.T) {
	r := newRig(t)
	project, _ := inRepo(t, r)
	r.permit("wide", "read(**)", "write(**)")
	opts := r.as("ember", nil)

	out := r.call(opts, tool("Read", filepath.Join(filepath.Dir(project), "elsewhere.txt"), project))
	if out.Code != hook.CodeBlock {
		t.Fatal("a path outside the project was readable with a project-wide clause")
	}
	if !strings.Contains(out.Stderr, "outside the project") {
		t.Errorf("it was refused as though a permission would help:\n%s", out.Stderr)
	}
}

// And the workspace is still the agent's, entirely — the half of this that was
// already true has to stay true. A change that rooted clauses at the project and
// quietly made the workspace need one would be the worse bug.
func TestTheWorkspaceNeedsNoClauseStill(t *testing.T) {
	r := newRig(t)
	_, workspace := inRepo(t, r)
	opts := r.as("ember", nil)

	for _, path := range []string{"/notes.md", "/go.mod", "/deep/unanticipated.txt"} {
		for _, verb := range []string{"Read", "Write", "Edit"} {
			if out := r.call(opts, tool(verb, workspace+path, workspace)); out.Code != hook.CodeOK {
				t.Errorf("%s %s in its own workspace needed a clause:\n%s", verb, path, out.Stderr)
			}
		}
	}
}

// A workspace in no repository falls back to itself, which is the narrow
// direction. Rooting a clause at a parent nobody chose would widen what a
// permission reaches by accident.
func TestNoRepositoryMeansTheWorkspaceIsTheProject(t *testing.T) {
	r := newRig(t)
	workspace := r.workspace()
	r.permit("wide", "read(**)")
	opts := r.as("ember", nil)

	outside := filepath.Join(filepath.Dir(workspace), "sibling.txt")
	if out := r.call(opts, tool("Read", outside, workspace)); out.Code != hook.CodeBlock {
		t.Error("with no repository, a clause reached outside the workspace")
	}
}
