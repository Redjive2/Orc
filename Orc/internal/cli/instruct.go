package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

// `orc instruct` — the standing instructions agents run under.
//
// Four kinds and two mechanisms (Claude/Docs/Orc/Instruct.md §2): system, role, and
// identity are prompt *layers* that compose additively into what an agent is told at
// the start of a session; wake is the *message* sent to one that has gone quiet, and
// the most specific of those wins outright.
//
// **A prompt is not a permission.** "do not edit Communique" is a request; the hook
// enforcing `write(Anno/**)` is a rule. The failure mode — somebody writing a prompt
// where they needed a permission — is silent and looks like it is working right up
// until it does not, so every screen here says so where somebody will read it.

// instructVerb is `orc instruct …`.
func (a App) instructVerb(args []string) error {
	var setFrom, clear, edit, diff, asJSON bool
	var from string
	rest, err := flagged(args, options{
		values: map[string]*string{"--set": &from},
		switches: map[string]*bool{
			"--clear": &clear, "--edit": &edit, "--diff": &diff, "--json": &asJSON,
		},
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(from) != "" {
		setFrom = true
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("instruct"); err != nil {
		return err
	}

	if len(rest) == 0 {
		if asJSON {
			return a.instructJSON(s)
		}
		return a.instructOverview(s)
	}

	// `show` addresses a composed prompt rather than a layer, so it is read before
	// anything tries to parse a target out of the words after it.
	if rest[0] == "show" {
		return a.instructShow(s, rest[1:], diff)
	}

	target, err := a.instructTarget(s, rest)
	if err != nil {
		return err
	}

	switch {
	case clear:
		return a.instructClear(s, target)
	case setFrom:
		return a.instructSet(s, target, from)
	case edit:
		return a.instructEdit(s, target)
	default:
		return a.instructPrint(s, target)
	}
}

// instructTarget reads `system`, `role <name>`, `identity <name>`, or `wake …` into
// the thing they address.
//
// `wake` takes the same three shapes, because a wake message is filed beside the
// prompt it belongs to and addressed the same way — only its bound and its
// composition rule differ.
func (a App) instructTarget(s caller, words []string) (store.Target, error) {
	wake := words[0] == "wake"
	if wake {
		words = words[1:]
		if len(words) == 0 {
			return store.FleetPrompt(true), nil
		}
	}

	switch words[0] {
	case "system":
		if wake {
			return store.Target{}, fault.Usage{Reason: "the fleet's wake message is `orc instruct wake`"}
		}
		if len(words) != 1 {
			return store.Target{}, fault.Usage{Reason: "the fleet prompt names nobody"}
		}
		return store.FleetPrompt(false), nil

	case "role":
		if len(words) != 2 {
			return store.Target{}, fault.Usage{Reason: "instruct role takes a role name"}
		}
		name, err := model.ParseName(words[1])
		if err != nil {
			return store.Target{}, err
		}
		if _, ok := s.fleet.Role(name); !ok {
			return store.Target{}, fault.NotFound{Target: "role " + name.String()}
		}
		return store.RolePrompt(name, wake), nil

	case "identity":
		if len(words) != 2 {
			return store.Target{}, fault.Usage{Reason: "instruct identity takes an identity"}
		}
		who, err := user.Parse(words[1])
		if err != nil {
			return store.Target{}, err
		}
		if _, err := s.fleet.Identity(who); err != nil {
			return store.Target{}, err
		}
		return store.IdentityPrompt(who, wake), nil

	default:
		return store.Target{}, fault.Usage{Reason: fmt.Sprintf(
			"%q is not something to instruct; it is `system`, `role <name>`, `identity <name>`, or `wake`",
			words[0])}
	}
}

// mayInstruct is §8's table.
//
// Editing a prompt is not editing a file; it is deciding how an agent thinks, so it
// sits with policy rather than with work. The fleet's layer is the operator's alone
// because it reaches every agent there is — the same rule `owner rename` has, for
// the same reason — and no permission grants it.
func (s caller) mayInstruct(t store.Target) error {
	operator := s.who.String() == s.fleet.Operator().String()

	switch t.Kind {
	case instruct.System:
		if !operator {
			return fault.Denied{Actor: s.who.String(), Action: "write", Target: "the fleet's instructions",
				Reason: "it reaches every agent in the fleet, so it is the operator's alone"}
		}
		return nil

	case instruct.Role:
		if operator || s.fleet.Holds(s.who, instructPermissionName()) {
			return nil
		}
		return fault.Denied{Actor: s.who.String(), Action: "write", Target: "a role's instructions",
			Reason: "it needs the `instruct` permission"}

	case instruct.Identity:
		if !operator && !s.fleet.Holds(s.who, instructPermissionName()) {
			return fault.Denied{Actor: s.who.String(), Action: "write", Target: "an agent's instructions",
				Reason: "it needs the `instruct` permission"}
		}
		// And ancestry: you may instruct what you control. The permission says you
		// may instruct at all; the tree says whom.
		if t.Identity.String() == s.who.String() {
			return fault.Denied{Actor: s.who.String(), Action: "instruct", Target: "itself",
				Reason: "an agent writing its own standing instructions is an agent deciding what it is for"}
		}
		return s.controls(t.Identity, "instruct")

	default:
		return fault.Internal{Where: "cli.mayInstruct", Detail: "unknown kind " + string(t.Kind)}
	}
}

// instructPermission is the toolkit permission §8 adds.
//
// Parsed rather than asserted, and the zero name on failure rather than a panic —
// the same shape `budgetName` uses, and for the same reason. It is unreachable:
// the string is a constant that `store.Toolkit()` already parses at startup, and a
// fleet whose toolkit will not parse is something `orc doctor` reports.
func instructPermissionName() model.Name {
	got, err := model.ParseName(instructPermission)
	if err != nil {
		return model.Name{}
	}
	return got
}

const instructPermission = "instruct"

// instructOverview is `orc instruct` alone: every layer, its size, and when it last
// moved.
func (a App) instructOverview(s caller) error {
	if err := a.say(a.out.Header("standing instructions") + "   " +
		a.out.Muted("what agents are told at the start of every session")); err != nil {
		return err
	}

	set := 0
	for _, row := range s.instructRows() {
		text, found, err := s.store.Prompt(row.target)
		if err != nil {
			// One unreadable layer must not hide the rest of the fleet's.
			if err := a.say(fmt.Sprintf("  %-28s %s", row.what, a.out.Alarm(err.Error()))); err != nil {
				return err
			}
			continue
		}
		if !found {
			continue
		}
		set++

		line := fmt.Sprintf("  %-28s %s", row.what, a.out.Value(sizeOf(len(text))))
		if change, ok, err := s.store.LastChange(row.target); err == nil && ok {
			line += "   " + a.out.Muted(fmt.Sprintf("%s, %s", change.By, ago(s.store.Now(), change.At)))
		}
		if err := a.say(line); err != nil {
			return err
		}
	}

	if set == 0 {
		if err := a.say("  " + a.out.Muted("nothing is set; every agent runs on claude's own instructions")); err != nil {
			return err
		}
	}
	return a.say("\n  " + a.out.Muted(
		"a prompt asks and a permission enforces — `orc instruct` cannot stop an agent doing anything"))
}

// instructRow is one layer, and what to call it on a screen.
type instructRow struct {
	what   string
	target store.Target
}

// instructRows is every layer the caller can see, in the order both views show them.
//
// One list rather than two, because a screen and its --json that disagreed about
// what a fleet has would send somebody looking for a layer that is there.
func (s caller) instructRows() []instructRow {
	rows := []instructRow{
		{"system", store.FleetPrompt(false)},
		{"wake", store.FleetPrompt(true)},
	}
	for _, role := range s.fleet.Roles() {
		name := role.Name()
		rows = append(rows,
			instructRow{"role " + name.String(), store.RolePrompt(name, false)},
			instructRow{"role " + name.String() + " wake", store.RolePrompt(name, true)})
	}
	for _, name := range s.fleet.Subtree(s.who) {
		rows = append(rows,
			instructRow{name.String(), store.IdentityPrompt(name, false)},
			instructRow{name.String() + " wake", store.IdentityPrompt(name, true)})
	}
	return rows
}

// instructPrint writes one layer to stdout, and nothing else.
//
// Verbatim, so `orc instruct system > system.md` round-trips. A heading or a size
// would make the output a report rather than the text, and the text is what a caller
// redirecting it wants.
func (a App) instructPrint(s caller, t store.Target) error {
	text, found, err := s.store.Prompt(t)
	if err != nil {
		return err
	}
	if !found {
		a.note("nothing is set there")
		return nil
	}
	return a.write(text)
}

// instructSet replaces a layer from a file, or from stdin for `-`.
func (a App) instructSet(s caller, t store.Target, from string) error {
	if err := s.mayInstruct(t); err != nil {
		return err
	}

	var data []byte
	var err error
	if from == "-" {
		if a.Stdin == nil {
			return fault.Usage{Reason: `--set is "-" but there is no standard input to read`}
		}
		data, err = io.ReadAll(io.LimitReader(a.Stdin, instruct.MaxLayer+1))
	} else {
		data, err = os.ReadFile(from)
	}
	if err != nil {
		return fault.IO{Op: "read", Path: from, Err: err}
	}

	if err := s.store.WritePrompt(t, s.who, string(data)); err != nil {
		return err
	}
	return a.sayInstructed(s, t, "set", len(data))
}

// instructClear removes a layer.
func (a App) instructClear(s caller, t store.Target) error {
	if err := s.mayInstruct(t); err != nil {
		return err
	}
	if err := s.store.ClearPrompt(t, s.who); err != nil {
		return err
	}
	return a.sayInstructed(s, t, "cleared", 0)
}

// instructEdit opens $EDITOR on a layer.
//
// The file it opens is the real one, so an editor that writes in place leaves the
// prompt where it belongs — but the result is read back and validated afterwards
// rather than trusted: an editor is not a caller that checked the bounds.
func (a App) instructEdit(s caller, t store.Target) error {
	if err := s.mayInstruct(t); err != nil {
		return err
	}

	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		return fault.Usage{Reason: "no $EDITOR is set; `orc instruct <target> --set <file>` takes a file instead"}
	}
	path, err := s.store.PromptPath(t)
	if err != nil {
		return err
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fault.IO{Op: "run " + editor + " on", Path: path, Err: err}
	}

	// Read it back through the store, which validates. An editor that saved 20 KiB
	// has written something the store would have refused, and finding that out here
	// beats finding it out when a session will not compose.
	text, found, err := s.store.Prompt(t)
	if err != nil {
		return err
	}
	if !found {
		return a.say(a.out.Muted("nothing was saved, so nothing changed"))
	}
	if err := s.store.WritePrompt(t, s.who, text); err != nil {
		return err
	}
	return a.sayInstructed(s, t, "set", len(text))
}

// sayInstructed reports the change and when it takes effect.
func (a App) sayInstructed(s caller, t store.Target, verb string, size int) error {
	line := fmt.Sprintf("%s %s", a.out.Good(verb), a.out.Value(describe(t)))
	if size > 0 {
		line += "   " + a.out.Muted(sizeOf(size))
	}
	if err := a.say(line); err != nil {
		return err
	}

	if t.Wake {
		// Nothing to restart: a wake message is sent to a session that already
		// exists, and the next wake uses it.
		return a.say("  " + a.out.Muted("the next wake will use it"))
	}
	// Accurate about both halves. A conversation in progress is unaffected — its
	// system prompt was fixed when the process started — but the next start of that
	// same session carries this, so a restart applies it without anybody asking.
	// Saying only "refresh" implied the edit sat inert until somebody did, which
	// sent people looking for a delivery problem that was really a timing one.
	return a.say("  " + a.out.Muted("a running session keeps what it started with until it restarts; "+
		"`orc refresh <identity>` applies it now, and `orc status <identity>` says what one was started with"))
}

// instructShow is the composed prompt, exactly as an agent gets it.
//
// Not a convenience. Layered configuration is only debuggable if the composition can
// be seen, and "why is this agent behaving like that" is the question this feature
// generates most.
func (a App) instructShow(s caller, words []string, diff bool) error {
	if len(words) != 1 {
		return fault.Usage{Reason: "instruct show takes one identity"}
	}
	who, err := user.Parse(words[0])
	if err != nil {
		return err
	}
	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}

	layers, err := s.store.Instructions(who, target.Role())
	if err != nil {
		return err
	}
	composed, err := instruct.Compose(layers)
	if err != nil {
		return err
	}

	if !diff {
		if composed == "" {
			return a.say(a.out.Muted(fmt.Sprintf(
				"%s runs on claude's own instructions; nothing is set for it", who)))
		}
		return a.write(composed + "\n")
	}

	// --diff: what would change if it restarted now. The session's own prompt is
	// not recorded, so the honest answer compares what it *would* be composed with
	// against what is running, which is only knowable as "there is a session and it
	// started before the last change".
	change, ok, err := s.store.LastChange(store.IdentityPrompt(who, false))
	if err != nil {
		return err
	}
	state, live, err := s.store.Session(who)
	if err != nil {
		return err
	}
	if !live {
		return a.say(a.out.Muted("no session is running, so the next one starts on what is set now"))
	}

	started, err := state.StartedAt()
	if err != nil {
		return err
	}
	if ok && change.At.After(started) {
		return a.say(a.out.Warn(fmt.Sprintf(
			"%s's instructions changed %s, after its session started", who, ago(s.store.Now(), change.At))) +
			"\n  " + a.out.Muted(fmt.Sprintf("`orc refresh %s` restarts it on them, and loses its context", who)))
	}
	return a.say(a.out.Good("its session started on the instructions that are set now"))
}

// describe names a target the way somebody typed it.
func describe(t store.Target) string {
	what := ""
	switch t.Kind {
	case instruct.System:
		what = "the fleet's"
	case instruct.Role:
		what = "the " + t.Role.String() + " role's"
	case instruct.Identity:
		what = t.Identity.String() + "'s"
	}
	if t.Wake {
		return what + " wake message"
	}
	return what + " prompt"
}

// sizeOf renders a byte count for a column.
func sizeOf(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

// ago is how long since, for a column where the exact time is not the point.
func ago(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// jsonPrompt is one standing instruction, as another program reads it.
//
// The text travels with it. cq's tab is an editor, not a listing, and an editor that
// had to fetch each layer separately would be one that could open a prompt somebody
// changed since the snapshot — which is the thing every other screen in cq is careful
// about.
type jsonPrompt struct {
	Kind string `json:"kind"`
	// Name is the role or identity it belongs to, empty for the fleet's own.
	Name string `json:"name,omitempty"`
	Wake bool   `json:"wake,omitempty"`
	Text string `json:"text"`
	Size int    `json:"size"`
	// Changed, By, and Digest are the journal's last word about this layer, absent
	// where nothing has ever written one through orc.
	Changed string `json:"changed,omitempty"`
	By      string `json:"by,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// instructJSON prints every layer the caller can see.
//
// The same set the overview shows, in the same order, because two views of one thing
// that disagree about what is in it are worse than either alone.
func (a App) instructJSON(s caller) error {
	out := make([]jsonPrompt, 0, 8)

	for _, row := range s.instructRows() {
		text, found, err := s.store.Prompt(row.target)
		if err != nil || !found {
			// A layer that will not read is left out rather than reported as empty:
			// `orc instruct` is where it is diagnosed, and a mirror that carried
			// "" would make a broken prompt look like a cleared one.
			continue
		}

		got := jsonPrompt{
			Kind: string(row.target.Kind), Wake: row.target.Wake,
			Text: text, Size: len(text),
		}
		switch row.target.Kind {
		case instruct.Role:
			got.Name = row.target.Role.String()
		case instruct.Identity:
			got.Name = row.target.Identity.String()
		}
		if change, ok, err := s.store.LastChange(row.target); err == nil && ok {
			got.Changed, got.By, got.Digest = clock.Format(change.At), change.By.String(), change.Digest
		}
		out = append(out, got)
	}
	return a.emitJSON(out)
}
