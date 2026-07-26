package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/provision"
	"orc/orc/internal/store"
)

// bootstrap makes the fleet: the store, the operator identity, its credential,
// and its mailbox.
//
// It is the only command that runs without a fleet, and the only one that does not
// authenticate — there is nothing to authenticate against until it has run, and
// requiring an identity to create the first identity is a rule that cannot be
// satisfied.
//
// Running it twice is not an error and not a re-creation: it reports what already
// exists and exits 0, so it is safe in a setup script. What it must never do is
// replace an operator, which is why store.SetOperator refuses rather than
// overwrites — a second operator would be a second root, and a tree with two roots
// is one whose authority caps mean nothing.
func (a App) bootstrap(args []string) error {
	var as string
	rest, err := flagged(args, options{values: map[string]*string{"--as": &as}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 0, "bootstrap takes no arguments; --as names the operator"); err != nil {
		return err
	}

	name, err := a.operatorName(as)
	if err != nil {
		return err
	}

	root, err := a.root()
	if err != nil {
		return err
	}
	s, err := store.Create(root, a.Clock)
	if err != nil {
		return err
	}

	if existing, err := s.Operator(); err == nil {
		if existing.String() != name.String() {
			// Said plainly rather than refused: the caller asked for something
			// this fleet cannot give, but nothing is wrong with the fleet.
			a.note("this fleet's operator is %s, not %s; there is one operator and it cannot be replaced",
				existing, name)
		}
		return a.say(fmt.Sprintf("%s is already bootstrapped, with %s as operator",
			a.out.Value(s.Root()), a.out.Operator(existing.String())))
	}

	p, err := provision.New(s, a.Provision)
	if err != nil {
		return err
	}
	// A zero boss is what makes this identity the root of the tree. It is the one
	// place in Orc that constructs one.
	if _, err := p.WithEntropy(a.Entropy).Identity(name, user.Name{}); err != nil {
		return err
	}
	if err := s.SetOperator(name); err != nil {
		return err
	}

	key, err := s.Key(name)
	if err != nil {
		return err
	}
	return a.sayBootstrapped(s, name, key)
}

// operatorName decides what the operator is called.
func (a App) operatorName(as string) (user.Name, error) {
	raw := strings.TrimSpace(as)
	if raw == "" {
		raw = a.User
	}
	if raw == "" {
		if v, ok := a.Env("USER"); ok {
			raw = v
		}
	}
	if strings.TrimSpace(raw) == "" {
		return user.Name{}, fault.Usage{Reason: "no operating-system user to name the operator after; pass --as <name>"}
	}
	name, err := user.Parse(raw)
	if err != nil {
		// The unix user's name may legitimately be one Mailman would refuse — a
		// capital letter, a plus sign — and that is not the caller's mistake, so
		// the message names the way round it rather than only the rule.
		return user.Name{}, fault.Usage{Reason: fmt.Sprintf(
			"%q cannot be a mailbox name (%s); pass --as <name> to choose another", raw, err)}
	}
	return name, nil
}

// sayBootstrapped prints the one screen in Orc that discloses a key.
//
// It is deliberate, and it is the reason the command exists: the operator's
// credential has to reach the operator's shell profile somehow, and printing it
// once at the moment they asked for it is better than a file they have to be told
// to go and read. `orc env` is the only other command that shows one, and it says
// so too.
func (a App) sayBootstrapped(s *store.Store, name user.Name, key string) error {
	p := a.out
	lines := []string{
		fmt.Sprintf("%s %s", p.Good("bootstrapped"), p.Value(s.Root())),
		"",
		fmt.Sprintf("  operator   %s   authority 100, every permission, the root of the tree",
			p.Operator(name.String())),
		fmt.Sprintf("  mailbox    %s   provisioned in mailman with the key below", p.Identity(name.String())),
		"",
		"put this in your shell profile — it is how mailman, muff, and cq know who you are:",
		"",
		fmt.Sprintf("  export %s=%s", p.Setting("ORC_USER"), name),
		fmt.Sprintf("  export %s=%s", p.Setting("ORC_KEY"), key),
		"",
		// `orc` itself does not need it, and saying so is worth a line: an operator who
		// thinks they have to export a key before they can look at their own fleet is
		// an operator who will paste one into a script.
		fmt.Sprintf("  %s", p.Muted("orc itself will find that key in the fleet when nothing is set — see `orc owner`")),
		"",
		fmt.Sprintf("then: %s to give somebody a job, %s to hire, %s to see the fleet",
			p.Command("orc new role"), p.Command("orc new identity"), p.Command("orc status")),
	}

	// cq mirrors the operator's mailbox and finds that out by asking orc, so
	// there is nothing to set for the ordinary case. The exception is worth a
	// line: a sync triggered by an *agent's* mail carries that agent's
	// credential, and cq refuses to mirror one account's mail as another's.
	if _, ok := a.Env("CQ_SERVER"); ok {
		lines = append(lines, "",
			fmt.Sprintf("this machine syncs to cq, which will mirror %s without being told", p.Operator(name.String())),
			fmt.Sprintf("  %s", p.Muted(fmt.Sprintf(
				"for an agent's mail to nudge a sync too, export %s and %s to the pair above",
				p.Setting("CQ_USER"), p.Setting("CQ_KEY")))))
	}
	return a.say(strings.Join(lines, "\n"))
}
