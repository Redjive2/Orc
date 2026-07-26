package source

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveFollowsEachToolsOwnOrder pins the resolution orcprobe copies from
// the other tools' plans. Getting this wrong would not fail loudly — it would
// quietly probe a store nobody uses.
func TestResolveFollowsEachToolsOwnOrder(t *testing.T) {
	tools := Tools()
	if len(tools) != 4 {
		t.Fatalf("Tools() returned %d tools; mailman, macmuffin, cq, and orc all have state", len(tools))
	}

	for _, tool := range tools {
		t.Run(tool.Command, func(t *testing.T) {
			// 1. the explicit override wins.
			got, err := tool.Resolve(MapEnv(map[string]string{tool.EnvHome: "/explicit"}), "/home/u")
			if err != nil {
				t.Fatal(err)
			}
			if got != "/explicit" {
				t.Fatalf("%s resolved %q, want the override", tool.EnvHome, got)
			}

			// 2. then the XDG directory.
			got, err = tool.Resolve(MapEnv(map[string]string{tool.XDGVar: "/xdg"}), "/home/u")
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join("/xdg", tool.XDGSub); got != want {
				t.Fatalf("resolved %q, want %q", got, want)
			}

			// 3. then the dot-directory in the home.
			got, err = tool.Resolve(MapEnv(map[string]string{}), "/home/u")
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join("/home/u", tool.DotDir); got != want {
				t.Fatalf("resolved %q, want %q", got, want)
			}

			// An override set to nothing is a mistake, not a fallback: silently
			// probing the default store when the operator said otherwise is how
			// the wrong world gets copied.
			if _, err := tool.Resolve(MapEnv(map[string]string{tool.EnvHome: "  "}), "/home/u"); err == nil {
				t.Fatalf("an empty %s was accepted", tool.EnvHome)
			}
			// With no home and no override there is nowhere to look, and saying
			// so beats guessing.
			if _, err := tool.Resolve(MapEnv(map[string]string{}), ""); err == nil {
				t.Fatal("resolution succeeded with no home and no override")
			}
		})
	}
}

func TestFindReportsWhatIsThere(t *testing.T) {
	home := t.TempDir()
	if err := mkdir(filepath.Join(home, ".mailman")); err != nil {
		t.Fatal(err)
	}

	roots, err := Find(MapEnv(map[string]string{}), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roots {
		switch r.Tool.Kind {
		case Mailman:
			if !r.Present {
				t.Fatal("mailman state exists and was reported missing")
			}
		default:
			if r.Present {
				t.Fatalf("%s was reported present and is not there", r.Tool.Name)
			}
		}
	}
}

func TestContains(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/bc", false}, // the trap: a prefix that is not a parent
		{"/a/b", "/a", false},
		{"/a/b", "/x", false},
	}
	for _, c := range cases {
		if got := Contains(c.root, c.path); got != c.want {
			t.Fatalf("Contains(%q, %q) = %v, want %v", c.root, c.path, got, c.want)
		}
	}
}

func TestRealNamesTheStoreThatWouldBeTouched(t *testing.T) {
	home := t.TempDir()
	mailman := filepath.Join(home, ".mailman")
	if err := mkdir(mailman); err != nil {
		t.Fatal(err)
	}
	env := MapEnv(map[string]string{})

	root, inside, err := Real(env, home, filepath.Join(mailman, "probes"))
	if err != nil {
		t.Fatal(err)
	}
	if !inside {
		t.Fatal("a path inside the mail store was not recognised as real")
	}
	if root != mailman {
		t.Fatalf("named %q as the store, want %q", root, mailman)
	}

	if _, inside, err = Real(env, home, filepath.Join(home, "probes")); err != nil {
		t.Fatal(err)
	}
	if inside {
		t.Fatal("an ordinary directory was called real state")
	}
}

func mkdir(path string) error { return os.MkdirAll(path, 0o700) }
