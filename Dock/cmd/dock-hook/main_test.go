package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// build compiles the real hook binary, so the contract is checked against the
// process Claude Code will actually run rather than against a function call.
func build(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("building the binary is slow")
	}
	bin := filepath.Join(t.TempDir(), "dock-hook")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin, input string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Stdin = strings.NewReader(input)
	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if ok := asExit(err, &exit); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("running the hook: %v", err)
		}
	}
	return out.String(), errs.String(), code
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestTheBinary(t *testing.T) {
	bin := build(t)

	dir := t.TempDir()
	doc := filepath.Join(dir, "guide.md")
	body := "# §1 Guide\n\ntext\n\n## §1.1 Install\n\nmore\n"
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("a document", func(t *testing.T) {
		in := fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":%q}}`, doc)
		out, errs, code := run(t, bin, in)
		if code != 0 {
			t.Fatalf("exited %d: %s", code, errs)
		}
		var got struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("stdout is not the expected JSON: %v\n%s", err, out)
		}
		if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "§1.1") {
			t.Errorf("context does not carry the index:\n%s", got.HookSpecificOutput.AdditionalContext)
		}
	})

	t.Run("everything else is silent", func(t *testing.T) {
		for _, in := range []string{
			"", "not json", "{}",
			`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"file_path":"` + doc + `"}}`,
			`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"/nope/nope.md"}}`,
		} {
			out, _, code := run(t, bin, in)
			if code != 0 {
				t.Errorf("exited %d for %q", code, in)
			}
			if out != "" {
				t.Errorf("said %q for %q", out, in)
			}
		}
	})
}
