package source

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"orc/common/nudge"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// Orc answers the two questions cq cannot answer for itself: who the operator of
// this machine's fleet is, and — when nobody is around to say — what their
// credential is.
//
// It exists so that a freshly bootstrapped machine syncs without being configured
// twice. Orc already knows the account whose mailbox this machine's mirror is
// *for*; making the operator copy that name into `$CQ_USER` is asking them to
// repeat something the machine can look up.
//
// The lookup is only ever used to *agree with* Orc, never to override it. Nothing
// here can make cq mirror an account Orc does not call the operator, which is the
// property that keeps an agent-triggered sync from publishing the agent's mailbox
// as the machine's.
type Orc struct {
	// Command is the orc executable, "orc" by default.
	Command string
	// Env is the environment every orc child runs with. Empty means the ambient
	// one, plus the nudge suppression.
	//
	// It exists because the two users of this adapter need opposite things. Working
	// out *whose* mailbox this machine mirrors must run under whatever credential
	// is ambient — that is the input to Orc's own decision about who is asking, and
	// clearing it would be cq quietly asking to be treated as the owner. Reading
	// and changing the fleet must run as the *mirrored account*, so a sync an agent
	// triggered still shows and changes the operator's fleet rather than the
	// agent's narrower view of it.
	Env []string
	// Run executes a command; it exists so tests can drive this without Orc
	// installed. Defaults to running the real thing.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewOrc returns an adapter over the usual command.
func NewOrc() *Orc { return &Orc{Command: "orc"} }

func (o *Orc) command() string {
	if o.Command == "" {
		return "orc"
	}
	return o.Command
}

// Operator is the name of the fleet's operator.
//
// It runs under whatever credential is ambient, because every identity may ask
// who the operator is — that is public within a fleet, and an agent needs the
// answer to know that it is *not* them.
func (o *Orc) Operator(ctx context.Context) (string, error) {
	out, err := o.run(ctx, "introspect", "--only", "operator")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", fault.IO{Op: "read", Subject: o.command() + " introspect --only operator",
			Err: fmt.Errorf("it printed nothing")}
	}
	return name, nil
}

// OwnerCredential is the operator's name and key, from Orc's own keyring.
//
// This succeeds in exactly one situation, and Orc decides it rather than cq: the
// caller presented no credential at all, and the fleet is private to this unix
// user. In an agent's session — where `$ORC_USER` is always set — Orc resolves
// that identity instead and refuses to hand over somebody else's key, so this
// cannot become a way for a session to reach the operator's credential.
//
// The key is a secret. It is returned to be put in a child's environment and must
// not be logged, printed, or written to the agent's state.
func (o *Orc) OwnerCredential(ctx context.Context) (user, key string, err error) {
	out, err := o.run(ctx, "owner", "env")
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case OrcUser:
			user = strings.TrimSpace(value)
		case OrcKey:
			key = strings.TrimSpace(value)
		}
	}
	if user == "" || key == "" {
		// Not a fault of the operator's, and not something a retry fixes: cq and
		// Orc disagree about the shape of a block cq is parsing.
		return "", "", fault.Parse{Where: o.command() + " owner env",
			Reason: "no " + OrcUser + " and " + OrcKey + " in the output"}
	}
	return user, key, nil
}

// MayUse asks Orc whether the ambient identity holds a permission.
//
// Orc answers with an exit code — 0 held, 8 not, 2 no such permission — the way
// `muff assign` asks about control. cq keeps no copy of the answer and no idea what
// a floor is: it asks, and repeats what it was told.
//
// Ambient credential on purpose. The question is about whoever is running this
// command, which is exactly what an agent's shell presents, and pinning it to the
// mirrored account would answer for somebody else.
func (o *Orc) MayUse(ctx context.Context, permission string) error {
	_, err := o.run(ctx, "check-permission", permission)
	return err
}

// Fleet reads the whole Orc store, as Orc derives it.
//
// `orc status --json` and nothing else: the shape it prints is the derived fleet
// — authority already capped by the boss chain, permissions already intersected —
// and cq carries that rather than the raw records. A second derivation in the
// browser would be a second opinion about who may do what, and the wrong one
// would be the one on screen.
//
// A machine with no fleet, or an orc that refuses, is not a failed sync. It is a
// machine that runs no agents, and the mirror says so in one line rather than
// showing an empty panel that reads as a broken one.
func (o *Orc) Fleet(ctx context.Context) protocol.Fleet {
	out, err := o.run(ctx, "status", "--json")
	if err != nil {
		return protocol.Fleet{Unreachable: oneLine(err)}
	}
	var fleet protocol.Fleet
	if err := decodeJSON(out, &fleet, o.command()+" status"); err != nil {
		return protocol.Fleet{Unreachable: oneLine(err)}
	}
	// A fleet is the caller's own branch, so an agent's sync would mirror less
	// than the operator's. That is Orc's rule and cq does not work around it; what
	// it does do is refuse to publish a partial fleet as the whole one, which is
	// what the operator's name being absent would mean.
	if fleet.Operator == "" && len(fleet.Identities) == 0 {
		return protocol.Fleet{Unreachable: "orc reported no fleet"}
	}

	// The standing instructions, in a second call because they are a second
	// question: `orc status` is who exists and what they may do, and this is what
	// they are told. A fleet whose prompts cannot be read is still a fleet — the
	// panel loses one tab rather than the whole mirror.
	if out, err := o.run(ctx, "instruct", "--json"); err == nil {
		var prompts []protocol.FleetPrompt
		if err := decodeJSON(out, &prompts, o.command()+" instruct"); err == nil {
			fleet.Prompts = prompts
		}
	}

	fleet.Sessions = o.sessions(ctx, fleet.Identities)
	return fleet
}

// SessionLines is how much of each agent's session travels.
//
// Smaller than `orc view`'s own default, and for a different reason: at a terminal
// the limit is a screenful, and here it is a snapshot that goes over the network
// every five minutes for every machine in the fleet. Twelve is enough to see what
// an agent is doing and to read why something was refused, which is what the panel
// is consulted for.
const SessionLines = 12

// sessions asks `orc view` about each employed identity.
//
// One call per agent rather than one for the fleet, because that is the command
// that exists and adding a bulk form to orc for cq's convenience would put a
// second, subtly different collector in the tool that owns the data.
//
// Only the employed. An identity with no session has nothing to show, and asking
// about all of them would spend a subprocess each per sync to learn that — on a
// fleet of thirty, most of them idle, that is the difference between a sync and a
// stall.
//
// Every failure here is silent and costs one agent's pane. The fleet is already
// collected at this point; refusing to publish it because one session's feed would
// not read would trade the whole panel for a corner of it.
func (o *Orc) sessions(ctx context.Context, ids []protocol.FleetID) []protocol.FleetSession {
	var out []protocol.FleetSession
	for _, id := range ids {
		if !id.Employed {
			continue
		}
		body, err := o.run(ctx, "view", id.Name, "--json", "--lines", strconv.Itoa(SessionLines))
		if err != nil {
			continue
		}
		var got protocol.FleetSession
		if err := decodeJSON(body, &got, o.command()+" view"); err != nil {
			continue
		}
		// Checked before it is kept, not only when the whole snapshot is validated
		// on the way out. A session that decodes but says nothing — no identity —
		// would otherwise make the entire mirror unpublishable, which is a whole
		// machine's mail lost to one agent's pane.
		if err := got.Validate(); err != nil {
			continue
		}
		out = append(out, got)
	}
	return out
}

func (o *Orc) environment() []string {
	if len(o.Env) > 0 {
		return o.Env
	}
	return os.Environ()
}

// oneLine flattens an error into something that fits on a line of a panel.
func oneLine(err error) string {
	text := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i]) + " …"
	}
	if len(text) > 400 {
		text = text[:400] + "…"
	}
	return text
}

// Apply performs one queued fleet action by running the Orc command it names.
//
// One operation, one command, exactly as the Mailman and Macmuffin halves work.
// cq mirrors Orc's API rather than reimplementing the model: a verb invented here
// would be a rule about authority that Orc does not have, and authority is the one
// thing in this tree that must have a single source.
//
// `--yes` goes on every command that asks for it. Orc requires it whenever stdin
// is not a terminal, which for a queued action is always, and the confirmation
// happened in the browser — hours ago and on another machine.
func (o *Orc) Apply(ctx context.Context, action protocol.Action) error {
	a := action.Args

	var args []string
	switch action.Op {
	case protocol.OpOrcNewIdentity:
		args = []string{"new", "identity", a.Identity}
	case protocol.OpOrcNewRole:
		args = []string{"new", "role", a.Role, strconv.Itoa(a.Authority), a.Description}
	case protocol.OpOrcNewPermission:
		args = append([]string{"new", "permission", a.Permission, strconv.Itoa(a.Floor)}, a.Patterns...)
	case protocol.OpOrcEditPermission:
		args = append([]string{"edit", "permission", a.Permission, "--floor", strconv.Itoa(a.Floor)}, a.Patterns...)
	case protocol.OpOrcAssignRole:
		args = []string{"assign", "role", a.Identity, a.Role}
	case protocol.OpOrcAssignAuthority:
		args = []string{"assign", "authority", a.Role, strconv.Itoa(a.Authority)}
	case protocol.OpOrcAssignPerm:
		args = []string{"assign", "permission", a.Role, a.Permission}
	case protocol.OpOrcRemoveIdentity:
		args = []string{"remove", "identity", a.Identity, "--yes"}
	case protocol.OpOrcRemoveRole:
		args = []string{"remove", "role", a.Role, "--yes"}
	case protocol.OpOrcRemovePerm:
		// With a role it narrows that one role; without, it deletes the permission
		// outright. Two different commands, and the queue shows which.
		args = []string{"remove", "permission", a.Permission}
		if a.Role != "" {
			args = append(args, "--from", a.Role)
		}
		args = append(args, "--yes")
	case protocol.OpOrcGrant:
		args = []string{"grant", "permission", a.Identity, a.Permission}
		if a.Until != "" {
			args = append(args, "--until", a.Until)
		}
	case protocol.OpOrcRevoke:
		args = []string{"revoke", "permission", a.Identity, a.Permission}
	case protocol.OpOrcMove:
		args = []string{"move", a.Identity, a.Boss}
	case protocol.OpOrcEmploy:
		args = []string{"employ", a.Identity}
		if a.Model != "" {
			args = append(args, "--model", a.Model)
		}
		if a.Effort != "" {
			args = append(args, "--effort", a.Effort)
		}
	case protocol.OpOrcFire:
		args = []string{"fire", a.Identity, "--yes"}
	case protocol.OpOrcBudget:
		args = []string{"budget", a.Role, strconv.Itoa(a.Load)}
	case protocol.OpOrcPoke:
		args = []string{"poke", a.Identity}
		if a.Message != "" {
			args = append(args, a.Message)
		}
	case protocol.OpOrcRefresh:
		args = []string{"refresh", a.Identity}
	case protocol.OpOrcWorkspace:
		// `from` is checked here rather than passed on, because orc has no opinion
		// about what the browser was looking at. A snapshot is minutes old by the
		// time somebody acts on it, and this is the moment the two can be compared:
		// the agent machine knows where the identity works *now*.
		if err := o.workspaceUnchanged(ctx, a.Identity, a.From); err != nil {
			return err
		}
		args = []string{"workspace", a.Identity, a.Workspace}
		if a.Adopt {
			args = append(args, "--adopt")
		}
	case protocol.OpOrcPace:
		// The layer is named by whichever of the two is set, and neither means the
		// fleet's own — the same shape the command takes.
		args = []string{"pace", a.Cycle}
		if a.Identity != "" {
			args = append(args, a.Identity)
		} else if a.Role != "" {
			args = append(args, a.Role)
		}
		// `--clear` first, so a form that cleared a layer and set a value in one
		// move means "start again from this", which is what it reads as.
		if a.PaceClear {
			args = append(args, "--clear")
		}
		for _, flag := range []struct{ name, value string }{
			{"--after", a.After}, {"--every", a.Every}, {"--watch", a.Watch},
		} {
			if flag.value != "" {
				args = append(args, flag.name, flag.value)
			}
		}
		if a.PaceOff {
			args = append(args, "--off")
		}
		if a.PaceOn {
			args = append(args, "--on")
		}

	case protocol.OpOrcTariff:
		// `--yes` for the same reason every queued verb carries it: the operator
		// confirmed in the browser, and the far end has no terminal to ask.
		args = []string{"tariff", a.Setting, strconv.Itoa(a.Load), "--yes"}

	case protocol.OpOrcTend:
		args = []string{"tend"}
	case protocol.OpOrcToolkit:
		// `bootstrap` on a fleet that exists adds the toolkit permissions it does
		// not have and touches nothing else. The operator is named so the command
		// is the same whichever user the sync runs as — on a fleet that exists it
		// only decides what a mismatch is reported against.
		args = []string{"bootstrap", "--as", a.Identity}

	case protocol.OpOrcInstructSet:
		// Through a file rather than argv: a prompt is up to 16 KiB of prose, and a
		// command line is both size-limited and visible in `ps` to everyone on the
		// machine — the same reason mail bodies do not travel as arguments.
		//
		// A file rather than stdin because the injected runner has no stdin, and
		// widening that interface for one operation would change every fake in
		// every test that uses it.
		path, done, err := tempMarkdown("cq-instruct-*.md", a.Text)
		if err != nil {
			return err
		}
		defer done()
		args = append(instructTarget(a), "--set", path)

	case protocol.OpOrcInstructClear:
		args = append(instructTarget(a), "--clear")
	default:
		return fault.Internal{Where: "source.Orc.Apply", Detail: "no command for operation " + string(action.Op)}
	}

	_, err := o.run(ctx, args...)
	return err
}

// instructTarget turns the queued operands into the words `orc instruct` takes.
func instructTarget(a protocol.Args) []string {
	args := []string{"instruct"}
	if a.Wake {
		args = append(args, "wake")
	}
	switch a.Prompt {
	case "system":
		// `orc instruct wake` addresses the fleet's message; `wake system` is not a
		// thing, which is why the word is only added for the prompt.
		if !a.Wake {
			args = append(args, "system")
		}
	case "role", "identity":
		args = append(args, a.Prompt, a.PromptName)
	}
	return args
}

// tempMarkdown writes prose where a `--set` flag can read it, and returns how to
// remove it.
//
// Every tool cq drives takes its prose from a file rather than from an argument, and
// so does cq: a command line is size-limited and visible in `ps` to everyone on the
// machine. A standing instruction and a task's description are both somebody's
// words, and neither should be readable by whoever happens to be logged in.
//
// 0600 and in the process's own temporary directory, for the length of one exec.
func tempMarkdown(pattern, text string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, fault.IO{Op: "create", Subject: "a temporary file for the text", Err: err}
	}
	done := func() { _ = os.Remove(f.Name()) }

	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		done()
		return "", func() {}, fault.IO{Op: "write", Subject: f.Name(), Err: err}
	}
	if err := f.Close(); err != nil {
		done()
		return "", func() {}, fault.IO{Op: "close", Subject: f.Name(), Err: err}
	}
	return f.Name(), done, nil
}

// WorkspaceUnchanged is the staleness check, for callers outside the apply path.
//
// `cq workspace --from` on the agent machine is the same question a queued action
// asks, and it should get the same answer from the same code: two implementations of
// "has this moved since you looked" is one of them eventually being wrong.
func (o *Orc) WorkspaceUnchanged(ctx context.Context, identity, from string) error {
	return o.workspaceUnchanged(ctx, identity, from)
}

// Output runs an orc command and hands back what it said, for a caller that is
// relaying rather than parsing.
func (o *Orc) Output(ctx context.Context, args ...string) ([]byte, error) {
	return o.run(ctx, args...)
}

// workspaceUnchanged refuses an action whose view of the world has moved on.
//
// The browser sends where it saw the identity working. If that is no longer true —
// somebody moved it on the machine while the action sat in the queue — applying
// anyway would silently overturn a decision the operator never saw. It is the same
// guard the library verbs get from `base`, for the one fleet value whose old
// location still exists on disk afterwards.
func (o *Orc) workspaceUnchanged(ctx context.Context, identity, from string) error {
	out, err := o.run(ctx, "workspace", identity)
	if err != nil {
		return err
	}

	// `orc workspace <identity>` says "<name> works in <path>", possibly with a
	// note after it. The path is what sits between the two, and comparing on
	// containment rather than parsing keeps this from breaking the first time the
	// sentence gains a word.
	if !strings.Contains(string(out), from) {
		return fault.Conflict{Subject: identity, Reason: fmt.Sprintf(
			"it no longer works in %s, so this move was made against a stale view; "+
				"reload and try again", from)}
	}
	return nil
}

// run invokes orc and returns its standard output.
//
// The environment is inherited untouched apart from the nudge suppression: what
// `$ORC_USER` says is precisely the input to Orc's own decision about who is
// asking, and clearing it here would be cq quietly asking to be treated as the
// owner.
func (o *Orc) run(ctx context.Context, args ...string) ([]byte, error) {
	if o.Run != nil {
		return o.Run(ctx, o.command(), args...)
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, o.command(), args...)
	// ORC_AGENT forces plain output, so nothing cq parses carries escapes.
	cmd.Env = append(o.environment(), nudge.Suppress+"=1", "ORC_AGENT=1")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fault.IO{Op: "run", Subject: o.command(), Err: fmt.Errorf("timed out after %s", Timeout)}
	}
	if err != nil {
		detail := strings.TrimSpace(errOut.String())
		if detail == "" {
			detail = err.Error()
		}
		failure := fault.IO{Op: "run", Subject: o.command() + " " + strings.Join(args, " "),
			Err: fmt.Errorf("%s", detail)}
		return nil, withCause{err: failure, cause: err}
	}
	return out.Bytes(), nil
}
