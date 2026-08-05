// Package instruct composes an agent's standing instructions.
//
// Four kinds, and they are two mechanisms rather than four of a kind — treating
// them alike is the first mistake available here (Claude/Docs/Orc/Instruct.md §2):
//
//   - **system, role, identity** are prompt *layers*. They shape an agent for a
//     whole session, are fixed once it starts, and are **additive**.
//   - **wake** is a *message*: one thing you say to a session that is already
//     running. Wake messages **override**, most specific winning.
//
// The two rules are opposites on purpose. A system prompt is a document, and
// documents concatenate; a wake message is a sentence, and three of them stapled
// together is not a message but a mess arriving in the middle of somebody's work.
//
// Nothing here reads a file or knows where one lives. It takes text and returns
// text, which is what lets the layering rules — the decisions everything else
// assumes — be tested without a store, a fleet, or a session.
package instruct

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"orc/common/fault"
)

// Kind is which standing instruction a piece of text is.
type Kind string

// The kinds. They are the words the CLI and cq use, so an operator reading a
// refusal sees the same name they typed.
const (
	// System is the fleet's own, and applies to every agent.
	System Kind = "system"
	// Role is what a job is.
	Role Kind = "role"
	// Identity is what one agent in particular is for.
	Identity Kind = "identity"
	// Wake is what to say to an agent that has nothing to do: one that has gone
	// quiet, and one that has just been started and has never been spoken to.
	Wake Kind = "wake"
)

// Kinds lists them, so a test can be total and a help text cannot drift.
func Kinds() []Kind { return []Kind{System, Role, Identity, Wake} }

// Valid reports whether k is one of them.
func (k Kind) Valid() bool {
	switch k {
	case System, Role, Identity, Wake:
		return true
	default:
		return false
	}
}

// Layer reports whether the kind is composed into the system prompt. Wake is the
// one that is not: it is a message, and it reaches a session that already exists.
func (k Kind) Layer() bool { return k == System || k == Role || k == Identity }

// The bounds.
//
// A prompt is text that enters every session's context, on every restart, for ever.
// That is a cost, and an unbounded one is a fleet that gets slower and more
// expensive in a way nobody can see. The composed total is also a bound on a command
// line, which is a real limit rather than a tidiness one.
const (
	// MaxLayer is one layer: long enough for real instructions, short enough that
	// somebody will read it before they add to it.
	MaxLayer = 16 << 10
	// MaxComposed is the three layers together, and what the argument list has to
	// carry.
	MaxComposed = 48 << 10
	// MaxWake is a wake message. It is a sentence, not a brief.
	MaxWake = 2 << 10
)

// DefaultWake is what an agent is told when nothing sets anything.
//
// It is the built-in bottom of the override chain, so a fleet that configures
// nothing behaves exactly as it did before this existed.
const DefaultWake = "continue"

// Limit is the bound on one kind's text.
func Limit(k Kind) int {
	if k == Wake {
		return MaxWake
	}
	return MaxLayer
}

// Check validates one piece of prompt text.
//
// Over the limit is a refusal that names the kind and both sizes — never a
// truncation. Silently cutting an instruction in half is how an agent ends up
// following the first paragraph of a rule and none of the rest, which is worse than
// following none of it, because it looks like obedience.
func Check(k Kind, text string) error {
	if !k.Valid() {
		return fault.Internal{Where: "instruct.Check", Detail: fmt.Sprintf("unknown kind %q", k)}
	}
	if !utf8.ValidString(text) {
		return fault.Parse{Path: string(k), Reason: "the text is not valid UTF-8"}
	}
	if limit := Limit(k); len(text) > limit {
		return fault.Usage{Reason: fmt.Sprintf(
			"the %s prompt is %s, and the limit is %s; shorten it rather than letting orc cut it in half",
			k, size(len(text)), size(limit))}
	}

	// It goes on a command line and into a pty. A control character in either is a
	// terminal doing something nobody asked for, and an escape sequence in a system
	// prompt is a prompt that can repaint somebody's screen.
	for i, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return fault.Parse{Path: string(k), Reason: fmt.Sprintf(
				"there is a control character (%q) at byte %d; a prompt goes onto a command line "+
					"and into a terminal, where one does something nobody asked for", r, i)}
		}
	}
	return nil
}

// Layers are the three that compose, in the order they are composed.
//
// Each is the text of one layer, empty where there is none. They are named rather
// than a slice so a caller cannot get the order wrong, which for an additive
// composition is the one mistake that changes the meaning.
type Layers struct {
	System   string
	Role     string
	Identity string

	// RoleName and IdentityName are what the headings say. A composed prompt that
	// said only "role" would leave an agent — and an operator reading `orc instruct
	// show` — unable to tell which of five roles it came from.
	RoleName     string
	IdentityName string
}

// Compose builds the system prompt, additively.
//
// Additive, not overriding: an identity prompt *adds to* its role's and cannot
// replace it. The fleet prompt is where an operator writes what must hold for every
// agent — how to use the tools, when to ask rather than guess, what never to do —
// and a design where a role could shadow that would make the fleet-wide instruction
// a default rather than a floor.
//
// An operator who wants one agent to ignore the fleet prompt does not need a feature
// for it. They need to edit the fleet prompt.
//
// Each layer arrives under a heading naming where it came from, because an agent
// following an instruction should be able to say which of three documents it is in,
// and so should the operator debugging why.
func Compose(l Layers) (string, error) {
	// The house rule first, and always. It is not a layer somebody sets: a fleet
	// where half the agents write one way is a fleet whose documents read as though
	// several people wrote them, which is the thing the rule exists to stop. Nobody
	// can turn it off and nobody has to remember to turn it on.
	parts := []string{"# how to write\n\n" + House}

	for _, layer := range []struct {
		kind Kind
		what string
		text string
	}{
		{System, "the fleet", l.System},
		{Role, "the " + or(l.RoleName, "role") + " role", l.Role},
		{Identity, or(l.IdentityName, "this agent"), l.Identity},
	} {
		text := strings.TrimSpace(layer.text)
		if text == "" {
			continue
		}
		if err := Check(layer.kind, text); err != nil {
			return "", err
		}
		parts = append(parts, "# "+layer.what+"\n\n"+text)
	}

	if len(parts) == 1 {
		// The house rule on its own. A fleet that has set no layer at all still gets
		// it, because it is about how anything is written rather than about what
		// this fleet does.
		return parts[0], nil
	}

	got := strings.Join(parts, "\n\n")
	if len(got) > MaxComposed {
		return "", fault.Usage{Reason: fmt.Sprintf(
			"the composed prompt is %s and the limit is %s; it is three layers on a command line, "+
				"so the total is bounded as well as each part", size(len(got)), size(MaxComposed))}
	}
	return got, nil
}

// WakeFor picks the message to send, most specific first.
//
// Overriding rather than additive, and the reason is the shape of the thing: a
// system prompt is a document and documents concatenate, while a wake message is one
// thing you say. The others are not sent, and `continue` is the bottom so a fleet
// that sets nothing behaves as it always did.
func WakeFor(identity, role, fleet string) string {
	for _, candidate := range []string{identity, role, fleet} {
		if got := strings.TrimSpace(candidate); got != "" {
			return got
		}
	}
	return DefaultWake
}

// Source names where a wake message came from, for a command that has to say which
// of four things it is about to send.
func Source(identity, role, fleet string) Kind {
	switch {
	case strings.TrimSpace(identity) != "":
		return Identity
	case strings.TrimSpace(role) != "":
		return Role
	case strings.TrimSpace(fleet) != "":
		return Wake
	default:
		return ""
	}
}

// size renders a byte count the way a refusal should read.
func size(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

func or(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// Beyond is how much of a composed prompt the fleet itself set.
//
// The house writing rule is in every composition and nobody set it, so the length of
// a composition no longer answers "did what I wrote reach the agent?". This
// subtracts the part that is always there.
//
// A composition that does not start with the house rule is one from an older build,
// and its whole length is the fleet's — which is what it was.
func Beyond(composed string) int {
	head := "# how to write\n\n" + House
	if !strings.HasPrefix(composed, head) {
		return len(composed)
	}
	return len(strings.TrimSpace(composed[len(head):]))
}

// Empty reports whether a fleet has set any of the three layers.
//
// Separate from a composition being empty, which it no longer can be: every agent
// receives the house writing rule whether or not anybody wrote anything. "Nothing is
// set" and "there is nothing to say to this agent" used to be the same question and
// are now two.
func (l Layers) Empty() bool {
	return strings.TrimSpace(l.System) == "" &&
		strings.TrimSpace(l.Role) == "" &&
		strings.TrimSpace(l.Identity) == ""
}

// House is the writing rule every agent is given, whatever else it is told.
//
// It says the rule and it names the tool, because a rule with no way to check it is
// one that drifts: an agent can run `orc prose` over what it wrote and see the same
// judgement anybody reviewing it would see.
//
// The banned words share a shape. Each one claims something about the writing rather
// than saying the thing — "honestly" implies the rest was not, and the others assert
// that something matters instead of showing that it does.
const House = "Write in Simplified Technical English (ASD-STE100), at 80% or better.\n" +
	"\n" +
	"- Short sentences. 25 words is the limit; most should be well under it.\n" +
	"- Active voice. Say who does the thing: `orc reads the store`, not\n" +
	"  `the store is read`.\n" +
	"- One thought per sentence. Split it rather than chaining clauses with\n" +
	"  `which`, `although`, `whereas`, `because`.\n" +
	"- Simple, consistent words. Use the same word for the same thing every time.\n" +
	"\n" +
	"Six spellings are never used, in any form: `honest`, `honestly`, `caveat`,\n" +
	"`genuine`, `genuinely`, `load-bearing`. Say the thing itself instead.\n" +
	"\n" +
	"`orc prose <path>` measures a file against this rule and names what it finds.\n" +
	"Run it on documentation before you call the work done."
