package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/provision"
)

// The owner's side of Orc.
//
// Everything else in this CLI is a verb an agent might also run. These three are
// not: renaming the operator, wiping the fleet, and seeing where the credential
// came from are things only the person who owns the machine does, and grouping
// them under one noun keeps them out of the way of the fleet's own vocabulary.
//
//	orc owner                 who the operator is, and how orc knows it is you
//	orc owner env             the operator's export block, found without being told
//	orc owner rename <name>   rename the operator
//	orc owner reset --yes     destroy the fleet and bootstrap a fresh one
//
// `rename` and `reset` are the only commands that require being the operator
// rather than merely controlling somebody. An agent with authority 99 is still not
// the owner, and the owner is not a role — it is whoever holds the root of the
// tree.
func (a App) owner(args []string) error {
	if len(args) == 0 {
		return a.ownerShow()
	}
	switch args[0] {
	case "rename":
		return a.ownerRename(args[1:])
	case "reset":
		return a.ownerReset(args[1:])
	case "env":
		if err := exactly(args[1:], 0, "owner env takes no arguments"); err != nil {
			return err
		}
		return a.ownerEnv()
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"orc owner takes env, rename, or reset, or nothing to show who the operator is (got %q)", args[0])}
	}
}

// ownerShow says who the operator is and where this command's credential came
// from.
//
// The second half is the point. With the keyring fallback (§begin) an operator
// never types a credential, which is convenient and makes "who am I, and why does
// orc believe me" a question with a non-obvious answer. This is where that answer
// lives.
func (a App) ownerShow() error {
	s, err := a.begin()
	if err != nil {
		return err
	}

	operator := s.fleet.Operator()
	line := fmt.Sprintf("%s   %s", a.out.Operator(operator.String()),
		a.out.Muted("the operator, authority 100, the root of the tree"))
	if err := a.say(line); err != nil {
		return err
	}

	source := "from the environment: $ORC_USER and $ORC_KEY are set"
	if s.fromKeyring {
		source = "from the fleet's own keyring, because you own it and nothing was set"
	}
	if err := a.say(fmt.Sprintf("%s   %s", a.out.Header("credential"), a.out.Muted(source))); err != nil {
		return err
	}
	if s.who.String() != operator.String() {
		a.note("you are acting as %s, not the operator; `orc owner rename` and `orc owner reset` need the operator",
			s.who)
	}

	private, why := s.store.OwnedByCaller()
	state := a.out.Good("private to you")
	if !private {
		state = a.out.Warn("not private: " + why)
	}
	return a.say(fmt.Sprintf("%s        %s   %s",
		a.out.Header("fleet"), a.out.Value(s.store.Root()), state))
}

// ownerRename renames the operator.
//
// The store does the careful half (see store.RenameIdentity for the ordering that
// keeps every intermediate state derivable). This does the half that reaches
// outside Orc — the mailbox — and says what it costs, because that part cannot be
// made lossless: Mailman has no rename, mail is addressed to a name, and the old
// mailbox's index goes when the account does.
func (a App) ownerRename(args []string) error {
	var yes bool
	rest, err := flagged(args, options{switches: map[string]*bool{"--yes": &yes}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "owner rename takes one new name"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mustBeOperator("rename the operator"); err != nil {
		return err
	}

	to, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	from := s.fleet.Operator()
	if from.String() == to.String() {
		return a.say(fmt.Sprintf("the operator is already called %s", a.out.Operator(to.String())))
	}
	if s.fleet.Has(to) {
		return fault.Conflict{Path: to.String(), Reason: fmt.Sprintf(
			"%s is already an identity in this fleet", to)}
	}

	// The mailbox is the lossy part, so it is what the confirmation is about.
	if !yes {
		return fault.Usage{Reason: fmt.Sprintf(
			"renaming %s to %s keeps its key, its memories, its workspace, and its place in the tree.\n"+
				"  what it cannot keep is mail: mailman has no rename, so %s gets a fresh mailbox and\n"+
				"  the mail addressed to %s stays in the old one, which is then removed.\n"+
				"  archive anything you want first, then pass --yes",
			from, to, to, from)}
	}

	// The new mailbox first, with the same key. If Mailman refuses — a name it
	// already has — nothing has moved yet, which is the order that makes a failure
	// cost nothing.
	p, err := provision.New(s.store, a.Provision)
	if err != nil {
		return err
	}
	key, err := s.store.Key(from)
	if err != nil {
		return err
	}
	if err := p.Mailbox(to, key); err != nil {
		return err
	}

	if err := s.store.RenameIdentity(s.who, from, to); err != nil {
		return err
	}
	if err := p.RemoveMailbox(from); err != nil {
		// The rename succeeded; the old mailbox lingering is untidy rather than
		// broken, and saying so beats failing a command that has already done the
		// thing it was asked to do.
		a.note("the old mailbox %s could not be removed: %v", from, err)
	}

	if err := a.say(fmt.Sprintf("%s %s to %s   same key, same memories, same tree",
		a.out.Good("renamed"), a.out.Identity(from.String()), a.out.Operator(to.String()))); err != nil {
		return err
	}
	// The credential in their shell now names somebody who does not exist. With the
	// keyring fallback that fixes itself the moment they unset it, which is worth
	// saying rather than leaving them to discover.
	if !s.fromKeyring {
		a.note("$ORC_USER still says %s; unset $ORC_USER and $ORC_KEY and orc will read the keyring, or re-export with `orc owner env`", from)
	}
	return nil
}

// ownerReset destroys the fleet and bootstraps a fresh one.
//
// It is the most destructive command in the tool, so the confirmation is specific
// rather than general: a count of what will go, by kind. "This will remove
// everything" is not something anybody can weigh; "3 identities, 2 workspaces
// holding files, 1 live session" is.
func (a App) ownerReset(args []string) error {
	var yes bool
	var as string
	rest, err := flagged(args, options{
		switches: map[string]*bool{"--yes": &yes},
		values:   map[string]*string{"--as": &as},
	})
	if err != nil {
		return err
	}
	if err := exactly(rest, 0, "owner reset takes no arguments; --as names the new operator"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mustBeOperator("reset the fleet"); err != nil {
		return err
	}

	census, err := s.store.Survey()
	if err != nil {
		return err
	}
	root := s.store.Root()

	if !yes {
		return fault.Usage{Reason: fmt.Sprintf(
			"reset removes the whole fleet at %s and bootstraps a new one.\n"+
				"  going: %d identit%s (%d employed, %d running), %d workspace%s with files in them,\n"+
				"         %d role%s, %d permission%s, and every key in the keyring.\n"+
				"  staying: the mail itself — mailman keeps messages; the accounts go.\n"+
				"  pass --yes if that is what you want",
			root,
			census.Identities, plural2(census.Identities, "y", "ies"), census.Employed, census.Live,
			census.Workspaces, plural(census.Workspaces),
			census.Roles, plural(census.Roles),
			census.Permission, plural(census.Permission))}
	}

	// The new operator's name is decided before anything is destroyed, so a bad
	// name is a refusal rather than a fleet that is gone and cannot be rebuilt.
	name, err := a.operatorName(as)
	if err != nil {
		return err
	}

	// Sessions first. A supervisor whose store has been removed underneath it would
	// keep a claude process alive with nowhere to write, so every one is stopped
	// before the store goes.
	identities, err := s.store.Identities()
	if err != nil {
		return err
	}
	for _, i := range identities {
		if _, live, err := s.store.Session(i.Name()); err == nil && live {
			if err := a.depopulate(s.store, i.Name()); err != nil {
				a.note("%s's session could not be stopped: %v", i.Name(), err)
			}
		}
	}

	// Then the mailboxes, so a fresh bootstrap can provision the name again —
	// `mailman admin user add` refuses a name it already has.
	p, err := provision.New(s.store, a.Provision)
	if err != nil {
		return err
	}
	for _, i := range identities {
		if err := p.RemoveMailbox(i.Name()); err != nil {
			a.note("%s's mailbox could not be removed: %v", i.Name(), err)
		}
	}

	if err := s.store.Destroy(); err != nil {
		return err
	}
	if err := a.say(fmt.Sprintf("%s %s   %d identit%s gone",
		a.out.Warn("destroyed"), a.out.Value(root),
		census.Identities, plural2(census.Identities, "y", "ies"))); err != nil {
		return err
	}

	// And bootstrap, in the same command. A reset that left the machine with no
	// fleet would be two commands where the second is easy to forget, and a
	// half-reset machine is one where every tool refuses.
	return a.bootstrap(bootstrapArgs(name))
}

// bootstrapArgs turns a resolved name back into the argument list bootstrap parses.
//
// It goes back through the flag rather than calling an internal path, so `reset`
// and `bootstrap` cannot drift: whatever bootstrap does about mailboxes, keys, and
// the shell block, reset gets exactly that.
func bootstrapArgs(name user.Name) []string { return []string{"--as", name.String()} }

// mustBeOperator refuses anybody who is not the root of the tree.
//
// Authority is not enough here, and that is deliberate: an agent at authority 99
// may direct almost everybody, and still must not be able to rename the operator
// or wipe the fleet. Those are the owner's, and the owner is a position rather than
// a level.
func (s caller) mustBeOperator(action string) error {
	if s.who.String() == s.fleet.Operator().String() {
		return nil
	}
	// "in this fleet" rather than "this fleet": Denied renders
	// `<actor> may not <action> <target>`, and every action passed here is already
	// a phrase with its own object — "rename the operator", "set a worklist budget".
	return fault.Denied{Actor: s.who.String(), Action: action, Target: "in this fleet",
		Reason: fmt.Sprintf("only %s can, and that is a position rather than an authority level",
			s.fleet.Operator())}
}

// ownerEnv is `orc owner env`: the export block for the operator, found without
// being told who they are.
//
// `orc env <identity>` already exists and needs a credential to run. This is the
// one an operator wants when they have none — after a rename, in a fresh shell, in
// a script — so it takes no argument and reads the keyring.
func (a App) ownerEnv() error {
	s, err := a.begin()
	if err != nil {
		return err
	}
	operator := s.fleet.Operator()
	key, err := s.store.Key(operator)
	if err != nil {
		return err
	}

	a.note("this prints %s's key; it is a credential, so do not log the output", operator)
	return a.say(strings.Join([]string{
		fmt.Sprintf("export ORC_USER=%s", operator),
		fmt.Sprintf("export ORC_KEY=%s", key),
		fmt.Sprintf("export ORC_HOME=%s", s.store.Root()),
	}, "\n"))
}
