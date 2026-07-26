// Command muff creates, tracks, and manages tasks in a shared pool.
//
// See Docs/Macmuffin for the CLI and the reasoning behind it.
package main

import (
	"os"

	"orc/macmuffin/internal/cli"
	"orc/theme"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Not fatal: the store can be named explicitly, and the commands that
		// need a home will say so.
		home = ""
	}

	os.Exit(cli.Main(cli.App{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Env:      os.LookupEnv,
		Home:     home,
		Colour:   true,
		Terminal: theme.IsTerminal(os.Stdout),
		// Asked separately: `muff pool > board.txt` still has a terminal to be
		// diagnosed on.
		ErrTerminal: theme.IsTerminal(os.Stderr),
	}, os.Args[1:]))
}
