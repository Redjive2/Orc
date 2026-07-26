// Command muff-hook is Macmuffin's Claude Code hook handler.
//
// It reads a PreToolUse event on standard input and blocks an edit that falls
// outside the scope of the task in force. Everything else — every other tool,
// every other event, and every failure it meets on the way — exits 0 in silence.
// See Claude/Docs/Macmuffin/Hooks.md for the settings.json wiring.
package main

import (
	"os"

	"orc/macmuffin/internal/hook"
)

func main() {
	os.Exit(hook.Main(os.Stdin, os.Stderr, hook.Options{Env: os.LookupEnv}))
}
