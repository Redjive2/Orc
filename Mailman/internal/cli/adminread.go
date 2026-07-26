package cli

import (
	"fmt"

	"orc/common/fault"
	"orc/common/identity"
	"orc/common/user"
	"orc/mailman/internal/render"
	"orc/mailman/internal/store"
	"orc/mailman/internal/view"
)

// adminOwner shows or names the account permitted to read the store whole.
//
// Naming the first owner does not authenticate, for the same reason `admin user
// add` does not: a store nobody owns yet has nothing to check a claim against.
// Changing an owner does, and as that owner — otherwise the setting would be a
// lock whose key is "run the command again".
func (a App) adminOwner(args []string, f flags) error {
	if len(args) > 1 {
		return fault.Usage{Reason: fmt.Sprintf("admin owner takes one name or none, got %d arguments", len(args))}
	}

	s, err := a.openStore()
	if err != nil {
		return err
	}
	current, err := s.Owner()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		if current.Zero() {
			return a.say("no owner; nobody may read this store whole\n" +
				"  name one with `mailman admin owner <name>`")
		}
		return a.say(current.String())
	}

	name, err := user.Parse(args[0])
	if err != nil {
		return err
	}

	if !current.Zero() {
		// Already owned, so this is a change, and a change needs the owner's key.
		who, err := a.authenticated(s)
		if err != nil {
			return err
		}
		if err := s.AuthoriseOwner(who); err != nil {
			return err
		}
		if current.String() == name.String() {
			return a.say(name.String() + " already owns this store")
		}
	}

	if err := s.SetOwner(name); err != nil {
		return err
	}
	if current.Zero() {
		return a.say(name.String() + " owns this store and may read it whole")
	}
	return a.say(name.String() + " owns this store; " + current.String() + " no longer does")
}

// adminMail prints every message in the store.
//
// This is the command cq's admin panel is built on, and the only way to see the
// whole store without reading its files directly. It authenticates, and refuses
// anyone but the owner.
func (a App) adminMail(args []string, f flags) error {
	if len(args) > 0 {
		return fault.Usage{Reason: fmt.Sprintf("admin mail takes no arguments, got %d", len(args))}
	}

	s, err := a.openStore()
	if err != nil {
		return err
	}
	who, err := a.authenticated(s)
	if err != nil {
		return err
	}
	if err := s.AuthoriseOwner(who); err != nil {
		return err
	}

	whole, damaged, err := view.WholeStore(s)
	if err != nil {
		return err
	}

	// Damage is reported on the other stream so it cannot corrupt the JSON a
	// program is reading, and so a person still sees it.
	for _, d := range damaged {
		a.note("part of the store could not be read and is not shown: %v", d.Err)
	}

	if f.json {
		return a.emitJSON(adminMailJSON(whole, !f.noBodies))
	}
	palette, err := a.paint()
	if err != nil {
		return err
	}
	out, err := render.WholeStore(whole, palette, a.Width)
	if err != nil {
		return err
	}
	return a.say(out)
}

// authenticated resolves and verifies the caller's identity.
//
// `admin` deliberately runs without a session — provisioning has to work on an
// empty store — so the commands that *do* need one ask for it explicitly here
// rather than the whole subcommand tree acquiring it and one path forgetting to.
func (a App) authenticated(s *store.Store) (user.Name, error) {
	cred, err := identity.New(identity.Env(a.Env)).Resolve()
	if err != nil {
		return user.Name{}, err
	}
	if err := s.Authenticate(cred.Name(), cred.Key()); err != nil {
		return user.Name{}, err
	}
	return cred.Name(), nil
}
