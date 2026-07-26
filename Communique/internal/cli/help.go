package cli

import (
	"fmt"
	"strings"

	"orc/common/guess"

	"orc/cq/internal/fault"
	"orc/cq/internal/style"
	"orc/theme"
)

// Help is data rather than a wall of text, so `cq help` and `cq help <command>`
// cannot drift apart: both are rendered from the same table, and a command
// added without documentation is a compile error waiting rather than a gap
// nobody notices.

// flagDoc documents one flag.
type flagDoc struct {
	name    string
	value   string // the placeholder, if it takes one
	summary string
	deflt   string
}

// commandDoc documents one command.
type commandDoc struct {
	name    string
	args    string
	summary string
	// detail is the prose `cq help <command>` adds. It says what the command
	// is *for* — the summary already says what it does.
	detail   string
	side     string // "server" or "agent", so it is obvious where it runs
	flags    []flagDoc
	examples []string
}

// commands is cq's whole surface, in the order a newcomer meets it.
var commands = []commandDoc{
	{
		name: "serve", side: "server",
		summary: "Serve the website and the cq API",
		detail: "Runs on the machine you can reach from a browser. It serves the\n" +
			"interface and the API on port 8080, and refuses to start until a\n" +
			"password and a sync token exist — a login gate with nothing behind\n" +
			"it is not a gate.\n\n" +
			"Nothing on the site is visible without logging in, including the\n" +
			"application itself.",
		flags: []flagDoc{
			{"--addr", "<host:port>", "Address to listen on", ":8080"},
			{"--state", "<dir>", "Where the server keeps its state", "$CQ_STATE"},
			{"--tls-cert", "<file>", "Certificate, to terminate TLS here", ""},
			{"--tls-key", "<file>", "Key that goes with it", ""},
			{"--no-admin", "", "Do not serve the admin panel at all", ""},
			{"--admin-metadata-only", "", "Record that bodies should be withheld", ""},
			{"--supervise", "", "Run a supervisor so the server can restart itself", "on"},
			{"--source", "<dir>", "Checkout `upgrade` pulls and builds", "$CQ_SOURCE"},
			{"--bin", "<dir>", "Where `upgrade` installs binaries", "$CQ_BIN"},
		},
		examples: []string{
			"cq serve",
			"cq serve --addr 127.0.0.1:8080",
			"cq serve --tls-cert cert.pem --tls-key key.pem",
		},
	},
	{
		name: "sync", side: "agent",
		summary: "Mirror this machine up, and bring queued actions down",
		detail: "Runs where Mailman and Macmuffin live. One round trip carries both\n" +
			"directions: this machine's whole state goes up, and whatever you did\n" +
			"in the browser comes down to be applied here.\n\n" +
			"The server can never reach back, so your replies leave on the next\n" +
			"sync — which is why --watch exists, and why every Mailman action\n" +
			"nudges one.\n\n" +
			"Whose mailbox it mirrors needs saying only if it is not the obvious\n" +
			"one: with nothing set, cq asks orc who the operator is and mirrors\n" +
			"them. Set $CQ_USER and $CQ_KEY to mirror somebody else — or to keep\n" +
			"mirroring the operator from a shell that is signed in as an agent.",
		flags: []flagDoc{
			{"--server", "<url>", "The cq server to sync against", "$CQ_SERVER"},
			{"--machine", "<name>", "What to call this machine", "$CQ_MACHINE, else the hostname"},
			{"--user", "<name>", "The mailbox to mirror", "$CQ_USER, else orc's operator"},
			{"--home", "<dir>", "Where the agent keeps its journal", "$CQ_HOME"},
			{"--watch", "<duration>", "Repeat at this interval instead of once", "off"},
			{"--nudge", "", "Coalescing form; what Mailman calls after each action", ""},
			{"--dry-run", "", "Collect and report, but send nothing", ""},
			{"--admin", "", "Include the whole-Mailman view", "on"},
			{"--admin-bodies", "", "Include other users' message bodies", "on"},
			{"--library", "<dir>", "A repository to mirror for reading", "$CQ_LIBRARY"},
		},
		examples: []string{
			"cq sync",
			"cq sync --watch 5m",
			"cq sync --dry-run",
		},
	},
	{
		name: "status", side: "agent",
		summary: "Say what the last sync came to",
		detail: "Reads only what is on this machine — it never touches the network,\n" +
			"so it answers even when the server is down. That is the point: it is\n" +
			"how you tell a server that is unreachable from a sync that was never\n" +
			"running.\n\n" +
			"It does run orc, to say whose mailbox this machine would mirror and\n" +
			"how it worked that out.",
		flags: []flagDoc{
			{"--home", "<dir>", "Where the agent keeps its journal", "$CQ_HOME"},
		},
		examples: []string{"cq status"},
	},
	{
		name: "queue", side: "server",
		summary: "See what is waiting, and deal with what did not work",
		detail: "Every action queued from the website is here, with what became of\n" +
			"it. An action the agent refused can be tried again or forgotten —\n" +
			"without that it would sit in the queue for ever, counted and\n" +
			"unclearable.\n" +
			"\n" +
			"A retry is a *new* action, and the reply says so. It has to be: the\n" +
			"agent remembers every action it has applied and skips the ones it\n" +
			"recognises, so reusing the identifier would produce something that\n" +
			"looks like a retry and is quietly ignored.\n" +
			"\n" +
			"An action interrupted mid-apply is marked in doubt rather than\n" +
			"failed, and a send in that state is not offered a retry: it may\n" +
			"already be in somebody's inbox, and cq cannot tell. Check your sent\n" +
			"mail and write it again if it never arrived.\n" +
			"\n" +
			"Ids may be given by their first few characters, as printed.\n" +
			"\n" +
			"The queue is a log as much as a queue: an action that is done stays\n" +
			"on the list. `clear` sweeps up the done ones, and `--all` takes the\n" +
			"refused and in-doubt ones too — those carry the only record of why\n" +
			"they failed, so they are never swept by default.",
		flags: []flagDoc{
			{"--state", "<dir>", "Where the server keeps its state", "$CQ_STATE"},
			{"--json", "", "Print the queue for another program", ""},
		},
		examples: []string{
			"cq queue",
			"cq queue retry 2c6f875a",
			"cq queue drop 2c6f875a",
			"cq queue clear",
			"cq queue clear --all",
		},
	},
	{
		name: "admin operator", side: "server",
		summary: "Set or change the login password",
		detail: "The password you type into the browser. It is stored only as a\n" +
			"PBKDF2 digest and can never be read back.\n\n" +
			"It is read from a prompt, or from $CQ_PASSWORD for a scripted\n" +
			"setup. Terminal echo is not disabled: doing that portably needs\n" +
			"either a dependency or raw terminal handling, so cq says so and\n" +
			"lets you pipe it in instead.",
		examples: []string{
			"cq admin operator",
			"printf 'my long password' | cq admin operator",
		},
	},
	{
		name: "admin token", args: "[label]", side: "server",
		summary: "Mint a sync token for one agent machine",
		detail: "Printed once and stored only as a digest, so it cannot be read back\n" +
			"later. Give it to the agent machine as $CQ_TOKEN.\n\n" +
			"Mint one per machine: they can then be told apart in the log, and\n" +
			"revoked one at a time.",
		examples: []string{
			"cq admin token studio",
			"cq admin token laptop",
		},
	},
	{
		name: "upgrade", side: "agent",
		summary: "Rebuild and restart every tool, everywhere",
		detail: "Pulls the tree, rebuilds every Orc tool, and restarts — on the\n" +
			"machine serving the site *and* on every agent machine.\n\n" +
			"Two halves, because the two are reachable in opposite directions.\n" +
			"The server upgrades itself: a local pull, a local build, and a\n" +
			"restart through its supervisor. Each agent machine gets a queued\n" +
			"action instead, and does the work on its next sync — the server\n" +
			"cannot reach them, which is the whole architecture.\n\n" +
			"Nothing queued is lost by the restart. The queue is on disk before\n" +
			"the server goes down, so an agent that synced during the gap retries\n" +
			"and one that had not finds its action waiting.\n\n" +
			"It needs Orc's `upgrade` permission, which is builtin at floor 90 —\n" +
			"executive agents only. `orc check-permission upgrade` says whether\n" +
			"you hold it.",
		flags: []flagDoc{
			{"--yes", "", "Required; this restarts the site and every agent", ""},
			{"--server", "<url>", "The cq server to ask", "$CQ_SERVER"},
			{"--token", "<token>", "The sync token", "$CQ_TOKEN"},
			{"--machines", "<a,b>", "Only these agent machines", "all of them"},
			{"--no-server", "", "Upgrade the agents but leave the site up", ""},
		},
		examples: []string{
			"cq upgrade --yes",
			"cq upgrade --yes --no-server",
			"cq upgrade --yes --machines studio",
		},
	},
	{
		name: "help", args: "[command]", side: "",
		summary:  "This, or the detail for one command",
		examples: []string{"cq help", "cq help sync"},
	},
}

// settings documents the environment, which is how cq is configured in the
// normal case: a flag is for overriding one run.
var settings = []struct {
	name, summary, side string
}{
	{"CQ_STATE", "Where the server keeps its state", "server"},
	{"CQ_PASSWORD", "The login password, for a scripted setup", "server"},
	{"CQ_HOME", "Where the agent keeps its journal", "agent"},
	{"CQ_SERVER", "The cq server to sync against", "agent"},
	{"CQ_TOKEN", "The sync token, from `cq admin token`", "agent"},
	{"CQ_MACHINE", "What to call this machine", "agent"},
	{"CQ_USER", "The mailbox to mirror; by default, orc's operator", "agent"},
	{"CQ_LIBRARY", "A repository to mirror for reading, if any", "agent"},
	{"CQ_KEY", "That mailbox's orc key, so any agent's action can nudge", "agent"},
	{"ORC", "The orc executable, if it is not on the path as `orc`", "agent"},
	{"CQ_SOURCE", "The checkout `upgrade` pulls and builds", ""},
	{"CQ_BIN", "Where `upgrade` installs binaries; else beside the running one", ""},
	{"ORC_THEME", "Colour scheme, shared with every Orc tool", ""},
}

// firstRun is the shortest path from nothing to a working mirror. It is the
// first thing `cq help` prints, because "what do I need to do" is the question
// someone reading it almost always has.
var firstRun = []struct{ where, step, why string }{
	{"server", "cq admin operator", "set the password you will log in with"},
	{"server", "cq admin token studio", "mint a token; copy it, it is shown once"},
	{"server", "cq serve", "serve the site on :8080"},
	{"agent", "export CQ_SERVER=https://… CQ_TOKEN=…", "point this machine at it"},
	{"agent", "cq sync --watch 5m", "mirror, and collect what you queue"},
}

// exitCodes are stable: scripts and the nudge path branch on them.
var exitCodes = []struct {
	code int
	name string
}{
	{0, "ok"}, {1, "usage"}, {2, "not found"}, {3, "ambiguous"}, {4, "parse"},
	{5, "i/o"}, {6, "conflict"}, {7, "unauthenticated"}, {8, "unreachable"},
}

// --- rendering -----------------------------------------------------------

// writer accumulates help text, painting as it goes and padding from the plain
// text — so a coloured column lines up exactly as an uncoloured one does.
type writer struct {
	b strings.Builder
	p style.Palette
}

func (w *writer) line(s ...string) {
	for _, part := range s {
		w.b.WriteString(part)
	}
	w.b.WriteByte('\n')
}

func (w *writer) blank() { w.b.WriteByte('\n') }

func (w *writer) heading(text string) {
	w.blank()
	w.line(w.p.Paint(text, style.Heading))
}

// pad returns spaces enough to bring plain text of the given width up to n.
func pad(plain string, n int) string {
	if d := n - len([]rune(plain)); d > 0 {
		return strings.Repeat(" ", d)
	}
	return " "
}

// Overview is `cq help`: what cq is, what to do first, and what it can do.
func Overview(p style.Palette) string {
	w := &writer{p: p}

	w.line(p.Paint("cq", style.Tool), " ", p.Paint("— communiqué, remote window into Orc", style.Quiet))
	w.blank()
	w.line(p.Paint("  It mirrors Mailman, Macmuffin, Anno, and Dock.", style.Quiet))
	w.line(p.Paint("  inbox to a website you can reach from anywhere, alongside the Macmuffin", style.Quiet))
	w.line(p.Paint("  board and an admin view over the whole of Mailman.", style.Quiet))

	w.heading("what you need to do first")
	stepWidth := 0
	for _, step := range firstRun {
		if n := len([]rune(step.step)); n > stepWidth {
			stepWidth = n
		}
	}
	for i, step := range firstRun {
		where := p.Paint(fmt.Sprintf("%-6s", step.where), style.Value)
		w.line("  ", p.Paint(fmt.Sprintf("%d.", i+1), style.Quiet), " ", where, " ",
			p.Paint(step.step, style.Command), pad(step.step, stepWidth+2),
			p.Paint(step.why, style.Quiet))
	}
	w.blank()
	w.line("  ", p.Paint("It mirrors whoever orc calls the operator. Set $CQ_USER and $CQ_KEY", style.Quiet))
	w.line("  ", p.Paint("only to mirror somebody else, or to keep mirroring the operator from", style.Quiet))
	w.line("  ", p.Paint("a shell signed in as an agent.", style.Quiet))
	w.blank()
	w.line("  ", p.Paint("The two sides never meet: the server cannot reach the agent, so", style.Quiet))
	w.line("  ", p.Paint("anything you do in the browser waits for the next sync. The site", style.Quiet))
	w.line("  ", p.Paint("says so — it shows how stale it is and marks queued things queued.", style.Quiet))

	w.heading("commands")
	width := 0
	for _, c := range commands {
		if n := len(c.name + " " + c.args); n > width {
			width = n
		}
	}
	for _, c := range commands {
		usage := c.name
		if c.args != "" {
			usage += " " + c.args
		}
		side := ""
		if c.side != "" {
			side = p.Paint(fmt.Sprintf("%-7s", c.side), style.Value)
		} else {
			side = strings.Repeat(" ", 7)
		}
		w.line("  ", side, " ", p.Paint(usage, style.Command), pad(usage, width+2),
			p.Paint(c.summary, style.Quiet))
	}

	w.heading("environment")
	for _, s := range settings {
		side := strings.Repeat(" ", 7)
		if s.side != "" {
			side = p.Paint(fmt.Sprintf("%-7s", s.side), style.Value)
		}
		w.line("  ", side, " ", p.Paint(s.name, style.Setting), pad(s.name, 14),
			p.Paint(s.summary, style.Quiet))
	}

	w.heading("exit codes")
	var codes []string
	for _, e := range exitCodes {
		codes = append(codes, p.Paint(fmt.Sprint(e.code), style.Value)+" "+p.Paint(e.name, style.Quiet))
	}
	w.line("  ", strings.Join(codes, p.Paint(" · ", style.Frame)))

	w.blank()
	for _, line := range strings.Split(theme.Help(), "\n") {
		w.line("  ", p.Paint(line, style.Quiet))
	}

	w.blank()
	w.line("  ", p.Paint("cq help <command>", style.Command), " ",
		p.Paint("for what a command is for, its flags, and examples.", style.Quiet))

	return strings.TrimRight(w.b.String(), "\n")
}

// Detail is `cq help <command>`.
func Detail(p style.Palette, name string) (string, bool) {
	doc, ok := find(name)
	if !ok {
		return "", false
	}
	w := &writer{p: p}

	usage := "cq " + doc.name
	if doc.args != "" {
		usage += " " + doc.args
	}
	if len(doc.flags) > 0 {
		usage += " [flags]"
	}
	w.line(p.Paint(usage, style.Command))
	w.blank()
	w.line("  ", p.Paint(doc.summary, style.Heading))
	if doc.side != "" {
		w.line("  ", p.Paint("runs on the "+doc.side+" machine", style.Quiet))
	}

	if doc.detail != "" {
		w.blank()
		for _, line := range strings.Split(doc.detail, "\n") {
			if line == "" {
				w.blank()
				continue
			}
			w.line("  ", p.Paint(line, style.Quiet))
		}
	}

	if len(doc.flags) > 0 {
		w.heading("flags")
		width := 0
		for _, f := range doc.flags {
			if n := len(f.name + " " + f.value); n > width {
				width = n
			}
		}
		for _, f := range doc.flags {
			label := f.name
			shown := p.Paint(f.name, style.Flag)
			if f.value != "" {
				label += " " + f.value
				shown += " " + p.Paint(f.value, style.Value)
			}
			line := []string{"  ", shown, pad(label, width+2), p.Paint(f.summary, style.Quiet)}
			if f.deflt != "" {
				line = append(line, p.Paint("  ("+f.deflt+")", style.Setting))
			}
			w.line(line...)
		}
	}

	if len(doc.examples) > 0 {
		w.heading("examples")
		for _, e := range doc.examples {
			w.line("  ", p.Paint(e, style.Command))
		}
	}

	return strings.TrimRight(w.b.String(), "\n"), true
}

// find matches a command by name, accepting "admin token" and "token" alike so
// `cq help token` does the obvious thing.
func find(name string) (commandDoc, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	for _, c := range commands {
		if after, ok := strings.CutPrefix(c.name, "admin "); ok && after == name {
			return c, true
		}
	}
	return commandDoc{}, false
}

// Names lists every documented command, for an error that suggests rather than
// only refuses.
func Names() []string {
	out := make([]string, 0, len(commands))
	for _, c := range commands {
		out = append(out, c.name)
	}
	return out
}

// Brief is what `cq` on its own prints: the commands, with the side each runs on,
// and nothing else.
//
// The overview is the right screen for somebody setting cq up and the wrong one
// for somebody who typed `cq` — it is fifty lines, most of it the first-run steps
// and the environment. This is the other answer.
func Brief(p style.Palette) string {
	w := &writer{p: p}

	w.line(p.Paint("cq", style.Tool), " ", p.Paint("— communiqué, remote window into Orc", style.Quiet))
	w.blank()

	width := 0
	for _, c := range commands {
		if n := len(c.name + " " + c.args); n > width {
			width = n
		}
	}
	for _, c := range commands {
		usage := c.name
		if c.args != "" {
			usage += " " + c.args
		}
		// Padding measured on the plain text and applied after the paint: escapes
		// have no width but do have length, so a painted %-*s lays the column out
		// one way with colour and another without.
		w.line("  ", p.Paint(fmt.Sprintf("%-6s", c.side), style.Value), " ",
			p.Paint(usage, style.Command), pad(usage, width+2),
			p.Paint(c.summary, style.Quiet))
	}

	w.blank()
	w.line("  ", p.Paint("cq help for the whole screen, including what to set up first", style.Quiet))
	w.line("  ", p.Paint("cq help <command> for one", style.Quiet))
	return strings.TrimRight(w.b.String(), "\n")
}

// unknown is the refusal for a command cq does not have: one line, with a guess
// when there is a good one, rather than the whole overview after it.
func unknown(command string) error {
	if near := guess.Nearest(command, Names()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `cq %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `cq help` lists them", command)}
}
