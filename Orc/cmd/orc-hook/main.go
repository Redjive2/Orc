// Command orc-hook is Orc's Claude Code hook: the permission boundary, and the event
// feed the live view is drawn from.
//
// It is wired into every session's own settings.json by `orc employ`, and is never run
// by hand. See internal/hook for what it decides and why, and Claude/Docs/Orc/Hooks.md
// for the wiring.
//
// Its exit codes are Claude's, not Orc's: 0 lets the tool call proceed, 2 blocks it and
// feeds stderr back to the agent. Nothing here ever exits with anything else — a hook
// that exited 70 on a defect would turn a bug in Orc into a broken session, so a
// defect exits 0 and says so on stderr.
package main

import (
	"os"

	"orc/orc/internal/hook"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Not fatal: ORC_HOME may say where the store is, and a hook that cannot find
		// a store falls to the ladder's third rung rather than failing.
		home = ""
	}

	os.Exit(hook.Main(os.Stdin, os.Stderr, hook.Options{
		Home: home,
		Env:  os.LookupEnv,
	}))
}
