package hook_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/anno/internal/fixture"
	"orc/anno/internal/hook"
)

// event builds a PostToolUse payload the way Claude Code sends one.
func event(t *testing.T, tool, path string) []byte {
	t.Helper()
	return payload(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       tool,
		"tool_input":      map[string]any{"file_path": path},
	})
}

func payload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// write puts content in a fresh directory and returns its path.
func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// context extracts the additionalContext an outcome carries, or "".
func context(t *testing.T, out hook.Outcome) string {
	t.Helper()
	if len(out.Stdout) == 0 {
		return ""
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Stdout, &got); err != nil {
		t.Fatalf("hook wrote invalid JSON %q: %v", out.Stdout, err)
	}
	if got.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want PostToolUse", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.AdditionalContext
}

func TestEditThatBreaksAnnotationsIsBlocked(t *testing.T) {
	path := write(t, "a.go", "// @:> section s\nx\n// @:< ghost\n")

	out := hook.Run(event(t, "Edit", path))
	if out.Code != hook.CodeBlock {
		t.Fatalf("exit %d, want %d", out.Code, hook.CodeBlock)
	}
	for _, want := range []string{path, "unparseable", "ghost", "anno index"} {
		if !strings.Contains(out.Stderr, want) {
			t.Errorf("stderr should mention %q:\n%s", want, out.Stderr)
		}
	}
	if len(out.Stdout) != 0 {
		t.Errorf("a blocking hook should write nothing to stdout, got %q", out.Stdout)
	}
}

func TestEveryEditingToolIsGuarded(t *testing.T) {
	for _, tool := range hook.Editors {
		t.Run(tool, func(t *testing.T) {
			path := write(t, "a.go", "// @:< ghost\n")
			if got := hook.Run(event(t, tool, path)).Code; got != hook.CodeBlock {
				t.Errorf("exit %d, want %d", got, hook.CodeBlock)
			}
		})
	}
}

func TestSoundEditsPassSilently(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"well formed annotations", fixture.ExampleGo},
		{"no annotations at all", "package main\n\nfunc main() {}\n"},
		{"empty file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := hook.Run(event(t, "Edit", write(t, "a.go", tc.content)))
			if out.Code != hook.CodeOK {
				t.Errorf("exit %d, want %d: %s", out.Code, hook.CodeOK, out.Stderr)
			}
			if out.Stderr != "" {
				t.Errorf("stderr should be empty, got %q", out.Stderr)
			}
		})
	}
}

// TestUnannotatedFilesAreNeverBlocked is the rule that keeps the guard from
// becoming a nuisance: a file that never carried annotations is not Anno's
// business, however it happens to fail to parse.
func TestUnannotatedFilesAreNeverBlocked(t *testing.T) {
	// A file with no markers at all cannot fail tree.Build, but the check is
	// belt and braces: a marker-free file passes even if something else changes.
	path := write(t, "notes.txt", "just prose, mentioning nothing special\n")
	if got := hook.Run(event(t, "Write", path)).Code; got != hook.CodeOK {
		t.Errorf("exit %d, want %d", got, hook.CodeOK)
	}
}

func TestUnreadableFilesPassSilently(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, path string }{
		{"missing", filepath.Join(dir, "gone.go")},
		{"a directory", dir},
		{"binary", write(t, "a.bin", "\x00\x01")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range []string{"Edit", "Read"} {
				out := hook.Run(event(t, tool, tc.path))
				if out.Code != hook.CodeOK {
					t.Errorf("%s: exit %d, want %d", tool, out.Code, hook.CodeOK)
				}
				if len(out.Stdout) != 0 {
					t.Errorf("%s: stdout = %q, want nothing", tool, out.Stdout)
				}
			}
		})
	}
}

func TestReadOfAnAnnotatedFileReturnsItsIndex(t *testing.T) {
	path := write(t, "example.go", fixture.ExampleGo)

	out := hook.Run(event(t, "Read", path))
	if out.Code != hook.CodeOK {
		t.Fatalf("exit %d, want %d", out.Code, hook.CodeOK)
	}

	got := context(t, out)
	for _, want := range []string{
		path,
		"part declarations",
		"|  |  symbol  Operate",
		"anno read",
		"anno write",
		"ambiguous",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context should mention %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("context must not be coloured; it is read by a model:\n%q", got)
	}
}

func TestReadOfAnUnannotatedFileSaysNothing(t *testing.T) {
	// The whole point is spending fewer tokens, so a hook on every read must
	// stay silent when it has nothing worth the space.
	for _, content := range []string{"package main\n", "", "// an ordinary comment\n"} {
		out := hook.Run(event(t, "Read", write(t, "a.go", content)))
		if out.Code != hook.CodeOK || len(out.Stdout) != 0 {
			t.Errorf("content %q gave exit %d and stdout %q, want a silent pass", content, out.Code, out.Stdout)
		}
	}
}

func TestReadOfAFileWithBrokenAnnotationsSaysNothing(t *testing.T) {
	// Reading is not the moment to complain; the edit hook already did.
	out := hook.Run(event(t, "Read", write(t, "a.go", "// @:< ghost\n")))
	if out.Code != hook.CodeOK || len(out.Stdout) != 0 {
		t.Errorf("exit %d with stdout %q, want a silent pass", out.Code, out.Stdout)
	}
}

func TestRelativePathsResolveAgainstTheSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(fixture.ExampleGo), 0o644); err != nil {
		t.Fatal(err)
	}

	out := hook.Run(payload(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "Read",
		"cwd":             dir,
		"tool_input":      map[string]any{"file_path": "a.go"},
	}))
	if got := context(t, out); !strings.Contains(got, "declarations") {
		t.Errorf("a relative path should resolve against cwd:\n%s", got)
	}
}

func TestNotebookPathIsUnderstood(t *testing.T) {
	path := write(t, "a.ipynb", "// @:< ghost\n")
	out := hook.Run(payload(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "NotebookEdit",
		"tool_input":      map[string]any{"notebook_path": path},
	}))
	if out.Code != hook.CodeBlock {
		t.Errorf("exit %d, want %d", out.Code, hook.CodeBlock)
	}
}

// TestNothingUnexpectedEverBlocks is the property that makes this safe to leave
// switched on: only a genuinely broken annotated file is allowed to interrupt.
func TestNothingUnexpectedEverBlocks(t *testing.T) {
	good := write(t, "a.go", fixture.ExampleGo)

	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"not JSON at all", []byte("this is not json")},
		{"empty input", nil},
		{"empty object", []byte(`{}`)},
		{"JSON array", []byte(`[1,2,3]`)},
		{"a different event", payload(t, map[string]any{
			"hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]any{"file_path": write(t, "b.go", "// @:< ghost\n")},
		})},
		{"an unrelated tool", event(t, "Bash", good)},
		{"no path at all", payload(t, map[string]any{
			"hook_event_name": "PostToolUse", "tool_name": "Edit",
		})},
		{"unexpected field types", []byte(`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":42}}`)},
		{"extra unknown fields", payload(t, map[string]any{
			"hook_event_name": "PostToolUse", "tool_name": "Read", "future_field": "ignored",
			"tool_input": map[string]any{"file_path": good, "unknown": true},
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hook.Run(tc.input).Code; got != hook.CodeOK {
				t.Errorf("exit %d, want %d", got, hook.CodeOK)
			}
		})
	}
}

// FuzzRun asserts the hook cannot be made to crash or to block on anything it
// does not understand, whatever arrives on its standard input.
func FuzzRun(f *testing.F) {
	path := filepath.Join(f.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte(fixture.ExampleGo), 0o644); err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{
		`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"` + path + `"}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":""}}`,
		`{}`, ``, `null`, `[]`, `{"tool_input":null}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		out := hook.Run([]byte(input))
		if out.Code != hook.CodeOK && out.Code != hook.CodeBlock {
			t.Fatalf("exit %d is not a code Claude Code understands", out.Code)
		}
		if len(out.Stdout) > 0 && !json.Valid(out.Stdout) {
			t.Fatalf("stdout is not valid JSON: %q", out.Stdout)
		}
		if out.Code == hook.CodeBlock && out.Stderr == "" {
			t.Fatalf("a block must explain itself")
		}
	})
}

// panicReader forces the recovery path in Main.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("stdin exploded") }

// failReader stands in for a stream that cannot be read.
type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("pipe broke") }

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("stream gone") }

func TestMainReportsAndExits(t *testing.T) {
	good := write(t, "a.go", fixture.ExampleGo)
	broken := write(t, "b.go", "// @:> section s\n// @:< ghost\n")

	t.Run("a read writes context and exits zero", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := hook.Main(bytes.NewReader(event(t, "Read", good)), &out, &errOut)
		if code != hook.CodeOK {
			t.Fatalf("exit %d, want %d", code, hook.CodeOK)
		}
		if !json.Valid(out.Bytes()) {
			t.Errorf("stdout is not JSON: %q", out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want empty", errOut.String())
		}
	})

	t.Run("a broken edit writes an explanation and blocks", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := hook.Main(bytes.NewReader(event(t, "Edit", broken)), &out, &errOut)
		if code != hook.CodeBlock {
			t.Fatalf("exit %d, want %d", code, hook.CodeBlock)
		}
		if !strings.Contains(errOut.String(), "unparseable") {
			t.Errorf("stderr should explain: %q", errOut.String())
		}
	})

	t.Run("an unreadable stdin exits zero", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := hook.Main(failReader{}, &out, &errOut); code != hook.CodeOK {
			t.Errorf("exit %d, want %d", code, hook.CodeOK)
		}
	})

	t.Run("a broken stdout exits zero", func(t *testing.T) {
		var errOut bytes.Buffer
		if code := hook.Main(bytes.NewReader(event(t, "Read", good)), failWriter{}, &errOut); code != hook.CodeOK {
			t.Errorf("exit %d, want %d", code, hook.CodeOK)
		}
	})

	t.Run("a broken stderr exits zero", func(t *testing.T) {
		var out bytes.Buffer
		if code := hook.Main(bytes.NewReader(event(t, "Edit", broken)), &out, failWriter{}); code != hook.CodeOK {
			t.Errorf("exit %d, want %d; a hook that cannot report must not block", code, hook.CodeOK)
		}
	})

	t.Run("a panic is recovered rather than escaping", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := hook.Main(panicReader{}, &out, &errOut)
		if code != hook.CodeOK {
			t.Fatalf("exit %d, want %d", code, hook.CodeOK)
		}
		if !strings.Contains(errOut.String(), "recovered") {
			t.Errorf("stderr should note the recovery: %q", errOut.String())
		}
	})
}
