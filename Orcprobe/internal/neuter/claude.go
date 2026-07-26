package neuter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// The settings files a probe carries. Anything else in a Claude directory is
// not copied in the first place (plan §5.5).
var settingsFiles = []string{"settings.json", "settings.local.json"}

// claude makes the copied hook configuration safe to run inside a probe.
//
// Hooks are the one piece of copied configuration that *executes*. A hook whose
// command is a bare name — `anno-hook`, `mailman` — is exactly what a probe
// wants: the probe's PATH puts the shims first, so it runs against probe state
// like everything else. A hook whose command is an absolute path outside the
// probe is the opposite: it names a binary the probe cannot vouch for, bypasses
// the shims entirely, and would run on every tool call.
//
// So the rule is one line: an absolute command outside the probe is disabled,
// everything else is left alone. Disabling is removal from the copied settings,
// never from anything real, and every removal is reported.
func claude(s Spec, rep *Report) error {
	for _, name := range settingsFiles {
		path := filepath.Join(s.ClaudeDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fault.IO{Op: "read", Path: path, Err: err}
		}

		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			// A settings file orcprobe cannot read is left exactly as it is and
			// reported. Rewriting a file on a guess about its shape is how a
			// working hook configuration becomes a broken one.
			rep.add(ActNote, "claude "+name, "could not be read as JSON and was left alone: "+err.Error())
			continue
		}

		disabled := prune(settings, s.ProbeDir, rep, name)
		if disabled == 0 {
			continue
		}

		out, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return fault.Internal{Where: "neuter.claude", Detail: err.Error()}
		}
		if err := os.WriteFile(path, append(out, '\n'), snapshot.FileMode); err != nil {
			return fault.IO{Op: "write", Path: path, Err: err}
		}
		rep.add(ActNote, "claude "+name, "rewritten; key order and formatting are not preserved")
	}
	return nil
}

// prune walks the hook structure and removes every command that points outside
// the probe, returning how many it removed.
//
// The walk is generic rather than typed against a schema. Claude's settings
// grow fields, and a strict decode would refuse a file that merely had
// something new in it — which for this tool means refusing to make a probe.
func prune(settings map[string]any, probeDir string, rep *Report, file string) int {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}

	removed := 0
	for eventName, raw := range hooks {
		groups, ok := raw.([]any)
		if !ok {
			continue
		}
		keptGroups := make([]any, 0, len(groups))

		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, rawGroup)
				continue
			}
			entries, ok := group["hooks"].([]any)
			if !ok {
				keptGroups = append(keptGroups, rawGroup)
				continue
			}

			kept := make([]any, 0, len(entries))
			for _, rawEntry := range entries {
				entry, ok := rawEntry.(map[string]any)
				if !ok {
					kept = append(kept, rawEntry)
					continue
				}
				command, _ := entry["command"].(string)
				if outside(command, probeDir) {
					removed++
					rep.Hooks = append(rep.Hooks, command)
					rep.add(ActDrop, "claude hook "+eventName,
						"disabled "+truncate(command, 60)+" — it names a binary outside the probe, which the shims cannot cover")
					continue
				}
				kept = append(kept, rawEntry)
			}

			// A group with no hooks left is removed whole: an empty matcher
			// group is a rule that fires and does nothing, which reads in a
			// settings file as a hook that should be working.
			if len(kept) == 0 {
				continue
			}
			group["hooks"] = kept
			keptGroups = append(keptGroups, group)
		}
		hooks[eventName] = keptGroups
	}
	_ = file
	return removed
}

// outside reports whether a hook command names an absolute path that is not
// inside the probe.
//
// A bare name is not outside: it resolves through the probe's PATH, where the
// shims come first. A relative path is not outside either — it resolves against
// the working directory, which inside a probe is the probe's own repo copy.
func outside(command, probeDir string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}
	binary := fields[0]
	if !filepath.IsAbs(binary) {
		return false
	}
	clean := filepath.Clean(binary)
	dir := filepath.Clean(probeDir)
	if clean == dir {
		return false
	}
	return !strings.HasPrefix(clean, dir+string(filepath.Separator))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
