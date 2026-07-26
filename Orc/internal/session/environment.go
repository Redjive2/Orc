package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"orc/common/fault"
	"orc/common/identity"
	"orc/common/sandbox"
	"orc/common/user"
	"orc/orc/internal/store"
)

// NewID mints a Claude session id.
//
// It is a UUID because that is what `--session-id` takes. Orc mints it rather than
// reading one back, which removes a whole class of problem: there is no window in
// which a session exists and Orc does not know its name, so `--resume` after a
// crash, transcript discovery, and tying a grant to a session are all deterministic
// rather than best-effort.
func NewID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fault.IO{Op: "read entropy for", Path: "a new session id", Err: err}
	}
	// Version 4, variant 1, as a UUID has to be for Claude to accept it.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	h := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

// Environment composes the whole environment a session runs with.
//
// It is composed rather than inherited, and that is the point: what a session can
// see is a decision somebody made and can read back, not whatever happened to be in
// the shell that ran `orc employ`. The list is Plan.md §5, and the two halves of it
// are different in kind — the credential and the config directory are what make the
// session *this identity*, and the rest is what keeps the other tools working.
func Environment(s *store.Store, name user.Name, id string) ([]string, error) {
	if s == nil {
		return nil, fault.Internal{Where: "session.Environment", Detail: "no store given"}
	}
	key, err := s.Key(name)
	if err != nil {
		return nil, err
	}

	env := map[string]string{
		// The credential contract every Orc tool reads. This is the whole of why an
		// agent Orc started needs no setup to use mailman, muff, anno, or dock.
		identity.EnvUser: name.String(),
		identity.EnvKey:  key,

		// Which identity and which session, for `orc introspect` inside the leaf.
		"ORC_IDENTITY": name.String(),
		"ORC_SESSION":  id,
		store.EnvHome:  s.Root(),

		// Per-identity memories, settings, and transcripts.
		"CLAUDE_CONFIG_DIR": s.ClaudeDir(name),

		// Plain output from every Orc tool: an agent parsing a table should not have
		// to strip escape sequences first.
		"ORC_AGENT": "1",
	}

	// Passed through rather than composed. A machine that mirrors to cq keeps
	// mirroring, a session needs a PATH and a HOME to be a usable shell, and TERM
	// is what makes the pty a terminal the TUI can draw into.
	//
	// HOME is deliberately the real one. Redirecting it would break git, the shell,
	// and Claude's own authentication — see Plan.md §5 — and the identity's own
	// state lives in CLAUDE_CONFIG_DIR, which is set above.
	for _, key := range []string{
		"PATH", "HOME", "SHELL", "LANG", "LC_ALL", "TZ", "TMPDIR", "SSH_AUTH_SOCK",
		"CQ_SERVER", "CQ_USER", "CQ_KEY", "CQ_TOKEN", "CQ_MACHINE",
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK",
		"MAILMAN_HOME", "MACMUFFIN_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
		sandbox.EnvActive,
	} {
		if v, ok := os.LookupEnv(key); ok {
			env[key] = v
		}
	}
	// A pty with no TERM is a terminal a TUI will not draw into.
	if _, ok := env["TERM"]; !ok {
		if v, ok := os.LookupEnv("TERM"); ok && v != "" {
			env["TERM"] = v
		} else {
			env["TERM"] = "xterm-256color"
		}
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out, nil
}

// Describe renders an environment for a log or a diagnostic, with the credential
// hidden.
//
// It exists so that a supervisor can record what it started a session with. The
// key is the one thing that must never reach a log, so this is the only way the
// environment is ever printed.
func Describe(env []string) string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if key == identity.EnvKey || strings.Contains(key, "API_KEY") || key == "CQ_KEY" || key == "CQ_TOKEN" {
			out = append(out, key+"=(hidden)")
			continue
		}
		out = append(out, entry)
	}
	return strings.Join(out, " ")
}

// OnSignal runs stop when the process is asked to end.
//
// A supervisor that died on SIGTERM without stopping its child would leave a claude
// process holding a pty nobody owns — visible in `ps`, invisible to Orc, and still
// spending money. So the signal is caught, the session is ended properly, and Run
// returns on its own.
func OnSignal(stop func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-ch
		stop()
	}()
}
