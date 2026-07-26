package hook_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/user"
	"orc/macmuffin/internal/hook"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// rig is a store, a git worktree, and a task bound to it — the situation the
// hook exists for.
type rig struct {
	t    testing.TB
	root string // store
	tree string // worktree
	env  map[string]string
}

func newRig(t testing.TB) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{
		t:    t,
		root: filepath.Join(base, "store"),
		tree: filepath.Join(base, "tree"),
		env:  map[string]string{},
	}
	// A worktree is a directory with a .git in it; repo reads it directly rather
	// than shelling out, so this is all it takes.
	for _, dir := range []string{r.tree, filepath.Join(r.tree, ".git"), filepath.Join(r.tree, "internal", "tree")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"internal/tree/tree.go", "internal/render.go", "README.md"} {
		if err := os.WriteFile(filepath.Join(r.tree, filepath.FromSlash(f)), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *rig) opts() hook.Options {
	return hook.Options{
		Root:  r.root,
		Env:   func(k string) (string, bool) { v, ok := r.env[k]; return v, ok },
		Clock: clock.NewFake(epoch, time.Millisecond),
	}
}

// task builds a scoped task, and binds it to the worktree unless bind is false.
func (r *rig) task(name string, entries []string, bind bool) {
	r.t.Helper()

	s, err := store.Open(r.root, clock.NewFake(epoch, time.Millisecond))
	if err != nil {
		r.t.Fatal(err)
	}
	n, err := task.ParseName(name)
	if err != nil {
		r.t.Fatal(err)
	}
	alice, err := user.Parse("alice")
	if err != nil {
		r.t.Fatal(err)
	}
	p, _ := task.NewPriority(3)
	d, _ := task.NewDifficulty(3)
	if _, err := s.Create(n, alice, p, d); err != nil {
		r.t.Fatal(err)
	}
	if len(entries) > 0 {
		if _, err := s.Apply(n, func(task.Task) (task.Event, error) {
			return task.Scope(alice, s.Now(), entries)
		}); err != nil {
			r.t.Fatal(err)
		}
	}
	if bind {
		if _, err := s.Apply(n, func(task.Task) (task.Event, error) {
			return task.BindWorktree(alice, s.Now(), r.tree)
		}); err != nil {
			r.t.Fatal(err)
		}
		if err := s.Bind(n, r.tree, r.tree); err != nil {
			r.t.Fatal(err)
		}
	}
}

// event builds a PreToolUse payload editing the given path.
func (r *rig) event(tool, path string) []byte {
	r.t.Helper()
	return r.raw(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       tool,
		"cwd":             r.tree,
		"tool_input":      map[string]any{"file_path": path},
	})
}

func (r *rig) bash(command string) []byte {
	r.t.Helper()
	return r.raw(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"cwd":             r.tree,
		"tool_input":      map[string]any{"command": command},
	})
}

func (r *rig) raw(v map[string]any) []byte {
	r.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		r.t.Fatal(err)
	}
	return data
}

func (r *rig) run(input []byte) hook.Outcome {
	r.t.Helper()
	return hook.Run(input, r.opts())
}

// TestABlockIsOnlyEverAGenuineViolation.
func TestBlocksAnOutOfScopeEdit(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	got := r.run(r.event("Edit", filepath.Join(r.tree, "internal/render.go")))
	if got.Code != hook.CodeBlock {
		t.Fatalf("an out-of-scope edit exited %d, want %d\n%s", got.Code, hook.CodeBlock, got.Stderr)
	}
	// The message says what was refused, what is allowed, and how to proceed. A
	// refusal that does not say how to proceed just gets worked around.
	for _, want := range []string{"internal/render.go", "fix-the-parser", "in scope", "internal/tree/", "muff scope"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got.Stderr)
		}
	}
}

func TestAllowsAnInScopeEdit(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	for _, path := range []string{
		filepath.Join(r.tree, "internal/tree/tree.go"), // absolute
		"internal/tree/tree.go",                        // relative to the session's cwd
	} {
		got := r.run(r.event("Edit", path))
		if got.Code != hook.CodeOK {
			t.Errorf("%s was blocked: %s", path, got.Stderr)
		}
	}
}

// TestMUFFTASKOutranksTheBinding: an agent that says what it is working on is
// believed, since that is §8.1's first rule.
func TestEnvTaskWins(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)
	r.task("write-the-docs", []string{"README.md"}, false)

	r.env[hook.EnvTask] = "write-the-docs"
	if got := r.run(r.event("Edit", filepath.Join(r.tree, "internal/tree/tree.go"))); got.Code != hook.CodeBlock {
		t.Errorf("the bound task was enforced instead of %s: %d", "write-the-docs", got.Code)
	}
	if got := r.run(r.event("Edit", filepath.Join(r.tree, "README.md"))); got.Code != hook.CodeOK {
		t.Errorf("an edit in MUFF_TASK's scope was blocked: %s", got.Stderr)
	}
}

// TestAnnoWritesAreCheckedThroughBash is the belt-and-braces half of §8.3.
func TestBashAnnoWrite(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	blocked := []string{
		"anno write internal/render.go body",
		"cd /somewhere && anno write internal/render.go body",
		"/usr/local/bin/anno write internal/render.go",
		"anno write 'internal/render.go'",
		"go build ./... && anno write internal/render.go",
	}
	for _, command := range blocked {
		if got := r.run(r.bash(command)); got.Code != hook.CodeBlock {
			t.Errorf("%q exited %d, want a block", command, got.Code)
		}
	}

	// In scope, and shapes that say nothing about what will be written.
	for _, command := range []string{
		"anno write internal/tree/tree.go body",
		"anno index internal/render.go",
		"echo hello > internal/render.go", // out of reach, and the docs say so
		"rm -rf internal/render.go",
		"anno write",
		"anno write --help",
	} {
		if got := r.run(r.bash(command)); got.Code != hook.CodeOK {
			t.Errorf("%q was blocked: %s", command, got.Stderr)
		}
	}
}

// TestEveryEditInAMultiEditIsChecked: they are usually one file, but nothing
// promises that.
func TestMultiEditTargets(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	input := r.raw(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "MultiEdit",
		"cwd":             r.tree,
		"tool_input": map[string]any{
			"edits": []map[string]any{
				{"file_path": filepath.Join(r.tree, "internal/tree/tree.go")},
				{"file_path": filepath.Join(r.tree, "internal/render.go")},
			},
		},
	})
	if got := r.run(input); got.Code != hook.CodeBlock {
		t.Errorf("a MultiEdit whose second edit is out of scope exited %d", got.Code)
	}
}

// TestNothingUnexpectedEverBlocks is §8.4 rule 1, and the reason this hook is
// safe to install. Everything here must pass.
func TestNothingUnexpectedEverBlocks(t *testing.T) {
	setup := map[string]func(*rig){
		"a scoped, bound task": func(r *rig) { r.task("fix-the-parser", []string{"internal/tree/"}, true) },
		// A scopeless task cannot be bound to a worktree, so it comes into
		// force the other way §8.1 allows.
		"a scopeless task": func(r *rig) {
			r.task("fix-the-parser", nil, false)
			r.env[hook.EnvTask] = "fix-the-parser"
		},
		"no store at all": func(r *rig) {},
	}

	cases := map[string][]byte{
		"empty input":            nil,
		"not json":               []byte("{{{"),
		"json but not an object": []byte(`"hello"`),
		"an empty object":        []byte(`{}`),
		"null":                   []byte(`null`),
		"a huge number":          []byte(`{"hook_event_name": 12345678901234567890}`),
	}

	for name, run := range setup {
		for what, input := range cases {
			r := newRig(t)
			run(r)
			if got := r.run(input); got.Code != hook.CodeOK {
				t.Errorf("with %s, %s exited %d\n%s", name, what, got.Code, got.Stderr)
			}
		}
	}

	// And the same for well-formed events that are simply not this hook's
	// business. Each is built against a store where a violation *would* block,
	// so a pass here is the payload being ignored rather than the check failing.
	events := map[string]map[string]any{
		"another event": {
			"hook_event_name": "PostToolUse", "tool_name": "Edit", "cwd": "",
			"tool_input": map[string]any{"file_path": "internal/render.go"},
		},
		"no event name": {
			"tool_name": "Edit", "tool_input": map[string]any{"file_path": "internal/render.go"},
		},
		"a tool we do not care about": {
			"hook_event_name": "PreToolUse", "tool_name": "Read",
			"tool_input": map[string]any{"file_path": "internal/render.go"},
		},
		"an unknown tool": {
			"hook_event_name": "PreToolUse", "tool_name": "Telepathy",
			"tool_input": map[string]any{"file_path": "internal/render.go"},
		},
		"no tool input": {
			"hook_event_name": "PreToolUse", "tool_name": "Edit",
		},
		"an empty path": {
			"hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]any{"file_path": ""},
		},
		"a path that is not there": {
			"hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]any{"file_path": "internal/render.go"},
			"cwd":        "/nonexistent/directory/entirely",
		},
		"no cwd, so no worktree": {
			"hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]any{"file_path": "/etc/passwd"},
		},
	}
	for what, event := range events {
		r := newRig(t)
		r.task("fix-the-parser", []string{"internal/tree/"}, true)
		if got := r.run(r.raw(event)); got.Code != hook.CodeOK {
			t.Errorf("%s exited %d\n%s", what, got.Code, got.Stderr)
		}
	}
}

// TestAScopelessTaskEnforcesNothing is §8.4 rule 2. Scope is opt-in per task.
func TestScopelessTaskNeverBlocks(t *testing.T) {
	r := newRig(t)
	// Scopeless tasks cannot be bound to a worktree — the store refuses, since a
	// binding that enforces nothing is a binding nobody can act on — so this is
	// the environment-variable half of §8.1.
	r.task("fix-the-parser", nil, false)
	r.env[hook.EnvTask] = "fix-the-parser"

	for _, path := range []string{"internal/render.go", "README.md", "/etc/passwd"} {
		if got := r.run(r.event("Write", path)); got.Code != hook.CodeOK {
			t.Errorf("a scopeless task blocked %s: %s", path, got.Stderr)
		}
	}
}

// TestAnAgentThatNeverOptedInIsNeverBlocked: no MUFF_TASK, no binding.
func TestNoTaskInForce(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, false) // scoped, unbound

	if got := r.run(r.event("Edit", filepath.Join(r.tree, "internal/render.go"))); got.Code != hook.CodeOK {
		t.Errorf("an agent with no task in force was blocked: %s", got.Stderr)
	}
}

// TestTheHookNeverWrites is §8.4 rule 3. It is checked by fingerprinting the
// store rather than by reading the code, so a future write of any kind fails it.
func TestHookNeverWrites(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	before := fingerprint(t, r.root)
	for _, input := range [][]byte{
		r.event("Edit", filepath.Join(r.tree, "internal/tree/tree.go")), // allowed
		r.event("Edit", filepath.Join(r.tree, "internal/render.go")),    // blocked
		r.bash("anno write internal/render.go"),                         // blocked
		[]byte("{{{"),                                                   // ignored
	} {
		r.run(input)
	}
	if after := fingerprint(t, r.root); after != before {
		t.Errorf("the hook changed the store.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestTheHookDoesNotConjureAStore. A hook that created one would leave a task
// store in whatever directory an agent happened to be in.
func TestHookCreatesNothing(t *testing.T) {
	r := newRig(t)
	if got := r.run(r.event("Edit", filepath.Join(r.tree, "internal/render.go"))); got.Code != hook.CodeOK {
		t.Fatalf("with no store, the hook exited %d", got.Code)
	}
	if _, err := os.Stat(r.root); !os.IsNotExist(err) {
		t.Errorf("the hook created a store at %s", r.root)
	}
}

// fingerprint lists every file under a directory with its size and contents
// hash, so any write at all shows up.
func fingerprint(t testing.TB, root string) string {
	t.Helper()

	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b.WriteString(rel + " " + info.Mode().String() + " " + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestMainRecoversAndReports drives the process entry point.
func TestMain_(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	var stderr bytes.Buffer
	code := hook.Main(bytes.NewReader(r.event("Edit", filepath.Join(r.tree, "internal/render.go"))), &stderr, r.opts())
	if code != hook.CodeBlock {
		t.Errorf("Main exited %d, want a block", code)
	}
	if !strings.Contains(stderr.String(), "outside the scope") {
		t.Errorf("stderr:\n%s", stderr.String())
	}

	// An unreadable stdin is not a violation.
	var quiet bytes.Buffer
	if code := hook.Main(errorReader{}, &quiet, r.opts()); code != hook.CodeOK {
		t.Errorf("an unreadable stdin exited %d", code)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, os.ErrInvalid }

// FuzzRun is Anno's property, and the reason this hook is safe to install: no
// input whatsoever produces an exit code other than 0 or 2, and a 2 only ever
// comes from a payload naming a path.
func FuzzRun(f *testing.F) {
	r := newRig(f)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	f.Add([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"x.go"}}`))
	f.Add([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"anno write x"}}`))
	f.Add([]byte(`{"hook_event_name":"PostToolUse"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"tool_input":{"edits":[{"file_path":"a"},{"file_path":"b"}]}}`))

	opts := r.opts()
	f.Fuzz(func(t *testing.T, input []byte) {
		got := hook.Run(input, opts)
		if got.Code != hook.CodeOK && got.Code != hook.CodeBlock {
			t.Fatalf("exit %d on %q", got.Code, input)
		}
		if got.Code == hook.CodeBlock && got.Stderr == "" {
			t.Fatalf("blocked without saying why: %q", input)
		}
	})
}
