// Package provision makes an identity real across every tool that has to know
// about it.
//
// Creating an identity is four things, not one: a record in Orc's store, a
// credential Orc keeps the only plaintext copy of, a Mailman mailbox with the
// same key, and a Claude configuration directory the identity's future sessions
// will be pointed at. Any of them can fail, and a half-made identity is worse
// than none — an identity with no mailbox cannot do the one thing every agent in
// this tree does, and one with no key is an account nobody can ever act as.
//
// So this package owns the order, the rollback, and exactly one rule about the
// other tools: **Orc provisions Mailman through Mailman's own command.** Writing
// another tool's records directly is the mistake Orcprobe's plan reached the same
// conclusion about — a store's owner is the only thing that should decide what a
// valid record in it looks like.
package provision

import (
	"fmt"
	"os/exec"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

// Binary is the mailman command to run. It is a variable only so a machine that
// installs Mailman under another name can say so, and so a test can point it at
// something harmless.
var Binary = "mailman"

// Run executes a provisioning command in another tool, passing text on stdin.
//
// It is injected everywhere in this package so the whole of provisioning is
// testable without a Mailman installed — and so no test can create a mailbox in
// the developer's own store, which is the kind of accident a fleet tool gets to
// have exactly once.
type Run func(args []string, stdin string) error

// Exec runs the real binary.
//
// The key goes on stdin rather than in argv, and that is the reason `mailman admin
// user add --key -` exists at all: argv is visible in `ps` to every process on
// the machine, and a fleet's credentials must not be.
func Exec(args []string, stdin string) error {
	cmd := exec.Command(Binary, args...)
	cmd.Stdin = strings.NewReader(stdin)

	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s %s: %s", Binary, strings.Join(args, " "), detail)
	}
	return nil
}

// Provisioner creates identities. The zero value is not usable; build one with
// New.
type Provisioner struct {
	store *store.Store
	run   Run
	// entropy is nil for the real thing, which uses crypto/rand. A test injects
	// a reader to pin an id.
	entropy readerFunc
}

// readerFunc is the entropy source, shaped so a nil value means "the real one"
// without this package importing io just to say so.
type readerFunc interface {
	Read(p []byte) (int, error)
}

// New builds a provisioner. A nil Run uses the real `mailman` binary.
func New(s *store.Store, run Run) (Provisioner, error) {
	if s == nil {
		return Provisioner{}, fault.Internal{Where: "provision.New", Detail: "no store given"}
	}
	if run == nil {
		run = Exec
	}
	return Provisioner{store: s, run: run}, nil
}

// WithEntropy returns a provisioner that mints ids from a fixed source.
func (p Provisioner) WithEntropy(r readerFunc) Provisioner {
	p.entropy = r
	return p
}

// Identity provisions a whole identity, and cleans up after itself if it cannot.
//
// The order is chosen so that every failure leaves either nothing or something
// `orc verify` can name:
//
//  1. the record and the directories, which is what makes the name unique;
//  2. the credential — digest and key together;
//  3. the mailbox, through Mailman's own command;
//  4. the Claude configuration.
//
// Steps 1–3 are rolled back on failure. Step 4 is not: a missing Claude directory
// is remade by the next populate, and an identity that exists everywhere except
// in its own configuration is a working identity, whereas one whose mailbox
// creation half-succeeded is not.
//
// boss is zero for the operator alone.
func (p Provisioner) Identity(name user.Name, boss user.Name) (model.Identity, error) {
	if p.store == nil {
		return model.Identity{}, fault.Internal{Where: "provision.Identity", Detail: "provisioner was not built with New"}
	}
	if name.Zero() {
		return model.Identity{}, fault.Internal{Where: "provision.Identity", Detail: "no name given"}
	}

	id, err := model.NewID(p.store.Now(), p.entropy)
	if err != nil {
		return model.Identity{}, err
	}

	made, err := p.store.CreateIdentity(name, id, boss)
	if err != nil {
		return model.Identity{}, err
	}

	// From here every failure removes the identity again. A name that exists with
	// no key behind it would be claimable by nobody and unremovable without
	// --yes, which is a worse state than not having tried.
	undo := func(cause error) (model.Identity, error) {
		if err := p.store.DeleteIdentity(name); err != nil {
			// Both problems are reported: the one that happened, and the fact that
			// the cleanup did not. Hiding the second would leave an operator
			// wondering why the name is taken.
			return model.Identity{}, fault.IO{
				Op:   fmt.Sprintf("roll back %s after %q; the half-made identity is still there and `orc remove identity %s --yes` clears it, at", name, cause, name),
				Path: p.store.IdentityDir(name), Err: err}
		}
		return model.Identity{}, cause
	}

	key, err := user.NewKey(nil)
	if err != nil {
		return undo(err)
	}
	if err := p.store.WriteCredential(name, key); err != nil {
		return undo(err)
	}

	// The mailbox, with the key Orc chose. `mailman admin user add` is not
	// idempotent — it refuses a name that exists — which is what makes this the
	// step that decides whether two agents creating the same identity both
	// succeed, even if Orc's own store were somehow permissive.
	if err := p.run([]string{"admin", "user", "add", name.String(), "--key", "-"}, key); err != nil {
		return undo(fault.Conflict{Path: name.String(), Reason: "mailbox could not be created: " + err.Error()})
	}

	if err := p.claudeConfig(name); err != nil {
		// Not rolled back: see the note above. Reported, so it is not a silence.
		return made, err
	}
	return made, nil
}

// Mailbox provisions a Mailman account with a key the caller chose.
//
// It is the half of Identity that reaches outside Orc, on its own, for the one
// caller that needs it without the rest: a rename gives an existing identity a new
// mailbox under a new name with the *same* key, so that everything holding the
// credential keeps working. Nothing about that is a new identity.
func (p Provisioner) Mailbox(name user.Name, key string) error {
	if p.store == nil {
		return fault.Internal{Where: "provision.Mailbox", Detail: "provisioner was not built with New"}
	}
	if name.Zero() {
		return fault.Internal{Where: "provision.Mailbox", Detail: "no name given"}
	}
	if err := user.CheckKey(key); err != nil {
		return err
	}
	if err := p.run([]string{"admin", "user", "add", name.String(), "--key", "-"}, key); err != nil {
		return fault.Conflict{Path: name.String(), Reason: "mailbox could not be created: " + err.Error()}
	}
	return nil
}

// RemoveMailbox retires an identity's Mailman account.
//
// Mailman keeps the mail — it belongs to its other participants too, and removing
// it would silently edit their mailboxes — so this removes the *account*, which is
// what frees the name for reuse and stops the key working.
//
// A mailbox that is already gone is not an error. `orc remove identity` is the
// caller, and a removal that cannot be retried because half of it already
// succeeded is worse than one that tolerates the half.
func (p Provisioner) RemoveMailbox(name user.Name) error {
	if p.store == nil {
		return fault.Internal{Where: "provision.RemoveMailbox", Detail: "provisioner was not built with New"}
	}
	if name.Zero() {
		return fault.Internal{Where: "provision.RemoveMailbox", Detail: "no name given"}
	}
	err := p.run([]string{"admin", "user", "remove", name.String()}, "")
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "nothing matches") || strings.Contains(err.Error(), "not found") {
		return nil
	}
	return fault.Conflict{Path: name.String(), Reason: "mailbox could not be removed: " + err.Error()}
}
