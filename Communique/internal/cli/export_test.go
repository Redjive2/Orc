package cli

// Exported for the tests, which check the two things that made `cq serve`
// unusable on Windows: whether this process can start a copy of itself, and
// what it says when it cannot.

var (
	Restartable = restartable
	CannotStart = cannotStart
)
