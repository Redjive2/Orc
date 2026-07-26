package cli_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/auth"
	"orc/cq/internal/cli"
	"orc/cq/internal/fault"
)

// result is the full observable outcome of one command.
type result struct {
	code   int
	stdout string
	stderr string
}

type harness struct {
	state string
	home  string
	env   map[string]string
	// served records what `serve` would have listened on, so the wiring can be
	// checked without opening a port.
	served  string
	handler http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{
		state: filepath.Join(root, "server"),
		home:  filepath.Join(root, "agent"),
	}
	h.env = map[string]string{"CQ_STATE": h.state, "CQ_HOME": h.home}
	return h
}

func (h *harness) run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errOut,
		Env: func(k string) (string, bool) {
			v, ok := h.env[k]
			return v, ok
		},
		Listen: func(addr string, handler http.Handler) error {
			h.served, h.handler = addr, handler
			return nil
		},
	}, args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (r result) mustSucceed(t *testing.T) result {
	t.Helper()
	if r.code != fault.ExitOK {
		t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	return r
}

func (r result) mustFail(t *testing.T, code int) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
	// A prompt may precede it — `admin operator` writes one to stderr so it
	// does not pollute stdout — so the check is that the tool named itself,
	// not that it did so first.
	if !strings.Contains(r.stderr, "cq: ") {
		t.Errorf("a failure should name the tool: %q", r.stderr)
	}
	return r
}

func TestUsageErrors(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"frobnicate"}},
		{"unknown flag", []string{"serve", "--nonsense"}},
		{"stray argument", []string{"status", "extra"}},
		{"admin with no subcommand", []string{"admin"}},
		{"unknown admin subcommand", []string{"admin", "nonsense"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := h.run(t, "", tc.args...).mustFail(t, fault.ExitUsage)
			// The refusal, and not the overview behind it. Fifty lines of first-run
			// steps after "unknown flag" is how a good message becomes unread.
			if strings.Contains(got.stderr, "what you need to do first") {
				t.Errorf("a usage error should not print the overview:\n%s", got.stderr)
			}
		})
	}
}

// TestBareCommandShowsTheCommands: `cq` with nothing after it is the one usage
// error that answers rather than only refuses. Nothing was named, so there is no
// refusal to read, and what the reader wants is the command list.
func TestBareCommandShowsTheCommands(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "").mustFail(t, fault.ExitUsage)

	if !strings.HasPrefix(got.stderr, "cq: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", got.stderr)
	}
	for _, want := range append(cli.Names(), "cq help") {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the short screen should list %q:\n%s", want, got.stderr)
		}
	}
	// And not the overview: the point of the short screen is that it is short.
	if strings.Contains(got.stderr, "what you need to do first") {
		t.Errorf("the short screen carried the overview with it:\n%s", got.stderr)
	}
}

// TestUnknownCommandGuesses: the answer to a typo is the command that was meant,
// not a list of every command there is.
func TestUnknownCommandGuesses(t *testing.T) {
	h := newHarness(t)

	got := h.run(t, "", "statsu").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "cq status") {
		t.Errorf("a near miss should be guessed:\n%s", got.stderr)
	}
	// And nothing resembling anything gets no guess, only the pointer.
	got = h.run(t, "", "frobnicate").mustFail(t, fault.ExitUsage)
	if strings.Contains(got.stderr, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "cq help") {
		t.Errorf("with no guess it should at least point at help:\n%s", got.stderr)
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t)
	for _, arg := range []string{"help", "-h", "--help"} {
		got := h.run(t, "", arg).mustSucceed(t)
		if !strings.Contains(got.stdout, "serve") {
			t.Errorf("%s should print the overview:\n%s", arg, got.stdout)
		}
	}
}

// TestSettingUpCredentials walks the two admin commands, which is the whole of
// what an operator does before the first `cq serve`.
func TestSettingUpCredentials(t *testing.T) {
	h := newHarness(t)

	// The password can be piped in, for a non-interactive setup.
	got := h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)
	if !strings.Contains(got.stdout, "password is set") {
		t.Errorf("stdout = %q", got.stdout)
	}

	// Or come from the environment.
	h.env["CQ_PASSWORD"] = "another good password"
	h.run(t, "", "admin", "operator").mustSucceed(t)
	delete(h.env, "CQ_PASSWORD")

	token := h.run(t, "", "admin", "token", "studio").mustSucceed(t)

	// Standard output is the token and nothing else, so that
	// `CQ_TOKEN=$(cq admin token studio)` gives a usable token rather than a
	// paragraph with a token somewhere in it.
	secret := strings.TrimSpace(token.stdout)
	if strings.ContainsAny(secret, " \n") {
		t.Errorf("stdout should be the bare token: %q", token.stdout)
	}

	// The advice is still given — on the other stream, where it belongs.
	if !strings.Contains(token.stderr, "shown once") {
		t.Errorf("the operator should be told the token is not stored: %q", token.stderr)
	}
	if !strings.Contains(token.stderr, "CQ_TOKEN=") {
		t.Errorf("the operator should be told what to do with it: %q", token.stderr)
	}

	// And it actually works against the store it was written to.
	creds, err := auth.Open(h.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.VerifyToken(secret); err != nil {
		t.Errorf("the minted token was rejected: %v", err)
	}
	if err := creds.VerifyPassword("another good password"); err != nil {
		t.Errorf("the password from the environment was not set: %v", err)
	}
}

// findToken picks the minted secret out of the surrounding guidance.
func findToken(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if id, rest, ok := strings.Cut(line, "."); ok && len(id) == 16 && len(rest) > 20 {
			return line
		}
	}
	t.Fatalf("no token in:\n%s", out)
	return ""
}

func TestAPasswordThatIsNotOneIsRefused(t *testing.T) {
	h := newHarness(t)
	h.run(t, "short\n", "admin", "operator").mustFail(t, fault.ExitUsage)
}

// TestServeRefusesToStartUnconfigured is the rule that matters most: a login
// gate with nothing behind it is not a gate.
func TestServeRefusesToStartUnconfigured(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "serve", "--addr", "127.0.0.1:0").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "operator") || !strings.Contains(got.stderr, "token") {
		t.Errorf("the refusal should name what is missing:\n%s", got.stderr)
	}
	if h.served != "" {
		t.Errorf("it listened anyway, on %s", h.served)
	}
}

func TestServeStartsOnceConfigured(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)
	h.run(t, "", "admin", "token").mustSucceed(t)

	got := h.run(t, "", "serve", "--addr", "127.0.0.1:9999").mustSucceed(t)
	if h.served != "127.0.0.1:9999" {
		t.Errorf("listened on %q", h.served)
	}
	if h.handler == nil {
		t.Fatalf("no handler was built")
	}
	if !strings.Contains(got.stdout, "macchiato") {
		t.Errorf("stdout should name the scheme in force: %q", got.stdout)
	}

	// The handler it built is the real one: a stranger gets the login page.
	req, err := http.NewRequestWithContext(t.Context(), "GET", "/api/v1/inbox", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := &recorder{header: http.Header{}}
	h.handler.ServeHTTP(w, req)
	if w.code != http.StatusUnauthorized {
		t.Errorf("the served handler answered %d to a stranger", w.code)
	}
}

type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.code = code }

func TestServeWarnsAboutMissingTLS(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)
	h.run(t, "", "admin", "token").mustSucceed(t)

	public := h.run(t, "", "serve", "--addr", ":8080").mustSucceed(t)
	if !strings.Contains(public.stdout, "no TLS") {
		t.Errorf("a public bind without TLS should say so:\n%s", public.stdout)
	}

	local := h.run(t, "", "serve", "--addr", "127.0.0.1:8080").mustSucceed(t)
	if strings.Contains(local.stdout, "no TLS") {
		t.Errorf("a loopback bind needs no warning:\n%s", local.stdout)
	}
}

func TestAnUnreadableThemeIsReportedRatherThanIgnored(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)
	h.run(t, "", "admin", "token").mustSucceed(t)

	h.env["ORC_THEME"] = "not-a-flavour"
	got := h.run(t, "", "serve", "--addr", "127.0.0.1:0").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "ORC_THEME") {
		t.Errorf("the refusal should name the setting:\n%s", got.stderr)
	}
}

func TestTheThemeFollowsTheSharedSetting(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)
	h.run(t, "", "admin", "token").mustSucceed(t)

	h.env["ORC_THEME"] = "latte"
	got := h.run(t, "", "serve", "--addr", "127.0.0.1:0").mustSucceed(t)
	if !strings.Contains(got.stdout, "latte") {
		t.Errorf("stdout should name the scheme: %q", got.stdout)
	}
}

func TestStatusWithoutASync(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "status").mustSucceed(t)
	for _, want := range []string{"last sync", "never", "waiting"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("status should mention %q: %q", want, got.stdout)
		}
	}

	// And, before any of that, which settings are missing — because a mirror
	// that has never synced is almost always one that was never told where to.
	for _, want := range []string{"CQ_SERVER", "CQ_TOKEN", "not set"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("status should name the missing settings: %q", got.stdout)
		}
	}
}

func TestSyncNeedsItsSettings(t *testing.T) {
	h := newHarness(t)

	got := h.run(t, "", "sync").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "server") {
		t.Errorf("the refusal should name the missing setting:\n%s", got.stderr)
	}

	h.env["CQ_SERVER"] = "http://127.0.0.1:9"
	got = h.run(t, "", "sync").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "token") {
		t.Errorf("the refusal should name the missing token:\n%s", got.stderr)
	}

	h.env["CQ_TOKEN"] = "t"
	h.env["CQ_SERVER"] = "127.0.0.1:9"
	got = h.run(t, "", "sync").mustFail(t, fault.ExitUsage)
	if !strings.Contains(got.stderr, "http://") {
		t.Errorf("a schemeless server should be refused clearly:\n%s", got.stderr)
	}
}

func TestSyncReportsAnUnreachableServer(t *testing.T) {
	h := newHarness(t)
	h.env["CQ_SERVER"] = "http://127.0.0.1:1"
	h.env["CQ_TOKEN"] = "t"
	h.env["CQ_USER"] = "redjive"
	h.env["CQ_MACHINE"] = "studio"

	// The source runs `mailman`, which is not installed here, so the failure is
	// the collection step rather than the network — either way it must not
	// exit zero, and it must not be an internal fault.
	got := h.run(t, "", "sync")
	if got.code == fault.ExitOK {
		t.Errorf("a sync with nothing behind it exited zero")
	}
	if got.code == fault.ExitInternal {
		t.Errorf("a missing tool should not read as a bug in cq:\n%s", got.stderr)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	h := newHarness(t)
	h.env["CQ_USER"] = "redjive"
	h.env["CQ_MACHINE"] = "studio"

	// With no mailman installed the collection fails, which is the point: a
	// dry run reports rather than pretending.
	got := h.run(t, "", "sync", "--dry-run")
	if got.code == fault.ExitOK {
		t.Errorf("a dry run with no tools exited zero: %q", got.stdout)
	}
	if strings.Contains(got.stdout, "sent") {
		t.Errorf("a dry run should not claim to have sent anything: %q", got.stdout)
	}
}

func TestMainSurvivesMissingStreams(t *testing.T) {
	if got := cli.Main(cli.App{}, []string{"help"}); got != fault.ExitInternal {
		t.Errorf("exit %d, want %d", got, fault.ExitInternal)
	}
}

func TestTheEnvironmentIsReadWhenNoFlagIsGiven(t *testing.T) {
	h := newHarness(t)
	h.run(t, "correct horse battery\n", "admin", "operator").mustSucceed(t)

	// The store the password landed in is the one CQ_STATE named.
	if _, err := os.Stat(filepath.Join(h.state, "operator.json")); err != nil {
		t.Errorf("CQ_STATE was not honoured: %v", err)
	}
}
