package cli

import (
	"fmt"
	"io"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/query"
	"orc/mailman/internal/render"
	"orc/mailman/internal/store"
	"orc/mailman/internal/view"
)

// inbox lists unread mail, or everything with --all.
func (a App) inbox(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: fmt.Sprintf("inbox takes no arguments, got %d", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	box, err := s.mailbox()
	if err != nil {
		return err
	}

	scope, title := view.Inbox, "inbox · "+s.who.String()
	switch {
	case f.all && f.sent:
		return fault.Usage{Reason: "--all and --sent select different things; pass one"}
	case f.all:
		scope, title = view.All, "inbox · "+s.who.String()+" · all"
	case f.sent:
		// The sender's own copies, which the inbox hides so that writing mail
		// does not inflate the count of mail waiting to be read.
		scope, title = view.Sent, "sent · "+s.who.String()
	}
	rows := box.In(scope)
	if f.json {
		return a.emitJSON(messagesJSON(rows, s.who, !f.noBodies))
	}
	out, err := render.Listing(title, rows, s.palette, a.Width)
	if err != nil {
		return err
	}
	return a.write(out)
}

// open shows the most recent message a query matches.
//
// The reference says "most recent", so `open` narrows where every other
// query command acts on the whole match set. That narrowing is real and
// deliberate, but it is never silent: when more than one message matched, the
// count and the way to see the rest go to stderr.
func (a App) open(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("open takes one query, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	rows, q, err := s.match(args[0], view.Everything)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fault.NotFound{Target: args[0]}
	}

	row, count, err := view.Latest(rows)
	if err != nil {
		return err
	}
	if count > 1 {
		// The narrowing is what the reference asks for, but it must never be
		// silent: a caller who meant a different one of these should be able to
		// see that there were others.
		a.note("%d messages matched %s; showing the most recent (id %d). narrow the query, or use `mailman open 'id=\"N\"'`, to reach the others",
			count, q.String(), row.PUID())
	}

	out, err := render.Card(row, s.palette, a.Width)
	if err != nil {
		return err
	}
	return a.write(out)
}

// convo shows one conversation as a thread.
func (a App) convo(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("convo takes one conversation identifier, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	id, err := mail.ParseID(args[0])
	if err != nil {
		return fault.Usage{Reason: fmt.Sprintf("%q is not a conversation identifier", args[0])}
	}

	// Membership is the access check, and it grants the whole thread — including
	// messages sent before the caller was added. A non-member gets not-found
	// rather than a refusal, so this cannot be used to discover conversations.
	c, rows, err := view.LoadThread(s.store, id, s.who)
	if err != nil {
		return err
	}

	if !f.all {
		var unread []view.Row
		for _, r := range rows {
			if r.Unread() {
				unread = append(unread, r)
			}
		}
		// A thread with nothing unread would render as an empty diagram, which
		// reads as "this conversation is empty" rather than "you have read it".
		if len(unread) > 0 {
			rows = unread
		}
	}

	if f.json {
		return a.emitJSON(struct {
			jsonThread
			Messages []jsonMessage `json:"messages"`
		}{threadJSON(c), messagesJSON(rows, s.who, !f.noBodies)})
	}
	out, err := render.Thread(c.Title(), c.ID().String(), rows, s.palette, a.Width)
	if err != nil {
		return err
	}
	return a.write(out)
}

// send delivers a new message to every recipient.
func (a App) send(args []string, f flags) error {
	if len(args) < 3 {
		return fault.Usage{Reason: "send takes a subject, at least one recipient, and a body"}
	}
	subject := args[0]
	to := args[1 : len(args)-1]
	rawBody := args[len(args)-1]

	s, err := a.begin()
	if err != nil {
		return err
	}
	body, err := a.body(rawBody)
	if err != nil {
		return err
	}

	recipients, err := user.ParseList(to)
	if err != nil {
		return err
	}
	if err := s.requireUsers(recipients); err != nil {
		return err
	}

	m, err := s.compose(mail.Ordinary, recipients, nil, subject, body, mail.ID{}, 0)
	if err != nil {
		return err
	}
	if err := s.store.Put(m); err != nil {
		return err
	}
	delivered, err := s.deliver(m)
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("sent %s to %s", m.ID().Short(), strings.Join(delivered, ", ")))
}

// reply answers a message, rooting a conversation on it if it does not have one.
func (a App) reply(args []string, f flags) error {
	if len(args) != 3 {
		return fault.Usage{Reason: fmt.Sprintf("reply takes a query, a subject, and a body, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	body, err := a.body(args[2])
	if err != nil {
		return err
	}

	parent, err := s.one(args[0], view.Everything)
	if err != nil {
		return err
	}

	// Root the conversation if the parent is standalone. Rooting is idempotent
	// in the store, so two agents replying at the same moment both succeed and
	// join the same thread.
	convo, threaded := parent.Message.Convo()
	if !threaded {
		c, err := s.store.OpenConvo(parent.Message, parent.Message.Subject())
		if err != nil {
			return err
		}
		convo = c.ID()

		bound, err := parent.Message.InConvo(convo, 1)
		if err != nil {
			return err
		}
		if err := s.store.Replace(bound); err != nil {
			// The conversation exists and the reply can still join it; only the
			// parent's own record is behind. Saying so is better than failing a
			// reply that is otherwise perfectly deliverable.
			a.note("the conversation was created, but the message it was rooted on could not be updated: %v", err)
		}
	}

	// A reply goes to everyone in the *conversation*, minus the sender — not to
	// the parent message's recipients. The two differ exactly when someone was
	// cc'd into the thread, and addressing the parent's list would silently drop
	// them from the conversation they were just added to.
	thread, err := s.store.Convo(convo)
	if err != nil {
		return err
	}
	recipients, err := s.reachable(s.others(thread.Participants()))
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return fault.Usage{Reason: "that conversation has no other participants to reply to"}
	}

	m, err := s.compose(mail.Ordinary, recipients, nil, args[1], body, convo, 1)
	if err != nil {
		return err
	}
	index, err := s.store.AddToConvo(convo, m.ID())
	if err != nil {
		return err
	}
	// The index is known only once the conversation grants it, so the message
	// is composed a second time with the real position rather than stored with
	// a placeholder that would then have to be corrected.
	m, err = s.recompose(m, convo, index)
	if err != nil {
		return err
	}
	if err := s.store.Put(m); err != nil {
		return err
	}
	delivered, err := s.deliver(m)
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("replied %s (#%d in %s) to %s", m.ID().Short(), index, convo.Short(), strings.Join(delivered, ", ")))
}

// archive archives every match, or shows the archive when given no query.
func (a App) archive(args []string, f flags) error {
	if len(args) > 1 {
		return fault.Usage{Reason: fmt.Sprintf("archive takes at most one query, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		box, err := s.mailbox()
		if err != nil {
			return err
		}
		rows := box.In(view.Archive)
		if f.json {
			return a.emitJSON(messagesJSON(rows, s.who, !f.noBodies))
		}
		out, err := render.Listing("archive · "+s.who.String(), rows, s.palette, a.Width)
		if err != nil {
			return err
		}
		return a.write(out)
	}

	rows, _, err := s.match(args[0], view.All)
	if err != nil {
		return err
	}
	// A query that matched nothing is reported rather than answered with
	// "archived 0 messages". Silently doing nothing is exactly how a caller
	// comes to believe their mail was filed when it was not.
	if len(rows) == 0 {
		return fault.NotFound{Target: args[0]}
	}
	for _, r := range rows {
		if err := s.store.Mark(s.who, r.Message.ID(), store.OpArchive); err != nil {
			return err
		}
	}
	return a.say(fmt.Sprintf("archived %s", plural(len(rows), "message")))
}

// prune deletes archived mail permanently.
//
// It is the only irreversible command, so it gets the only confirmation:
// nothing outside the archive may be matched, the full list is printed first,
// and a non-interactive caller — which every agent is — must pass --yes.
func (a App) prune(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("prune takes one query, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	all, q, err := s.match(args[0], view.Everything)
	if err != nil {
		return err
	}
	var rows []view.Row
	var live int
	for _, r := range all {
		if r.Archived() {
			rows = append(rows, r)
			continue
		}
		live++
	}
	if live > 0 {
		return fault.Usage{Reason: fmt.Sprintf(
			"%s also matches %s that %s not archived; prune only deletes archived mail.\n  archive them first, or narrow the query with & archived=true",
			q.String(), plural(live, "message"), isAre(live))}
	}
	if len(rows) == 0 {
		return fault.NotFound{Target: args[0]}
	}

	if err := a.say(fmt.Sprintf("prune %s matching %s:", plural(len(rows), "archived message"), q.String())); err != nil {
		return err
	}
	for _, r := range rows {
		if err := a.say(fmt.Sprintf("  [%d] %s  %s  %s",
			r.PUID(), clock.Show(r.Sent()), r.Message.From(), r.Message.Subject())); err != nil {
			return err
		}
	}
	if !f.yes {
		return fault.Usage{Reason: "nothing was deleted; pass --yes to confirm"}
	}

	for _, r := range rows {
		// Journal first, then delete. A crash between the two leaves a message
		// nothing references, which `verify` can clean up; the other order
		// would leave a reference to a message that is gone.
		if err := s.store.Mark(s.who, r.Message.ID(), store.OpPrune); err != nil {
			return err
		}
		if err := s.deleteIfUnreferenced(r.Message); err != nil {
			return err
		}
	}
	return a.say(fmt.Sprintf("pruned %s", plural(len(rows), "message")))
}

// read marks every match read.
func (a App) read(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("read takes one query, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	rows, _, err := s.match(args[0], view.Everything)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fault.NotFound{Target: args[0]}
	}

	now := a.Clock.Now()
	for _, r := range rows {
		if err := s.store.Mark(s.who, r.Message.ID(), store.OpRead); err != nil {
			return err
		}
		// The receipt is what makes the read "visible for all recipients", as
		// the reference requires. It is written only for mail actually
		// addressed to the reader: a sender's own copy is read by definition,
		// and a receipt from a non-recipient would make `check` report a
		// reader who was never sent anything.
		if !user.Contains(r.Message.Recipients(), s.who) {
			continue
		}
		if err := s.store.PutReceipt(r.Message.ID(), s.who, now); err != nil {
			return err
		}
	}
	return a.say(fmt.Sprintf("marked %s read", plural(len(rows), "message")))
}

// check reports who has and has not read the matches.
func (a App) check(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("check takes one query, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	rows, _, err := s.match(args[0], view.Everything)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fault.NotFound{Target: args[0]}
	}

	if f.json {
		var out []jsonReceipt
		for _, r := range rows {
			statuses, err := view.Check(s.store, r.Message)
			if err != nil {
				return err
			}
			out = append(out, receiptsJSON(r.Message.ID().String(), statuses)...)
		}
		return a.emitJSON(out)
	}

	for i, r := range rows {
		statuses, err := view.Check(s.store, r.Message)
		if err != nil {
			return err
		}
		if i > 0 {
			if err := a.say(""); err != nil {
				return err
			}
		}
		out, err := render.Receipts(r.Message.Subject(), statuses, s.palette, a.Width)
		if err != nil {
			return err
		}
		if err := a.write(out); err != nil {
			return err
		}
	}
	return nil
}

// cc adds a user to a conversation.
//
// The addition travels as a real message of its own, which is what makes
// `mailman check` work on it, as the reference requires. Prior messages become
// visible to the new participant through `convo`; they are not backfilled into
// their inbox, because an unread count that includes history nobody sent them
// is not an unread count.
func (a App) cc(args []string, f flags) error {
	if len(args) != 2 {
		return fault.Usage{Reason: fmt.Sprintf("cc takes a query and a user, got %d arguments", len(args))}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	joiner, err := user.Parse(args[1])
	if err != nil {
		return err
	}
	if err := s.requireUsers([]user.Name{joiner}); err != nil {
		return err
	}

	rows, _, err := s.match(args[0], view.Everything)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fault.NotFound{Target: args[0]}
	}

	// The query must name one conversation. Adding someone to several threads
	// because a query was loose is a disclosure, not a convenience.
	convo, title, err := s.oneConvo(rows, args[0])
	if err != nil {
		return err
	}

	thread, err := s.store.Convo(convo)
	if err != nil {
		return err
	}
	if thread.HasMember(joiner) {
		return a.say(fmt.Sprintf("%s is already in this conversation", joiner))
	}

	// Membership is recorded before the notice is sent. That order matters: a
	// crash between the two leaves someone in the conversation without having
	// been told, which they will see the next time anyone replies. The other
	// order would leave a notice about a thread they cannot open.
	if err := s.store.AddParticipant(convo, joiner); err != nil {
		return err
	}

	existing, err := s.reachable(s.others(thread.Participants()))
	if err != nil {
		return err
	}
	recipients := append(existing, joiner)

	subject := "cc: " + joiner.String() + " added to " + title
	body := fmt.Sprintf("%s added %s to this conversation.\n", s.who, joiner)

	m, err := s.compose(mail.Notice, recipients, nil, subject, []byte(body), convo, 1)
	if err != nil {
		return err
	}
	index, err := s.store.AddToConvo(convo, m.ID())
	if err != nil {
		return err
	}
	m, err = s.recompose(m, convo, index)
	if err != nil {
		return err
	}
	if err := s.store.Put(m); err != nil {
		return err
	}
	if _, err := s.deliver(m); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("added %s to %s", joiner, convo.Short()))
}

// plural renders a count with its noun.
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// body reads a message body, from stdin when the argument is "-".
func (a App) body(raw string) ([]byte, error) {
	if raw != "-" {
		return []byte(raw), nil
	}
	if a.Stdin == nil {
		return nil, fault.Usage{Reason: `the body is "-" but no standard input is available`}
	}
	data, err := io.ReadAll(io.LimitReader(a.Stdin, mail.MaxBody+1))
	if err != nil {
		return nil, fault.IO{Op: "read standard input for", Path: "the message body", Err: err}
	}
	if len(data) > mail.MaxBody {
		return nil, fault.Usage{Reason: fmt.Sprintf("the body is larger than the %d byte limit", mail.MaxBody)}
	}
	return data, nil
}

// match resolves a query against the caller's mailbox.
func (s session) match(raw string, scope view.Scope) ([]view.Row, query.Query, error) {
	q, err := query.Parse(raw)
	if err != nil {
		return nil, query.Query{}, err
	}
	box, err := s.mailbox()
	if err != nil {
		return nil, query.Query{}, err
	}
	rows, err := box.Select(q, scope, s.now())
	if err != nil {
		return nil, query.Query{}, err
	}
	return rows, q, nil
}

// one resolves a query to exactly one message, refusing to guess.
func (s session) one(raw string, scope view.Scope) (view.Row, error) {
	rows, _, err := s.match(raw, scope)
	if err != nil {
		return view.Row{}, err
	}
	switch len(rows) {
	case 1:
		return rows[0], nil
	case 0:
		return view.Row{}, fault.NotFound{Target: raw}
	default:
		candidates := make([]string, 0, len(rows))
		for _, r := range rows {
			candidates = append(candidates, fmt.Sprintf(`id="%d"  %s  %s`, r.PUID(), r.Message.From(), r.Message.Subject()))
		}
		return view.Row{}, fault.Ambiguous{Target: raw, Candidates: candidates}
	}
}

// oneConvo checks that every matched row belongs to the same conversation.
func (s session) oneConvo(rows []view.Row, raw string) (mail.ID, string, error) {
	var convo mail.ID
	title := ""
	for _, r := range rows {
		id, threaded := r.Message.Convo()
		if !threaded {
			return mail.ID{}, "", fault.Usage{Reason: fmt.Sprintf(
				"message %d is not in a conversation; reply to it first so there is a conversation to add someone to", r.PUID())}
		}
		if convo.Zero() {
			convo, title = id, r.Title
			continue
		}
		if convo.String() != id.String() {
			return mail.ID{}, "", fault.Ambiguous{
				Target:     raw,
				Candidates: []string{convo.String(), id.String()},
			}
		}
	}
	if title == "" {
		title = "the conversation"
	}
	return convo, title, nil
}

// others returns everyone in a list except the caller.
func (s session) others(names []user.Name) []user.Name {
	out := make([]user.Name, 0, len(names))
	for _, n := range names {
		if n.String() != s.who.String() {
			out = append(out, n)
		}
	}
	return out
}

// reachable drops participants whose mailbox no longer exists, reporting each.
//
// A conversation outlives its members: `admin user remove` deletes a mailbox
// and leaves the mail alone, so a thread can name someone who is gone. Failing
// the whole reply because one long-departed participant cannot be written to
// would make the conversation unusable for everyone still in it.
func (s session) reachable(names []user.Name) ([]user.Name, error) {
	out := make([]user.Name, 0, len(names))
	for _, n := range names {
		ok, err := s.store.HasUser(n)
		if err != nil {
			return nil, err
		}
		if !ok {
			s.app.note("%s is in this conversation but has no mailbox any more, and was left out", n)
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// requireUsers refuses to address mail to a mailbox that does not exist, before
// anything is written. Mail addressed into a void is the failure this whole
// tool exists to avoid.
func (s session) requireUsers(names []user.Name) error {
	var missing []string
	for _, n := range names {
		ok, err := s.store.HasUser(n)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, n.String())
		}
	}
	if len(missing) > 0 {
		known, err := s.store.Users()
		if err != nil {
			return err
		}
		return fault.NotFound{
			Target: strings.Join(missing, ", "),
			Near:   user.Names(known),
		}
	}
	return nil
}

// compose builds a message from the caller.
func (s session) compose(kind mail.Kind, to, cc []user.Name, subject string, body []byte, convo mail.ID, index int) (mail.Message, error) {
	if err := mail.CheckSubject(subject); err != nil {
		return mail.Message{}, err
	}
	at := s.app.Clock.Now()
	id, err := mail.NewID(at, nil)
	if err != nil {
		return mail.Message{}, err
	}
	return mail.New(id, kind, s.who, to, cc, subject, convo, index, at, body)
}

// recompose rebuilds a message at a different position in its conversation,
// keeping its identifier and everything else.
func (s session) recompose(m mail.Message, convo mail.ID, index int) (mail.Message, error) {
	return mail.New(m.ID(), m.Kind(), m.From(), m.To(), m.CC(), m.Subject(), convo, index, m.Sent(), m.Body())
}

// deliver puts a message in every recipient's mailbox.
//
// A recipient whose delivery fails is reported and the rest still go: mail that
// reaches four of five people and says so is better than mail that reaches
// none because the fifth mailbox was unwritable.
func (s session) deliver(m mail.Message) ([]string, error) {
	var delivered []string
	var failed []string

	for _, name := range m.Recipients() {
		if _, err := s.store.Deliver(name, m.ID()); err != nil {
			s.app.note("could not deliver to %s: %v", name, err)
			failed = append(failed, name.String())
			continue
		}
		delivered = append(delivered, name.String())
	}

	if len(delivered) == 0 {
		return nil, fault.IO{
			Op:   "deliver",
			Path: m.ID().String(),
			Err:  fmt.Errorf("no recipient could be written to (%s)", strings.Join(failed, ", ")),
		}
	}

	// The sender keeps a copy of their own outgoing mail, already read.
	//
	// Without it a sent message exists in nobody's mailbox but its recipients',
	// so the sender cannot name it in a query — which would make `mailman
	// check` unable to answer the one question it exists for: who has read what
	// I sent. The copy is excluded from the inbox by view.Row.Mine, so it costs
	// the sender nothing to look at.
	if !user.Contains(m.Recipients(), s.who) {
		if _, err := s.store.Deliver(s.who, m.ID()); err != nil {
			s.app.note("your own copy of %s could not be filed, so `mailman check` will not find it: %v", m.ID().Short(), err)
		} else if err := s.store.Mark(s.who, m.ID(), store.OpRead); err != nil {
			s.app.note("your own copy of %s could not be marked read: %v", m.ID().Short(), err)
		}
	}
	return delivered, nil
}

// deleteIfUnreferenced removes a message once nobody's mailbox still holds it.
//
// A message belongs to everyone it was sent to. One recipient pruning it must
// not delete it out from under the others, so the file only goes when the last
// reference does.
func (s session) deleteIfUnreferenced(m mail.Message) error {
	for _, name := range m.Participants() {
		st, err := s.store.Replay(name)
		if err != nil {
			// A mailbox that cannot be read might still hold a reference, so the
			// message stays. Leaving a file behind is recoverable; deleting one
			// somebody still has in their inbox is not.
			s.app.note("keeping %s: %s's mailbox could not be checked: %v", m.ID().Short(), name, err)
			return nil
		}
		entry, ok := st.Lookup(m.ID())
		if ok && !entry.Pruned {
			return nil
		}
	}
	return s.store.Delete(m.ID())
}
