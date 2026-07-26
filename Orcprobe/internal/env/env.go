// Package env composes the environment a probe runs in.
//
// Nothing about it is implicit. The whole environment is written to the probe
// as a readable, shell-sourceable file, so it can be diffed, pasted, and read
// by someone asking what a probe actually changes. That file is also what the
// shim re-asserts from, which is what makes "a subshell clobbered MAILMAN_HOME"
// a corrected mistake rather than an escape.
//
// Variables come in two kinds, and the difference matters:
//
//   - Enforced variables are isolation. The shim restores them on every
//     invocation, because a probe that can be talked out of its redirection is
//     not a probe.
//   - Unenforced variables are the operator's business — chiefly identity. `as
//     bob` deliberately overrides ORC_USER, and a shim that "corrected" that
//     would make the god-agent's main verb not work.
//
// The rule stated once: the shim protects isolation, never identity.
package env

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// Var is one environment variable.
type Var struct {
	Key   string
	Value string
	// Enforced marks a variable the shim restores. See the package comment.
	Enforced bool
	// Why is a one-line comment written above the variable in the env file.
	Why string
}

// Names of the variables this package sets, in one place so a reader can see
// the whole surface at once and so nothing else in the tree spells them.
const (
	Active        = "ORCPROBE_ACTIVE"
	Dir           = "ORCPROBE_DIR"
	Name          = "ORCPROBE_NAME"
	MailmanHome   = "MAILMAN_HOME"
	MacmuffinHome = "MACMUFFIN_HOME"
	CQHome        = "CQ_HOME"
	OrcHome       = "ORC_HOME"
	XDGData       = "XDG_DATA_HOME"
	XDGState      = "XDG_STATE_HOME"
	NoNudge       = "ORC_NO_NUDGE"
	ClaudeConfig  = "CLAUDE_CONFIG_DIR"
	GitConfig     = "GIT_CONFIG_GLOBAL"
	Path          = "PATH"
	Home          = "HOME"
	User          = "ORC_USER"
	Key           = "ORC_KEY"
	Prompt        = "ORCPROBE_PROMPT"
)

// Spec is everything the environment is composed from. Every path is absolute
// and inside the probe; nothing here is resolved from the ambient environment,
// so composing is a pure function of its input.
type Spec struct {
	ProbeID   string
	ProbeName string
	ProbeDir  string

	MailmanDir   string
	MacmuffinDir string
	CQDir        string
	OrcDir       string
	XDGDir       string
	BinDir       string
	ClaudeDir    string
	RepoDir      string
	GitConfig    string

	// FakeHome redirects HOME into the probe. Empty leaves the real one alone,
	// which is the default: redirecting HOME breaks the shell, git, and Claude
	// in ways that make a probe unlike the thing it models.
	FakeHome string

	// BasePath is the PATH to prepend the probe's shims to.
	BasePath string
}

// Compose builds the probe-wide environment. Identity is not part of it —
// Identity() layers that on at spawn time, because it changes per invocation.
func Compose(s Spec) ([]Var, error) {
	if strings.TrimSpace(s.ProbeID) == "" || strings.TrimSpace(s.ProbeDir) == "" {
		return nil, fault.Internal{Where: "env.Compose", Detail: "probe id and directory are required"}
	}

	vars := []Var{
		{Active, s.ProbeID, true, "the probe this process is inside; the tripwire every guard keys off"},
		{Name, s.ProbeName, true, "the probe's name, for prompts and messages"},
		{Dir, s.ProbeDir, true, "the probe's root"},
	}

	for _, redirect := range []struct {
		key, dir, why string
	}{
		{MailmanHome, s.MailmanDir, "Mailman reads mail from here, not from the real store"},
		{MacmuffinHome, s.MacmuffinDir, "Macmuffin reads tasks from here"},
		{CQHome, s.CQDir, "Communiqué keeps its sync state here"},
		{OrcHome, s.OrcDir, "Orc reads the fleet from here — the store that holds the keyring"},
		{XDGData, s.XDGDir, "backstop for any tool without its own override"},
		{XDGState, s.XDGDir, "backstop, as above"},
		{ClaudeConfig, s.ClaudeDir, "hooks resolve to the probe's copies"},
		{GitConfig, s.GitConfig, "no credential helper, no real git identity"},
	} {
		if strings.TrimSpace(redirect.dir) == "" {
			continue
		}
		vars = append(vars, Var{redirect.key, redirect.dir, true, redirect.why})
	}

	vars = append(vars,
		Var{NoNudge, "1", true, "stops mailman and muff spawning `cq sync` at the source; the name is orc/common/nudge's"},
		Var{Path, prefix(s.BinDir, s.BasePath), true, "the probe's shims come first"},
	)
	if s.FakeHome != "" {
		vars = append(vars, Var{Home, s.FakeHome, true, "--fake-home: even a tool that ignores its override lands inside the probe"})
	}
	return vars, nil
}

// Identity returns the variables that say who a command is running as. They are
// unenforced on purpose: see the package comment.
func Identity(user, key string) []Var {
	return []Var{
		{User, user, false, "the mailbox this shell acts as"},
		{Key, key, false, "its probe key — worthless against the real store"},
	}
}

// PromptVar carries the marker a probe shell shows, so a probe shell is never
// mistaken for a real one.
func PromptVar(probe, user string) Var {
	return Var{Prompt, fmt.Sprintf("probe:%s(%s)", probe, user), false, "what the shell prompt should say"}
}

// prefix puts the probe's bin directory at the front of a PATH, removing it
// from anywhere else so a repeated shell entry cannot bury it.
func prefix(bin, base string) string {
	if bin == "" {
		return base
	}
	parts := []string{bin}
	for _, p := range filepath.SplitList(base) {
		if p == "" || p == bin {
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, string(filepath.ListSeparator))
}

// Render writes the environment as a shell-sourceable file.
func Render(vars []Var) string {
	var b strings.Builder
	b.WriteString("# Written by orcprobe. This is the entire environment a probe applies.\n")
	b.WriteString("#\n")
	b.WriteString("# Lines marked (enforced) are isolation: the shims restore them on every\n")
	b.WriteString("# command, so a subshell cannot talk a probe out of its redirection.\n")
	b.WriteString("# Everything else is yours to override — identity, chiefly.\n")
	for _, v := range vars {
		b.WriteString("\n")
		if v.Why != "" {
			mark := ""
			if v.Enforced {
				mark = " (enforced)"
			}
			b.WriteString("# " + v.Why + mark + "\n")
		}
		b.WriteString("export " + v.Key + "=" + quote(v.Value) + "\n")
	}
	return b.String()
}

// Write stores the environment file inside a probe.
func Write(path string, vars []Var) error {
	if err := os.MkdirAll(filepath.Dir(path), snapshot.DirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}
	if err := os.WriteFile(path, []byte(Render(vars)), snapshot.FileMode); err != nil {
		return fault.IO{Op: "write", Path: path, Err: err}
	}
	return nil
}

// Load reads an environment file back.
//
// The parser understands exactly what Render writes and refuses anything else,
// rather than doing a best-effort job on a hand-edited file: the shim decides
// whether a command is isolated based on what this returns, and a line it
// misread would be a hole.
func Load(path string) ([]Var, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fault.NotFound{Target: path, Near: []string{"this probe has no env file; it may be unfinished"}}
		}
		return nil, fault.IO{Op: "read", Path: path, Err: err}
	}

	var (
		vars     []Var
		enforced bool
		scanner  = bufio.NewScanner(bytes.NewReader(data))
	)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		switch {
		case text == "":
			continue
		case strings.HasPrefix(text, "#"):
			enforced = strings.Contains(text, "(enforced)")
			continue
		case !strings.HasPrefix(text, "export "):
			return nil, fault.Parse{Path: path, Line: line, Reason: "expected an export line"}
		}

		body := strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(body, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fault.Parse{Path: path, Line: line, Reason: "malformed export"}
		}
		unquoted, err := unquote(value)
		if err != nil {
			return nil, fault.Parse{Path: path, Line: line, Reason: err.Error()}
		}
		vars = append(vars, Var{Key: key, Value: unquoted, Enforced: enforced})
		enforced = false
	}
	if err := scanner.Err(); err != nil {
		return nil, fault.IO{Op: "read", Path: path, Err: err}
	}
	return vars, nil
}

// Enforced returns only the isolation variables.
func Enforced(vars []Var) []Var {
	out := make([]Var, 0, len(vars))
	for _, v := range vars {
		if v.Enforced {
			out = append(out, v)
		}
	}
	return out
}

// Apply overlays vars onto a base environment in os.Environ form, replacing any
// existing value and appending the rest. The result is sorted so two runs of the
// same command produce the same environment, which is what lets a test compare
// one.
func Apply(base []string, vars ...[]Var) []string {
	index := make(map[string]string, len(base)+8)
	order := make([]string, 0, len(base)+8)

	remember := func(key, value string) {
		if _, seen := index[key]; !seen {
			order = append(order, key)
		}
		index[key] = value
	}
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		remember(key, value)
	}
	for _, set := range vars {
		for _, v := range set {
			remember(v.Key, v.Value)
		}
	}

	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+index[key])
	}
	return out
}

// Lookup finds a variable's value.
func Lookup(vars []Var, key string) (string, bool) {
	for _, v := range vars {
		if v.Key == key {
			return v.Value, true
		}
	}
	return "", false
}

// quote renders a value as a single-quoted shell word, which has exactly one
// escape rule and therefore exactly one way to be wrong.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func unquote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || !strings.HasPrefix(s, "'") || !strings.HasSuffix(s, "'") {
		return "", fault.Parse{Reason: "value is not single-quoted"}
	}
	return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'"), nil
}
