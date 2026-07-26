// Command orc creates and governs the identities a fleet of Claude Code sessions
// runs as.
//
// See Docs/Orc for the CLI and Claude/Docs/Orc/Plan.md for the reasoning behind
// it.
package main

import (
	"os"
	"os/user"

	"orc/orc/internal/cli"
	"orc/theme"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Not fatal: the store can be named explicitly with ORC_HOME, and the
		// commands that need a home say so.
		home = ""
	}

	// The operating-system user is what `orc bootstrap` names the operator after.
	// A machine where this cannot be resolved is not a failure either — bootstrap
	// takes --as, and it says so.
	who := ""
	if u, err := user.Current(); err == nil {
		who = u.Username
	}

	os.Exit(cli.Main(cli.App{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Env:      os.LookupEnv,
		Home:     home,
		User:     who,
		Colour:   true,
		Terminal: theme.IsTerminal(os.Stdout),
		// Asked separately: `orc status > fleet.txt` still has a terminal to be
		// diagnosed on.
		ErrTerminal: theme.IsTerminal(os.Stderr),
	}, os.Args[1:]))
}
