package cli

// Which commands change the store, and therefore what the mirror needs to be
// told about.
//
// Getting this wrong in one direction costs a wasted sync, and in the other
// leaves the website stale until the next timer. Neither is serious, which is
// exactly why it is worth being exact about rather than guessing: a table is
// cheap and an approximation would never get looked at again.
var changesTheStore = map[string]bool{
	"send":    true,
	"reply":   true,
	"prune":   true,
	"read":    true, // read state is mirrored, so marking mail read is a change
	"cc":      true,
	"inbox":   false,
	"open":    false,
	"convo":   false,
	"check":   false,
	"verify":  false,
	"help":    false,
	"archive": false, // decided from its arguments; see mutates
	"admin":   false, // likewise
}

// mutates reports whether a successful command changed anything.
//
// Two commands cannot be decided from their name. `archive` with a query files
// mail; with no query it prints the archive. `admin user list` reads, while add
// and remove write. Both are settled here rather than by the commands
// themselves, so the whole answer is in one readable place.
func mutates(command string, args []string) bool {
	switch command {
	case "archive":
		return len(args) > 0
	case "admin":
		return len(args) >= 2 && args[0] == "user" &&
			(args[1] == "add" || args[1] == "remove")
	default:
		return changesTheStore[command]
	}
}
