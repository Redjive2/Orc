package cli

import (
	"os"
	"path/filepath"
	"testing"

	stored "orc/cq/internal/settings"
)

// Which directory a run mirrors, and why the order is the way round it is.
//
// The recorded choice beating the environment is the whole feature: a watcher is
// handed os.Environ() when it launches, so $CQ_LIBRARY is whatever was in the
// shell that started it — possibly weeks ago — and the recorded choice is what
// somebody decided from the website since.
func TestLibraryFor(t *testing.T) {
	home := t.TempDir()
	if err := stored.Write(home, stored.Settings{Library: "/srv/chosen"}); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()

	for _, c := range []struct {
		name     string
		typed    bool
		fallback string
		home     string
		want     string
	}{
		{"the website's choice beats the environment", false, "/srv/env", home, "/srv/chosen"},
		{"a typed flag beats the website's choice", true, "/srv/typed", home, "/srv/typed"},
		{"the environment answers when nobody has chosen", false, "/srv/env", empty, "/srv/env"},
		{"nothing anywhere is no library at all", false, "", empty, ""},
		// No home is a machine that has not been set up. There is nowhere for a
		// choice to have been recorded, so the environment is the only answer.
		{"no home falls back rather than failing", false, "/srv/env", "", "/srv/env"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := libraryFor(c.typed, c.fallback, c.home)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("libraryFor(%v, %q, home) = %q, want %q", c.typed, c.fallback, got, c.want)
			}
		})
	}
}

// A settings file that will not parse is reported rather than stepped over.
//
// Falling back to the environment would mirror a directory nobody chose while
// showing no sign that the choice on disk was being ignored.
func TestABrokenSettingsFileIsNotSilentlyIgnored(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := libraryFor(false, "/srv/env", home); err == nil {
		t.Error("a settings file that will not parse was ignored")
	}
}
