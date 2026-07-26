package source

import (
	"sort"
	"strings"
	"time"

	"orc/cq/internal/protocol"
)

// The shapes Mailman and Macmuffin emit under `--json`.
//
// These are cq's own types, not theirs. Decoding into them and mapping to
// protocol keeps the two tools free to name things as suits them, and keeps
// cq's wire format free to differ — which it does: Mailman calls a message id
// `id`, cq calls it `mid`, and neither should have to change for the other.
//
// Unknown fields are **accepted** here, unlike everywhere else in cq. The rule
// elsewhere is that an unknown field means the two ends disagree and guessing
// is unsafe; but both ends there are cq. Here the far end is a tool with its
// own release cycle, and refusing a field it added would mean every Mailman
// improvement breaks the mirror until cq catches up.

// wireMessage is Mailman's projection of one message.
type wireMessage struct {
	PUID     int        `json:"puid"`
	ID       string     `json:"id"`
	Sent     time.Time  `json:"sent"`
	From     string     `json:"from"`
	To       []string   `json:"to"`
	CC       []string   `json:"cc"`
	Subject  string     `json:"subject"`
	Convo    *wireConvo `json:"convo"`
	Read     bool       `json:"read"`
	Archived bool       `json:"archived"`
	Mine     bool       `json:"mine"`
	Filed    bool       `json:"filed"`
	Body     string     `json:"body"`
}

type wireConvo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Index int    `json:"index"`
}

type wireUser struct {
	Name string `json:"name"`
}

type wireReceipt struct {
	MID       string     `json:"mid"`
	Recipient string     `json:"recipient"`
	Read      bool       `json:"read"`
	At        *time.Time `json:"at"`
}

// wireTask is Macmuffin's projection of one task.
type wireTask struct {
	Name          string   `json:"name"`
	Author        string   `json:"author"`
	Owner         string   `json:"owner"`
	Collaborators []string `json:"collaborators"`
	Priority      int      `json:"priority"`
	Difficulty    int      `json:"difficulty"`
	Status        int      `json:"status"`
	Done          int      `json:"done"`
	Total         int      `json:"total"`
	Draft         bool     `json:"draft"`
	Completed     bool     `json:"completed"`
	Scope         []string `json:"scope"`
	Worktree      string   `json:"worktree"`
	// Subtasks arrive only from `muff info`; the board omits them.
	Subtasks []wireSubtask `json:"subtasks"`
}

type wireSubtask struct {
	Name string `json:"name"`
	Done bool   `json:"done"`
}

// --- mapping -------------------------------------------------------------

func (m wireMessage) protocol(body bool) protocol.Message {
	out := protocol.Message{
		PUID: m.PUID, MID: m.ID, Sent: m.Sent, From: m.From,
		To: m.To, CC: m.CC, Subject: m.Subject,
		Read: m.Read, Archived: m.Archived,
	}
	if m.Convo != nil {
		out.Convo = protocol.ConvoRef{UID: m.Convo.ID, Title: m.Convo.Title, Index: m.Convo.Index}
	}
	if body {
		out.Body = m.Body
	}
	return out
}

func (r wireReceipt) protocol() protocol.Receipt {
	out := protocol.Receipt{MID: r.MID, Recipient: r.Recipient, Read: r.Read}
	if r.Read && r.At != nil {
		at := *r.At
		out.At = &at
	}
	return out
}

func (u wireUser) protocol() protocol.AdminUser {
	return protocol.AdminUser{Name: u.Name}
}

// protocol maps a task, clamping the scales rather than refusing.
//
// Macmuffin reports 0 for a score nobody has set, and cq's scales start at 1.
// A task nobody has scored is a real task, so it is shown at the bottom of the
// board rather than dropped from it.
func (t wireTask) protocol() protocol.Task {
	return protocol.Task{
		Name:          t.Name,
		Owner:         t.Owner,
		Collaborators: t.Collaborators,
		Priority:      clampScore(t.Priority),
		Difficulty:    clampScore(t.Difficulty),
		Status:        clampStatus(t.Status),
		Done:          t.Done,
		Total:         t.Total,
		Draft:         t.Draft,
		Scope:         t.Scope,
		Worktree:      t.Worktree,
	}
}

func clampScore(n int) int {
	switch {
	case n < 1:
		return 1
	case n > 5:
		return 5
	default:
		return n
	}
}

// clampStatus maps Macmuffin's "unreported" (0) onto cq's "nominal" (3), which
// is what an unreported task looks like on a board: not broken, not done, no
// claim either way.
func clampStatus(n int) int {
	switch {
	case n < 1 || n > 4:
		return 3
	default:
		return n
	}
}

// messages projects a listing.
func messages(msgs []wireMessage, body bool) []protocol.Message {
	out := make([]protocol.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.protocol(body))
	}
	return out
}

// wireThread is what `mailman convo --json` answers with: the conversation
// itself, and the messages in it.
type wireThread struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Members  []string      `json:"members"`
	Count    int           `json:"count"`
	Messages []wireMessage `json:"messages"`
}

func (t wireThread) protocol() protocol.Convo {
	members := make([]string, 0, len(t.Members))
	for _, m := range t.Members {
		members = append(members, normaliseName(m))
	}
	sort.Strings(members)
	return protocol.Convo{UID: t.ID, Title: t.Title, Members: members, Count: t.Count}
}

// convoUIDs collects the conversations the mail mentions, in the order they are
// first seen.
//
// Only the identifiers. The conversation *itself* is asked for separately,
// because Mailman knows things about a thread that its messages do not: a
// conversation cq holds two messages of may have five, and reporting two would
// be cq mistaking its own view for the truth.
//
// The lists are taken together because a thread does not respect them: a reply
// sits in the sent list while the message it answers sits in the inbox.
func convoUIDs(lists ...[]wireMessage) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, m := range list {
			if m.Convo == nil || m.Convo.ID == "" || seen[m.Convo.ID] {
				continue
			}
			seen[m.Convo.ID] = true
			out = append(out, m.Convo.ID)
		}
	}
	return out
}

// convosFrom is the fallback: what can be said about a thread from the messages
// alone, when Mailman cannot be asked.
//
// It undercounts, which is why it is second choice. It is still much better
// than dropping the conversation, since the title is what the mailbox shows.
func convosFrom(lists ...[]wireMessage) []protocol.Convo {
	var order []string
	byUID := map[string]*protocol.Convo{}
	members := map[string]map[string]bool{}

	for _, list := range lists {
		for _, m := range list {
			if m.Convo == nil || m.Convo.ID == "" {
				continue
			}
			uid := m.Convo.ID
			c, ok := byUID[uid]
			if !ok {
				order = append(order, uid)
				c = &protocol.Convo{UID: uid, Title: m.Convo.Title}
				byUID[uid], members[uid] = c, map[string]bool{}
			}
			c.Count++
			// A later message may carry a title an earlier one lacked.
			if c.Title == "" {
				c.Title = m.Convo.Title
			}
			for _, who := range append([]string{m.From}, append(m.To, m.CC...)...) {
				members[uid][normaliseName(who)] = true
			}
		}
	}

	out := make([]protocol.Convo, 0, len(order))
	for _, uid := range order {
		c := byUID[uid]
		c.Members = sortedKeys(members[uid])
		out = append(out, *c)
	}
	return out
}

// sortedKeys gives the membership a stable order, so two syncs of an unchanged
// conversation produce identical snapshots and the interface does not flicker.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normaliseName lowercases and trims a name the way every Orc tool does, so a
// value that only differs in case does not read as a different account.
func normaliseName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// --- the whole-store view ------------------------------------------------

// wireWhole is one message as `mailman admin mail` reports it: the message, the
// mailboxes that hold it, and the receipts against it.
type wireWhole struct {
	ID       string       `json:"id"`
	Sent     time.Time    `json:"sent"`
	From     string       `json:"from"`
	To       []string     `json:"to"`
	CC       []string     `json:"cc"`
	Subject  string       `json:"subject"`
	Convo    *wireConvo   `json:"convo"`
	Holders  []wireHolder `json:"holders"`
	Receipts []wireSeen   `json:"receipts"`
	Body     string       `json:"body"`
}

type wireHolder struct {
	User     string `json:"user"`
	PUID     int    `json:"puid"`
	Read     bool   `json:"read"`
	Archived bool   `json:"archived"`
	Mine     bool   `json:"mine"`
}

type wireSeen struct {
	User string    `json:"user"`
	At   time.Time `json:"at"`
}

// protocol maps one message for the panel.
//
// The read and archived flags are the *sender's* view — cq's Message has one of
// each, and a message held by four people has four answers. The panel's job is
// to show what exists and who has seen it, and the per-holder detail lives in
// the receipts, so this reports the message rather than any one reading of it.
func (w wireWhole) protocol(body bool) protocol.Message {
	out := protocol.Message{
		PUID:    puidOf(w.Holders),
		MID:     w.ID,
		Sent:    w.Sent,
		From:    normaliseName(w.From),
		To:      normaliseNames(w.To),
		CC:      normaliseNames(w.CC),
		Subject: w.Subject,
		Read:    allRead(w.Holders),
	}
	if w.Convo != nil {
		out.Convo = protocol.ConvoRef{UID: w.Convo.ID, Title: w.Convo.Title, Index: w.Convo.Index}
	}
	if body {
		out.Body = w.Body
	}
	return out
}

// receipts reports one entry per holder who was sent the message, read or not.
//
// The panel's question is "who has this and have they seen it", so a holder who
// has not read it is an answer, not an absence. The sender's own copy is not a
// delivery and is left out.
func (w wireWhole) receipts() []protocol.Receipt {
	seen := map[string]time.Time{}
	for _, r := range w.Receipts {
		seen[normaliseName(r.User)] = r.At
	}

	out := make([]protocol.Receipt, 0, len(w.Holders))
	for _, h := range w.Holders {
		if h.Mine {
			continue
		}
		who := normaliseName(h.User)
		r := protocol.Receipt{MID: w.ID, Recipient: who}
		if at, ok := seen[who]; ok && !at.IsZero() {
			when := at
			r.Read, r.At = true, &when
		}
		out = append(out, r)
	}
	return out
}

// puidOf takes the sender's number for the message, falling back to the first
// holder's. A puid is per-mailbox, so a whole-store view has no single right
// answer; the sender's is the one the operator is most likely to recognise.
func puidOf(holders []wireHolder) int {
	for _, h := range holders {
		if h.Mine {
			return h.PUID
		}
	}
	if len(holders) > 0 {
		return holders[0].PUID
	}
	return 0
}

// allRead reports whether every recipient has read it, which is the only
// reading of "read" that means anything for a message held by several people.
func allRead(holders []wireHolder) bool {
	any := false
	for _, h := range holders {
		if h.Mine {
			continue
		}
		any = true
		if !h.Read {
			return false
		}
	}
	return any
}

func normaliseNames(list []string) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, normaliseName(n))
	}
	return out
}
