package cli

import (
	"fmt"

	"orc/common/fault"
)

// A backstop must not stop.
//
// Both cycles — `tend --watch`, which keeps sessions running, and `orc wake --every`,
// which keeps them moving — used to count consecutive failures and return an error at
// five. The reasoning was that a loop unable to make progress should say so rather
// than fill a terminal with one line, and the reasoning was about the terminal rather
// than about the fleet.
//
// Nothing restarts these. There is no service, no supervisor over them, and `orc
// doctor` only reports that a cycle is missing to whoever runs `orc doctor`. So the
// five-failure rule turned a bad half hour — a store on a disk that filled, a machine
// asleep, an upgrade replacing binaries — into a fleet with no backstop at all, and
// the agents went quiet with nothing left running to notice.
//
// Five passes is also not much evidence. At a one-minute cycle it is five minutes.
//
// So a pass can fail as often as it likes and the loop keeps its interval. What
// changes is only how often the failure is *said*: the first few, then one in ten,
// which keeps a broken fleet from scrolling and still leaves a trail. Recovery is
// always said, because "it is working again" is the line somebody is waiting for.
type backstop struct {
	app  App
	what string // "tending" or "waking", for the messages

	failures int
}

// pass runs one round and absorbs whatever it does.
func (b *backstop) pass(run func() error) {
	err := b.guard(run)
	if err == nil {
		if b.failures > 0 {
			b.app.note("%s is working again, after %d %s that did not",
				b.what, b.failures, passes(b.failures))
		}
		b.failures = 0
		return
	}

	b.failures++
	if b.audible() {
		b.app.note("%s: pass %d in a row failed: %v", b.what, b.failures, err)
		if b.failures == LoudFailures {
			b.app.note("%s: further failures are reported one in %d; the cycle keeps running",
				b.what, QuietFailures)
		}
	}
}

// guard turns a panic in a pass into an error.
//
// A backstop is the last thing running over a fleet nobody is watching, and a nil map
// or a bad index somewhere below would take it down and leave every agent to stop on
// its own schedule. The panic is a bug and is reported as one — it is a fault.Internal
// with the stack's own words in it — but it is reported *by a cycle that is still
// running*, which is the difference between a fleet that logs a defect and a fleet
// that quietly dies.
func (b *backstop) guard(run func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fault.Internal{
				Where:  "backstop." + b.what,
				Detail: fmt.Sprintf("the pass panicked: %v", r),
			}
		}
	}()
	return run()
}

// passes names a count of them. `plural` appends an "s", which makes "passs".
func passes(n int) string {
	if n == 1 {
		return "pass"
	}
	return "passes"
}

// audible reports whether this failure is one of the ones said out loud.
func (b *backstop) audible() bool {
	if b.failures <= LoudFailures {
		return true
	}
	return b.failures%QuietFailures == 0
}

// How much a failing cycle says.
const (
	// LoudFailures is how many consecutive failures are reported in full. Three
	// covers the ordinary case — one blip, said once — and settles quickly when it
	// is not one.
	LoudFailures = 3
	// QuietFailures is one-in-how-many after that. At a one-minute cycle a fleet
	// that has been broken for an hour has said six lines, which is a trail rather
	// than a flood.
	QuietFailures = 10
)
