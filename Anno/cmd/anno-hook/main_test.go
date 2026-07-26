package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orc/anno/internal/fixture"
	"orc/anno/internal/hook"
)

// binary builds the hook handler as Claude Code would invoke it.
func binary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anno-hook")
	build := exec.Command("go", "build", "-o", path, "orc/anno/cmd/anno-hook")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building anno-hook: %v\n%s", err, out)
	}
	return path
}

// fire runs the hook with a payload on stdin, exactly as the harness does.
func fire(t *testing.T, bin string, event any) (int, string, string) {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin)
	cmd.Stdin = bytes.NewReader(body)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running the hook: %v", err)
		}
		code = exit.ExitCode()
	}
	return code, out.String(), errOut.String()
}

func TestHookEndToEnd(t *testing.T) {
	bin := binary(t)
	dir := t.TempDir()

	sound := filepath.Join(dir, "sound.go")
	if err := os.WriteFile(sound, []byte(fixture.ExampleGo), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.go")
	if err := os.WriteFile(broken, []byte("// @:> section s\nx\n// @:< ghost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("a broken edit blocks with an explanation", func(t *testing.T) {
		code, stdout, stderr := fire(t, bin, map[string]any{
			"hook_event_name": "PostToolUse",
			"tool_name":       "Edit",
			"tool_input":      map[string]any{"file_path": broken},
		})
		if code != hook.CodeBlock {
			t.Fatalf("exit %d, want %d (stderr: %s)", code, hook.CodeBlock, stderr)
		}
		if !strings.Contains(stderr, "unparseable") {
			t.Errorf("stderr should explain the block:\n%s", stderr)
		}
		if stdout != "" {
			t.Errorf("stdout should be empty when blocking, got %q", stdout)
		}
	})

	t.Run("a sound edit is silent", func(t *testing.T) {
		code, stdout, stderr := fire(t, bin, map[string]any{
			"hook_event_name": "PostToolUse",
			"tool_name":       "Write",
			"tool_input":      map[string]any{"file_path": sound},
		})
		if code != hook.CodeOK || stdout != "" || stderr != "" {
			t.Errorf("exit %d, stdout %q, stderr %q; want a silent pass", code, stdout, stderr)
		}
	})

	t.Run("a read returns parseable JSON context", func(t *testing.T) {
		code, stdout, stderr := fire(t, bin, map[string]any{
			"hook_event_name": "PostToolUse",
			"tool_name":       "Read",
			"tool_input":      map[string]any{"file_path": sound},
		})
		if code != hook.CodeOK {
			t.Fatalf("exit %d, want %d (stderr: %s)", code, hook.CodeOK, stderr)
		}

		var got struct {
			HookSpecificOutput struct {
				HookEventName     string `json:"hookEventName"`
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
		}
		if got.HookSpecificOutput.HookEventName != "PostToolUse" {
			t.Errorf("hookEventName = %q", got.HookSpecificOutput.HookEventName)
		}
		if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "part declarations") {
			t.Errorf("context should carry the index:\n%s", got.HookSpecificOutput.AdditionalContext)
		}
	})

	t.Run("nonsense on stdin exits zero", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Stdin = strings.NewReader("not json at all")
		if err := cmd.Run(); err != nil {
			t.Errorf("the hook should never fail on unrecognised input: %v", err)
		}
	})

	t.Run("closed stdin exits zero", func(t *testing.T) {
		cmd := exec.Command(bin)
		if err := cmd.Run(); err != nil {
			t.Errorf("the hook should tolerate empty input: %v", err)
		}
	})
}
