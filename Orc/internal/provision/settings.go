package provision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"orc/orc/internal/hook"
	"os"
	"sort"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

// Compiling a session's settings.json.
//
// This is the cheap layer of Plan.md §7.2: rules Claude can enforce itself, written
// once when a session is prepared, so most violations are refused before a tool call
// is even attempted. It is *not* the mechanism. Two reasons, and the second is the
// one that decides how this file is written:
//
//  1. It was true when the session started. A `grant` or a `move` a minute later is
//     not in it, and `orc-hook` is what reads live state.
//  2. **Whether a deny rule holds under `bypassPermissions` is unverified.** The
//     probe is `Claude/Mock/deny-probe.sh`; it needs a credential and could not be
//     run when this was written. So everything here is built to the pessimistic
//     reading — these rules are documentation until somebody proves otherwise, and
//     every denial that matters is *also* enforced by the hook.
//
// Which is why the `Agent` denial appears both here and in the hook, and why the
// keyring's is in the hook at all: a rule that might be ignored cannot be the only
// thing standing in front of the fleet's credentials.

// HookBinary is the command the compiled hooks call.
//
// A bare name rather than a path: it resolves through the session's PATH, which is
// what lets a machine install Orc anywhere and what makes an Orcprobe shim able to
// stand in front of it. `orc doctor` is what says whether it is actually findable.
const HookBinary = "orc-hook"

// HookTimeout is the outer bound Claude puts on the hook, in seconds. The hook's own
// deadline is 2s, well inside it: this is a backstop, not the mechanism.
const HookTimeout = 10

// The matchers. PreToolUse is the enforcing one; the rest are the event feed, and
// they are separate entries because they answer different questions and a single
// matcher over both would make the hook guess which job it was doing.
const (
	EnforceMatcher = "Read|Edit|Write|NotebookEdit|MultiEdit|Bash|Agent"
	FeedMatcher    = ""
)

// FeedEvents are the lifecycle events the clean view is drawn from.
var FeedEvents = []string{
	"UserPromptSubmit", "PostToolUse", "Notification",
	"Stop", "SubagentStop", "SessionStart", "SessionEnd",
}

// SettingsSpec is what a session's settings are compiled from.
type SettingsSpec struct {
	// Clauses are the identity's effective permissions.
	Clauses []authz.Clause
	// OrcHome is the store root. The protected shapes inside it are denied — see
	// Protected, and note that the root itself is *not*, because an identity's
	// workspace lives under it.
	OrcHome string
	// Workspace is what a relative clause is relative to, so the rules Claude sees
	// are absolute and cannot mean something different depending on the cwd.
	Workspace string
}

// WriteSettings compiles an identity's settings.json.
//
// It preserves anything Orc does not manage. An operator who added an MCP server or a
// model preference to an identity's settings should not lose it to the next populate,
// so only the keys below are replaced:
//
//	permissions.allow, permissions.deny, permissions.defaultMode, hooks
//
// A settings file that will not parse is **left exactly as it is** and reported.
// Rewriting a file on a guess about its shape is how a working configuration becomes
// a broken one — the same rule Orcprobe's plan reached about copied hooks.
func WriteSettings(s *store.Store, name user.Name, spec SettingsSpec) error {
	if s == nil {
		return fault.Internal{Where: "provision.WriteSettings", Detail: "no store given"}
	}
	if name.Zero() {
		return fault.Internal{Where: "provision.WriteSettings", Detail: "no identity named"}
	}

	existing, err := readSettings(s, name)
	if err != nil {
		return err
	}

	perms := map[string]any{}
	if got, ok := existing["permissions"].(map[string]any); ok {
		perms = got
	}
	perms["allow"] = allowRules(spec)
	perms["deny"] = denyRules(spec)
	// The command line carries `--permission-mode` too — see the supervisor's Args,
	// which passes this same value. A documented flag beats a settings key nobody
	// has verified; this is here so the file describes the session it configures
	// rather than disagreeing with it.
	perms["defaultMode"] = Mode()

	existing["permissions"] = perms

	// The acceptance screen, skipped.
	//
	// `bypassPermissions` makes Claude open on a full-page warning — "By proceeding,
	// you accept all responsibility" — with a menu that has to be answered from a
	// keyboard. An unattended fleet has no keyboard: every new agent sat at that
	// screen until somebody attached to its pty and pressed enter, which is most of
	// why hiring an agent used to mean babysitting one.
	//
	// An operator who has ever run Claude themselves has already accepted it, and
	// the acceptance lives in *their* user settings — but this file **is** the user
	// settings for an agent's CLAUDE_CONFIG_DIR, so it replaced the answer rather
	// than inheriting it. Writing it here is Orc carrying forward the answer the
	// operator already gave, for sessions the operator started, in a mode the
	// operator chose.
	//
	// Verified by A/B against a real Claude in an isolated configuration directory:
	// with the key the session opens at the prompt, without it at the warning.
	if Mode() == "bypassPermissions" {
		existing["skipDangerousModePermissionPrompt"] = true
	}

	existing["hooks"] = hookRules()

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fault.Internal{Where: "provision.WriteSettings", Detail: err.Error()}
	}
	return s.WriteClaudeFile(name, "settings.json", append(data, '\n'))
}

// readSettings reads what is there, or an empty object.
func readSettings(s *store.Store, name user.Name) (map[string]any, error) {
	path := s.ClaudeDir(name) + "/settings.json"

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"settings are not json (%s); orc left the file alone rather than guessing at its shape", err)}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// allowRules turns effective clauses into rules Claude understands.
//
// Read clauses become `Read`, and write clauses become `Edit` and `Write` both:
// Claude's rules name tools, and a clause that permitted editing but not writing
// would allow changing a file and refuse creating one, which is not a distinction
// Orc's model makes.
func allowRules(spec SettingsSpec) []string {
	var out []string
	for _, c := range spec.Clauses {
		arg := absolute(spec.Workspace, c.Pattern.Arg())
		switch c.Pattern.Kind() {
		case model.KindRead:
			out = append(out, "Read("+arg+")")
		case model.KindWrite:
			out = append(out, "Edit("+arg+")", "Write("+arg+")", "NotebookEdit("+arg+")")
		}
	}
	return tidy(out)
}

// Protected are the shapes inside the store that no session may touch, relative to
// the store root.
//
// The obvious rule — deny the whole store — is wrong, and wrong in the way that breaks
// every session: **an identity's workspace lives inside the store**, at
// identities/<name>/workspace. Denying the root would deny an agent its own files. So
// the protected set is enumerated by role instead, and it is the same rule the hook
// applies in `protected`, spelled as globs.
//
// What is here: every credential, every journal, the policy, and each session's own
// permission snapshot — because an agent that could rewrite the snapshot could rewrite
// what the hook's second rung believes. What is deliberately absent: workspaces and
// memories, which are the agents' own.
//
// Another identity's workspace is not here either, and cannot be: a glob that denied
// every workspace would deny this session's too, and deny beats allow. The hook refuses
// those as outside this identity's workspace, which is the layer that can tell them
// apart.
var Protected = []string{
	"identities/*/key",
	"identities/*/user.json",
	"identities/*/identity.json",
	"identities/*/journal.jsonl",
	"identities/*/lock",
	"identities/*/session/**",
	"identities/*/claude/settings.json",
	"roles/**",
	"permissions/**",
	"operator",
	"version",
	"lock",
}

// EnvMode names the permission mode a session runs under.
const EnvMode = "ORC_PERMISSION_MODE"

// DefaultMode is what a fleet runs under unless it says otherwise.
const DefaultMode = "bypassPermissions"

// Mode is the permission mode sessions start in.
//
// Configurable because of a screen: `bypassPermissions` makes Claude open on a
// full-page warning — "By proceeding, you accept all responsibility" — that has to
// be answered from a keyboard. On an unattended fleet that means every new agent
// waits at a wall nobody is looking at, and hiring somebody means attaching to it.
//
// It is no longer needed for that: the acceptance screen is skipped by
// `skipDangerousModePermissionPrompt` above, so the default mode starts unattended.
// The variable stays as the escape hatch for a fleet that wants a different posture.
//
// **`dontAsk` is not that escape hatch for most fleets.** Claude's own words: it
// "auto-denies tools unless pre-approved via /permissions or permissions.allow
// rules". Orc's allow list is compiled from an identity's read and write clauses
// and names no `Bash`, so under `dontAsk` an agent would be refused every command
// it tried to run — silently, one tool call at a time, looking like an agent that
// had decided not to work. Do not set it without first widening the allow list to
// cover every tool an agent needs.
//
// The modes Claude accepts are acceptEdits, auto, bypassPermissions, manual,
// dontAsk, and plan. An unrecognised one is refused here rather than at session
// start, where the failure would be a child that exits before it draws anything.
func Mode() string {
	got := strings.TrimSpace(os.Getenv(EnvMode))
	if got == "" {
		return DefaultMode
	}
	for _, known := range []string{"acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan"} {
		if got == known {
			return got
		}
	}
	return DefaultMode
}

// denyRules are the refusals that do not come from a clause.
//
// Every one of them is also enforced by the hook, because a rule that might be ignored
// under bypassPermissions cannot be the only thing in front of a fleet's credentials or
// its accounting.
func denyRules(spec SettingsSpec) []string {
	root := strings.TrimRight(spec.OrcHome, "/")

	// Subagents, under both names. Confirmed with the user: all parallelism goes
	// through `orc employ`, so the worklist is the whole picture of what is thinking
	// and the load budget is exact rather than approximate.
	//
	// Claude decides whether a call is a subagent by testing the tool name against
	// *both* `Agent` and `Task`, and which one a build uses varies. Denying one of
	// them left a fleet that spelled it the other way with a rule that matched
	// nothing — and with `orc doctor` reporting that subagents were off.
	out := append([]string{}, hook.SubagentTools...)
	for _, glob := range Protected {
		full := glob
		if root != "" {
			full = root + "/" + glob
		}
		// `Edit` alone on the deny side, not `Edit` and `Write` both.
		//
		// Claude checks deny rules for file edits against `Edit(path)` only, and an
		// `Edit` rule covers every file-editing tool — Write and NotebookEdit
		// included. A `Write(path)` deny rule is therefore not a second fence; it is
		// a rule that matches nothing, and Claude says so on stderr at every start,
		// once per protected glob. A dozen warnings across an agent's opening screen
		// is how somebody learns to stop reading them.
		//
		// The allow side keeps both, because there the tools are named individually
		// and a clause that permitted editing but not writing would allow changing a
		// file and refuse creating one.
		out = append(out,
			"Read("+full+")",
			"Edit("+full+")",
		)
	}
	return tidy(out)
}

// hookRules wires orc-hook: once for enforcement, once per feed event.
func hookRules() map[string]any {
	entry := func(matcher string) map[string]any {
		hook := map[string]any{
			"type":    "command",
			"command": HookBinary,
			"timeout": HookTimeout,
		}
		out := map[string]any{"hooks": []any{hook}}
		if matcher != "" {
			out["matcher"] = matcher
		}
		return out
	}

	hooks := map[string]any{
		"PreToolUse": []any{entry(EnforceMatcher)},
	}
	for _, name := range FeedEvents {
		hooks[name] = []any{entry(FeedMatcher)}
	}
	return hooks
}

// absolute makes a workspace-relative glob absolute.
//
// Claude's rules are matched against real paths, and a relative rule would mean
// something different depending on where the session's cwd happened to be. The
// workspace is the one place Orc knows a clause is relative to.
func absolute(workspace, glob string) string {
	if workspace == "" || strings.HasPrefix(glob, "/") {
		return glob
	}
	return strings.TrimRight(workspace, "/") + "/" + glob
}

// tidy sorts and de-duplicates, so two identical permissions produce one rule and a
// diff of two settings files is about what changed rather than about ordering.
func tidy(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
