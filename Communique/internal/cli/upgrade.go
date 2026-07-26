package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

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
		return err
	}
	return a.sayUpgrade(view)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return upgradeView{}, fault.Usage{Reason: fmt.Sprintf("%s is not a usable server address: %v", serverURL, err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return upgradeView{}, fault.Unavailable{Peer: serverURL, Err: err}
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
