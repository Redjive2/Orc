package hook_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/dock/internal/fixture"
	"orc/dock/internal/hook"
)

// event builds a PostToolUse Read payload for a path.
func event(path string) []byte {
	return []byte(fmt.Sprintf(
		`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":%q}}`, path))
}

// document writes a file and returns its path.
func document(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// context runs the hook and returns the additionalContext it emitted, or "".
func context(t *testing.T, input []byte) string {
	t.Helper()
	out := hook.Run(input)
	if len(out) == 0 {
		return ""
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the hook emitted invalid JSON: %v\n%s", err, out)
	}
	if got.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.AdditionalContext
}

func TestTheIndexIsHandedBack(t *testing.T) {
	path := document(t, "guide.md", fixture.Guide)
	got := context(t, event(path))
	if got == "" {
		t.Fatal("a document with sections produced nothing")
	}
	for _, want := range []string{
		"guide.md carries dock sections",
		"§1.2.1", "Numbering", "dock read guide.md§1.2", "dock links",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the context is missing %q:\n%s", want, got)
		}
	}
	// It hands back structure, never content — that is the whole saving.
	for _, leak := range []string{"Dock reads documentation", "go install", "https://example.com"} {
		if strings.Contains(got, leak) {
			t.Errorf("the hook leaked content: %q", leak)
		}
	}
}

// TestSilenceIsTheDefault. A hook that fired on every read and spent tokens on
// files that are not documents would invert the tool's whole purpose.
func TestSilenceIsTheDefault(t *testing.T) {
	for name, body := range map[string]string{
		"no sections":                   "# An ordinary heading\n\njust prose\n",
		"empty":                         "",
		"prose only":                    "no headings at all\n",
		"a section sign but no heading": "the § sign appears in prose here\n",
		"sections in a fence":           "# Not marked\n\n```\n## §1.1 example\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := document(t, "notes.md", body)
			if got := context(t, event(path)); got != "" {
				t.Errorf("produced context for a file that is not a document:\n%s", got)
			}
		})
	}
}

// TestNothingUnexpectedEverMisbehaves enumerates everything a hook can be handed
// that it was not designed for. All of it must be silent success: this fires on
// every read in every session, and breaking one is far worse than saying nothing.
func TestNothingUnexpectedEverMisbehaves(t *testing.T) {
	good := document(t, "guide.md", fixture.Guide)
	dir := filepath.Dir(good)

	// A binary, a non-UTF-8 file, and a document whose numbering is broken.
	binary := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(binary, []byte{0x89, 'P', 'N', 'G', 0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	latin := filepath.Join(dir, "latin.md")
	if err := os.WriteFile(latin, []byte{'#', ' ', 0xa7, '1', ' ', 0xe9, '\n'}, 0o644); err != nil {
		t.Fatal(err)
	}
	broken := document(t, "broken.md", "## §1.1 Orphan\n\ntext\n")

	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"empty input", nil},
		{"not json", []byte("this is not json at all")},
		{"truncated json", []byte(`{"hook_event_name":"PostTool`)},
		{"json array", []byte(`[1,2,3]`)},
		{"json null", []byte(`null`)},
		{"empty object", []byte(`{}`)},
		{"wrong event", []byte(`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"` + good + `"}}`)},
		{"wrong tool", []byte(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"file_path":"` + good + `"}}`)},
		{"an editing tool", []byte(`{"hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"` + good + `"}}`)},
		{"no path", []byte(`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{}}`)},
		{"empty path", event("")},
		{"path is a number", []byte(`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":42}}`)},
		{"tool_input is a string", []byte(`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":"nope"}`)},
		{"event name is a number", []byte(`{"hook_event_name":7,"tool_name":"Read"}`)},
		{"missing file", event(filepath.Join(dir, "nope.md"))},
		{"a directory", event(dir)},
		{"a binary", event(binary)},
		{"not utf-8", event(latin)},
		{"broken numbering", event(broken)},
		{"nul in path", event("bad\x00path.md")},
		{"very long path", event(strings.Repeat("a", 5000) + ".md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			code := hook.Main(bytes.NewReader(tc.input), &out, &errs)
			if code != hook.CodeOK {
				t.Errorf("exited %d; a read hook has nothing to block", code)
			}
			if out.Len() != 0 {
				t.Errorf("said something about an input it does not handle:\n%s", out.String())
			}
		})
	}
}

// TestABrokenDocumentIsNotTheHooksBusiness: the agent just read it and can see
// the headings, and a hook is not the place to report a fault nobody asked
// about. `dock check` is.
func TestABrokenDocumentIsNotTheHooksBusiness(t *testing.T) {
	path := document(t, "broken.md", "# §1 A\n\n## §1.3 Gap\n")
	if got := context(t, event(path)); got != "" {
		t.Errorf("the hook reported a numbering fault:\n%s", got)
	}
}

func TestARelativePathIsResolvedAgainstTheSessionDirectory(t *testing.T) {
	path := document(t, "guide.md", fixture.Guide)
	dir := filepath.Dir(path)
	input := []byte(fmt.Sprintf(
		`{"hook_event_name":"PostToolUse","tool_name":"Read","cwd":%q,"tool_input":{"file_path":"guide.md"}}`, dir))
	if got := context(t, input); got == "" {
		t.Error("a relative path was not resolved against cwd")
	}
}

func TestUnknownFieldsAreIgnored(t *testing.T) {
	path := document(t, "guide.md", fixture.Guide)
	input := []byte(fmt.Sprintf(
		`{"hook_event_name":"PostToolUse","tool_name":"Read","session_id":"x","future_field":{"a":[1,2]},"tool_input":{"file_path":%q,"offset":3}}`, path))
	if got := context(t, input); got == "" {
		t.Error("an unfamiliar field stopped the hook working; the schema may grow")
	}
}

// TestTheContextIsNeverColoured: it is read by a model, not a terminal.
func TestTheContextIsNeverColoured(t *testing.T) {
	path := document(t, "guide.md", fixture.Guide)
	got := context(t, event(path))
	if strings.Contains(got, "\x1b[") {
		t.Errorf("the context carries escape sequences:\n%q", got)
	}
}

// TestALargeIndexIsBounded. The context is spent on every read, so a
// hundred-section reference must not cost more than it saves.
func TestALargeIndexIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("# §1 Reference\n\ntext\n")
	for i := 1; i <= hook.MaxSections+30; i++ {
		fmt.Fprintf(&b, "## §1.%d Section %d\n\nbody\n", i, i)
	}
	path := document(t, "big.md", b.String())

	got := context(t, event(path))
	if got == "" {
		t.Fatal("a large document produced nothing")
	}
	// Count section rows only: the usage lines below the list mention §1.2 too.
	rows := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "  §") {
			rows++
		}
	}
	if rows > hook.MaxSections {
		t.Errorf("listed %d sections, past the bound of %d", rows, hook.MaxSections)
	}
	if rows != hook.MaxSections {
		t.Errorf("listed %d sections, want the full bound of %d", rows, hook.MaxSections)
	}
	if !strings.Contains(got, "and ") || !strings.Contains(got, "dock index") {
		t.Errorf("the bound was applied silently:\n%s", got)
	}
}

// TestTheNumberColumnAligns: § is two bytes and one column, so padding by byte
// count would shear the list.
func TestTheNumberColumnAligns(t *testing.T) {
	path := document(t, "deep.md", "# §1 A\n\nx\n\n## §1.1 B\n\nx\n\n### §1.1.1 C\n\nx\n")
	got := context(t, event(path))

	var starts []int
	for _, line := range strings.Split(got, "\n") {
		i := strings.Index(line, " line")
		if !strings.HasPrefix(line, "  §") || i < 0 {
			continue
		}
		starts = append(starts, len([]rune(line[:i])))
	}
	if len(starts) < 3 {
		t.Fatalf("expected three section rows:\n%s", got)
	}
	for i := 1; i < len(starts); i++ {
		if starts[i] != starts[0] {
			t.Errorf("the size column does not line up: %v\n%s", starts, got)
		}
	}
}

func FuzzRun(f *testing.F) {
	dir := f.TempDir()
	path := filepath.Join(dir, "guide.md")
	if err := os.WriteFile(path, []byte(fixture.Guide), 0o644); err != nil {
		f.Fatal(err)
	}

	for _, s := range []string{
		"", "{}", "null", "[]", "not json",
		string(event(path)),
		string(event(filepath.Join(dir, "nope.md"))),
		`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":42}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Read","cwd":"` + dir + `","tool_input":{"file_path":"guide.md"}}`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		var out, errs bytes.Buffer
		code := hook.Main(strings.NewReader(input), &out, &errs)

		// The one property: no input whatsoever produces a status other than 0.
		// A read hook has nothing to block, so anything else would be a session
		// interrupted by a document.
		if code != hook.CodeOK {
			t.Fatalf("exited %d for %q", code, input)
		}
		// Whatever it says must be well-formed JSON carrying the right event, or
		// the agent gets a parse error instead of context.
		if out.Len() == 0 {
			return
		}
		var got struct {
			HookSpecificOutput struct {
				HookEventName     string `json:"hookEventName"`
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("emitted invalid JSON for %q: %v\n%s", input, err, out.String())
		}
		if got.HookSpecificOutput.HookEventName != "PostToolUse" {
			t.Errorf("wrong event name %q", got.HookSpecificOutput.HookEventName)
		}
		if got.HookSpecificOutput.AdditionalContext == "" {
			t.Error("emitted a response with nothing in it")
		}
	})
}
