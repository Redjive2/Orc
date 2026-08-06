package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/view"
)

// `orc view` — what an agent has been doing, without joining it.
//
// `attach` already shows this, and shows it better: live, with the transcript
// beside the feed and a box to type into. What it also does is take over the
// terminal. That is the right trade for sitting down with one agent and the wrong
// one for every other reason somebody wants to look — checking on four agents in
// turn, seeing why one went quiet, pasting the last few tool calls into a message,
// or asking from a script.
//
// So this reads the same two files and prints them. No pty, no socket, no raw
// mode, nothing written anywhere: it cannot disturb the session it is looking at,
// which is the property that makes it safe to run against an agent mid-turn and
// the reason it needs no more authority than reading the fleet does.
//
// The two sources are Orc's and Claude's, and they degrade differently on purpose.
// The **event feed** is Orc's own record of what the agent did — tool calls,
// decisions, refusals — and is authoritative. The **transcript** is Claude's file,
// so an absent or unreadable one costs the prose and nothing else; that asymmetry
// is view/transcript.go's rule and this inherits it rather than restating it.

// DefaultViewLines is how much of each source is shown when nothing is asked for.
//
// Twenty is about a screen without scrolling, which is what somebody checking on
// an agent wants. Anybody who wants the whole thing has `--lines` and, for the
// conversation itself, `attach --direct`.
const DefaultViewLines = 20

// view is `orc view <identity> [--lines n] [--json]`.
func (a App) view(args []string) error {
	var lines string
	var wantJSON bool
	rest, err := flagged(args, options{
		values:   map[string]*string{"--lines": &lines},
		switches: map[string]*bool{"--json": &wantJSON},
	})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "view takes one identity"); err != nil {
		return err
	}

	count := DefaultViewLines
	if strings.TrimSpace(lines) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(lines))
		if err != nil || n < 1 {
			return fault.Usage{Reason: fmt.Sprintf("--lines takes a whole number of lines, not %q", lines)}
		}
		count = n
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	// Looking is not directing. The verb gate is deliberately not consulted —
	// `mayRunVerb` is for the verbs that *change* something, and reading a feed of
	// an agent already inside the caller's own subtree is not a privilege. What is
	// checked is ancestry, the same as `attach`: whose sessions you may look at is
	// a question about the tree, not about permissions.
	if who.String() != s.who.String() {
		if err := s.controls(who, "view"); err != nil {
			return err
		}
	}

	got, err := a.collectView(s, who, count)
	if err != nil {
		return err
	}
	if wantJSON {
		body, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			return fault.Internal{Where: "cli.view", Detail: err.Error()}
		}
		return a.say(string(body))
	}
	return a.showView(got)
}

// ViewOf is one agent's session, as much of it as was asked for.
//
// It is a named type with JSON tags because cq reads it: the browser cannot reach
// an agent machine, so what the website shows is whatever the last sync carried,
// and this is the shape that travels. Which also means the field names here are a
// compatibility surface — see Communique's protocol.SessionView, which mirrors it.
type ViewOf struct {
	Identity string `json:"identity"`
	Role     string `json:"role,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	// Live reports whether a session is running at all. Everything below is empty
	// when it is not, and that is a different state from a session that is running
	// and has done nothing — which is why this is here rather than inferred from
	// the rows being empty.
	Live bool `json:"live"`
	// Waiting reports that the agent has finished a turn and is waiting to be
	// spoken to. It is the single most useful fact for somebody checking on four
	// agents, which is why `orc wake` is built on it.
	Waiting bool   `json:"waiting"`
	Turn    int    `json:"turn"`
	Started string `json:"started,omitempty"`

	// Prose is what was said, oldest last. Absent when Claude's transcript could
	// not be read, which Available tells apart from "it said nothing".
	Prose          []ViewLine `json:"prose,omitempty"`
	ProseAvailable bool       `json:"prose_available"`
	// Rows are Orc's own record: tool calls and what was decided about them.
	Rows []ViewRow `json:"rows,omitempty"`
	// Note is anything that went wrong while reading, in the reader's terms. A
	// feed that will not parse is worth saying rather than showing as an empty
	// session, which reads as an idle agent.
	Note string `json:"note,omitempty"`
}

// ViewLine is one thing somebody said.
type ViewLine struct {
	Who  string `json:"who"`
	Text string `json:"text"`
	// At is when it was said, as Claude's transcript records it, or empty from a
	// transcript that does not carry one.
	//
	// It is here so a reader can *merge* this stream with the timestamped feed
	// beside it — which is what a pane showing what an agent said and what it did
	// as one conversation needs. It is not an order in its own right: see
	// view.Prose, where what it is and is not good for is written down.
	At string `json:"at,omitempty"`
}

// ViewRow is one event from the feed.
type ViewRow struct {
	At      string `json:"at"`
	Turn    int    `json:"turn"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
}

// collectView reads the feed and the transcript, and keeps the tail of each.
func (a App) collectView(s caller, who user.Name, count int) (ViewOf, error) {
	out := ViewOf{Identity: who.String()}

	if target, err := s.fleet.Identity(who); err == nil {
		out.Role = target.Role().String()
		// Only when they mean something. An identity that has never been employed
		// has no model, and "unset/unset" in the header is a fact about the struct
		// rather than about the agent.
		if target.Model().Valid() {
			out.Model = target.Model().String()
			out.Effort = target.Effort().String()
		}
	}

	state, live, err := s.store.Session(who)
	if err != nil {
		return ViewOf{}, err
	}
	out.Live = live
	if live {
		out.Started = state.Started
	}

	session, err := view.Load(s.store.EventsPath(who), who)
	if err != nil {
		// Reported, not returned. A feed that will not parse still leaves the
		// facts above worth printing, and an empty screen with an exit code says
		// less than a screen that names the problem.
		out.Note = "the event feed could not be read: " + viewOneLine(err)
		return out, nil
	}
	out.Turn = session.Turn
	out.Waiting = session.Waiting

	rows := session.Rows
	if len(rows) > count {
		rows = rows[len(rows)-count:]
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, ViewRow{
			At: r.At.Format(time.RFC3339), Turn: r.Turn, Kind: viewKind(r.Kind),
			Tool: r.Tool, Detail: r.Detail, Verdict: string(r.Verdict),
			Reason: r.Reason, Blocked: r.Blocked(),
		})
	}

	prose, available := view.ReadProse(session.Transcript)
	out.ProseAvailable = available
	if len(prose) > count {
		prose = prose[len(prose)-count:]
	}
	for _, p := range prose {
		out.Prose = append(out.Prose, ViewLine{Who: string(p.Who), Text: p.Text, At: p.At})
	}
	return out, nil
}

// showView prints it for a person.
//
// Linear rather than a drawn pane, and that is the whole difference from `attach`:
// this output is meant to be scrolled back through, piped, and pasted into a
// message. A box redrawn over the alternate screen cannot be any of those.
func (a App) showView(v ViewOf) error {
	head := a.out.Identity(v.Identity)
	if v.Role != "" {
		head += "   " + a.out.Muted(v.Role)
	}
	if v.Model != "" {
		head += "   " + a.out.Value(v.Model+"/"+v.Effort)
	}
	switch {
	case !v.Live:
		head += "   " + a.out.Muted("no session")
	case v.Waiting:
		// The state somebody is usually looking for: it has stopped and is waiting
		// to be spoken to, which is what `orc poke` is for. Named rather than left
		// to be inferred from a feed that simply stops.
		head += "   " + a.out.Warn("waiting") + a.out.Muted(fmt.Sprintf("   turn %d", v.Turn))
	default:
		head += "   " + a.out.Good("working") + a.out.Muted(fmt.Sprintf("   turn %d", v.Turn))
	}
	if err := a.say(head); err != nil {
		return err
	}

	if v.Note != "" {
		if err := a.say("  " + a.out.Alarm(v.Note)); err != nil {
			return err
		}
	}
	if !v.Live && len(v.Rows) == 0 && len(v.Prose) == 0 {
		return a.say("  " + a.out.Muted("nothing to show; `orc employ` starts a session"))
	}

	if len(v.Prose) > 0 {
		if err := a.say("\n" + a.out.Header("said")); err != nil {
			return err
		}
		for _, p := range v.Prose {
			who := a.out.Muted("·")
			if p.Who == "assistant" {
				who = a.out.Value("»")
			}
			if err := a.say("  " + who + " " + p.Text); err != nil {
				return err
			}
		}
	} else if !v.ProseAvailable && v.Live {
		// Told apart from "said nothing", because the two send somebody to
		// different places: one is an agent that has not spoken, the other is a
		// transcript this build could not read.
		if err := a.say("\n  " + a.out.Muted("no transcript to read — `orc attach --direct` shows the session itself")); err != nil {
			return err
		}
	}

	if len(v.Rows) > 0 {
		if err := a.say("\n" + a.out.Header("did")); err != nil {
			return err
		}
		for _, r := range v.Rows {
			line := "  " + a.out.Muted(shortTime(r.At)) + " "
			switch {
			case r.Blocked:
				line += a.out.Alarm("blocked") + " "
			case r.Verdict != "":
				line += a.out.Good(r.Verdict) + " "
			}
			if r.Tool != "" {
				line += a.out.Value(r.Tool) + " "
			}
			// A row with neither a tool nor a detail is a turn boundary, and drawing
			// it as a bare timestamp reads as a line that failed to render. The kind
			// is what it has to say, so the kind is what it says.
			if r.Tool == "" && r.Detail == "" {
				line += a.out.Muted(r.Kind)
			}
			line += r.Detail
			if err := a.say(strings.TrimRight(line, " ")); err != nil {
				return err
			}
			if r.Reason != "" {
				// On its own line, indented under what it explains: "blocked"
				// without the reason sends the reader to the permissions table to
				// find out what they already needed to know.
				if err := a.say("      " + a.out.Muted(r.Reason)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// viewKind names an event kind for a reader and for JSON.
//
// Spelled here rather than as a String() on view.Kind, because the pane draws
// kinds as glyphs and colour rather than as words — there was nothing to reuse,
// and adding a method to satisfy one caller would put this vocabulary in the
// package that deliberately has none.
func viewKind(k view.Kind) string {
	switch k {
	case view.Prompt:
		return "prompt"
	case view.Action:
		return "action"
	case view.Waiting:
		return "waiting"
	case view.Lifecycle:
		return "lifecycle"
	default:
		// Shown rather than hidden: a session doing something this build has no
		// opinion about is exactly what somebody looking at it wants to see.
		return "unknown"
	}
}

// viewOneLine flattens an error onto one line. Prefixed rather than called
// `oneLine` because this package is edited by several hands at once and a bare
// helper name is the kind of thing two people add on the same afternoon.
func viewOneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// shortTime is the clock part of a timestamp, which is what a reader wants on a
// line they are scanning. The date is on the header's `started`.
func shortTime(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.Format("15:04:05")
}
