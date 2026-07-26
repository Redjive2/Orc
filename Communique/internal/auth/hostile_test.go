package auth_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
)

func readOnly(t *testing.T, dir string) {
	t.Helper()
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// TestCredentialFailuresAreReported checks that a store which cannot be written
// says so. A credential command that silently did nothing would leave the
// operator believing they had set a password they had not.
func TestCredentialFailuresAreReported(t *testing.T) {
	t.Run("password cannot be written", func(t *testing.T) {
		s := open(t)
		readOnly(t, s.Root())
		if err := s.SetPassword(password, at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("token cannot be written", func(t *testing.T) {
		s := open(t)
		readOnly(t, filepath.Join(s.Root(), "tokens"))
		if _, _, err := s.NewToken("studio", at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("session cannot be written", func(t *testing.T) {
		s := open(t)
		readOnly(t, filepath.Join(s.Root(), "sessions"))
		if _, _, err := s.NewSession(at, time.Hour); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("tokens cannot be listed", func(t *testing.T) {
		if !modeBitsBite() {
			t.Skip("this machine cannot make a file unreadable to its owner")
		}
		s := open(t)
		if _, _, err := s.NewToken("studio", at); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(s.Root(), "tokens")
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if _, err := s.Tokens(); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
		if s.HasToken() {
			t.Errorf("an unreadable token directory must not count as configured")
		}
	})

	t.Run("a corrupt token record is refused", func(t *testing.T) {
		s := open(t)
		path := filepath.Join(s.Root(), "tokens", strings.Repeat("a", 16)+".json")
		if err := os.WriteFile(path, []byte(`{"id":"nope"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Tokens(); !errors.Is(err, fault.ErrParse) {
			t.Errorf("error = %v, want a parse fault", err)
		}
	})

	t.Run("sessions cannot be swept", func(t *testing.T) {
		if !modeBitsBite() {
			t.Skip("this machine cannot make a file unreadable to its owner")
		}
		s := open(t)
		dir := filepath.Join(s.Root(), "sessions")
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if _, err := s.SweepSessions(at); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("an expired session cannot be removed", func(t *testing.T) {
		s := open(t)
		if _, _, err := s.NewSession(at, time.Minute); err != nil {
			t.Fatal(err)
		}
		readOnly(t, filepath.Join(s.Root(), "sessions"))
		if _, err := s.SweepSessions(at.Add(time.Hour)); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("missing directories are not an error", func(t *testing.T) {
		s := open(t)
		for _, dir := range []string{"tokens", "sessions"} {
			if err := os.RemoveAll(filepath.Join(s.Root(), dir)); err != nil {
				t.Fatal(err)
			}
		}
		if list, err := s.Tokens(); err != nil || len(list) != 0 {
			t.Errorf("Tokens on a missing directory = %v, %v", list, err)
		}
		if n, err := s.SweepSessions(at); err != nil || n != 0 {
			t.Errorf("SweepSessions on a missing directory = %d, %v", n, err)
		}
	})

	t.Run("the store directory cannot be created", func(t *testing.T) {
		dir := t.TempDir()
		blocked := filepath.Join(dir, "file")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.Open(filepath.Join(blocked, "creds")); !errors.Is(err, fault.ErrIO) {
			t.Errorf("error = %v, want an i/o fault", err)
		}
	})

	t.Run("only a token is configured", func(t *testing.T) {
		s := open(t)
		if _, _, err := s.NewToken("studio", at); err != nil {
			t.Fatal(err)
		}
		err := s.Configured()
		if !errors.Is(err, fault.ErrUsage) || !strings.Contains(err.Error(), "password") {
			t.Errorf("error = %v, want the missing password named", err)
		}
	})
}

// modeBitsBite reports whether this machine can genuinely deny the process
// access to a file it owns.
//
// Two machines cannot, and the tests that chmod something all need one that
// can. Root is
// refused nothing. Windows has no mode bit for this at all: os.Chmod there
// toggles the read-only attribute and leaves reading alone, and does not make a
// directory unwritable — so the failure these tests provoke simply does not
// happen, and the assertion would fail for the wrong reason.
func modeBitsBite() bool {
	return os.Geteuid() != 0 && runtime.GOOS != "windows"
}
