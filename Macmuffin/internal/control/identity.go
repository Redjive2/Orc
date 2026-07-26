package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
)

// Verifying an identity, as opposed to resolving one.
//
// `$ORC_USER` and `$ORC_KEY` are what an agent *claims*. Macmuffin has no user
// records of its own — they live in Mailman's store, and Orc mints one key per
// identity and provisions both — so on its own Macmuffin can check the shape of
// a credential and nothing more. Every permission decision in `policy` rests on
// the caller being who they say, which makes an unchecked claim the weakest
// joint in the tool.
//
// Orc can answer, because Orc authenticates on every command: `orc introspect
// --only identity` prints the identity the credential really belongs to, and
// exits 7 when the key does not prove anything.

// FieldIdentity is the introspect field naming the caller.
const FieldIdentity = "identity"

// CodeAuth is what Orc exits when a credential proves nothing.
const CodeAuth = fault.CodeAuth

// Verifier checks that a claimed identity is the credential's real one.
type Verifier func(claimed user.Name) error

// Unverifiable reports that no authority was available to ask.
//
// It is separate from a failed verification because the two mean opposite
// things: one says the caller is not who they claim, the other says nobody
// could be asked. Only the first may refuse a command.
type Unverifiable struct {
	Reason string
}

func (e Unverifiable) Error() string { return "identity could not be verified: " + e.Reason }

// Unwrap makes this exit 10 if it ever reaches the top, though the CLI handles
// it rather than returning it.
func (e Unverifiable) Unwrap() error { return fault.ErrUnavailable }

// Verified asks the real `orc` binary, within Deadline.
func Verified(claimed user.Name) error { return VerifiedWithin(Deadline, claimed) }

// VerifiedWithin is Verified with the deadline given.
//
// Where no Orc is installed this reports Unverifiable rather than refusing.
// Macmuffin predates Orc and has to keep working beside it — `muff` on a
// machine with no fleet is a task list, and refusing every command because the
// authority for a claim nobody is contesting is absent would make it useless.
//
// That is a real limit and is stated rather than implied away: an agent that
// controls its own `PATH` can hide Orc as easily as it can lie about its name,
// so this catches a *mistaken* identity — a stale key, a typo, a credential
// copied from another agent — and not a determined one. Stopping that needs an
// authority Macmuffin cannot be denied, which today means Orc being the thing
// that starts the session.
func VerifiedWithin(deadline time.Duration, claimed user.Name) error {
	if claimed.Zero() {
		return fault.Internal{Where: "control.Verified", Detail: "no identity claimed"}
	}

	if _, err := exec.LookPath(Binary); err != nil {
		return Unverifiable{Reason: Binary + " is not installed, so no fleet can confirm who you are"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, Binary, "introspect", "--only", FieldIdentity)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	if err == nil {
		return sameIdentity(claimed, stdout.String())
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return Unverifiable{Reason: fmt.Sprintf("%s could not be run: %v", Binary, err)}
	}
	if exit.ExitCode() == CodeAuth {
		// A definite no. Orc holds the key digest and says this credential
		// proves nothing, which is the one answer that must stop the command.
		return fault.Auth{Reason: reasonOr(stderr.String(),
			fmt.Sprintf("orc does not accept this credential for %s", claimed))}
	}
	// Orc answered, but not with an answer: a store it cannot read, a fleet
	// that will not derive. Nobody has said the caller is lying.
	return Unverifiable{Reason: fmt.Sprintf("%s exited %d: %s", Binary, exit.ExitCode(),
		reasonOr(stderr.String(), "no reason given"))}
}

// sameIdentity compares what Orc says against what the environment claims.
//
// In practice Orc refuses the pair first: a key that belongs to bob does not
// authenticate alice, so `introspect` exits 7 and this is never reached. The
// comparison is here anyway because it costs one string compare and it is the
// only thing standing between Macmuffin and an authority that answers a
// different question than the one asked — a key valid for several identities,
// say. Trusting a reply without checking it answers the right question is how
// bridges like this quietly stop meaning anything.
func sameIdentity(claimed user.Name, printed string) error {
	got, err := user.Parse(strings.TrimSpace(printed))
	if err != nil {
		return Unverifiable{Reason: Binary + " named an identity that will not parse: " + err.Error()}
	}
	if got.String() != claimed.String() {
		return fault.Auth{Reason: fmt.Sprintf(
			"ORC_USER says %s, but that key belongs to %s; the two must agree", claimed, got)}
	}
	return nil
}

// reasonOr trims a tool's diagnostic, falling back when it said nothing.
func reasonOr(stderr, fallback string) string {
	got := strings.TrimSpace(stderr)
	got = strings.TrimSpace(strings.TrimPrefix(got, Binary+":"))
	if got == "" {
		return fallback
	}
	return got
}
