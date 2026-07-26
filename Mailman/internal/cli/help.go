package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/guess"

	"orc/mailman/internal/query"
	"orc/mailman/internal/style"
	"orc/theme"
)

// usage is the help text.
//
// It is painted rather than printed flat, for the reason every other tool in this
// tree paints its prose: `mailman help` is the screen a new agent reads first, and
// one whose tables are coloured and whose sentences are not looks half-finished. The
// roles are the shared ones, so a command name looks the same here as it does in
// `muff help` and `orc help`.
//
// Colour is a layer and never information: every line below reads the same stripped
// of its escapes, which is what lets `mailman help | grep` lose nothing and what the
// test asserts byte for byte.
func usage(p style.Palette) string {
	var b strings.Builder

	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	// Padding is computed on the *plain* form and applied after the paint, never by
	// handing a painted string to %-42s. Escape sequences have no display width but
	// do have length, so padding a painted string lays the screen out one way with
	// colour and another way without.
	column := func(form, does string) {
		gap := 44 - theme.Width(form)
		if gap < 1 {
			gap = 1
		}
		fmt.Fprintf(&b, "  %s%s%s\n", p.Command(form), strings.Repeat(" ", gap), does)
	}
	setting := func(name, does string) {
		gap := 12 - theme.Width(name)
		if gap < 1 {
			gap = 1
		}
		fmt.Fprintf(&b, "  %s%s%s\n", p.Setting(name), strings.Repeat(" ", gap), p.Muted(does))
	}

	line("%s — inter-agent mail", p.Tool("mailman"))
	line("")

	line("%s", p.Header("reading"))
	column("mailman inbox   [--all|--sent]", "unread mail, everything, or what you sent")
	column("mailman open    <query>", "the most recent match, in full")
	column("mailman convo   <conversation> [--all]", "one conversation, as a thread")
	column("mailman archive [query]", "archive matches, or show the archive")
	column("mailman check   <query>", "who has and has not read")
	line("")

	line("%s", p.Header("writing"))
	column("mailman send    <subject> <to...> <body>", `send to every recipient ("-" reads stdin)`)
	column("mailman reply   <query> <subject> <body>", "reply within a conversation")
	column("mailman cc      <query> <user>", "add a user to a conversation")
	column("mailman read    <query>", "mark matches read, visibly to all")
	line("")

	line("%s", p.Header("keeping it"))
	column("mailman prune   <query> --yes", "delete archived matches, permanently")
	column("mailman verify", "check the store for damage")
	column("mailman admin   user add|remove|list", "provisioning; orc drives this")
	column("mailman admin   owner|mail", "the whole store, for the owner alone")
	line("")

	line("%s", p.Header("queries select mail by field"))
	line("  %s", p.Value(`mailman open 'from="boss"'`))
	line("  %s", p.Value(`mailman open 'from="boss" & subject="RE: work"'`))
	line("  %s", p.Value(`mailman open 'id="0"'`))
	line("")
	line("  fields: %s", p.Muted(strings.Join(query.FieldNames(), ", ")))
	line("  joined by %s and %s, grouped with %s, negated with %s",
		p.Value("&"), p.Value("|"), p.Value("()"), p.Value("!"))
	line("  operators: %s exact · %s absent · %s contains",
		p.Value("="), p.Value("!="), p.Value("~"))
	line("")

	line("%s", p.Header("identity comes from orc, and is checked on every command"))
	setting("$ORC_USER", "the mailbox to act as")
	setting("$ORC_KEY", "the key that proves it")
	line("")

	line("%s   %s", p.Header("flags"),
		strings.Join([]string{
			p.Flag("--all"), p.Flag("--sent"), p.Flag("--yes"),
			p.Flag("--json"), p.Flag("--no-color"), p.Flag("--width <n>"),
		}, "  "))
	line("")

	// theme.Help() is the shared paragraph about ORC_THEME, NO_COLOR, and ORC_AGENT.
	// It is shared so that four tools cannot describe the same three settings four
	// slightly different ways.
	line("%s", theme.Help())
	line("")

	line("%s  0 ok · 1 usage · 2 not found · 3 ambiguous · 4 parse · 5 i/o", p.Header("exit"))
	line("       6 conflict · 7 auth · 8 denied · 11 escape · 70 internal")

	return strings.TrimRight(b.String(), "\n")
}

// topic is what `mailman help <command>` shows: the forms, the one-liner, what the
// command is *for*, and examples worth pasting.
//
// The forms are repeated from usage() rather than shared with it, because usage is
// laid out by hand in groups and threading a table through it would cost more than
// it saved. What keeps the two from drifting is a test: every verb has a topic,
// every topic's form appears in usage, and neither list may grow without the other.
type topic struct {
	forms    []string
	does     string
	detail   string
	examples []string
}

var topics = map[string]topic{
	"inbox": {
		forms: []string{"mailman inbox [--all|--sent]"},
		does:  "unread mail, everything, or what you sent",
		detail: "Unread mail by default, because that is the question being asked almost\n" +
			"every time. --all adds what has been read, --sent shows your own\n" +
			"outgoing mail, and --json prints the stable shape Communiqué mirrors\n" +
			"through.\n\n" +
			"Archived mail is not here; `mailman archive` shows that.",
		examples: []string{"mailman inbox", "mailman inbox --all", "mailman inbox --sent"},
	},
	"open": {
		forms: []string{"mailman open <query>"},
		does:  "the most recent match, in full",
		detail: "The one query command that takes the *most recent* match rather than\n" +
			"acting on the whole set — it is for reading, and reading twenty messages\n" +
			"at once is not reading.\n\n" +
			"Opening a message marks it read, visibly to the sender: `mailman check`\n" +
			"is how they see that.",
		examples: []string{`mailman open 'from="ember"'`, `mailman open 'id="4"'`},
	},
	"convo": {
		forms: []string{"mailman convo <conversation> [--all]"},
		does:  "one conversation, as a thread",
		detail: "Every message in one conversation, oldest first. A conversation is made\n" +
			"by the first reply, and it is the unit `cc` widens — an agent added to a\n" +
			"conversation gets its history, not just what comes next.",
		examples: []string{"mailman convo 4fa73fa3", "mailman convo 4fa73fa3 --all"},
	},
	"send": {
		forms: []string{"mailman send <subject> <to...> <body>"},
		does:  `send to every recipient ("-" reads stdin)`,
		detail: "Every name before the body is a recipient. A body of \"-\" reads standard\n" +
			"input, which is how anything longer than a line should be sent: argv is\n" +
			"size-limited and visible in `ps` to everyone on the machine.\n\n" +
			"A message to somebody with no mailbox is refused rather than queued.",
		examples: []string{
			`mailman send "the parser is fixed" ember "it passes now"`,
			`mailman send "long note" ember - < note.md`,
		},
	},
	"reply": {
		forms: []string{"mailman reply <query> <subject> <body>"},
		does:  "reply within a conversation",
		detail: "Replies to the matched message, addressed to everyone in its\n" +
			"conversation — including anyone `cc` added, which is the point of\n" +
			"membership being stored rather than copied from the parent.\n\n" +
			"The first reply is what turns two messages into a conversation.",
		examples: []string{`mailman reply 'subject~"parser"' "re: the parser" "confirmed"`},
	},
	"cc": {
		forms: []string{"mailman cc <query> <user>"},
		does:  "add a user to a conversation",
		detail: "Adds somebody to a conversation and gives them its history. It needs a\n" +
			"conversation to add them to, so a message nobody has replied to yet is\n" +
			"refused with that reason.",
		examples: []string{`mailman cc 'subject~"parser"' dock`},
	},
	"read": {
		forms: []string{"mailman read <query>"},
		does:  "mark matches read, visibly to all",
		detail: "Marks everything matching as read without printing it — for clearing a\n" +
			"batch you have dealt with elsewhere. It is visible to the sender, so it\n" +
			"is a claim about having read them.",
		examples: []string{`mailman read 'from="orcprobe"'`, "mailman read 'unread=\"true\"'"},
	},
	"archive": {
		forms: []string{"mailman archive [query]"},
		does:  "archive matches, or show the archive",
		detail: "With a query it files everything matching; with no query it shows what\n" +
			"has been filed. Archiving is per-mailbox and does not touch anybody\n" +
			"else's copy of the same message.",
		examples: []string{`mailman archive 'from="orcprobe"'`, "mailman archive"},
	},
	"prune": {
		forms: []string{"mailman prune <query> --yes"},
		does:  "delete archived matches, permanently",
		detail: "The only irreversible command, and it only ever touches the archive:\n" +
			"deleting from an inbox would be deleting mail somebody has not read.\n\n" +
			"--yes is required when stdin is not a terminal, which for an agent is\n" +
			"always. A conversation still naming a pruned message is expected, not\n" +
			"damage, and `verify` says so.",
		examples: []string{`mailman prune 'before="2026-01-01"' --yes`},
	},
	"check": {
		forms: []string{"mailman check <query>"},
		does:  "who has and has not read",
		detail: "Read receipts for mail you sent: who has opened it and who has not. It\n" +
			"is the answer to \"did they see it\" that does not require asking them.",
		examples: []string{`mailman check 'id="4"'`, `mailman check 'subject~"parser"'`},
	},
	"verify": {
		forms: []string{"mailman verify"},
		does:  "check the store for damage",
		detail: "Walks every mailbox and conversation and reports what is wrong without\n" +
			"changing anything. It never repairs — an automatic repair of damage\n" +
			"nobody has understood is how one bad file becomes many.\n\n" +
			"Exit 6 when it finds something, so a script can branch on it.",
		examples: []string{"mailman verify"},
	},
	"admin": {
		forms: []string{"mailman admin user add|remove|list", "mailman admin owner [<name>]", "mailman admin mail [--json]"},
		does:  "provisioning; orc drives this",
		detail: "Provisioning does not authenticate: an empty store has no identity to\n" +
			"check against, and bootstrapping has to be possible. The store's 0700\n" +
			"permissions are the boundary.\n\n" +
			"Reading the store whole is a different act and does authenticate, as the\n" +
			"owner and only the owner. Orc normally drives all of this — it mints one\n" +
			"key per identity and provisions the mailbox with it.",
		examples: []string{"mailman admin user add ember", "mailman admin owner", "mailman admin mail --json"},
	},
}

// commandHelp is `mailman help <command>`: that command and nothing else.
//
// Nothing else is the point. A reader who asked about one verb has already seen the
// list, and reprinting it with the query language and the settings after it buries
// the three lines they came for.
func commandHelp(p style.Palette, name string) (string, bool) {
	got, ok := topics[name]
	if !ok {
		return "", false
	}

	var b strings.Builder
	for _, form := range got.forms {
		b.WriteString(p.Command(form) + "\n")
	}
	b.WriteString("\n  " + p.Muted(got.does) + "\n")

	if got.detail != "" {
		b.WriteString("\n")
		for _, line := range strings.Split(got.detail, "\n") {
			if line == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("  " + line + "\n")
		}
	}
	if len(got.examples) > 0 {
		b.WriteString("\n" + p.Header("examples") + "\n")
		for _, example := range got.examples {
			b.WriteString("  " + p.Value(example) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// noSuchTopic is `mailman help <something that is not a command>`.
func noSuchTopic(name string) error {
	if near := guess.Nearest(name, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("no help for %q — did you mean `mailman help %s`?", name, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("no help for %q; `mailman help` lists the commands", name)}
}

// help answers `mailman help`, and `mailman help <command>` for one of them.
func (a App) help(args []string) error {
	switch len(args) {
	case 0:
		return a.say(usage(a.out))
	case 1:
		got, ok := commandHelp(a.out, args[0])
		if !ok {
			return noSuchTopic(args[0])
		}
		return a.say(got)
	default:
		return fault.Usage{Reason: "help takes one command, or nothing"}
	}
}

// brief is what `mailman` on its own prints: the verbs, grouped, and nothing else.
//
// The full screen carries the query language and the settings, which is what a
// reader wants once — and is in the way every other time. `mailman` alone is
// almost always somebody checking what the verb was called.
func brief(p style.Palette) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	group := func(name string, verbs ...string) {
		painted := make([]string, len(verbs))
		for i, v := range verbs {
			painted[i] = p.Command(v)
		}
		// Padding on the plain name, appended after the paint: the rule the rest of
		// this file follows, for the same reason.
		gap := 13 - theme.Width(name)
		if gap < 1 {
			gap = 1
		}
		line("  %s%s%s", p.Header(name), strings.Repeat(" ", gap), strings.Join(painted, p.Muted(" · ")))
	}

	line("%s — inter-agent mail", p.Tool("mailman"))
	line("")
	group("reading", "inbox", "open", "convo", "archive", "check")
	group("writing", "send", "reply", "cc", "read")
	group("keeping it", "prune", "verify", "admin")
	line("")
	line("  %s", p.Muted("mailman help for all of it: every form, the query language, the settings"))
	return strings.TrimRight(b.String(), "\n")
}

// verbs is every command mailman answers to, for suggesting the one that was meant.
func verbs() []string {
	return []string{"inbox", "open", "convo", "archive", "check", "send", "reply",
		"cc", "read", "prune", "verify", "admin", "help"}
}

// unknown is the refusal for a verb mailman does not have: one line, with a guess
// when there is a good one, rather than the whole screen after it.
func unknown(command string) error {
	if near := guess.Nearest(command, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `mailman %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `mailman help` lists them", command)}
}
