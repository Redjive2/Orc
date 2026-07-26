package provision

import (
	"encoding/json"
	"os"
	"path/filepath"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/store"
)

// Getting a new agent past Claude's first-run wizard.
//
// Every identity gets its own CLAUDE_CONFIG_DIR — that is what makes an agent's
// memories and settings its own rather than the machine's. The cost was that every
// new identity's first session was a *virgin* configuration, so Claude opened on
// "Let's get started", the theme picker, and then the trust prompt. None of that can
// be answered by an agent, and none of it can be answered by anybody who is not
// attached to the pty, so hiring somebody meant `orc attach --direct` and clicking
// through a wizard before the agent could do anything at all.
//
// Two flags in `.claude.json` skip both screens, and they are the only two:
//
//   - `hasCompletedOnboarding` — the theme picker and the welcome;
//   - `projects[<workspace>].hasTrustDialogAccepted` — the "is this a project you
//     trust?" prompt, which is per directory and so has to name the one the session
//     will actually start in.
//
// Both were found by running Claude against a scratch configuration directory and
// watching which screen came up, one flag at a time. Neither is inferred from
// documentation, and if a future Claude adds a third screen this is where the answer
// goes.
//
// **Answering the trust prompt on the operator's behalf is a real decision**, and it
// is theirs to have made: the directory is one Orc created or was told to adopt, for
// an agent the operator hired, running under permissions the operator compiled. The
// prompt asks whether the *person* trusts the folder, and by the time a session
// starts they have already said so four other ways. What Orc must not do is answer it
// for a directory nobody chose, which is why the entry names the workspace rather
// than being a blanket setting.
const configFile = ".claude.json"

// SeedOnboarding makes a session start at the prompt rather than at a wizard.
//
// It **merges**. Claude keeps a great deal in this file — session metrics, per
// project history, migration flags — and a version of this that wrote the two keys
// and nothing else would erase the agent's own accumulated state on every start. So
// what is not ours is read and written back untouched, and what is ours is only set
// when it is not already right.
//
// Returns whether it changed anything, so a caller can say "seeded" once rather than
// on every restart.
func SeedOnboarding(dir, workspace string) (bool, error) {
	if dir == "" {
		return false, fault.Internal{Where: "provision.SeedOnboarding", Detail: "no config directory"}
	}
	path := filepath.Join(dir, configFile)

	config := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A file that will not parse is left exactly as it is. Rewriting it would
		// discard whatever an agent had accumulated, to fix a wizard — and the
		// wizard is the smaller problem.
		if err := json.Unmarshal(data, &config); err != nil {
			return false, fault.Parse{Path: path,
				Reason: "it is not JSON, so it was left alone: " + err.Error()}
		}
	case !os.IsNotExist(err):
		return false, fault.IO{Op: "read", Path: path, Err: err}
	}

	changed := false
	if done, _ := config["hasCompletedOnboarding"].(bool); !done {
		config["hasCompletedOnboarding"] = true
		changed = true
	}
	if workspace != "" {
		if trustProject(config, workspace) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fault.Internal{Where: "provision.SeedOnboarding", Detail: err.Error()}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fault.IO{Op: "create", Path: dir, Err: err}
	}
	// Written through a temporary file in the same directory: a half-written
	// `.claude.json` is a configuration Claude refuses to start on, and this runs
	// while a session is starting.
	temp := path + ".orc-tmp"
	if err := os.WriteFile(temp, append(out, '\n'), 0o600); err != nil {
		return false, fault.IO{Op: "write", Path: temp, Err: err}
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return false, fault.IO{Op: "replace", Path: path, Err: err}
	}
	return true, nil
}

// trustProject records that the workspace is trusted, without disturbing whatever
// else Claude keeps under that project.
//
// The key is the resolved path, because that is what Claude looks the directory up
// by: a workspace reached through a symlink — every path under /tmp on macOS — would
// otherwise be trusted under a spelling the session never uses.
func trustProject(config map[string]any, workspace string) bool {
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}

	projects, _ := config["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		config["projects"] = projects
	}
	entry, _ := projects[workspace].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		projects[workspace] = entry
	}
	if trusted, _ := entry["hasTrustDialogAccepted"].(bool); trusted {
		return false
	}
	entry["hasTrustDialogAccepted"] = true
	return true
}

// SeedIdentity is SeedOnboarding for one identity, for callers holding a store.
func SeedIdentity(s *store.Store, name user.Name, workspace string) (bool, error) {
	if s == nil {
		return false, fault.Internal{Where: "provision.SeedIdentity", Detail: "no store given"}
	}
	return SeedOnboarding(s.ClaudeDir(name), workspace)
}
