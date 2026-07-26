// Command anno-hook is Anno's Claude Code hook handler.
//
// It reads a PostToolUse event on standard input and either blocks an edit that
// broke a file's annotations or hands back that file's annotation index. See
// Claude/Docs/Anno/Hooks.md for the settings.json wiring.
package main

import (
	"os"

	"orc/anno/internal/hook"
)

func main() {
	os.Exit(hook.Main(os.Stdin, os.Stdout, os.Stderr))
}
