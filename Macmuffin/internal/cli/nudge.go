package cli

// Which commands change the pool, and therefore what the mirror needs to be
// told about.
//
// Getting this wrong in one direction costs a wasted sync, and in the other
// leaves the website stale until the next timer. Neither is serious, which is
// exactly why it is worth being exact about rather than guessing: a table is
// cheap and an approximation would never get looked at again.
var changesThePool = map[string]bool{
	"create":   true,
	"push":     true,
	"claim":    true,
	"scope":    true,
	"worktree": true,
	// Ordering decides whether a task can be started at all, which the board
	// shows and which changes what a reader may pick up.
	"block":   true,
	"unblock": true,
	// A description is a field the board shows.
	"describe": true,
	// A rebind rewrites the worktree every affected task is bound to, which is a
	// field the board shows.
	"rebind":   true,
	"status":   true,
	"complete": true,
	"delete":   true,
	"invite":   true,
	"kick":     true,
	"leave":    true,

	"pool":        false,
	"info":        false,
	"check-scope": false,
	"verify":      false,
	"help":        false,

	// `assign` gives a task an owner, which is exactly the kind of change the
	// board should show without waiting for a poll.
	"assign": true,
}

// mutates reports whether a successful command changed anything.
//
// Nothing in Macmuffin needs its arguments inspected the way Mailman's `archive`
// does, so the name alone decides. A command missing from the table answers
// false, which the tests refuse to let happen.
func mutates(command string) bool { return changesThePool[command] }
