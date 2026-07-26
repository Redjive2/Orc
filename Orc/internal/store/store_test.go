package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/sandbox"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

func fresh(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "fleet")
	s, err := store.Create(root, clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("creating a store: %v", err)
	}
	return s, root
}

func mustUser(t *testing.T, raw string) user.Name {
	t.Helper()
	n, err := user.Parse(raw)
	if err != nil {
		t.Fatalf("user %q: %v", raw, err)
	}
	return n
}

func mustName(t *testing.T, raw string) model.Name {
	t.Helper()
	n, err := model.ParseName(raw)
	if err != nil {
		t.Fatalf("name %q: %v", raw, err)
	}
	return n
}

func mustAuthority(t *testing.T, n int) model.Authority {
	t.Helper()
	a, err := model.NewAuthority(n)
	if err != nil {
		t.Fatalf("authority %d: %v", n, err)
	}
	return a
}

// TestDoors: Create makes a store, Open refuses one that is not there, and Read
// refuses to bring one into being.
//
// The distinction is the reason there are three doors. `orc bootstrap` calls Create;
// every other command calls Open, so a mistyped ORC_HOME is a message rather than a
// fleet quietly appearing in a directory nobody meant.
func TestDoors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fleet")
	c := clock.NewFake(epoch, time.Second)

	if _, err := store.Open(root, c); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("opening a missing store gave %v, want not found", err)
	}
	if _, err := store.Read(root, c); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("reading a missing store gave %v, want not found", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("a refused open created the store anyway")
	}

	if _, err := store.Create(root, c); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if _, err := store.Open(root, c); err != nil {
		t.Errorf("opening an existing store: %v", err)
	}

	// The read-only door refuses every write, which is what makes it safe for a
	// hook that fires on every tool call.
	ro, err := store.Read(root, c)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if err := ro.SetOperator(mustUser(t, "boss")); !errors.Is(err, fault.ErrDenied) {
		t.Errorf("a read-only store accepted a write: %v", err)
	}
}

// TestVersionRefusal: a store from a newer Orc is refused rather than half read.
func TestVersionRefusal(t *testing.T) {
	s, root := fresh(t)
	_ = s

	if err := os.WriteFile(filepath.Join(root, "version"), []byte("99\n"), 0o600); err != nil {
		t.Fatalf("writing a version: %v", err)
	}
	_, err := store.Open(root, clock.NewFake(epoch, time.Second))
	if !errors.Is(err, fault.ErrParse) {
		t.Errorf("an unknown version gave %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "upgrade orc") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

// TestRoundTrip: everything written comes back as itself, through a fresh handle.
func TestRoundTrip(t *testing.T) {
	s, root := fresh(t)

	patterns, err := model.ParsePatterns([]string{"read(Anno/**)", "write(Anno/internal/**)"})
	if err != nil {
		t.Fatalf("patterns: %v", err)
	}
	if _, err := s.CreatePermission(mustName(t, "edit-anno"), mustAuthority(t, 40), patterns); err != nil {
		t.Fatalf("permission: %v", err)
	}
	if _, err := s.CreateRole(mustName(t, "engineer"), mustAuthority(t, 60), "writes the code"); err != nil {
		t.Fatalf("role: %v", err)
	}
	if _, err := s.CreateIdentity(mustUser(t, "boss"), "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}

	// A second handle, so nothing is being read out of memory.
	again, err := store.Open(root, clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}

	perm, err := again.Permission(mustName(t, "edit-anno"))
	if err != nil {
		t.Fatalf("reading the permission: %v", err)
	}
	if got := model.PatternStrings(perm.Patterns()); strings.Join(got, " ") != "read(Anno/**) write(Anno/internal/**)" {
		t.Errorf("patterns came back as %v", got)
	}
	if perm.Floor().Int() != 40 {
		t.Errorf("floor came back as %s", perm.Floor())
	}

	role, err := again.Role(mustName(t, "engineer"))
	if err != nil {
		t.Fatalf("reading the role: %v", err)
	}
	if role.Authority().Int() != 60 || role.Description() != "writes the code" {
		t.Errorf("role came back as %s / %q", role.Authority(), role.Description())
	}

	boss, err := again.Identity(mustUser(t, "boss"))
	if err != nil {
		t.Fatalf("reading the identity: %v", err)
	}
	if !boss.IsOperator() {
		t.Errorf("the operator came back with a boss")
	}

	// The name is unique, and the filesystem decides it.
	if _, err := again.CreateIdentity(mustUser(t, "boss"), "0000000b-00000002", user.Name{}); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("a duplicate identity gave %v, want a conflict", err)
	}
}

// TestJournalRecovery is the recovery rule the whole append-only design exists for: a
// process killed mid-append can only damage the last line, so that one is dropped
// with a count and any other bad line is corruption.
func TestJournalRecovery(t *testing.T) {
	s, root := fresh(t)
	name := mustUser(t, "atlas")
	if _, err := s.CreateIdentity(name, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := s.ApplyIdentity(name, func(model.Identity) (model.IdentityEvent, error) {
		return model.AssignRole(name, epoch, mustName(t, "engineer"))
	}); err != nil {
		t.Fatalf("assigning: %v", err)
	}

	journal := filepath.Join(root, "identities", "atlas", "journal.jsonl")
	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}

	t.Run("truncated final line is dropped", func(t *testing.T) {
		// Exactly what a kill during a write leaves: a partial line, no newline.
		if err := os.WriteFile(journal, append(original, []byte(`{"op":"move","by":"atl`)...), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		got, skipped, err := s.InspectIdentity(name)
		if err != nil {
			t.Fatalf("an interrupted append should be recovered, not refused: %v", err)
		}
		if skipped == 0 {
			t.Errorf("the interrupted bytes were not counted")
		}
		if got.Role().String() != "engineer" {
			t.Errorf("the recovered identity lost its role")
		}
	})

	t.Run("corruption in the middle is refused", func(t *testing.T) {
		broken := append([]byte("{\"op\":\"nonsense\"\n"), original...)
		if err := os.WriteFile(journal, broken, 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		if _, err := s.Identity(name); !errors.Is(err, fault.ErrParse) {
			t.Errorf("corruption gave %v, want a parse fault", err)
		}
	})

	t.Run("an unknown op is refused", func(t *testing.T) {
		// A journal from a newer Orc. Guessing at what it meant is how an authority
		// change silently reverts, so the whole identity is refused instead.
		if err := os.WriteFile(journal, append(original,
			[]byte("{\"op\":\"promote\",\"by\":\"atlas\",\"at\":\"2026-07-25T12:00:00.000Z\"}\n")...), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		_, err := s.Identity(name)
		if !errors.Is(err, fault.ErrParse) {
			t.Errorf("an unknown op gave %v, want a parse fault", err)
		}
		if !strings.Contains(err.Error(), "promote") {
			t.Errorf("the refusal does not name the op: %v", err)
		}
	})

	t.Run("a journal event that cannot apply is corruption", func(t *testing.T) {
		// The operator cannot be moved, so a move in its journal is a journal
		// somebody rewrote — the event was not legal when it was appended.
		if err := os.WriteFile(journal, append(original,
			[]byte("{\"op\":\"move\",\"by\":\"atlas\",\"at\":\"2026-07-25T12:00:00.000Z\",\"boss\":\"ghost\"}\n")...), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		if _, err := s.Identity(name); !errors.Is(err, fault.ErrParse) {
			t.Errorf("an inapplicable event gave %v, want a parse fault", err)
		}
	})
}

// TestCredentials: the digest verifies, the plaintext comes back, and a wrong key
// fails closed.
func TestCredentials(t *testing.T) {
	s, _ := fresh(t)
	name := mustUser(t, "atlas")
	if _, err := s.CreateIdentity(name, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}

	key, err := user.NewKey(nil)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := s.WriteCredential(name, key); err != nil {
		t.Fatalf("writing the credential: %v", err)
	}

	if got, err := s.Key(name); err != nil || got != key {
		t.Errorf("the key did not come back: %v", err)
	}
	if err := s.Authenticate(name, key); err != nil {
		t.Errorf("the right key did not authenticate: %v", err)
	}
	if err := s.Authenticate(name, strings.Repeat("z", 40)); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("a wrong key gave %v, want an auth fault", err)
	}
	// An identity with no credential fails the same way as a wrong key: which of
	// the two happened is not something an unauthenticated caller may learn.
	if err := s.Authenticate(mustUser(t, "ghost"), key); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("an unknown identity gave %v, want an auth fault", err)
	}

	// The store holds credentials, so its permissions are the whole boundary.
	for _, path := range []string{"identities/atlas/key", "identities/atlas/user.json"} {
		info, err := os.Stat(filepath.Join(s.Root(), path))
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is mode %o, want 600", path, info.Mode().Perm())
		}
	}
	if info, err := os.Stat(s.Root()); err != nil {
		t.Fatalf("stat root: %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Errorf("the store is mode %o, want 700", info.Mode().Perm())
	}
}

// TestConcurrentApply: the lock spans the read and the write, so concurrent grants
// all land rather than overwriting each other.
func TestConcurrentApply(t *testing.T) {
	s, _ := fresh(t)
	name := mustUser(t, "atlas")
	if _, err := s.CreateIdentity(name, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			permission := mustName(t, "perm"+string(rune('a'+i)))
			_, errs[i] = s.ApplyIdentity(name, func(model.Identity) (model.IdentityEvent, error) {
				g, err := model.TimedGrant(permission, "boss", epoch, time.Hour)
				if err != nil {
					return model.IdentityEvent{}, err
				}
				return model.GrantPermission(name, epoch, g)
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got, err := s.Identity(name)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(got.Grants()) != writers {
		t.Errorf("%d concurrent grants left %d, want %d", writers, len(got.Grants()), writers)
	}
}

// TestSandboxGuard is the row Orcprobe's plan left open: with $ORCPROBE_ACTIVE set,
// Orc refuses a store that is not part of that probe — before creating anything.
func TestSandboxGuard(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if _, err := store.Create(real, clock.NewFake(epoch, time.Second)); err != nil {
		t.Fatalf("creating the real store: %v", err)
	}
	probeRoot := filepath.Join(t.TempDir(), "probe")

	t.Setenv(sandbox.EnvActive, "probe-1234")

	for _, door := range []struct {
		name string
		open func(string) error
	}{
		{"create", func(p string) error { _, err := store.Create(p, clock.NewFake(epoch, time.Second)); return err }},
		{"open", func(p string) error { _, err := store.Open(p, clock.NewFake(epoch, time.Second)); return err }},
		{"read", func(p string) error { _, err := store.Read(p, clock.NewFake(epoch, time.Second)); return err }},
	} {
		t.Run(door.name+" refuses an unstamped store", func(t *testing.T) {
			err := door.open(real)
			if !errors.Is(err, fault.ErrEscape) {
				t.Errorf("gave %v, want an escape", err)
			}
			if fault.Code(err) != fault.CodeEscape {
				t.Errorf("exited %d, want %d", fault.Code(err), fault.CodeEscape)
			}
		})
	}

	// Nothing was created on the way to those refusals — the guard runs before the
	// layout does, which is the property that makes it worth having.
	if _, err := os.Stat(probeRoot); !os.IsNotExist(err) {
		t.Errorf("a refused create made a directory")
	}

	// A stamped store inside the probe is fine.
	if err := os.MkdirAll(probeRoot, 0o700); err != nil {
		t.Fatalf("making the probe root: %v", err)
	}
	if err := sandbox.Stamp(probeRoot, "probe-1234"); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	if _, err := store.Create(probeRoot, clock.NewFake(epoch, time.Second)); err != nil {
		t.Errorf("a stamped store was refused: %v", err)
	}

	// A store stamped for another probe is refused, and says so differently: two
	// probes' state must never mix.
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatalf("making the other root: %v", err)
	}
	if err := sandbox.Stamp(other, "probe-9999"); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	if _, err := store.Create(other, clock.NewFake(epoch, time.Second)); !errors.Is(err, fault.ErrEscape) {
		t.Errorf("another probe's store gave %v, want an escape", err)
	}
}

// TestDeleteIdentity removes everything, including the workspace, and refuses a path
// that is not an identity directory.
func TestDeleteIdentity(t *testing.T) {
	s, _ := fresh(t)
	name := mustUser(t, "atlas")
	if _, err := s.CreateIdentity(name, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := s.WriteCredential(name, mustKey(t)); err != nil {
		t.Fatalf("credential: %v", err)
	}
	if err := s.WriteClaudeFile(name, "CLAUDE.md", []byte("# atlas\n")); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	if err := s.DeleteIdentity(name); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if _, err := os.Stat(s.IdentityDir(name)); !os.IsNotExist(err) {
		t.Errorf("the identity directory survived")
	}
	if err := s.DeleteIdentity(name); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("deleting twice gave %v, want not found", err)
	}
}

// TestClaudeFileEscapes: the one write path whose destination comes from a string
// rather than from a validated name refuses to leave the identity's directory.
func TestClaudeFileEscapes(t *testing.T) {
	s, _ := fresh(t)
	name := mustUser(t, "atlas")
	if _, err := s.CreateIdentity(name, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}

	for _, rel := range []string{"../../escape", "/etc/passwd", "..", ""} {
		if err := s.WriteClaudeFile(name, rel, []byte("x")); !errors.Is(err, fault.ErrEscape) {
			t.Errorf("writing %q gave %v, want an escape", rel, err)
		}
	}
	if err := s.WriteClaudeFile(name, "memory/notes.md", []byte("x")); err != nil {
		t.Errorf("a legitimate nested path was refused: %v", err)
	}
}

func mustKey(t *testing.T) string {
	t.Helper()
	key, err := user.NewKey(nil)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return key
}
