package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"

	"orc/cq/internal/upgrade"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/source"
	"orc/cq/internal/style"
)

// `cq upgrade` — rebuild and restart every Orc tool, everywhere.
//
// It asks the server, which is the only party that can reach both halves: the
// server upgrades itself, and every agent machine gets a queued action it applies
// on its next sync. See internal/server/upgrade.go for why that is the shape.
//
// The gate is Orc's, and it is checked *here* rather than at the server. That
// looks backwards — a check on the client is a check the client can skip — and it
// is the right place anyway, for a reason worth stating: the server has no Orc
// fleet. It runs on a different machine, authenticates with a password and a
// token, and has never heard of an identity. Asking it to enforce an Orc
// permission would mean teaching it the model, which is exactly the second copy of
// authority this tree exists to avoid.
//
// So the real boundary is the one that was already there: to reach the server at
// all you need `$CQ_TOKEN` or a login. This adds Orc's answer on top, so that an
// *agent* with a shell — which is the thing an operator is actually worried about
// — cannot rebuild the fleet's binaries without being senior enough to hold the
// permission. It is a floor of 90 in a fleet whose agents sit at 1 to 99.
const UpgradePermission = "upgrade"

func (a App) upgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serverURL := fs.String("server", a.look("CQ_SERVER", ""), "the cq server to ask")
	token := fs.String("token", a.look("CQ_TOKEN", ""), "the sync token")
	machines := fs.String("machines", "", "only these agent machines, comma separated")
	noServer := fs.Bool("no-server", false, "upgrade the agent machines but leave the site up")
	yes := fs.Bool("yes", false, "required: this restarts the site and every agent")
	dirty := fs.Bool("dirty", false, "build even though the checkout has uncommitted changes")
	if err := parse(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fault.Usage{Reason: fmt.Sprintf("upgrade takes no arguments, got %q", rest[0])}
	}

	if *serverURL == "" {
		return fault.Usage{Reason: "no server address; set --server or $CQ_SERVER"}
	}
	if *token == "" {
		return fault.Usage{Reason: "no sync token; set $CQ_TOKEN"}
	}
	if !*yes {
		// Said in full rather than as "are you sure": somebody who has read what
		// this does has been told something, and somebody who has read "confirm?"
		// has not.
		return fault.Usage{Reason: "upgrade pulls the tree, rebuilds every tool, and restarts " +
			"the site and every agent machine.\n  pass --yes to go ahead"}
	}

	if err := a.mayUpgrade(context.Background()); err != nil {
		return err
	}

	body := map[string]any{}
	if *noServer {
		body["server"] = false
	}
	if list := splitList(*machines); len(list) > 0 {
		body["machines"] = list
	}

	view, err := a.askUpgrade(context.Background(), *serverURL, *token, body)
	if err != nil {
		// The server could not be reached at all, after riding out the window it
		// makes for itself. This is where the command used to stop — and it is the
		// worst place to stop, because a server that will not come up is one of the
		// reasons somebody rebuilds in the first place.
		//
		// So this machine rebuilds itself, if it has a checkout to build from, and
		// says plainly what did not happen. A local build is what `cq upgrade` would
		// have asked the server to do here anyway; what is lost is the fleet, and
		// losing it silently is the part worth refusing.
		if fault.Classify(err) != fault.CodeUnavailable {
			return err
		}
		return a.upgradeHere(context.Background(), err, *dirty)
	}
	if err := a.sayUpgrade(view); err != nil {
		return err
	}
	if !view.Restarting {
		return nil
	}
	return a.watchBuild(context.Background(), *serverURL, *token)
}

// watchBuild follows the server's own rebuild to its end.
//
// Without this the command said "upgrading" and returned, and a build that failed
// four minutes later was in a log nobody was reading. The one moment somebody is
// watching for the result is the moment they ran the command.
//
// The server goes away in the middle of this, on purpose — that is the restart —
// so a gap is not a failure here either. What ends the wait is the server saying
// the build failed, or the server coming back up on a build that is no longer the
// one that was running.
func (a App) watchBuild(ctx context.Context, serverURL, token string) error {
	deadline := time.Now().Add(upgrade.Timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(BuildPoll):
		}

		got, err := a.askBuilt(ctx, serverURL, token)
		if err != nil {
			// The window it makes for itself. Nothing to report and nothing wrong.
			continue
		}
		switch got.State {
		case "failed":
			return fault.IO{Op: "build", Subject: serverURL,
				Err: fmt.Errorf("the server's own rebuild failed: %s", got.Error)}
		case "restarting", "":
			return a.say("  %s", a.ink("the server rebuilt and is restarting", style.Good))
		}
	}
	return a.say("  %s", a.ink(
		"the build is still running; `cq upgrade` stopped watching, it did not stop", style.Quiet))
}

// BuildPoll is how often the server is asked how its rebuild went. The build takes
// tens of seconds at least, so this is not a busy-wait.
const BuildPoll = 2 * time.Second

// builtView is the server's answer about its own last build. Written out rather
// than imported, for the reason upgradeView is.
type builtView struct {
	State string `json:"state"`
	Error string `json:"error"`
}

func (a App) askBuilt(ctx context.Context, serverURL, token string) (builtView, error) {
	url := strings.TrimSuffix(serverURL, "/") + "/api/v1/upgrade"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return builtView{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: ReachTimeout}).Do(req)
	if err != nil {
		return builtView{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return builtView{}, fmt.Errorf("the server answered %s", resp.Status)
	}

	var body struct {
		Last *builtView `json:"last"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return builtView{}, err
	}
	if body.Last == nil {
		return builtView{}, fmt.Errorf("the server has not started a build")
	}
	return *body.Last, nil
}

// upgradeHere rebuilds this machine when the server cannot be told to.
//
// It runs the same `upgrade.Options.Upgrade` the server runs, from the same
// settings, so there is one definition of what a rebuild is. The failure that
// brought it here is repeated at the end rather than swallowed: the fleet is the
// larger half of the job and it did not happen.
func (a App) upgradeHere(ctx context.Context, why error, dirty bool) error {
	source := a.look("CQ_SOURCE", "")
	if strings.TrimSpace(source) == "" {
		return fault.Unavailable{Peer: "the server", Err: fmt.Errorf(
			"%w; this machine has no checkout to rebuild from either, so nothing was done "+
				"(set $CQ_SOURCE to rebuild here when the server is away)", why)}
	}

	if err := a.say("%s %s", a.ink("rebuilding here instead", style.Warn),
		a.ink("from "+source, style.Quiet)); err != nil {
		return err
	}

	report, err := upgrade.Options{
		Source: source,
		Target: a.look("CQ_BIN", ""),
		Dirty:  dirty,
	}.Upgrade(ctx)
	if err != nil {
		return err
	}
	if err := a.sayReport(report); err != nil {
		return err
	}

	// Said last, because it is the part somebody has to act on. A machine that
	// rebuilt itself and left the fleet on the old build is a fleet that disagrees
	// with itself, and the only way anybody learns that is by being told.
	return a.say("  %s", a.ink(
		"the server was not reached, so no agent machine was queued — "+
			"run `cq upgrade --yes` again when it is back", style.Warn))
}

// sayReport draws what a local rebuild came to.
func (a App) sayReport(r upgrade.Report) error {
	what := "no change"
	if r.Changed {
		what = r.Before + " → " + r.After
	}
	if err := a.say("  %s %s", a.ink(what, style.Value),
		a.ink(strings.Join(r.Built, " "), style.Quiet)); err != nil {
		return err
	}
	return nil
}

// mayUpgrade asks Orc whether this identity holds the permission.
//
// Orc answers with an exit code, the way `muff assign` asks about control. cq does
// not read the fleet, does not know what a floor is, and holds no copy of the
// answer — it asks, and repeats what it was told.
//
// A machine with no Orc at all is refused rather than waved through. That is the
// conservative reading and the right one here: this rebuilds every binary on every
// machine, and "there was nobody to ask" is not permission.
func (a App) mayUpgrade(ctx context.Context) error {
	orc := a.orc()
	if err := orc.MayUse(ctx, UpgradePermission); err != nil {
		// Unauthenticated rather than a refusal of cq's own: the answer is Orc's,
		// and repeating its words is the whole of what cq knows. cq's own
		// vocabulary has no "denied" — the token got you this far, and this is the
		// second gate.
		return fault.Unauthenticated{Reason: fmt.Sprintf(
			"orc did not grant `%s`: %v\n"+
				"  it is a builtin permission at floor 90, held by executive agents only.\n"+
				"  `orc check-permission %s` says whether you hold it",
			UpgradePermission, oneLine(err), UpgradePermission)}
	}
	return nil
}

// askUpgrade posts the request and decodes what the server intends to do.
func (a App) askUpgrade(ctx context.Context, serverURL, token string, body map[string]any) (upgradeView, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return upgradeView{}, fault.Internal{Where: "cli.askUpgrade", Detail: err.Error()}
	}
	url := strings.TrimSuffix(serverURL, "/") + "/api/v1/upgrade"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return upgradeView{}, fault.Usage{Reason: fmt.Sprintf("%s is not a usable server address: %v", serverURL, err)}
	}
	// So a retry can send the body again. http.NewRequest sets this for the reader
	// types it knows; a retried request without it posts nothing the second time.
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.reachServer(ctx, req)
	if err != nil {
		return upgradeView{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return upgradeView{}, fault.IO{Op: "read", Subject: serverURL, Err: err}
	}
	if resp.StatusCode != http.StatusAccepted {
		return upgradeView{}, fault.IO{Op: "ask", Subject: serverURL,
			Err: fmt.Errorf("the server answered %s: %s", resp.Status, strings.TrimSpace(string(raw)))}
	}

	var view upgradeView
	if err := json.Unmarshal(raw, &view); err != nil {
		return upgradeView{}, fault.Parse{Where: url, Reason: err.Error()}
	}
	return view, nil
}

// upgradeView mirrors what the server answers. It is written out rather than
// imported so the CLI and the server can be different builds for the length of an
// upgrade — which, for this command of all commands, they will be.
type upgradeView struct {
	Queued     []string `json:"queued"`
	Server     string   `json:"server"`
	Restarting bool     `json:"restarting"`
}

func (a App) sayUpgrade(view upgradeView) error {
	if err := a.say("%s %s", a.ink("upgrading", style.Good), a.ink(view.Server, style.Quiet)); err != nil {
		return err
	}
	if len(view.Queued) == 0 {
		return a.say("  %s", a.ink("no agent machine has ever synced, so none was queued", style.Quiet))
	}
	if err := a.say("  %s %s", a.ink(fmt.Sprint(len(view.Queued)), style.Value),
		a.ink("agent machine(s) queued: "+strings.Join(view.Queued, ", "), style.Quiet)); err != nil {
		return err
	}
	// The honest caveat. Nothing here has happened yet on the agents, and a queue
	// that leaves on the next sync is a fact worth repeating at the one moment
	// somebody is watching for a result.
	return a.say("  %s", a.ink("each rebuilds on its next sync; `cq queue` says what came of it", style.Quiet))
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// orc is the adapter for asking Orc a question: who the operator is, when working
// out whose mailbox to mirror, and whether this identity may upgrade.
//
// `$ORC` names the executable for the unusual machine where it is not on the path
// under its own name.
func (a App) orc() *source.Orc {
	return &source.Orc{Command: a.look("ORC", "orc")}
}

// How long the command waits for a server that is not answering.
//
// The numbers come from the window the server makes for itself. `upgradeSelf`
// waits restartGrace (2s), the listener drains for up to shutdownGrace (15s), and
// the supervisor then sleeps its backoff before exec — so a `cq upgrade` issued
// while a previous one restarts finds a closed port for something over twenty
// seconds. Nothing retried, so it failed, which is the refused dial an operator
// sees and reads as "upgrade is broken".
const (
	// ReachTries is how many times the POST is attempted.
	ReachTries = 7
	// ReachBackoff is the first wait; each attempt doubles it, so seven attempts
	// span about half a minute.
	ReachBackoff = 400 * time.Millisecond
	// ReachTimeout bounds one attempt. The server answers this route in
	// milliseconds — it queues and returns 202, and the build happens after — so a
	// long wait here means the socket is open and nobody is behind it.
	ReachTimeout = 10 * time.Second
)

// reachServer posts the request, riding out a server that is restarting.
//
// Only a gap is retried. A refused connection, a reset, and a timeout all mean
// "nobody is listening yet"; every answer the server gives — including a refusal —
// is an answer, and asking again would turn one clear "no" into seven.
func (a App) reachServer(ctx context.Context, req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: ReachTimeout}
	wait := ReachBackoff

	var last error
	for attempt := 1; attempt <= ReachTries; attempt++ {
		// A fresh body each time. Clone copies the request but not the position of
		// the reader inside it, so a second attempt posted an empty body under the
		// first one's Content-Length and the server rejected it.
		try := req.Clone(ctx)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fault.Internal{Where: "cli.reachServer", Detail: err.Error()}
			}
			try.Body = body
		}
		resp, err := client.Do(try)
		if err == nil {
			if attempt > 1 {
				a.tell("%s", a.ink("the server answered", style.Good))
			}
			return resp, nil
		}
		last = err
		if !unreached(err) {
			return nil, fault.Unavailable{Peer: req.URL.Host, Err: err}
		}
		if attempt == ReachTries {
			break
		}
		// Said out loud. A command that sits silent for half a minute is one
		// somebody interrupts, and interrupting this one is how a fleet ends up
		// half upgraded.
		if attempt == 1 {
			a.tell("%s", a.ink("the server is not answering; it may be restarting — waiting", style.Warn))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
	return nil, fault.Unavailable{Peer: req.URL.Host, Err: fmt.Errorf(
		"no answer in %d attempts over about %s: %w", ReachTries, roundWait(ReachTries), last)}
}

// unreached reports whether an error means nobody was listening, as against the
// server having said something.
func unreached(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF) {
		return true
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	// A dial that failed for a reason the platform spells differently. The string
	// is the last resort rather than the first, and it only ever adds patience.
	text := err.Error()
	return strings.Contains(text, "connection refused") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "no route to host")
}

// roundWait is how long the attempts spanned, for the message.
func roundWait(tries int) time.Duration {
	total := time.Duration(0)
	wait := ReachBackoff
	for i := 1; i < tries; i++ {
		total += wait
		wait *= 2
	}
	return total.Round(time.Second)
}
