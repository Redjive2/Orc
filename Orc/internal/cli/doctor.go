package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"orc/common/fault"
	"orc/common/watch"
	"orc/orc/internal/style"
)

// doctor reports every guard and says which are in force.
//
// It is the screen Plan.md §7.5 is printed on. That is why it exists at all: a
// tool whose boundary is Claude's tool layer rather than the operating system's
// has to be able to say so out loud, or an operator will believe in a jail that
// is not there. So the holes are printed on a healthy fleet too — they are not
// faults to be fixed, they are the shape of the wall — and only the guards that
// were *supposed* to hold and did not set the exit code.
//
// Nothing here changes anything, and nothing here kills anything. A stray
// process is named, never signalled: killing something an agent is mid-way
// through is worse than telling somebody it is there.

// guardState is what became of one guard.
type guardState int

const (
	// inForce was checked and is working.
	inForce guardState = iota
	// absent was checked and is not there. This is the one the screen exists to
	// be able to say.
	absent
	// partial is present but weaker than it should be.
	partial
	// unchecked could not be determined. It is not reassurance, and it is never
	// counted as a pass.
	unchecked
)

func (g guardState) String() string {
	switch g {
	case inForce:
		return "in force"
	case absent:
		return "absent"
	case partial:
		return "partial"
	default:
		return "not checked"
	}
}

// paint colours a state, redundantly with the word so a pipe loses nothing.
func (g guardState) paint(p style.Palette, s string) string {
	switch g {
	case inForce:
		return p.Good(s)
	case absent:
		return p.Alarm(s)
	case partial:
		return p.Warn(s)
	default:
		return p.Muted(s)
	}
}

// check is one guard and what was found.
type check struct {
	guard  string
	state  guardState
	detail string
	// lifted marks a session line whose limit is already over — the half that has
	// something to be done about it. It is on the row rather than recomputed later
	// so the advice and the state cannot disagree about the same clock.
	lifted bool
	// hole marks a guard that is *known* not to exist — §7.5's list. It prints
	// like an absence because it is one, but it is not a defect and does not
	// affect the exit code.
	hole bool
}

func (a App) doctor(args []string) error {
	if err := exactly(args, 0, "doctor takes no arguments"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	// The sessions are read before the guards, because whether a fleet needs a wake
	// cycle depends on whether anything is running in it.
	running, live := a.sessions(s)

	checks := a.guards(s, live)
	checks = append(checks, holes()...)
	// What to advise about a stopped agent depends on whether anything is watching
	// it: "nobody is looking" and "the cycle has not come round" are different
	// things to do about, so the lines are finished once that is known.
	watching := false
	for _, c := range checks {
		if c.guard == "wake cycle" && c.state == inForce {
			watching = true
		}
	}
	running = adviseOn(running, watching)

	if err := a.say(fmt.Sprintf("fleet: %s   %s",
		a.out.Value(s.store.Root()), a.where())); err != nil {
		return err
	}
	if err := a.say(""); err != nil {
		return err
	}

	width := 0
	for _, c := range append(append([]check{}, checks...), running...) {
		width = max(width, len(c.guard))
	}
	broken := 0
	for _, c := range checks {
		if (c.state == absent || c.state == partial) && !c.hole {
			broken++
		}
		if err := a.row(c, width); err != nil {
			return err
		}
	}

	// The fleet's own state, under its own heading. Kept apart from the guards
	// because it answers a different question — "is anything stopped" rather than
	// "is the wall holding" — and because mixing the two would put a usage limit in
	// the same column as a missing sandbox.
	if err := a.say(""); err != nil {
		return err
	}
	if err := a.say("  " + a.out.Header("sessions")); err != nil {
		return err
	}
	for _, c := range running {
		if err := a.row(c, width); err != nil {
			return err
		}
	}

	if err := a.say(""); err != nil {
		return err
	}
	if broken == 0 {
		if err := a.say(a.out.Good("every guard that can hold is holding") + "   " +
			a.out.Muted("the absences above are the wall's shape, not its damage")); err != nil {
			return err
		}
		return nil
	}
	if err := a.say(fmt.Sprintf("%s   %s",
		a.out.Alarm(fmt.Sprintf("%d guard%s not in force", broken, plural(broken))),
		a.out.Muted("each line above says what to do"))); err != nil {
		return err
	}
	return fault.Conflict{Path: s.store.Root(), Reason: fmt.Sprintf(
		"%d guard%s not in force", broken, plural(broken))}
}

// row draws one line of the screen, wrapped.
//
// The pad is applied to the plain word and the colour added around it: %-*s over a
// painted string would indent a coloured line differently from a plain one.
//
// The detail is the part that says what to do, so it is wrapped rather than
// truncated: a table cut at the terminal's edge would lose exactly the half a reader
// needs. Continuation lines align under the column so the names stay a scannable
// stripe down the left.
func (a App) row(c check, width int) error {
	indent := 2 + width + 2 + 12 + 1
	for i, part := range wrap(c.detail, a.detailWidth(indent)) {
		if i == 0 {
			if err := a.say(fmt.Sprintf("  %s  %s %s",
				pad(a.out.Header(c.guard), c.guard, width),
				pad(c.state.paint(a.out, c.state.String()), c.state.String(), 12),
				a.out.Muted(part))); err != nil {
				return err
			}
			continue
		}
		if err := a.say(strings.Repeat(" ", indent) + a.out.Muted(part)); err != nil {
			return err
		}
	}
	return nil
}

// detailWidth is how much room the detail column has.
//
// A zero Width means nobody said, which is a pipe or a test rather than a narrow
// terminal, so the text is left as one line: wrapping output that is about to be
// grepped only makes it harder to grep.
func (a App) detailWidth(indent int) int {
	if a.Width <= 0 {
		return 0
	}
	if room := a.Width - indent; room > 24 {
		return room
	}
	return 24
}

// wrap breaks text at spaces to fit a width, or returns it whole when width is
// zero.
func wrap(text string, width int) []string {
	if width <= 0 || len(text) <= width {
		return []string{text}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// pad widens a painted string to the column its plain twin would reach.
func pad(painted, plain string, column int) string {
	for len(plain) < column {
		painted += " "
		plain += " "
	}
	return painted
}

// guards runs every check that can be run against this fleet.
func (a App) guards(s caller, live int) []check {
	var out []check
	out = append(out, sessionLock())
	out = append(out, a.hookOnPath())
	out = append(out, a.keyringMode(s))
	out = append(out, a.compiledSettings(s))
	out = append(out, a.strayClaudes(s))
	out = append(out, a.workspaceDrift(s))
	out = append(out, a.sessionCredential())
	out = append(out, a.wakeCycle(s, live))
	return out
}

// where says whether this is a probe or the real fleet.
//
// It is not a guard, which is why it sits on the header line rather than in the
// list: a real fleet has no sandbox by design, and counting that as a guard not
// in force would mean doctor never exits zero anywhere it matters. But it is the
// first thing an operator needs to know, and the one they most want to be wrong
// about in the safe direction — believing you are in a probe when you are not is
// how a test wrecks a live fleet — so the real fleet is the answer that is
// painted to catch the eye.
func (a App) where() string {
	if home, ok := a.Env("ORCPROBE_HOME"); ok && strings.TrimSpace(home) != "" {
		return a.out.Good("(a probe — the real fleet is elsewhere)")
	}
	return a.out.Warn("(the real fleet — commands here affect live sessions)")
}

// sessionLock reports the advisory lock that keeps two Orc processes from
// populating one identity at once.
func sessionLock() check {
	if runtime.GOOS == "windows" {
		return check{guard: "session lock", state: absent,
			detail: "no flock on this platform; two orc processes could populate one identity"}
	}
	return check{guard: "session lock", state: inForce,
		detail: "flock on the identity directory"}
}

// hookOnPath reports whether orc-hook can be found.
//
// Without it the compiled deny list is all that is left, and §7.2 put Claude in
// bypassPermissions — so the deny list is a request rather than a fence. That is
// the single most consequential absence this screen can report.
func (a App) hookOnPath() check {
	path, err := exec.LookPath("orc-hook")
	if err != nil {
		return check{guard: "orc-hook", state: absent, detail: "not on PATH — " +
			"nothing enforces the permission model at the tool layer; build it with " +
			"`go build -o ~/.local/bin/orc-hook ./cmd/orc-hook`"}
	}
	return check{guard: "orc-hook", state: inForce, detail: path}
}

// keyringMode reports whether the store's own files are readable by anyone else.
//
// The keys are plaintext (§7.5), so the directory modes are the only thing
// between another unix user and the whole fleet. Every regular file under an
// identity is checked rather than the key alone: a mailbox credential or a
// session log is not as bad, but it is not nothing either.
func (a App) keyringMode(s caller) check {
	identities, err := s.store.Identities()
	if err != nil {
		return check{guard: "keyring mode", state: unchecked, detail: err.Error()}
	}

	var loose []string
	for _, i := range identities {
		dir := s.store.IdentityDir(i.Name())
		if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o077 != 0 {
			loose = append(loose, fmt.Sprintf("%s/ is %04o", i.Name(), info.Mode().Perm()))
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if info.Mode().Perm()&0o077 != 0 {
				loose = append(loose, fmt.Sprintf("%s/%s is %04o", i.Name(), e.Name(), info.Mode().Perm()))
			}
		}
	}

	if len(loose) == 0 {
		return check{guard: "keyring mode", state: inForce,
			detail: fmt.Sprintf("%d identit%s, nothing readable by anyone else",
				len(identities), plural2(len(identities), "y", "ies"))}
	}
	return check{guard: "keyring mode", state: partial, detail: fmt.Sprintf(
		"%s — the keys are plaintext, so this is the whole fleet; `chmod -R go-rwx %s`",
		strings.Join(loose, ", "), s.store.Root())}
}

// compiledSettings reports whether each populated identity has settings Claude
// will actually read.
//
// A file Orc cannot parse is reported and left alone, per the contract: an
// operator who hand-edited it meant something by it, and rewriting it would
// destroy the evidence of what.
func (a App) compiledSettings(s caller) check {
	identities, err := s.store.Identities()
	if err != nil {
		return check{guard: "compiled settings", state: unchecked, detail: err.Error()}
	}

	var missing, unreadable, weak []string
	populated := 0
	for _, i := range identities {
		if _, live, err := s.store.Session(i.Name()); err != nil || !live {
			continue
		}
		populated++

		path := filepath.Join(s.store.ClaudeDir(i.Name()), "settings.json")
		data, err := os.ReadFile(path)
		if err != nil {
			missing = append(missing, i.Name().String())
			continue
		}
		var settings struct {
			PermissionMode string `json:"permissionMode"`
			Permissions    struct {
				Deny []string `json:"deny"`
			} `json:"permissions"`
			Hooks map[string]any `json:"hooks"`
		}
		if json.Unmarshal(data, &settings) != nil {
			unreadable = append(unreadable, i.Name().String())
			continue
		}
		switch {
		case len(settings.Hooks) == 0:
			weak = append(weak, i.Name().String()+" has no hooks")
		case !denies(settings.Permissions.Deny, "Agent"):
			weak = append(weak, i.Name().String()+" does not deny the Agent tool")
		case !denies(settings.Permissions.Deny, s.store.Root()):
			weak = append(weak, i.Name().String()+" does not deny reading the store")
		}
	}

	switch {
	case populated == 0:
		return check{guard: "compiled settings", state: unchecked,
			detail: "no identity is populated, so there is nothing to compile against"}
	case len(unreadable) > 0:
		return check{guard: "compiled settings", state: partial, detail: fmt.Sprintf(
			"%s: settings.json will not parse; orc left it alone — fix or delete it and re-employ",
			strings.Join(unreadable, ", "))}
	case len(missing) > 0:
		return check{guard: "compiled settings", state: absent, detail: fmt.Sprintf(
			"%s: no settings.json, so nothing is compiled — `orc refresh <name>`",
			strings.Join(missing, ", "))}
	case len(weak) > 0:
		return check{guard: "compiled settings", state: partial,
			detail: strings.Join(weak, "; ")}
	}
	return check{guard: "compiled settings", state: inForce, detail: fmt.Sprintf(
		"%d populated identit%s, each denying the store and the Agent tool",
		populated, plural2(populated, "y", "ies"))}
}

func denies(rules []string, needle string) bool {
	for _, r := range rules {
		if strings.Contains(r, needle) {
			return true
		}
	}
	return false
}

// credential reports whether a session Orc starts can authenticate at all.
//
// This is the guard for the failure that looks like nothing: a session with no
// credential does not crash, it opens a *login prompt* — and a login prompt on a pty
// nobody is attached to is an agent that sits there for ever, employed, running, and
// doing nothing. `orc status` calls it live because it is.
//
// The order below is Claude's own precedence, so what this names is what a session
// would actually use rather than the first thing Orc happens to find.
//
// A subscription login is the case Orc cannot check from here. It lives in the
// macOS keychain — or `~/.claude/.credentials.json` elsewhere — reached through the
// real HOME a session inherits, and reading it to find out would be Orc handling
// somebody's credential to answer a question about it. So it is reported as
// "cannot say from here", with what to run, rather than guessed at in either
// direction.
func (a App) sessionCredential() check {
	const guard = "a session can authenticate"

	for _, named := range []struct{ env, what string }{
		{"CLAUDE_CODE_USE_BEDROCK", "Amazon Bedrock"},
		{"CLAUDE_CODE_USE_VERTEX", "Google Cloud"},
		{"CLAUDE_CODE_USE_FOUNDRY", "Microsoft Foundry"},
		{"ANTHROPIC_AUTH_TOKEN", "$ANTHROPIC_AUTH_TOKEN"},
		{"ANTHROPIC_API_KEY", "$ANTHROPIC_API_KEY"},
		{"CLAUDE_CODE_OAUTH_TOKEN", "$CLAUDE_CODE_OAUTH_TOKEN, from `claude setup-token`"},
	} {
		if v, ok := a.Env(named.env); ok && strings.TrimSpace(v) != "" {
			return check{guard: guard, state: inForce,
				detail: "sessions inherit " + named.what}
		}
	}

	return check{guard: guard, state: unchecked, detail: "no credential is in orc's environment, so a " +
		"session falls back to the subscription login in the keychain — which orc cannot read from here. " +
		"if agents stop at a login prompt, `claude setup-token` mints a token for exactly this and " +
		"$CLAUDE_CODE_OAUTH_TOKEN reaches every session"}
}

// workspaceDrift reports sessions working somewhere their identity no longer says.
//
// A workspace moved while a session runs leaves the agent writing to a directory
// Orc does not consider its workspace — and whose paths its compiled permissions
// were written against. `orc workspace <identity>` says so for one agent; nobody
// runs that for every agent, and this is the check that is run when something is
// already suspected.
//
// A session that predates the workspace being recorded says nothing: "cannot say"
// is not a disagreement, and reporting it as one would make doctor cry wolf on
// every fleet that upgraded.
func (a App) workspaceDrift(s caller) check {
	const guard = "workspace drift"

	identities, err := s.store.Identities()
	if err != nil {
		return check{guard: guard, state: unchecked, detail: err.Error()}
	}

	var drifted []string
	for _, i := range identities {
		state, live, err := s.store.Session(i.Name())
		if err != nil || !live || state.Workspace == "" {
			continue
		}
		want := s.store.WorkspaceDir(i.Name())
		if filepath.Clean(state.Workspace) == filepath.Clean(want) {
			continue
		}
		drifted = append(drifted, fmt.Sprintf("%s is working in %s, not %s", i.Name(), state.Workspace, want))
	}

	if len(drifted) == 0 {
		return check{guard: guard, state: inForce,
			detail: "every running session is in the directory its identity names"}
	}
	// partial rather than absent: the guard exists and is doing its job — it is
	// what it found that is wrong. The fix is named because it is one command and
	// the alternative is an operator guessing at `orc workspace` variants.
	return check{guard: guard, state: partial,
		detail: strings.Join(drifted, "; ") + " — `orc refresh <identity>` starts a session in the right place"}
}

// strayClaudes looks for thinking Orc did not start.
//
// §7.5 is explicit that a session can reach parallelism through a Bash call to
// `claude -p`, which arrives with no identity, no budget, and no place in the
// tree. This finds the ones whose parent is an Orc session, which is the case
// Orc can actually recognise, and reports them. It never kills one.
func (a App) strayClaudes(s caller) check {
	if runtime.GOOS == "windows" {
		return check{guard: "stray claudes", state: unchecked,
			detail: "process listing is not implemented on this platform"}
	}

	supervisors := map[int]string{}
	identities, err := s.store.Identities()
	if err != nil {
		return check{guard: "stray claudes", state: unchecked, detail: err.Error()}
	}
	known := map[int]bool{}
	for _, i := range identities {
		st, live, err := s.store.Session(i.Name())
		if err != nil || !live {
			continue
		}
		supervisors[st.Supervisor] = i.Name().String()
		known[st.Child] = true
	}
	if len(supervisors) == 0 {
		return check{guard: "stray claudes", state: inForce, detail: "no session is running"}
	}

	procs, err := processes()
	if err != nil {
		return check{guard: "stray claudes", state: unchecked,
			detail: "could not list processes: " + err.Error()}
	}

	var strays []string
	for _, p := range procs {
		if known[p.pid] || !strings.Contains(p.command, "claude") {
			continue
		}
		if who, ok := supervisors[p.ppid]; ok {
			strays = append(strays, fmt.Sprintf("pid %d under %s", p.pid, who))
		}
	}
	if len(strays) == 0 {
		return check{guard: "stray claudes", state: inForce, detail: fmt.Sprintf(
			"%d session%s, no thinking orc did not start", len(supervisors), plural(len(supervisors)))}
	}
	return check{guard: "stray claudes", state: partial, detail: fmt.Sprintf(
		"%s — outside the worklist and the budget; orc will not kill them",
		strings.Join(strays, ", "))}
}

// proc is one entry of the process table.
type proc struct {
	pid     int
	ppid    int
	command string
}

// processes lists the process table through ps, which is the portable-enough
// answer on the unixes Orc runs on.
func processes() ([]proc, error) {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, proc{pid: pid, ppid: ppid, command: strings.Join(fields[2:], " ")})
	}
	return procs, nil
}

// holes are §7.5, printed rather than implied.
//
// They are not defects and they do not set the exit code. They are here because
// an operator reading a screen full of "in force" would otherwise conclude the
// permission model is a fence, and it is not: it is a request that one hook
// enforces, on one side of one tool layer.
func holes() []check {
	return []check{
		{guard: "shell reads the keyring", state: absent, hole: true,
			detail: "a session that can run a shell can cat ~/.orc/*/key; orc-hook blocks the " +
				"obvious shapes and records an escape, but a path can be obfuscated and none of " +
				"this is a kernel boundary"},
		{guard: "subagents", state: absent, hole: true,
			detail: "the Agent tool is denied, so the worklist is the whole picture — except for " +
				"a Bash call to `claude -p`, which orc cannot decide and only names above"},
		{guard: "bash writes", state: absent, hole: true,
			detail: "`anno write` is covered because anno asks; `sed -i` is not"},
		{guard: "pattern breadth", state: absent, hole: true,
			detail: "a permission is only as narrow as its patterns, and write(**) is one orc will " +
				"happily enforce"},
	}
}

// --- what keeps the fleet moving ------------------------------------------

// wakeCycle reports whether anything is poking silent agents.
//
// It is a guard rather than a remark, and it counts, because its absence is the
// difference between a fleet that recovers and one that does not. Every other guard
// here answers "is the wall holding"; this one answers "is anybody watching" — and
// an unwatched fleet fails in the quietest way there is. An agent finishes a turn
// and waits. An agent hits its usage limit and waits. Both are states somebody has
// to speak to, and with no cycle running nobody ever does: the fleet is simply
// stopped, at no particular moment, with nothing on any screen saying so.
//
// The answer comes from the watcher registry rather than from the process table.
// Looking for an `orc wake --every` in `ps` is wrong in both directions: a cycle
// watching *another* fleet has the same command line and would read as this one
// being covered, and the registry already checks that the pid it names is alive.
// This is the question `cq upgrade` asks of the same file, and two ways of asking
// it would be two answers to keep in step.
func (a App) wakeCycle(s caller, live int) check {
	const guard = "wake cycle"

	running, err := watch.Registry{Dir: filepath.Join(s.store.Root(), "watchers")}.Running(watch.Wake)
	if err != nil {
		return check{guard: guard, state: unchecked, detail: err.Error()}
	}
	// A fleet with nothing running has nothing to wake, and a guard that failed on
	// an empty fleet would fail on every fleet the moment it was made — which is
	// how a check earns the reputation that makes people stop reading it.
	if live == 0 && !running {
		return check{guard: guard, state: inForce,
			detail: "no session is running, so there is nothing to wake yet — a fleet with agents " +
				"in it wants `orc wake --every 5m --tend`"}
	}
	if running {
		return check{guard: guard, state: inForce,
			detail: "a sweep is running over this fleet — `orc wake --dry-run` says what its next pass would do"}
	}
	// A `tend --watch` is not this. It keeps sessions *running*, which is the other
	// backstop and the one that cannot resume a session that is already up.
	return check{guard: guard, state: absent,
		detail: "nothing is waking silent agents, so an agent that finishes a turn or hits its " +
			"usage limit stays stopped until somebody notices — run `orc wake --every 5m --tend`"}
}

// sessions is what the running fleet is doing, as opposed to what is guarding it.
//
// It is a second section rather than more guards, and it does not touch the exit
// code, because these are not defects: an agent at a usage limit is a fleet working
// normally against a clock. Counting it as a guard not in force would mean `orc
// doctor` failed a cron every time an agent hit a limit, and an alarm that fires on
// weather is an alarm nobody reads.
//
// What earns a line is a session that is up and cannot move on its own. That is one
// state today — the usage limit — and it is here because it is invisible everywhere
// else: the child is alive, the socket answers, and until this was read from the
// transcript the only symptom was a fleet that had quietly stopped.
// It returns the lines and how many sessions are up, because the guard above needs
// the second: a fleet with nothing running does not need a cycle.
func (a App) sessions(s caller) ([]check, int) {
	identities, err := s.store.Identities()
	if err != nil {
		return []check{{guard: "sessions", state: unchecked, detail: err.Error(), hole: true}}, 0
	}

	now := s.store.Now()
	var out []check
	live := 0
	for _, i := range identities {
		if _, up, err := s.store.Session(i.Name()); err != nil || !up {
			continue
		}
		live++

		limit, hit := a.limitOf(s, i.Name())
		if !hit {
			continue
		}
		state := partial
		if !limit.Over(now) {
			// Nothing to do but wait, so it is not dressed up as a problem.
			state = inForce
		}
		out = append(out, check{guard: i.Name().String(), state: state,
			detail: limit.Says(now), hole: true, lifted: limit.Over(now)})
	}

	if len(out) == 0 {
		what := fmt.Sprintf("%d session%s, none stopped", live, plural(live))
		if live == 0 {
			what = "nothing is running"
		}
		return []check{{guard: "sessions", state: inForce, detail: what, hole: true}}, live
	}
	return out, live
}

// adviseOn finishes the session lines once it is known whether anything is watching.
//
// The advice is the part that differs, and it differs in a way that matters: a
// stopped agent with no cycle needs one started, and a stopped agent with a cycle
// running that has not resumed it means the cycle is not doing its job — which is
// the case that looks perfectly healthy on every other screen.
func adviseOn(lines []check, watching bool) []check {
	out := make([]check, 0, len(lines))
	for _, c := range lines {
		switch {
		case !c.lifted:
		case !watching:
			c.detail += " — nothing is waking it; `orc wake` resumes it now, and a cycle " +
				"would keep doing so"
		default:
			c.detail += " — a wake cycle is running and has not resumed it, so check it is " +
				"the current build (`orc wake --dry-run` says what a pass would do)"
		}
		out = append(out, c)
	}
	return out
}
