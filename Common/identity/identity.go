// Package identity finds out who is running this command.
//
// Orc's tools do not issue identities. Vision.md places account control in Orc's
// remote auth, and no tool has an auth command, so this package is a
// consumer: it reads a credential that Orc provisioned and hands it to the
// tool to be verified. There is no session, no cache, and no token with a
// lifetime — "authentication happens on every request" is taken at its word,
// which also means a revoked account stops working on the next command rather
// than at some later expiry.
//
// The credential comes from the environment and from nowhere else. That is a
// deliberately small contract: Orc spawns the agent, so Orc controls the
// agent's environment, and a single source means there is no precedence order
// in which a typo in one place can silently authenticate as whoever another
// place names. When Orc's remote auth is specified, this file is the one that
// should need rewriting, and no tool that consumes it should.
//
// Nothing here touches the filesystem, which is why this package has no
// injected operations and no tests that need a temporary directory.
package identity

import (
	"fmt"
	"os"
	"strings"

	"orc/common/fault"
	"orc/common/user"
)

// The environment variables of the credential contract.
const (
	EnvUser = "ORC_USER"
	EnvKey  = "ORC_KEY"
)

// Credential is a resolved identity: a name and the key that proves it.
type Credential struct {
	name user.Name
	key  string
}

// Name returns whose credential this is.
func (c Credential) Name() user.Name { return c.name }

// Key returns the secret.
//
// It is deliberately absent from String: Mailman prints its own errors, and a
// credential that stringifies its key eventually finds its way into one.
func (c Credential) Key() string { return c.key }

// Zero reports whether the credential was never resolved.
func (c Credential) Zero() bool { return c.name.Zero() }

// String describes the credential without disclosing it.
func (c Credential) String() string {
	if c.Zero() {
		return "no credential"
	}
	return fmt.Sprintf("%s (from the environment)", c.name)
}

func (c Credential) validate() error {
	if c.name.Zero() {
		return fault.Internal{Where: "identity.Credential", Detail: "name is unset"}
	}
	return user.CheckKey(c.key)
}

// Env looks up an environment variable, reporting whether it was set at all.
// It is shaped like os.LookupEnv so that "set but empty" stays distinguishable
// from "absent" — the two need different messages.
type Env func(key string) (string, bool)

// OSEnv reads the real environment.
func OSEnv(key string) (string, bool) { return os.LookupEnv(key) }

// MapEnv reads an injected environment, for tests.
func MapEnv(m map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// Resolver reads a credential from an environment. The zero value is not
// usable; build one with New.
type Resolver struct {
	env Env
}

// New builds a resolver. A nil environment reads the process's own.
func New(env Env) Resolver {
	if env == nil {
		env = OSEnv
	}
	return Resolver{env: env}
}

// Resolve reads the credential.
//
// Both variables must be present together. A half-set environment is an error
// rather than a fall-through to some other source, because there is no other
// source: being told exactly which half is missing is the whole diagnostic.
func (r Resolver) Resolve() (Credential, error) {
	if r.env == nil {
		return Credential{}, fault.Internal{Where: "identity.Resolver", Detail: "resolver was not built with New"}
	}

	rawName, hasName := r.env(EnvUser)
	rawKey, hasKey := r.env(EnvKey)

	switch {
	case !hasName && !hasKey:
		return Credential{}, missing()
	case !hasKey:
		return Credential{}, fault.Auth{
			Reason: fmt.Sprintf("%s is set but %s is not; orc sets both", EnvUser, EnvKey),
			Detail: "env: user without key",
		}
	case !hasName:
		return Credential{}, fault.Auth{
			Reason: fmt.Sprintf("%s is set but %s is not; orc sets both", EnvKey, EnvUser),
			Detail: "env: key without user",
		}
	}

	// The name's own error is shown: a bad user name is a configuration mistake
	// the operator can act on, and it discloses nothing. The key's is reported
	// as a length or encoding problem without echoing any part of the value.
	name, err := user.Parse(rawName)
	if err != nil {
		return Credential{}, fault.Auth{
			Reason: fmt.Sprintf("the user name in %s is not usable: %s", EnvUser, err),
			Detail: "bad name",
		}
	}
	if err := user.CheckKey(rawKey); err != nil {
		return Credential{}, fault.Auth{
			Reason: fmt.Sprintf("the key in %s is not usable: %s", EnvKey, err),
			Detail: "bad key",
		}
	}

	c := Credential{name: name, key: rawKey}
	if err := c.validate(); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// missing explains that no credential was supplied. An agent that has not been
// given an identity has nothing else to go on, so the message is the whole
// contract.
func missing() error {
	var b strings.Builder
	b.WriteString("no orc credential; every orc tool authenticates on every command\n")
	fmt.Fprintf(&b, "  set %s and %s in the environment:\n", EnvUser, EnvKey)
	fmt.Fprintf(&b, "    export %s=<mailbox>\n", EnvUser)
	fmt.Fprintf(&b, "    export %s=<key>\n", EnvKey)
	b.WriteString("  orc normally provides these; run the tool's `help` for more")
	return fault.Auth{Reason: b.String(), Detail: "no credential in the environment"}
}
