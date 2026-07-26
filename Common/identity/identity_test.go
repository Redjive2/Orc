package identity_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/common/identity"
)

const goodKey = "0123456789abcdef0123456789abcdef"

func env(pairs ...string) identity.Env {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return identity.MapEnv(m)
}

func TestResolve(t *testing.T) {
	got, err := identity.New(env(identity.EnvUser, "Alice", identity.EnvKey, goodKey)).Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The name is normalised on the way in, so the rest of the program never
	// handles "Alice".
	if got.Name().String() != "alice" {
		t.Errorf("Name() = %q, want %q", got.Name(), "alice")
	}
	if got.Key() != goodKey {
		t.Error("the key did not survive")
	}
	if got.Zero() {
		t.Error("a resolved credential should not be zero")
	}
}

// TestNameIsNormalised covers the forms a credential actually arrives in: an
// environment variable read from a file routinely carries a trailing newline.
func TestNameIsNormalised(t *testing.T) {
	for _, raw := range []string{"alice", "ALICE", " alice ", "alice\n", "Alice\r\n"} {
		got, err := identity.New(env(identity.EnvUser, raw, identity.EnvKey, goodKey)).Resolve()
		if err != nil {
			t.Fatalf("Resolve(%q): %v", raw, err)
		}
		if got.Name().String() != "alice" {
			t.Errorf("Resolve(%q) gave %q", raw, got.Name())
		}
	}
}

// TestHalfSetEnvironmentIsNamedPrecisely: with a single source there is nothing
// to fall through to, so saying which half is missing is the whole diagnostic.
func TestHalfSetEnvironmentIsNamedPrecisely(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  identity.Env
		says string
	}{
		{"user without key", env(identity.EnvUser, "alice"), identity.EnvKey + " is not"},
		{"key without user", env(identity.EnvKey, goodKey), identity.EnvUser + " is not"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.New(tc.env).Resolve()
			if !errors.Is(err, fault.ErrAuth) {
				t.Fatalf("Resolve = %v, want an auth fault", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("message should say %q:\n%s", tc.says, err)
			}
		})
	}
}

// TestSetButEmptyIsNotAbsent: "" and unset need different messages, which is
// why the lookup reports presence separately from value.
func TestSetButEmptyIsNotAbsent(t *testing.T) {
	_, err := identity.New(env(identity.EnvUser, "", identity.EnvKey, goodKey)).Resolve()
	if !errors.Is(err, fault.ErrAuth) {
		t.Fatalf("an empty user = %v, want an auth fault", err)
	}
	// It is a bad name, not a missing credential.
	if strings.Contains(err.Error(), "no orc credential") {
		t.Errorf("an empty %s should not read as absent:\n%s", identity.EnvUser, err)
	}
}

func TestBadCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  identity.Env
		says string
	}{
		{"traversal in the name", env(identity.EnvUser, "../etc", identity.EnvKey, goodKey), "user name"},
		{"space in the name", env(identity.EnvUser, "a b", identity.EnvKey, goodKey), "user name"},
		{"reserved name", env(identity.EnvUser, "system", identity.EnvKey, goodKey), "user name"},
		{"short key", env(identity.EnvUser, "alice", identity.EnvKey, "abc"), "key"},
		{"empty key", env(identity.EnvUser, "alice", identity.EnvKey, ""), "key"},
		{"key with a newline", env(identity.EnvUser, "alice", identity.EnvKey, goodKey+"\n"), "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.New(tc.env).Resolve()
			if !errors.Is(err, fault.ErrAuth) {
				t.Fatalf("Resolve = %v, want an auth fault", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("message should mention %q:\n%s", tc.says, err)
			}
		})
	}
}

// TestKeyIsNeverEchoed: a bad key is reported as a length or encoding problem,
// never by quoting the value back into a message that will be logged.
func TestKeyIsNeverEchoed(t *testing.T) {
	secret := "secret-but-far-too-short"
	_, err := identity.New(env(identity.EnvUser, "alice", identity.EnvKey, secret)).Resolve()
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the message echoes the key:\n%s", err)
	}
}

func TestMissingCredentialIsExplained(t *testing.T) {
	_, err := identity.New(env()).Resolve()
	if !errors.Is(err, fault.ErrAuth) {
		t.Fatalf("Resolve = %v, want an auth fault", err)
	}
	// An agent that has not been given an identity has nothing else to go on,
	// so the message is the whole contract.
	for _, want := range []string{identity.EnvUser, identity.EnvKey, "export"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q:\n%s", want, err)
		}
	}
}

// TestCredentialDoesNotStringifyItsKey: a credential ends up in error messages,
// and a String method that included the key would put it there too.
func TestCredentialDoesNotStringifyItsKey(t *testing.T) {
	got, err := identity.New(env(identity.EnvUser, "alice", identity.EnvKey, goodKey)).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.String(), goodKey) {
		t.Errorf("String() discloses the key: %q", got.String())
	}
	if !strings.Contains(got.String(), "alice") {
		t.Errorf("String() = %q, should name the user", got.String())
	}

	if (identity.Credential{}).String() != "no credential" {
		t.Error("the zero Credential should describe itself")
	}
	if !(identity.Credential{}).Zero() {
		t.Error("the zero Credential should report itself as zero")
	}
	if (identity.Credential{}).Key() != "" {
		t.Error("the zero Credential should hold no key")
	}
}

func TestZeroResolverIsRefused(t *testing.T) {
	var r identity.Resolver
	if _, err := r.Resolve(); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("the zero Resolver = %v, want an internal fault", err)
	}
}

// TestNilEnvironmentReadsTheProcess checks the production path without
// depending on what the process environment happens to hold: whatever it says,
// resolution must classify rather than panic.
func TestNilEnvironmentReadsTheProcess(t *testing.T) {
	t.Setenv(identity.EnvUser, "alice")
	t.Setenv(identity.EnvKey, goodKey)

	got, err := identity.New(nil).Resolve()
	if err != nil {
		t.Fatalf("Resolve from the process environment: %v", err)
	}
	if got.Name().String() != "alice" {
		t.Errorf("Name() = %q", got.Name())
	}
}
