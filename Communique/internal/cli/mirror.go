package cli

import (
	"context"
	"fmt"
	"strings"

	"orc/cq/internal/fault"
	"orc/cq/internal/source"
)

// Whose mailbox this machine mirrors.
//
// A machine's mirror belongs to one account — the operator's — and everything cq
// shows is that account's view. Getting the account wrong is not a small mistake:
// the server replaces its copy of the machine wholesale, so a sync that named the
// wrong account would publish somebody else's mail as the operator's inbox.
//
// It used to be one setting, `$CQ_USER`, and nothing else. That is a setting the
// machine can almost always work out for itself — Orc knows who the operator is,
// and cq is mirroring an Orc fleet — so asking for it was asking the operator to
// repeat themselves, and the failure when they had not was `no user is configured
// for this machine`, which named neither the setting nor the way out of it.
//
// So there is a ladder now, and it is short:
//
//  1. what was asked for — `--user`, else `$CQ_USER`. An explicit answer wins,
//     always, because a machine can legitimately mirror an account that is not the
//     operator and only a person can say so.
//  2. `$ORC_USER`, **if Orc agrees that it is the operator**. This is the rung the
//     operator's own shell lands on after `orc bootstrap`.
//  3. Orc's keyring, when no credential is present at all. Orc hands this over
//     only for a fleet private to this unix user, and never inside a session.
//
// Rungs 2 and 3 both end at the account Orc calls the operator. That is the
// property worth stating plainly: **no rung below the first can resolve to
// anything but the operator**, so an agent whose `mailman send` triggers a sync
// cannot cause its own mailbox to be published as this machine's.
type mirror struct {
	// User is the account whose mailbox is mirrored.
	User string
	// Key is that account's Orc credential, or empty to use the ambient one.
	Key string
	// How says which rung answered, for `cq status` and for a diagnosis.
	How string
}

// mirrored resolves the account this machine mirrors.
//
// asked is what the command line and `$CQ_USER` came to, which is empty when
// neither said anything.
func (a App) mirrored(ctx context.Context, asked string) (mirror, error) {
	if asked = strings.TrimSpace(asked); asked != "" {
		how := "$CQ_USER"
		if a.look("CQ_USER", "") != asked {
			how = "--user"
		}
		return mirror{User: asked, Key: a.look("CQ_KEY", ""), How: how}, nil
	}

	orc := a.orc()
	ambient := strings.TrimSpace(a.look("ORC_USER", ""))

	// Nobody is presenting a credential, so Orc's own owner fallback applies and
	// this is the operator's own machine. Take both halves from it: the mirror
	// then reads the operator's mailbox whoever later triggers a sync.
	if ambient == "" {
		user, key, err := orc.OwnerCredential(ctx)
		if err != nil {
			return mirror{}, a.unresolved(err)
		}
		return mirror{User: user, Key: key, How: "orc's keyring, because you own this fleet"}, nil
	}

	// Something is presenting a credential. It may be the operator in a shell that
	// followed `orc bootstrap`, or it may be an agent whose action triggered this
	// sync — and those must not be treated alike.
	operator, err := orc.Operator(ctx)
	if err != nil {
		return mirror{}, a.unresolved(err)
	}
	if operator = strings.TrimSpace(operator); !strings.EqualFold(ambient, operator) {
		// Conflict rather than usage: this is the same refusal source.checkIdentity
		// makes, for the same reason, and nothing the caller typed was wrong.
		keep := fmt.Sprintf("to keep mirroring %s, whoever triggers the sync:", operator)
		swap := fmt.Sprintf("to mirror %s instead:", ambient)
		width := max(len(keep), len(swap))
		return mirror{}, fault.Conflict{Reason: fmt.Sprintf(
			"this machine mirrors %s's mailbox, but $ORC_USER is %s — refusing to publish one account's mail as another's.\n"+
				"  %s  export CQ_USER=%s CQ_KEY=<%s's orc key, from `orc owner env`>\n"+
				"  %s  export CQ_USER=%s",
			operator, ambient,
			keep+strings.Repeat(" ", width-len(keep)), operator, operator,
			swap+strings.Repeat(" ", width-len(swap)), ambient)}
	}
	return mirror{User: ambient, Key: a.look("CQ_KEY", ""), How: "$ORC_USER, which is this fleet's operator"}, nil
}

// unresolved is the failure the operator reads when cq cannot work out whose
// mailbox this is.
//
// It carries what Orc said underneath, because the two interesting cases look the
// same from here and read completely differently: a machine with no fleet on it,
// and a fleet cq was not allowed to ask about.
func (a App) unresolved(cause error) error {
	lines := []string{
		"cq does not know whose mailbox this machine mirrors, and could not ask orc",
		"  orc said: " + oneLine(cause),
		"",
		"  if there is an orc fleet here, this shell is not its owner; name the account instead:",
		"    export CQ_USER=<name> CQ_KEY=<that account's orc key>",
		"  `orc owner env` prints the operator's pair, and `orc env <name>` any other's",
		"",
		"  if there is no fleet here yet:  orc bootstrap",
	}
	return fault.Usage{Reason: strings.Join(lines, "\n")}
}

// oneLine flattens a nested tool failure into something that fits in a sentence.
// Orc's own errors are already one line; a process failure underneath one is not.
func oneLine(err error) string {
	if err == nil {
		return "nothing"
	}
	text := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i]) + " …"
	}
	return text
}

// orc is the adapter for asking Orc about the fleet. `$ORC` names the executable
// for the unusual machine where it is not on the path under its own name.
func (a App) orc() *source.Orc {
	return &source.Orc{Command: a.look("ORC", "orc")}
}
