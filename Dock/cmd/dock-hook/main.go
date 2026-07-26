// Command dock-hook is Dock's Claude Code integration.
//
// It reads a PostToolUse event on stdin and, when an agent has just read a
// document carrying § headings, hands back that document's index so the agent
// can address a section by name instead of re-reading the whole thing.
//
// It always exits 0. There is no such thing as a read that should have been
// refused, so this hook has no way to interrupt a session.
package main

import (
	"os"

	"orc/dock/internal/hook"
)

func main() {
	os.Exit(hook.Main(os.Stdin, os.Stdout, os.Stderr))
}
