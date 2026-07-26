package cli_test

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/auth"
	"orc/cq/internal/cli"
	"orc/cq/internal/fault"
	"orc/cq/internal/style"
)

// TestTheOverviewAnswersBothQuestions: someone reading `cq help` wants to know
// what they can do and what they must do first, and the second is the one a
// bare command list never answers.
func TestTheOverviewAnswersBothQuestions(t *testing.T) {
	got := cli.Overview(style.Plain())

	for _, want := range []string{
		"what you need to do first",
		"cq admin operator",
		"cq admin token",
		"cq serve",
		"cq sync",
		"commands",
		"environment",
		"exit codes",
		"cq help <command>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the overview should mention %q", want)
		}
	}

	// The setup steps come before the command list: what to do first is the
	// question, and an answer buried under a reference table is not one.
	if strings.Index(got, "what you need to do first") > strings.Index(got, "environment") {
		t.Errorf("the first-run steps should come before the reference material")
	}

	// It says which side each command runs on, because the two machines are
	// the thing about cq that is easiest to get wrong.
	if !strings.Contains(got, "server") || !strings.Contains(got, "agent") {
		t.Errorf("the overview should say which machine each command runs on")
	}
}

// TestEveryCommandIsDocumented guards the table: a command with no entry is a
// command nobody can discover.
func TestEveryCommandIsDocumented(t *testing.T) {
	overview := cli.Overview(style.Plain())
	for _, name := range cli.Names() {
		if !strings.Contains(overview, name) {
			t.Errorf("%q is not in the overview", name)
		}
		detail, ok := cli.Detail(style.Plain(), name)
		if !ok {
			t.Fatalf("%q has no detail page", name)
		}
		if !strings.Contains(detail, "cq "+name) {
			t.Errorf("%q's detail should open with its usage line:\n%s", name, detail)
		}
	}
}

// TestEveryFlagIsDocumented compares the help against the flags the commands
// actually parse, so a flag added without a line here is caught.
func TestEveryFlagIsDocumented(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		command string
		flags   []string
	}{
		{"serve", []string{"--addr", "--state", "--tls-cert", "--tls-key", "--no-admin", "--admin-metadata-only"}},
		{"sync", []string{"--server", "--machine", "--user", "--home", "--watch", "--nudge", "--dry-run", "--admin", "--admin-bodies", "--library"}},
		{"status", []string{"--home"}},
		{"queue", []string{"--state", "--json"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			detail, ok := cli.Detail(style.Plain(), tc.command)
			if !ok {
				t.Fatalf("no detail page")
			}
			for _, flag := range tc.flags {
				if !strings.Contains(detail, flag) {
					t.Errorf("%s is not documented", flag)
				}
				// And the flag really is accepted, so the help is not fiction.
				got := h.run(t, "", tc.command, flag+"=x")
				if got.code == fault.ExitUsage && strings.Contains(got.stderr, "not defined") {
					t.Errorf("%s is documented but not accepted", flag)
				}
			}
		})
	}
}

func TestDetailForAnUnknownCommand(t *testing.T) {
	if _, ok := cli.Detail(style.Plain(), "frobnicate"); ok {
		t.Errorf("an invented command should have no detail page")
	}
}

// TestAdminSubcommandsAreReachableByShortName: `cq help token` does what a hand
// reaching for it expects.
func TestAdminSubcommandsAreReachableByShortName(t *testing.T) {
	for _, name := range []string{"admin token", "token", "admin operator", "operator"} {
		if _, ok := cli.Detail(style.Plain(), name); !ok {
			t.Errorf("`cq help %s` finds nothing", name)
		}
	}
}

func TestHelpForOneCommand(t *testing.T) {
	h := newHarness(t)

	got := h.run(t, "", "help", "sync").mustSucceed(t)
	for _, want := range []string{"cq sync", "flags", "--watch", "examples", "agent machine"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("`cq help sync` should mention %q:\n%s", want, got.stdout)
		}
	}

	// The flag form means the same thing.
	viaFlag := h.run(t, "", "sync", "--help").mustSucceed(t)
	if viaFlag.stdout != got.stdout {
		t.Errorf("`cq sync --help` should match `cq help sync`")
	}
}

func TestHelpForAnUnknownCommandSuggests(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "help", "frobnicate").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "serve") || !strings.Contains(got.stderr, "sync") {
		t.Errorf("the refusal should list what does exist:\n%s", got.stderr)
	}
}

func TestAnUnknownCommandSuggests(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "frobnicate").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "cq help") {
		t.Errorf("the refusal should point somewhere:\n%s", got.stderr)
	}
}

// TestColourNeverChangesLayout is the property that makes colour safe: escape
// sequences occupy no columns, so stripping them from coloured help must give
// back the plain help byte for byte.
func TestColourNeverChangesLayout(t *testing.T) {
	pages := map[string]func(style.Palette) string{
		"overview": cli.Overview,
		"brief":    cli.Brief,
	}
	for _, name := range cli.Names() {
		pages[name] = func(p style.Palette) string {
			out, _ := cli.Detail(p, name)
			return out
		}
	}

	for name, render := range pages {
		t.Run(name, func(t *testing.T) {
			plain := render(style.Plain())
			coloured := render(style.Coloured())
			if coloured == plain {
				t.Fatalf("colour was requested but nothing was painted")
			}
			if got := strip(coloured); got != plain {
				t.Errorf("stripped colour differs from plain:\n got %q\nwant %q", got, plain)
			}
		})
	}
}

// TestHelpIsPlainWhenColourIsNotWanted: a piped `cq help` carries no escape
// sequences, which is what makes it greppable.
func TestHelpIsPlainWhenColourIsNotWanted(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "help").mustSucceed(t)
	if strings.Contains(got.stdout, "\x1b[") {
		t.Errorf("help carried escape sequences to a plain stream")
	}
}

// strip removes SGR escape sequences.
func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestStatusNamesMissingSettings: "it does not work" and "$CQ_TOKEN is not
// set" are different messages, and only one of them is useful.
func TestStatusNamesMissingSettings(t *testing.T) {
	h := newHarness(t)

	bare := h.run(t, "", "status").mustSucceed(t)
	if !strings.Contains(bare.stdout, "sync needs this") {
		t.Errorf("status should say which settings sync needs:\n%s", bare.stdout)
	}

	h.env["CQ_SERVER"] = "https://cq.example"
	h.env["CQ_USER"] = "redjive"
	h.env["CQ_TOKEN"] = "d26e4d3db5281234.averylongsecretvaluegoeshere"
	set := h.run(t, "", "status").mustSucceed(t)
	if !strings.Contains(set.stdout, "https://cq.example") {
		t.Errorf("status should show what is set:\n%s", set.stdout)
	}
	if strings.Contains(set.stdout, "CQ_SERVER   not set") {
		t.Errorf("a set value should not read as missing:\n%s", set.stdout)
	}
}

// TestStatusRedactsTheToken: enough to recognise which token is in force, and
// not enough to use it — `cq status` is the command someone pastes into a chat
// when asking why the mirror is not running.
func TestStatusRedactsTheToken(t *testing.T) {
	h := newHarness(t)
	const secret = "averylongsecretvaluegoeshereandshouldnotappear"
	h.env["CQ_TOKEN"] = "d26e4d3db5281234." + secret

	got := h.run(t, "", "status").mustSucceed(t)
	if strings.Contains(got.stdout, secret) {
		t.Errorf("the token's secret half is on screen:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "d26e4d3db5281234") {
		t.Errorf("the token's id should show, so it can be told from another:\n%s", got.stdout)
	}
}

func TestStatusRedactsATokenWithNoIdHalf(t *testing.T) {
	h := newHarness(t)
	h.env["CQ_TOKEN"] = "averylongopaquetokenwithnodot"

	got := h.run(t, "", "status").mustSucceed(t)
	if strings.Contains(got.stdout, "opaquetokenwithnodot") {
		t.Errorf("an unshaped token should still be redacted:\n%s", got.stdout)
	}

	h.env["CQ_TOKEN"] = "short"
	brief := h.run(t, "", "status").mustSucceed(t)
	if strings.Contains(brief.stdout, "short") {
		t.Errorf("a short token should be redacted entirely:\n%s", brief.stdout)
	}
}

// TestServeSaysWhatItIsServing, so the operator can see the admin panel is on
// and that TLS is not, without reading the flags back.
func TestServeSaysWhatItIsServing(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)
	h.run(t, "", "admin", "token").mustSucceed(t)

	got := h.run(t, "", "serve", "--addr", ":8080").mustSucceed(t)
	for _, want := range []string{"admin panel is served", "--no-admin", "no TLS"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("serve should mention %q:\n%s", want, got.stdout)
		}
	}

	off := h.run(t, "", "serve", "--addr", "127.0.0.1:8080", "--no-admin").mustSucceed(t)
	if strings.Contains(off.stdout, "admin panel is served") {
		t.Errorf("with the panel off, serve should not say it is on:\n%s", off.stdout)
	}
}

// TestThePasswordPromptEndsAtTheNewline is the regression for a prompt that
// appeared to hang.
//
// A terminal has no end of input until the operator presses Ctrl-D, so reading
// to EOF means pressing Enter does nothing visible. This gives the reader a
// line and then *nothing* — never closing it, exactly as a terminal behaves —
// and requires the command to finish anyway.
func TestThePasswordPromptEndsAtTheNewline(t *testing.T) {
	h := newHarness(t)

	stdin, writer := io.Pipe()
	go func() {
		_, _ = io.WriteString(writer, "correct horse battery\n")
		// Deliberately never closed: a terminal does not close either.
	}()

	done := make(chan result, 1)
	go func() {
		var out, errOut bytes.Buffer
		code := cli.Main(cli.App{
			Stdin: stdin, Stdout: &out, Stderr: &errOut,
			Env: func(k string) (string, bool) {
				v, ok := h.env[k]
				return v, ok
			},
		}, []string{"admin", "operator"})
		done <- result{code: code, stdout: out.String(), stderr: errOut.String()}
	}()

	select {
	case got := <-done:
		if got.code != fault.ExitOK {
			t.Fatalf("exit %d: %s", got.code, got.stderr)
		}
		if !strings.Contains(got.stdout, "password is set") {
			t.Errorf("stdout = %q", got.stdout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt never returned after a newline — it is waiting for end of input")
	}

	// And the password that landed is the line, without its newline.
	creds, err := auth.Open(h.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.VerifyPassword("correct horse battery"); err != nil {
		t.Errorf("the password was not stored as typed: %v", err)
	}
}

// TestAnEmptyPasswordLineIsRefused: pressing Enter at the prompt should say so
// rather than set an empty password or wait for more.
func TestAnEmptyPasswordLineIsRefused(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "\n", "admin", "operator").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "no password given") {
		t.Errorf("stderr = %q", got.stderr)
	}
}

// TestAPipedPasswordNeedsNoNewline, which is what `printf` gives.
func TestAPipedPasswordNeedsNoNewline(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery", "admin", "operator").mustSucceed(t)

	creds, err := auth.Open(h.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.VerifyPassword("correct horse battery"); err != nil {
		t.Errorf("a piped password was not stored: %v", err)
	}
}

// TestTheReferenceDocMentionsNoFlagThatDoesNotExist catches the drift that
// actually happened: `--retry <id>` sat in the flags table of
// Docs/Communique/Reference.md for weeks without ever being implemented. A
// document that promises a flag is worse than one that omits it — the reader
// tries it and concludes the tool is broken.
//
// The doc is outside the module, so a checkout without it skips rather than
// fails: this guards the pair when both are present and never blocks a build.
func TestTheReferenceDocMentionsNoFlagThatDoesNotExist(t *testing.T) {
	const doc = "../../../Docs/Communique/Reference.md"
	text, err := os.ReadFile(doc)
	if err != nil {
		t.Skipf("the reference is not in this checkout: %v", err)
	}

	// Only the Flags table, which is where cq declares its own flags — and where
	// the phantom lived. The rest of the document cites Mailman's flags to say
	// what cq mirrors (`inbox --all`, `inbox --sent`), and those are somebody
	// else's contract to keep.
	table, ok := flagsTable(string(text))
	if !ok {
		t.Skip("the reference has no flags table in the shape this test reads")
	}

	// Every flag-shaped token in it. Deliberately not anchored to
	// backticks-around-the-flag: the phantom was written `--retry <id>`, with the
	// placeholder inside the quoting, so a pattern that demanded a closing
	// backtick after the name walked straight past the one case this exists for.
	named := regexp.MustCompile(`--[a-z][a-z-]*`).FindAllString(table, -1)
	if len(named) == 0 {
		t.Fatal("no flags found in the reference; the pattern has gone stale")
	}

	real := map[string]bool{}
	for _, name := range cli.Names() {
		detail, ok := cli.Detail(style.Plain(), name)
		if !ok {
			continue
		}
		for _, m := range regexp.MustCompile(`--[a-z-]+`).FindAllString(detail, -1) {
			real[m] = true
		}
	}

	for _, flag := range named {
		if !real[flag] {
			t.Errorf("the reference's flags table documents %s, which no command accepts", flag)
		}
	}
}

// flagsTable returns the section of the reference that lists cq's own flags.
func flagsTable(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "#") || !strings.Contains(line, "Flags") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "#") {
				return strings.Join(lines[i:j], "\n"), true
			}
		}
		return strings.Join(lines[i:], "\n"), true
	}
	return "", false
}
