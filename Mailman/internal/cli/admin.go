package cli

import (
	"fmt"
	"io"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/render"
	"orc/mailman/internal/store"
	"orc/mailman/internal/style"
	"orc/theme"
)

// adminUsage is the provisioning command's help, painted like every other screen.
func adminUsage(p style.Palette) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	column := func(form, does string) {
		gap := 36 - theme.Width(form)
		if gap < 1 {
			gap = 1
		}
		fmt.Fprintf(&b, "  %s%s%s\n", p.Command(form), strings.Repeat(" ", gap), does)
	}

	line("%s — provisioning and the whole-store view", p.Tool("mailman admin"))
	line("")
	column("mailman admin user add    <name>", "create a mailbox and print a fresh key")
	column("mailman admin user add    <name> --key -", "create one with a key read from stdin")
	column("mailman admin user remove <name>", "remove a mailbox")
	column("mailman admin user list", "list mailboxes")
	line("")
	column("mailman admin owner [<name>]", "show, or name, who may read the store whole")
	column("mailman admin mail  [--json]", "every message, and whose mailbox holds it")
	line("")
	line("%s", p.Muted("provisioning does not authenticate: an empty store has no identity to check"))
	line("%s", p.Muted("against, and it has to be possible to bootstrap. reading the store whole is a"))
	line("%s", p.Muted("different act and does authenticate — as the owner, and only the owner. on a"))
	line("%s", p.Muted("machine where several agents run as the same operating-system user, file"))
	line("%s", p.Muted("permissions separate nothing, so a key is the only thing that can."))
	line("")
	line("%s", p.Muted("account control belongs to orc, which provisions every mailbox through this"))
	line("%s%s%s", p.Muted("command with "), p.Flag("--key"), p.Muted(", so that one key is chosen once and orc's record and"))
	line("%s", p.Muted("mailman's agree about it. this command stays after orc rather than being deleted"))
	line("%s", p.Muted("by it: it is how an empty store is bootstrapped, and how mailman is tested on a"))
	line("%s", p.Muted("machine with no orchestrator installed. it is not part of the CLI in"))
	line("%s", p.Muted("Docs/Mailman/Reference.md."))

	return strings.TrimRight(b.String(), "\n")
}

// admin provisions mailboxes.
//
// It deliberately does not authenticate: there is no identity to check against
// a store that has no users yet, and requiring one would make an empty store
// impossible to bootstrap. The store's 0700 permissions are what protect it,
// which is the same boundary every other command ultimately rests on.
func (a App) admin(args []string, f flags) error {
	if len(args) == 0 {
		return a.say(adminUsage(a.out))
	}
	switch args[0] {
	case "owner":
		return a.adminOwner(args[1:], f)
	case "mail":
		return a.adminMail(args[1:], f)
	case "user":
	default:
		return fault.Usage{Reason: fmt.Sprintf("unknown admin command %q; try `mailman admin user list`", args[0])}
	}
	if len(args) < 2 {
		return fault.Usage{Reason: "admin user takes add, remove, or list"}
	}

	s, err := a.openStore()
	if err != nil {
		return err
	}

	switch args[1] {
	case "add":
		return a.adminAdd(s, args[2:], f)
	case "remove":
		return a.adminRemove(s, args[2:])
	case "list":
		return a.adminList(s, args[2:], f)
	default:
		return fault.Usage{Reason: fmt.Sprintf("unknown admin user command %q; try add, remove, or list", args[1])}
	}
}

func (a App) adminAdd(s *store.Store, args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("admin user add takes one name, got %d arguments", len(args))}
	}
	name, err := user.Parse(args[0])
	if err != nil {
		return err
	}

	key, given, err := a.chooseKey(f)
	if err != nil {
		return err
	}
	if err := s.CreateUser(name, key); err != nil {
		return err
	}

	// A key the caller chose is not echoed. They already have it, and Orc — the
	// caller that supplies one — provisions mailboxes non-interactively, where an
	// echoed credential is a credential in a log file.
	if given {
		return a.say(fmt.Sprintf("created mailbox %s with the key supplied by the caller", name))
	}

	// The key is printed once and never stored in a form anything can recover
	// it from. Saying so is part of the output, because a caller who does not
	// capture it here has to have the mailbox recreated.
	if err := a.say(fmt.Sprintf("created mailbox %s", name)); err != nil {
		return err
	}
	if err := a.say("this key is shown once and cannot be recovered:"); err != nil {
		return err
	}
	if err := a.say("  " + key); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("\nuse it with:\n  export ORC_USER=%s\n  export ORC_KEY=%s", name, key))
}

// chooseKey returns the key a new mailbox should be created with, and reports
// whether the caller chose it.
//
// Orc mints keys, because Orc is the fleet's account authority and has to be able
// to hand the same credential out again on every session restart. A key Mailman
// invented and printed would have to be scraped back off this command's stdout,
// and a credential that travels through a terminal is one that ends up in a
// scrollback buffer. So `--key -` reads it from stdin, where it is never an
// argument in anybody's process table either.
//
// Without --key nothing changes: a fresh key is minted and printed, which is what
// a person bootstrapping a store by hand needs.
func (a App) chooseKey(f flags) (key string, given bool, err error) {
	if !f.keyGiven {
		key, err = user.NewKey(nil)
		return key, false, err
	}

	key = f.key
	if key == "-" {
		if a.Stdin == nil {
			return "", true, fault.Usage{Reason: "--key - reads the key from stdin, and there is no stdin"}
		}
		// Bounded: a key is at most user.MaxKeyLen, and reading an unbounded
		// stream into memory because somebody piped a file by mistake is a way to
		// turn a typo into a dead machine.
		data, readErr := io.ReadAll(io.LimitReader(a.Stdin, user.MaxKeyLen+1))
		if readErr != nil {
			return "", true, fault.IO{Op: "read the key from", Path: "stdin", Err: readErr}
		}
		key = strings.TrimSpace(string(data))
	}
	if err := user.CheckKey(key); err != nil {
		return "", true, err
	}
	return key, true, nil
}

func (a App) adminRemove(s *store.Store, args []string) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("admin user remove takes one name, got %d arguments", len(args))}
	}
	name, err := user.Parse(args[0])
	if err != nil {
		return err
	}

	// Removing the owner would lock the whole-store view permanently: the view
	// requires the owner's key, and so does naming a new owner, so once that
	// account is gone neither is possible again. Refusing is the only outcome
	// that leaves a way forward.
	owner, err := s.Owner()
	if err != nil {
		return err
	}
	if !owner.Zero() && owner.String() == name.String() {
		return fault.Conflict{Reason: fmt.Sprintf(
			"%s owns this store, and removing them would lock the whole-store view for good.\n"+
				"  hand it over first: `mailman admin owner <someone else>`, as %s",
			name, name)}
	}

	if err := s.DeleteUser(name); err != nil {
		return err
	}
	// Mail is not deleted: it belongs to its other participants too, and
	// removing it would silently edit their mailboxes.
	return a.say(fmt.Sprintf("removed mailbox %s; the mail they sent and received is untouched", name))
}

func (a App) adminList(s *store.Store, args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: fmt.Sprintf("admin user list takes no arguments, got %d", len(args))}
	}
	names, err := s.Users()
	if err != nil {
		return err
	}
	if f.json {
		// Accounts carry no creation time: a mailbox is a directory, and
		// Mailman has never had a reason to record when it appeared. The field
		// is omitted rather than invented.
		out := make([]jsonUser, 0, len(names))
		for _, n := range names {
			out = append(out, jsonUser{Name: n.String()})
		}
		return a.emitJSON(out)
	}
	palette, err := a.paint()
	if err != nil {
		return err
	}
	out, err := render.Users(names, palette, a.Width)
	if err != nil {
		return err
	}
	return a.write(out)
}

// verify walks the store and reports what is wrong, without changing anything.
//
// A store that several unsupervised agents write to needs a way to answer "is
// this healthy?" that is not "read the source". It is additive: nothing else
// depends on it, and it never repairs, because an automatic repair of a store
// whose damage is not understood is how one bad file becomes many.
func (a App) verify(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: fmt.Sprintf("verify takes no arguments, got %d", len(args))}
	}
	s, err := a.openStore()
	if err != nil {
		return err
	}

	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	names, err := s.Users()
	if err != nil {
		return err
	}
	if err := a.say(fmt.Sprintf("store: %s", s.Root())); err != nil {
		return err
	}

	// pruned records mail that was deliberately deleted. A conversation still
	// naming it is the expected outcome of `prune`, not damage, and reporting
	// it as a problem would make a healthy store look broken every time
	// somebody tidied up.
	pruned := map[string]bool{}
	for _, name := range names {
		st, err := s.Replay(name)
		if err != nil {
			report("%s: journal will not replay: %v", name, err)
			continue
		}
		for _, id := range st.PrunedIDs() {
			pruned[id.String()] = true
		}
		if st.Skipped() > 0 {
			report("%s: %d bytes at the end of the journal were left by an interrupted write", name, st.Skipped())
		}

		live, missing := 0, 0
		for _, e := range st.Entries() {
			ok, err := s.HasMessage(e.MID)
			if err != nil {
				report("%s: %s could not be checked: %v", name, e.MID.Short(), err)
				continue
			}
			if !ok {
				report("%s: inbox references %s, which is not in the store", name, e.MID.Short())
				missing++
				continue
			}
			if _, err := s.Get(e.MID); err != nil {
				report("%s: %s will not decode: %v", name, e.MID.Short(), err)
				continue
			}
			live++
		}

		// The journal and the receipts both record reads. They are allowed to
		// be redundant; they are not allowed to disagree.
		//
		// Only mail actually addressed to this user is checked: a sender's own
		// copy is filed already-read and carries no receipt, by design.
		for _, e := range st.Entries() {
			msg, err := s.Get(e.MID)
			if err != nil {
				continue // already reported above
			}
			if !user.Contains(msg.Recipients(), name) {
				continue
			}
			receipts, err := s.Receipts(e.MID)
			if err != nil {
				report("%s: receipts for %s could not be read: %v", name, e.MID.Short(), err)
				continue
			}
			hasReceipt := false
			for _, r := range receipts {
				if r.User.String() == name.String() {
					hasReceipt = true
				}
			}
			if !e.Unread() && !hasReceipt {
				report("%s: %s is marked read but has no receipt, so `check` will not show it", name, e.MID.Short())
			}
			if e.Unread() && hasReceipt {
				report("%s: %s has a receipt but is not marked read", name, e.MID.Short())
			}
		}

		if err := a.say(fmt.Sprintf("  %-20s %3d live · %3d missing · next id %d",
			name.String(), live, missing, st.NextPUID())); err != nil {
			return err
		}
	}

	convos, err := s.Convos()
	if err != nil {
		return err
	}
	for _, id := range convos {
		c, err := s.Convo(id)
		if err != nil {
			report("conversation %s will not load: %v", id.Short(), err)
			continue
		}
		if c.Skipped() > 0 {
			report("conversation %s: %d bytes were left by an interrupted write", id.Short(), c.Skipped())
		}
		for _, mid := range c.Messages() {
			ok, err := s.HasMessage(mid)
			if err != nil {
				report("conversation %s: %s could not be checked: %v", id.Short(), mid.Short(), err)
				continue
			}
			if !ok && !pruned[mid.String()] {
				report("conversation %s references %s, which is not in the store and was never pruned", id.Short(), mid.Short())
			}
		}
	}
	if err := a.say(fmt.Sprintf("  %-20s %3d", "conversations", len(convos))); err != nil {
		return err
	}

	if len(problems) == 0 {
		return a.say("\nno problems found")
	}
	if err := a.say(fmt.Sprintf("\n%s:", plural(len(problems), "problem"))); err != nil {
		return err
	}
	for _, p := range problems {
		if err := a.say("  " + p); err != nil {
			return err
		}
	}
	// A damaged store is a real failure, so the exit code says so and a hook
	// can branch on it.
	return fault.Conflict{Path: s.Root(), Reason: fmt.Sprintf("%s found", plural(len(problems), "problem"))}
}
