// Command mailman sends and reads inter-agent mail.
//
// See Docs/Mailman for the CLI and the reasoning behind it.
package main

import (
	"os"

	"orc/mailman/internal/cli"
	"orc/theme"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Not fatal: a home directory is only one of the places a credential and
		// a store can live, and both can be named explicitly. The commands that
		// need one will say so.
		home = ""
	}

	os.Exit(cli.Main(cli.App{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Env:      os.LookupEnv,
		Home:     home,
		Width:    terminalWidth(),
		Colour:   true,
		Terminal: theme.IsTerminal(os.Stdout),
		// Asked separately: `mailman inbox > mail.txt` still has a terminal to be
		// diagnosed on.
		ErrTerminal: theme.IsTerminal(os.Stderr),
	}, os.Args[1:]))
}
