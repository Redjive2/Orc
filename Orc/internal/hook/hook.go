// Package hook implements Orc's PreToolUse guard and its event feed.
//
// It is the boundary. Every other layer in Plan.md §7 is cheaper or earlier, and
// none of them can be relied on: the compiled settings were true when the session
// started, and whether a `deny` rule survives `bypassPermissions` is unverified
// (`Claude/Mock/deny-probe.sh`). `PreToolUse` hooks run regardless of permission
// mode, so this is what actually stands between an agent and a file it may not
// touch.
//
// Which makes one rule different here from every other hook in this tree. Anno's and
// Macmuffin's are bystanders and fail open in every direction; this one cannot, so it
// fails open **for reads** and closed **for writes**:
//
//	live store readable      → decide from current permissions, grants included
//	only authz.json readable → decide from the permissions at populate, and say so
//	neither readable         → reads pass; writes and Agent block
//
// The third rung is the honest consequence of being the only brake. A stalled write
// is recoverable and says what to do; an unbounded one is not. Reads still pass
// because a blocked read produces a confused agent and discloses nothing new — it
// already has whatever the last successful read gave it.
//
// Everything else Macmuffin's hook guarantees carries over: a 2-second deadline,
// unparseable input and unknown events exit 0, and no input produces an exit other
// than 0 or 2. And one clarification, because §7.3 said "never writes" and this hook
// does write: it never writes **fleet state**. It appends to its own session's event
// feed, unlocked and outside the derivation, and nothing else.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/identity"
	"orc/common/user"
	"orc/orc/internal/event"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

// Exit codes, as Claude Code reads them: 0 lets the tool call proceed, 2 blocks it
// and feeds stderr back to the agent. The shared table in Claude/Docs/ExitCodes.md is
// deliberately not used here — a hook has its own contract.
const (
	CodeOK    = 0
	CodeBlock = 2
)

// Deadline bounds the whole check.
//
// A store on a slow disk or behind a stalled lock must not freeze an agent's session.
// Two seconds is far longer than a healthy check — a read, a fold, a glob match — and
// far shorter than a human notices as a hang.
const Deadline = 2 * time.Second

// The environment the hook reads. ORC_IDENTITY is what Orc sets for a session;
// ORC_USER is the credential contract and is the fallback, so a hook fired in a shell
// somebody set up by hand still knows who it is.
const (
	EnvIdentity = "ORC_IDENTITY"
	EnvSession  = "ORC_SESSION"
)

// Writers are the tools whose target path must match a `write` clause.
var Writers = []string{"Edit", "Write", "NotebookEdit", "MultiEdit"}

// Readers are the tools whose target path is checked against `read` clauses.
var Readers = []string{"Read", "NotebookRead"}

// SubagentTool is denied outright. Confirmed with the user: all parallelism goes
// through `orc employ`, so the worklist is the whole picture of what is thinking.
const SubagentTool = "Agent"

// Options is everything the hook needs from outside itself. Every field has a working
// default, so a test sets only what it cares about.
type Options struct {
	// Root overrides where the store lives.
	Root string
	// Home is the user's home directory, used to find the default store.
	Home string
	// Env reads the environment.
	Env func(key string) (string, bool)
	// Clock stamps events. The hook makes no decision that depends on the time.
	Clock clock.Clock
	// Deadline bounds the check. Zero means Deadline.
	Deadline time.Duration
	// Now, injected, is what a test uses to make an event's timestamp predictable.
}

func (o Options) env(key string) (string, bool) {
	if o.Env == nil {
		return os.LookupEnv(key)
	}
	return o.Env(key)
}

func (o Options) clock() clock.Clock {
	if o.Clock == nil {
		return clock.Real{}
	}
	return o.Clock
}

func (o Options) deadline() time.Duration {
	if o.Deadline <= 0 {
		return Deadline
	}
	return o.Deadline
}

// Outcome is everything the process should do: what to say, and what to exit with.
// There is no stdout — a hook that says nothing costs the session nothing.
type Outcome struct {
	Code   int
	Stderr string
}

var pass = Outcome{Code: CodeOK}

// Main runs the hook end to end and returns the process exit code.
//
// It recovers from a panic rather than letting one escape: a handler that crashed an
// agent's session would be far worse than one that occasionally says nothing.
func Main(stdin io.Reader, stderr io.Writer, opts Options) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(stderr, "orc-hook: recovered from %v\n", r)
			code = CodeOK
		}
	}()

	input, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return CodeOK
	}

	out := Run(input, opts)
	if out.Stderr != "" {
		if _, err := fmt.Fprintln(stderr, out.Stderr); err != nil {
			return CodeOK
		}
	}
	return out.Code
}

// payload is the part of a hook event Orc reads. Unknown fields are ignored, so an
// addition to Claude's schema cannot break the hook.
type payload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	ToolName       string `json:"tool_name"`
	CWD            string `json:"cwd"`
	ToolInput      struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
		Path         string `json:"path"`
		Edits        []struct {
			FilePath string `json:"file_path"`
		} `json:"edits"`
	} `json:"tool_input"`
}

// Run decides what to do about one hook event.
//
// It never returns an error. A hook that cannot make sense of its input has nothing
// useful to say, and saying nothing is always safe — except for the one case the
// third rung covers, where saying nothing about a *write* would be the hook failing at
// the only job nothing else does.
func Run(input []byte, opts Options) Outcome {
	var p payload
	if err := json.Unmarshal(input, &p); err != nil {
		return pass
	}
	if strings.TrimSpace(p.HookEventName) == "" {
		return pass
	}

	who, ok := whose(opts)
	if !ok {
		// No identity: this is not a session Orc started. Nothing to enforce, and
		// nothing to record.
		return pass
	}

	// The whole check runs under the deadline, not just the part that reads
	// permissions. That distinction is the difference between a bounded hook and a
	// hung session: opening the store is itself a read, and a store on a stalled
	// mount blocks there — before any timer inside would ever be consulted.
	//
	// The goroutine may outlive the select. That is deliberate and harmless: this
	// process exits moments later, and the alternative — waiting for a read that may
	// never return, in order to tidy up — is the hang this bound exists to prevent.
	done := make(chan Outcome, 1)
	go func() { done <- run(opts, who, p) }()

	select {
	case out := <-done:
		return out

	case <-time.After(opts.deadline()):
		// Nothing answered in time, so this is the ladder's third rung: reads pass,
		// writes block. No feed either — the path to it lives in the store, and the
		// store is what did not answer.
		if _, kind := p.targets(); kind == model.KindWrite {
			targets, _ := p.targets()
			return Outcome{Code: CodeBlock, Stderr: refuseSlow(targets, opts.deadline())}
		}
		return pass
	}
}

// run is the check itself, on the other side of the deadline.
func run(opts Options, who user.Name, p payload) Outcome {
	// One read-only handle for the whole run. The store is the only package that
	// knows the layout, so the feed's path and the workspace's come from it rather
	// than being composed here — and a hook on a hot path should open it once.
	//
	// A nil store is the third rung: no feed, and no permissions to read.
	s := open(opts)

	// The feed first, so that even a call this hook then blocks is visible in the
	// view. It is best-effort by construction — a feed that could not be written is a
	// view with a gap in it, and never a reason to fail a tool call.
	feed := newFeed(s, opts, who, p)

	if p.HookEventName != "PreToolUse" {
		feed.record(event.Verdict(""), "")
		return pass
	}

	out := decide(opts, s, who, p)
	verdict := event.VerdictAllow
	if out.Code == CodeBlock {
		verdict = event.VerdictBlock
	}
	feed.record(verdict, out.Stderr)
	return out
}

// whose resolves the identity this session belongs to.
func whose(opts Options) (user.Name, bool) {
	for _, key := range []string{EnvIdentity, identity.EnvUser} {
		if raw, ok := opts.env(key); ok && strings.TrimSpace(raw) != "" {
			if name, err := user.Parse(raw); err == nil {
				return name, true
			}
		}
	}
	return user.Name{}, false
}

// decide is the ladder.
func decide(opts Options, s *store.Store, who user.Name, p payload) Outcome {
	// The subagent denial comes first, and comes from nowhere else: it does not
	// depend on a store being readable, because the load accounting depends on it.
	// A rule that might be ignored under bypassPermissions cannot be the only thing
	// holding it.
	if p.ToolName == SubagentTool {
		return Outcome{Code: CodeBlock, Stderr: refuseSubagent()}
	}

	// The shell is checked before anything path-shaped, because it is decided by
	// what a command is *called* rather than by what it touches — and because it
	// is the one gate that refuses by default, so it must not depend on a path
	// having been found first.
	if p.ToolName == "Bash" {
		if out, decided := decideShell(s, who, p.ToolInput.Command); decided {
			return out
		}
	}

	targets, kind := p.targets()

	// Reaching into the fleet's own store is an escape rather than an ordinary
	// refusal, and it is checked before any permission is consulted: no clause can
	// permit it, so there is nothing to consult.
	if s != nil {
		for _, t := range targets {
			if protected(s, who, resolve(p.CWD, t)) {
				return Outcome{Code: CodeBlock, Stderr: refuseStore(t, s.Root())}
			}
		}
	}

	// What the identity keeps is nobody's to permit, so it is taken out of the
	// question before the permissions are consulted at all.
	//
	// Without this the carve-out in `protected` was dead letter. It says an
	// agent's CLAUDE.md and memory are "the agent's to keep" and lets them past
	// the escape check — and then the workspace test below refused them anyway,
	// because they sit *beside* the workspace rather than inside it and no
	// workspace-relative clause can ever reach them. The provisioned CLAUDE.md
	// tells every agent "anything you want to survive this session goes in
	// `memory/`", so the one thing every session was told to do was the one thing
	// it could not.
	//
	// Before the permissions ladder rather than inside the loop below, so that
	// what an identity keeps needs no clause of any kind — not a `write` clause it
	// would have to be granted, and not a `read` clause to see its own notes.
	//
	// It does *not* survive the third rung, and that is a limit rather than an
	// oversight. The directory is found by asking the store where it is, because
	// the store is the only package that knows the layout; with no store there is
	// nothing to ask, and a hook that composed the path itself would be a second
	// place that had to agree about it — which is the bug this whole function
	// replaces. A store that cannot be opened is also one whose memory directory
	// is not there to write to.
	targets = slices.DeleteFunc(targets, func(t string) bool {
		return keeps(s, who, resolve(p.CWD, t))
	})
	if len(targets) == 0 {
		return pass
	}

	patterns, source, ok := permissions(s, who)
	if !ok {
		// Third rung: nothing readable. Reads pass, writes block.
		if kind == model.KindRead {
			return pass
		}
		return Outcome{Code: CodeBlock, Stderr: refuseBlind(targets)}
	}

	// Reads narrow only when there is something to narrow them with. An identity
	// with no read clause at all is unrestricted — the same rule `orc`'s own verb
	// gating uses, and for the same reason: a rule that has to be bootstrapped
	// before anything can be read is not a rule, it is a deadlock.
	if kind == model.KindRead && !holdsKind(patterns, model.KindRead) {
		return pass
	}

	workspace := ""
	if s != nil {
		workspace = s.WorkspaceDir(who)
	}
	for _, t := range targets {
		full := resolve(p.CWD, t)
		rel, inside := relativeTo(workspace, full)
		if !inside {
			// Outside the workspace. A write there cannot be permitted by a
			// workspace-relative clause, so it is refused; a read there is refused
			// too, because the identity holds read clauses and this is not one of
			// them.
			return Outcome{Code: CodeBlock, Stderr: refuseOutside(t, workspace, kind, source)}
		}
		if !allows(patterns, kind, rel) {
			return Outcome{Code: CodeBlock, Stderr: refuseClause(t, rel, kind, patterns, source)}
		}
	}
	return pass
}

// decideShell gates a command line on the identity's `shell` clauses.
//
// It reports whether it decided: a pass here means "the shell gate is content",
// and the caller carries on to the path checks, because `rm Docs/x` is both a
// command and a write.
//
// **This gate refuses by default**, which no other one does. An identity with no
// shell clause may run model.Innocuous and nothing else. That is the point of it:
// every other kind narrows something agents may otherwise do freely, and a shell
// is every capability at once.
//
// The blind rung is a refusal too, and this is the one place that differs from
// reads. When no permissions can be read at all, a read passes because a blocked
// read discloses nothing new. A command is not like that — it could be anything —
// so an unreadable store stops the shell rather than opening it.
//
// Except for the default set, which is checked *before* the store is consulted at
// all. Those commands need no clause, so an unreadable list of clauses cannot
// change the answer — and the one that matters is mail: an agent whose store has
// gone away is exactly the agent that needs to be able to read its instructions
// and say what happened.
func decideShell(s *store.Store, who user.Name, command string) (Outcome, bool) {
	line := strings.TrimSpace(command)
	if line == "" {
		return pass, false
	}

	// An opaque line hides what it runs, so the only clause that can honestly
	// permit it is one that permits everything. Anything narrower would be
	// deciding on a name that says nothing about what happens. It is asked first
	// because a line whose commands cannot be named must not reach the default
	// set on the strength of the one name it did find.
	opaque := Opaque(line)

	runs := Runs(line)
	if !opaque {
		needed := false
		for _, r := range runs {
			if !model.InnocuousRun(r.Name, r.Args) {
				needed = true
				break
			}
		}
		if !needed {
			return pass, false
		}
	}

	patterns, source, ok := permissions(s, who)
	if !ok {
		return Outcome{Code: CodeBlock, Stderr: refuseBlindShell(line)}, true
	}

	if opaque {
		if allows(patterns, model.KindShell, "**") {
			return pass, false
		}
		return Outcome{Code: CodeBlock, Stderr: refuseOpaque(line, patterns, source)}, true
	}

	for _, r := range runs {
		if model.InnocuousRun(r.Name, r.Args) {
			continue
		}
		if !allows(patterns, model.KindShell, r.Name) {
			// A name on the default list can only have failed InnocuousRun on its
			// guarded subcommand, so that is what the refusal is about. A clause
			// naming the command covers it, which is why this is reached at all.
			if sub := model.GuardedSubcommand(r.Name); sub != "" {
				return Outcome{Code: CodeBlock, Stderr: refuseGuarded(r.Name, sub, line, patterns, source)}, true
			}
			return Outcome{Code: CodeBlock, Stderr: refuseShell(r.Name, line, patterns, source)}, true
		}
	}
	return pass, false
}

// permissions climbs the ladder: the live store, then the snapshot.
//
// The bool is whether *either* worked. A caller that treated an unreadable store as
// "no permissions" would silently drop to a permit-nothing state; a caller that
// treated it as "all permissions" would be worse. Both are wrong, so the distinction
// is returned rather than folded away.
func permissions(s *store.Store, who user.Name) (patterns []model.Pattern, source string, ok bool) {
	if s == nil {
		return nil, "", false
	}

	if fleet, err := s.Fleet(); err == nil {
		for _, c := range fleet.Clauses(who) {
			patterns = append(patterns, c.Pattern)
		}
		return patterns, "live", true
	}

	// Second rung. One small unlocked file, which is the whole reason it exists.
	snapshot, found, err := s.ReadAuthz(who)
	if err != nil || !found {
		return nil, "", false
	}
	got, dropped := snapshot.Patterns()
	source = "the permissions this session started with"
	if dropped > 0 {
		source += fmt.Sprintf(" (%d clause%s in it could not be read)", dropped, plural(dropped))
	}
	return got, source, true
}

// targets returns the paths this call would touch, and which kind of permission
// governs them.
func (p payload) targets() ([]string, model.Kind) {
	switch {
	case known(p.ToolName, Writers):
		var out []string
		for _, candidate := range []string{p.ToolInput.FilePath, p.ToolInput.NotebookPath, p.ToolInput.Path} {
			if strings.TrimSpace(candidate) != "" {
				out = append(out, candidate)
			}
		}
		// MultiEdit carries its targets in a list. They are usually all one file, but
		// nothing promises that, so each is checked.
		for _, e := range p.ToolInput.Edits {
			if strings.TrimSpace(e.FilePath) != "" {
				out = append(out, e.FilePath)
			}
		}
		return out, model.KindWrite

	case known(p.ToolName, Readers):
		for _, candidate := range []string{p.ToolInput.FilePath, p.ToolInput.NotebookPath, p.ToolInput.Path} {
			if strings.TrimSpace(candidate) != "" {
				return []string{candidate}, model.KindRead
			}
		}
		return nil, model.KindRead

	case p.ToolName == "Bash":
		// Deciding what an arbitrary shell command will write is undecidable, and
		// this does not try. It recognises two shapes: `anno write <path>`, because
		// that is how Anno reaches the filesystem, and any mention of the store root,
		// because that is the keyring. Everything else passes, and Plan.md §7.5 says
		// so rather than implying a guarantee that does not hold.
		if paths := annoWrites(p.ToolInput.Command); len(paths) > 0 {
			return paths, model.KindWrite
		}
		// The tools that read a file by name, checked against read clauses for the
		// same reason `anno write` is checked against write ones: they are how an
		// agent reaches the filesystem on purpose, so the clause that was supposed
		// to decide it should get to.
		//
		// This is what lets them be run without a `shell` clause at all. A reader
		// that no clause governed would be a second path to what `read(...)` decides
		// — the objection that keeps `cat` off the default list — and these are not
		// that, because the path is right there in the command line where it can be
		// checked.
		if paths := toolReads(p.ToolInput.Command); len(paths) > 0 {
			return paths, model.KindRead
		}
		if paths := storeMentions(p.ToolInput.Command); len(paths) > 0 {
			return paths, model.KindRead
		}
		return nil, model.KindUnset

	default:
		return nil, model.KindUnset
	}
}

// annoWrites finds `anno write <target>` in a shell command, including after a
// leading `cd … &&`.
func annoWrites(command string) []string {
	var out []string
	for _, segment := range splitCommands(command) {
		fields := strings.Fields(segment)
		for len(fields) > 1 && (fields[0] == "cd" || fields[0] == "sudo" || fields[0] == "env") {
			fields = fields[2:]
		}
		if len(fields) < 3 {
			continue
		}
		if filepath.Base(fields[0]) != "anno" || fields[1] != "write" {
			continue
		}
		if target := unquote(fields[2]); !strings.HasPrefix(target, "-") {
			out = append(out, target)
		}
	}
	return out
}

// toolReads finds the files an Orc tool has been asked to read.
//
// Two shapes, because there are two tools that take a path and read it:
//
//	anno read <path>     anno's whole purpose, and its `index` and `blocks` too
//	dock <path>          documentation by name
//
// Only the shapes that name a path. `anno index` with no argument reads nothing an
// agent could have chosen, and a form this does not recognise falls through to the
// same place every other shell command does — which Plan.md §7.5 already says is a
// hole rather than a guarantee.
func toolReads(command string) []string {
	var out []string
	for _, segment := range splitCommands(command) {
		fields := strings.Fields(segment)
		for len(fields) > 1 && (fields[0] == "cd" || fields[0] == "sudo" || fields[0] == "env") {
			fields = fields[2:]
		}
		if len(fields) < 2 {
			continue
		}

		var target string
		switch filepath.Base(fields[0]) {
		case "anno":
			// `anno <verb> <path>`, for the verbs that take one.
			if len(fields) < 3 {
				continue
			}
			switch fields[1] {
			case "read", "index", "blocks", "show":
				target = unquote(fields[2])
			}
		case "dock":
			// `dock <path>`, and `dock read <path>`.
			target = unquote(fields[1])
			if target == "read" && len(fields) > 2 {
				target = unquote(fields[2])
			}
		}
		if target == "" || strings.HasPrefix(target, "-") {
			continue
		}
		out = append(out, target)
	}
	return out
}

// storeMentions finds an argument that looks like a path inside an Orc store.
//
// It is a *shape* match rather than a resolution, because a shell command is not
// something this hook can evaluate: `$ORC_HOME`, `~/.orc`, and a literal store path
// all mean the keyring, and any of them in a command is worth refusing. The cost is a
// false positive on a command that merely mentions the string, which is a refusal
// somebody can rephrase — much cheaper than the alternative.
func storeMentions(command string) []string {
	var out []string
	for _, field := range strings.Fields(command) {
		clean := unquote(field)
		if strings.Contains(clean, "$ORC_HOME") || strings.Contains(clean, "/.orc/") ||
			strings.HasSuffix(clean, "/.orc") {
			out = append(out, clean)
		}
	}
	return out
}

func splitCommands(command string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(command, func(r rune) bool { return r == ';' || r == '\n' || r == '|' }) {
		for _, part := range strings.Split(strings.ReplaceAll(f, "||", "&&"), "&&") {
			if strings.TrimSpace(part) != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func known(name string, against []string) bool {
	for _, candidate := range against {
		if name == candidate {
			return true
		}
	}
	return false
}

// allows reports whether any clause of the right kind matches.
func allows(patterns []model.Pattern, kind model.Kind, rel string) bool {
	for _, p := range patterns {
		if p.Kind() == kind && p.Matches(rel) {
			return true
		}
	}
	return false
}

func holdsKind(patterns []model.Pattern, kind model.Kind) bool {
	for _, p := range patterns {
		if p.Kind() == kind {
			return true
		}
	}
	return false
}

// rootOf resolves the store root the same way every other Orc command does.
func rootOf(opts Options) (string, error) {
	if opts.Root != "" {
		return opts.Root, nil
	}
	home := opts.Home
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	return store.DefaultRoot(store.Env(opts.env), home)
}

// open takes a read-only handle on the store, or nil.
//
// Read-only is the point: it creates nothing and refuses every write, which is what
// makes it safe to open from something that fires on every tool call. A nil result is
// the ladder's third rung, and it is a normal answer rather than an error — a session
// running outside a fleet has no permissions to consult.
func open(opts Options) *store.Store {
	root, err := rootOf(opts)
	if err != nil {
		return nil
	}
	s, err := store.Read(root, opts.clock())
	if err != nil {
		return nil
	}
	return s
}

// resolve makes a tool's path absolute, against the session's cwd.
func resolve(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(cwd, path))
}

// relativeTo reports a path's position inside a root.
func relativeTo(root, path string) (string, bool) {
	if root == "" {
		return "", false
	}
	root = filepath.Clean(root)
	if path == root {
		return ".", true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// protected reports whether a path is part of the fleet's own state rather than an
// agent's work.
//
// The naive rule — everything under the store root — is wrong, and wrong in a way that
// breaks every session: an identity's **workspace lives inside the store**, at
// identities/<name>/workspace, so "deny the store" would deny an agent its own files.
// That is not a test artifact; it is where the workspace really is.
//
// So the rule is by role rather than by prefix. Off limits: the keyring, every
// credential, every journal, the policy, and — this one matters — the session's own
// `authz.json`, because an agent that could rewrite the snapshot could rewrite what the
// hook's second rung believes. Allowed: its own workspace, its own CLAUDE.md, and its
// own memory directory, because those are the agent's to keep.
//
// Another identity's workspace is protected here too. It would be refused a moment
// later as outside this identity's workspace, but a path that names somebody else's
// files should not need the permission check to notice.
func protected(s *store.Store, who user.Name, path string) bool {
	if _, inside := relativeTo(s.Root(), path); !inside {
		return false
	}
	if _, own := relativeTo(s.WorkspaceDir(who), path); own {
		return false
	}
	return !keeps(s, who, path)
}

// keeps reports whether a path is one of the things Orc provisions for an
// identity and then leaves alone.
//
// Two of them: its CLAUDE.md and its memory directory. Both live in the identity's
// Claude configuration, which is inside the fleet's store and *beside* the
// workspace rather than inside it — so neither the escape check nor any
// workspace-relative clause reaches them correctly, and both have to ask this
// instead.
//
// settings.json is deliberately not here. It carries the hook's own wiring, and an
// agent that could edit it could switch off the thing refusing this.
//
// It is one function rather than a test in each caller because the two callers
// disagreeing is exactly the bug this replaces: `protected` allowed these through
// and `decide` then refused them, so the fleet's own documentation described a
// capability no agent had.
func keeps(s *store.Store, who user.Name, path string) bool {
	if s == nil {
		return false
	}
	rel, own := relativeTo(s.ClaudeDir(who), path)
	if !own {
		return false
	}
	if rel == "CLAUDE.md" || underMemory(rel) {
		return true
	}
	// The project-scoped memory, which is where the harness actually points.
	//
	// Claude Code keeps per-project state under `projects/<slug>/` inside its
	// config directory, and its own auto-memory instructions name
	// `projects/<slug>/memory/` — not the `memory/` beside CLAUDE.md. Orc sets
	// CLAUDE_CONFIG_DIR to the identity's `claude/` dir, so that path lands
	// *inside the store*, matched neither carve-out, and was refused as fleet
	// state. An agent following the instructions it was given had its memory
	// writes blocked; only an agent that had read CLAUDE.md and used the other
	// directory got through.
	//
	// Just the memory, and not `projects/**`. The rest of that tree is Claude
	// Code's own per-project state, and settings among it — which is exactly what
	// `settings.json` is protected for. Widening the hole to the whole subtree to
	// fix a memory path would hand back the thing the carve-out is careful about.
	if rest, ok := strings.CutPrefix(rel, "projects"+string(filepath.Separator)); ok {
		if _, after, found := strings.Cut(rest, string(filepath.Separator)); found {
			return underMemory(after)
		}
	}
	return false
}

// underMemory reports whether a path is the memory directory or something in it.
func underMemory(rel string) bool {
	return rel == store.MemoryDir ||
		strings.HasPrefix(rel, store.MemoryDir+string(filepath.Separator))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
