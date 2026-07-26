package cli

import (
	"encoding/json"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/store"
	"orc/mailman/internal/view"
)

// The `--json` projection.
//
// It exists so other Orc tools can read a mailbox without parsing the box-drawn
// tables the other renderers produce — a presentation format is a bad contract,
// and Communiqué needs a good one to mirror a mailbox to the web.
//
// Two rules keep it usable as a contract:
//
//   - It is a projection of the same view.Row every other command renders, so
//     JSON and the table can never disagree about what the mailbox holds.
//   - Fields are added, never repurposed or removed. A reader that ignores
//     what it does not recognise keeps working across a version of Mailman it
//     has not seen.

// jsonMessage is one message as one reader sees it.
type jsonMessage struct {
	PUID     int        `json:"puid"`
	ID       string     `json:"id"`
	Sent     time.Time  `json:"sent"`
	From     string     `json:"from"`
	To       []string   `json:"to"`
	CC       []string   `json:"cc,omitempty"`
	Subject  string     `json:"subject"`
	Convo    *jsonConvo `json:"convo,omitempty"`
	Read     bool       `json:"read"`
	Archived bool       `json:"archived"`
	Mine     bool       `json:"mine"`
	Filed    bool       `json:"filed"`
	Body     string     `json:"body,omitempty"`
}

// jsonConvo locates a message in its conversation.
type jsonConvo struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Index int    `json:"index"`
}

// jsonThread is a conversation as a whole.
type jsonThread struct {
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Members []string `json:"members,omitempty"`
	Count   int      `json:"count"`
}

// jsonReceipt says whether one recipient has read one message.
//
// At is a pointer because `omitempty` does nothing for a time.Time: a struct is
// never "empty" to encoding/json, so a value field would serialise the zero
// time and claim an unread message was read in year one.
type jsonReceipt struct {
	MID       string     `json:"mid"`
	Recipient string     `json:"recipient"`
	Read      bool       `json:"read"`
	At        *time.Time `json:"at,omitempty"`
}

// jsonUser is an account. There is no creation time: a mailbox is a directory,
// and Mailman has never had a reason to record when it appeared.
type jsonUser struct {
	Name string `json:"name"`
}

// messageJSON projects one row. Bodies are included only when asked for: an
// admin listing is metadata, and a caller that wants the contents of everyone's
// mail should have to say so.
func messageJSON(r view.Row, owner user.Name, body bool) jsonMessage {
	out := jsonMessage{
		PUID:     r.PUID(),
		ID:       r.Message.ID().String(),
		Sent:     r.Sent(),
		From:     r.Message.From().String(),
		To:       names(r.Message.To()),
		CC:       names(r.Message.CC()),
		Subject:  r.Message.Subject(),
		Read:     !r.Unread(),
		Archived: r.Archived(),
		Mine:     r.Mine(owner),
		Filed:    r.Filed,
	}
	if convo, ok := r.Message.Convo(); ok {
		out.Convo = &jsonConvo{ID: convo.String(), Title: r.Title, Index: r.Message.Index()}
	}
	if body {
		out.Body = r.Message.BodyString()
	}
	return out
}

func messagesJSON(rows []view.Row, owner user.Name, body bool) []jsonMessage {
	out := make([]jsonMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, messageJSON(r, owner, body))
	}
	return out
}

func threadJSON(c store.Convo) jsonThread {
	return jsonThread{
		ID: c.ID().String(), Title: c.Title(),
		Members: names(c.Participants()), Count: c.Len(),
	}
}

func receiptsJSON(mid string, statuses []view.Status) []jsonReceipt {
	out := make([]jsonReceipt, 0, len(statuses))
	for _, s := range statuses {
		r := jsonReceipt{MID: mid, Recipient: s.User.String(), Read: s.Read()}
		if s.Read() {
			at := s.ReadAt
			r.At = &at
		}
		out = append(out, r)
	}
	return out
}

func names(list []user.Name) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, n.String())
	}
	return out
}

// emitJSON writes one document, indented so a human can read it too and
// newline-terminated so a shell prompt lands where it should.
func (a App) emitJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.IO{Op: "encode", Path: "standard output", Err: err}
	}
	return a.write(string(data) + "\n")
}

// --- the whole-store view ------------------------------------------------

// jsonWhole is one message and everything the store knows about it.
//
// It is message-centric rather than mailbox-centric, unlike jsonMessage: the
// question an admin view answers is "who has this and have they read it", and
// answering that by listing the same message once per mailbox would make the
// reader do the join.
type jsonWhole struct {
	ID       string       `json:"id"`
	Sent     time.Time    `json:"sent"`
	From     string       `json:"from"`
	To       []string     `json:"to"`
	CC       []string     `json:"cc,omitempty"`
	Subject  string       `json:"subject"`
	Convo    *jsonConvo   `json:"convo,omitempty"`
	Holders  []jsonHolder `json:"holders"`
	Receipts []jsonSeen   `json:"receipts,omitempty"`
	Body     string       `json:"body,omitempty"`
}

// jsonHolder is one mailbox the message is in.
type jsonHolder struct {
	User     string `json:"user"`
	PUID     int    `json:"puid"`
	Read     bool   `json:"read"`
	Archived bool   `json:"archived"`
	Mine     bool   `json:"mine"`
}

// jsonSeen is one read receipt. At is never absent here — a receipt exists only
// once it has been read — so it is a value rather than a pointer.
type jsonSeen struct {
	User string    `json:"user"`
	At   time.Time `json:"at"`
}

func adminMailJSON(whole []view.Whole, bodies bool) []jsonWhole {
	out := make([]jsonWhole, 0, len(whole))
	for _, w := range whole {
		m := w.Message
		row := jsonWhole{
			ID:      m.ID().String(),
			Sent:    m.Sent(),
			From:    m.From().String(),
			To:      names(m.To()),
			CC:      names(m.CC()),
			Subject: m.Subject(),
		}
		if convo, ok := m.Convo(); ok {
			row.Convo = &jsonConvo{ID: convo.String(), Title: w.Title, Index: m.Index()}
		}
		if bodies {
			row.Body = string(m.Body())
		}
		for _, h := range w.Holders {
			row.Holders = append(row.Holders, jsonHolder{
				User: h.User.String(), PUID: h.PUID,
				Read: h.Read, Archived: h.Archived, Mine: h.Mine,
			})
		}
		for _, r := range w.Receipts {
			row.Receipts = append(row.Receipts, jsonSeen{User: r.User.String(), At: r.At})
		}
		out = append(out, row)
	}
	return out
}
