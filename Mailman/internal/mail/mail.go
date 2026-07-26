// Package mail holds the one thing Mailman is about: a message.
//
// A Message is immutable and write-once. Nothing in Mailman edits one after it
// is sent, which is why this package has constructors and accessors and no
// setters at all — everything that changes about a message (read, archived)
// lives elsewhere, keyed by its identifier.
//
// The codec is the interesting part. A body is arbitrary markdown written by
// an agent, so it may contain anything at all, including text that looks
// exactly like this format's own header. Encode therefore states the body's
// length and Decode consumes precisely that many bytes rather than scanning for
// a delimiter. A body cannot forge a header, however hard it tries.
package mail

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// Format is the codec's magic line. A version bump changes it, and Decode
// refuses anything it does not recognise rather than guessing.
const Format = "mailman/1"

// Bounds on a message. Each is a refusal rather than a truncation: silently
// shortening someone's mail is worse than declining to send it.
const (
	// MaxBody is generous for markdown and small enough that a runaway agent
	// cannot fill a disk with one send.
	MaxBody = 16 << 20

	// MaxSubject is measured in bytes. A subject is a table cell, not a document.
	MaxSubject = 256

	// MaxRecipients bounds one send. Beyond this it is a broadcast, and a
	// broadcast should be a deliberate loop the caller can see.
	MaxRecipients = 256

	// MaxHeaderLine bounds a single header line while decoding, so a damaged
	// file cannot be read into memory as one enormous "line".
	MaxHeaderLine = 64 << 10

	// MaxIndex bounds a conversation's length.
	MaxIndex = 1 << 20
)

// Kind distinguishes ordinary mail from the notices Mailman sends on its own
// behalf. A cc notice is a real message — that is what makes `check` work on
// it — but it is rendered differently and never carries a body the sender
// wrote.
type Kind int

const (
	// Ordinary is mail one user sent another.
	Ordinary Kind = iota
	// Notice is a cc notice: Mailman announcing that someone joined a thread.
	Notice
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	switch k {
	case Ordinary:
		return "mail"
	case Notice:
		return "cc"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Valid reports whether k is a defined kind.
func (k Kind) Valid() bool { return k == Ordinary || k == Notice }

// ParseKind reads a stored kind.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "mail":
		return Ordinary, nil
	case "cc":
		return Notice, nil
	default:
		return 0, fault.Parse{Reason: fmt.Sprintf("unknown message kind %q, want \"mail\" or \"cc\"", s)}
	}
}

// idLen is the exact length of an identifier: sixteen hex digits of microsecond
// timestamp, a dash, then eight of randomness.
const idLen = 16 + 1 + 8

// ID identifies a message uniquely across every process writing to a store.
//
// The timestamp prefix is fixed-width so that lexical order is send order —
// which means a directory listing is already sorted, and no index is needed to
// answer "what is the most recent". The random suffix is what makes two agents
// sending in the same microsecond safe without any coordination between them.
type ID struct {
	s string
}

// NewID mints an identifier for a message sent at at.
//
// entropy is injected so tests can pin an identifier; a short read from it is a
// hard failure, because an identifier with predictable low bits would collide
// exactly when two agents send at once, which is the case it exists for.
func NewID(at time.Time, entropy io.Reader) (ID, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	at = clock.Normalise(at)
	if at.Before(clock.Earliest) || !at.Before(clock.Latest) {
		return ID{}, fault.Internal{Where: "mail.NewID", Detail: fmt.Sprintf("send time %s is out of range", at.Format(clock.Layout))}
	}

	micros := at.UnixMicro()
	if micros < 0 {
		return ID{}, fault.Internal{Where: "mail.NewID", Detail: fmt.Sprintf("send time %s is before the epoch", at.Format(clock.Layout))}
	}

	var suffix [4]byte
	if _, err := io.ReadFull(entropy, suffix[:]); err != nil {
		return ID{}, fault.IO{Op: "read entropy for", Path: "message id", Err: err}
	}

	id := ID{s: fmt.Sprintf("%016x-%02x%02x%02x%02x", micros, suffix[0], suffix[1], suffix[2], suffix[3])}
	if err := id.validate(); err != nil {
		return ID{}, err
	}
	return id, nil
}

// ParseID validates a stored identifier.
func ParseID(s string) (ID, error) {
	id := ID{s: s}
	if err := id.validate(); err != nil {
		return ID{}, fault.Parse{Reason: fmt.Sprintf("bad message id %q: %s", s, reasonOf(err))}
	}
	return id, nil
}

func (id ID) validate() error {
	const where = "mail.ID"
	if err := fault.Check(len(id.s) == idLen, where, "id %q is %d characters, want %d", id.s, len(id.s), idLen); err != nil {
		return err
	}
	if err := fault.Check(id.s[16] == '-', where, "id %q has no dash at position 17", id.s); err != nil {
		return err
	}
	for i := 0; i < len(id.s); i++ {
		if i == 16 {
			continue
		}
		c := id.s[i]
		if err := fault.Check((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
			where, "id %q has non-hex %q at position %d", id.s, rune(c), i+1); err != nil {
			return err
		}
	}
	return nil
}

// String returns the identifier.
func (id ID) String() string { return id.s }

// Short returns the identifier abbreviated for a narrow column. The random
// suffix is kept and the timestamp head dropped, since the suffix is what
// actually distinguishes two ids in the same listing.
func (id ID) Short() string {
	if len(id.s) != idLen {
		return id.s
	}
	return id.s[len(id.s)-8:]
}

// Zero reports whether the identifier was never constructed.
func (id ID) Zero() bool { return id.s == "" }

// Sent recovers the instant encoded in the identifier. It is a cross-check
// against the message's own sent field, not a replacement for it.
func (id ID) Sent() (time.Time, error) {
	if err := id.validate(); err != nil {
		return time.Time{}, err
	}
	micros, err := strconv.ParseInt(id.s[:16], 16, 64)
	if err != nil {
		return time.Time{}, fault.Internal{Where: "mail.ID.Sent", Detail: "timestamp prefix is not hex: " + err.Error()}
	}
	return clock.Normalise(time.UnixMicro(micros)), nil
}

// Compare orders identifiers, which is send order.
func (id ID) Compare(other ID) int { return strings.Compare(id.s, other.s) }

// Envelope is everything about a message except its body. It is the part that
// gets indexed, queried, and rendered in a table.
type Envelope struct {
	id      ID
	kind    Kind
	from    user.Name
	to      []user.Name
	cc      []user.Name
	subject string
	convo   ID  // zero when the message is standalone
	index   int // 1-based position within convo; 0 when standalone
	sent    time.Time
}

// Message is an envelope and its markdown body.
type Message struct {
	env  Envelope
	body []byte
}

// New builds a message, validating every part.
//
// Recipients are normalised and deduplicated by the caller's user.ParseList
// before they arrive here; New rechecks anyway, because the cost is a loop over
// a short slice and the alternative is trusting a caller that may be a future
// version of some other package.
func New(id ID, kind Kind, from user.Name, to, cc []user.Name, subject string, convo ID, index int, sent time.Time, body []byte) (Message, error) {
	m := Message{
		env: Envelope{
			id:      id,
			kind:    kind,
			from:    from,
			to:      slices.Clone(to),
			cc:      slices.Clone(cc),
			subject: subject,
			convo:   convo,
			index:   index,
			sent:    clock.Normalise(sent),
		},
		body: slices.Clone(body),
	}
	if err := m.validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// CheckSubject validates a subject line.
//
// Control characters are refused rather than escaped. A subject travels through
// a single header line and through every table Mailman draws, and a value that
// needs escaping in one place and sanitising in another is a value that will
// eventually be handled correctly in only one of them.
func CheckSubject(s string) error {
	switch {
	case s == "":
		return fault.Usage{Reason: "subject is empty"}
	case len(s) > MaxSubject:
		return fault.Usage{Reason: fmt.Sprintf("subject is %d bytes, limit is %d", len(s), MaxSubject)}
	case !utf8.ValidString(s):
		return fault.Usage{Reason: "subject is not valid UTF-8"}
	case strings.TrimSpace(s) == "":
		return fault.Usage{Reason: "subject is only whitespace"}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fault.Usage{Reason: fmt.Sprintf("subject contains a control character (%U)", r)}
		}
	}
	return nil
}

// CheckBody validates a message body.
func CheckBody(body []byte) error {
	switch {
	case len(body) > MaxBody:
		return fault.Usage{Reason: fmt.Sprintf("body is %d bytes, limit is %d", len(body), MaxBody)}
	case !utf8.Valid(body):
		return fault.Usage{Reason: "body is not valid UTF-8"}
	}
	if i := bytes.IndexByte(body, 0); i >= 0 {
		return fault.Usage{Reason: fmt.Sprintf("body contains a NUL byte at offset %d", i)}
	}
	return nil
}

func (m Message) validate() error {
	const where = "mail.Message"
	e := m.env

	if err := e.id.validate(); err != nil {
		return err
	}
	if err := fault.Check(e.kind.Valid(), where, "kind %d is not defined", int(e.kind)); err != nil {
		return err
	}
	if e.from.Zero() {
		return fault.Internal{Where: where, Detail: "sender is unset"}
	}
	if err := CheckSubject(e.subject); err != nil {
		return err
	}
	if err := CheckBody(m.body); err != nil {
		return err
	}

	if err := fault.Check(len(e.to) > 0, where, "message %s has no recipients", e.id); err != nil {
		return err
	}
	if err := fault.Check(len(e.to)+len(e.cc) <= MaxRecipients, where,
		"message %s has %d recipients, limit is %d", e.id, len(e.to)+len(e.cc), MaxRecipients); err != nil {
		return err
	}

	// One name must not appear twice across to and cc, or the message would be
	// delivered twice and counted twice as unread.
	seen := make(map[string]bool, len(e.to)+len(e.cc))
	for _, list := range [][]user.Name{e.to, e.cc} {
		for _, n := range list {
			if n.Zero() {
				return fault.Internal{Where: where, Detail: "recipient is unset"}
			}
			if seen[n.String()] {
				return fault.Internal{Where: where, Detail: fmt.Sprintf("recipient %s appears twice in %s", n, e.id)}
			}
			seen[n.String()] = true
		}
	}

	// A conversation reference is all-or-nothing: an index without a
	// conversation cannot be resolved, and a conversation without an index
	// cannot be ordered.
	switch {
	case e.convo.Zero() && e.index != 0:
		return fault.Internal{Where: where, Detail: fmt.Sprintf("message %s has index %d but no conversation", e.id, e.index)}
	case !e.convo.Zero():
		if err := e.convo.validate(); err != nil {
			return err
		}
		if err := fault.Check(e.index >= 1 && e.index <= MaxIndex, where,
			"message %s has conversation index %d, want 1..%d", e.id, e.index, MaxIndex); err != nil {
			return err
		}
	}

	if err := fault.Check(!e.sent.IsZero(), where, "message %s has no send time", e.id); err != nil {
		return err
	}
	if err := fault.Check(!e.sent.Before(clock.Earliest) && e.sent.Before(clock.Latest), where,
		"message %s was sent at %s, outside the allowed range", e.id, e.sent.Format(clock.Layout)); err != nil {
		return err
	}

	// The identifier encodes its own send time; a disagreement means one of the
	// two was rewritten by hand.
	stamped, err := e.id.Sent()
	if err != nil {
		return err
	}
	return fault.Check(stamped.Equal(e.sent), where,
		"message %s is stamped %s but says it was sent at %s",
		e.id, stamped.Format(clock.Layout), e.sent.Format(clock.Layout))
}

// ID returns the message's identifier.
func (m Message) ID() ID { return m.env.id }

// Kind returns whether this is ordinary mail or a notice.
func (m Message) Kind() Kind { return m.env.kind }

// From returns the sender.
func (m Message) From() user.Name { return m.env.from }

// To returns a copy of the direct recipients.
func (m Message) To() []user.Name { return slices.Clone(m.env.to) }

// CC returns a copy of the carbon-copied recipients.
func (m Message) CC() []user.Name { return slices.Clone(m.env.cc) }

// Recipients returns every user the message was delivered to, direct and
// copied, in that order.
func (m Message) Recipients() []user.Name {
	out := make([]user.Name, 0, len(m.env.to)+len(m.env.cc))
	out = append(out, m.env.to...)
	return append(out, m.env.cc...)
}

// Participants returns everyone involved: the sender and every recipient. It is
// what a reply addresses and what a cc notice announces to.
func (m Message) Participants() []user.Name {
	out := make([]user.Name, 0, 1+len(m.env.to)+len(m.env.cc))
	out = append(out, m.env.from)
	for _, n := range m.Recipients() {
		if !user.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// Subject returns the subject line.
func (m Message) Subject() string { return m.env.subject }

// Convo returns the conversation the message belongs to, if any.
func (m Message) Convo() (ID, bool) { return m.env.convo, !m.env.convo.Zero() }

// Index returns the message's 1-based position in its conversation, or 0.
func (m Message) Index() int { return m.env.index }

// Sent returns when the message was sent.
func (m Message) Sent() time.Time { return m.env.sent }

// Body returns a copy of the markdown body.
func (m Message) Body() []byte { return slices.Clone(m.body) }

// BodyString returns the body as text.
func (m Message) BodyString() string { return string(m.body) }

// InConvo returns a copy of the message rebound to a conversation.
//
// This is the one shape-changing operation, and it exists for exactly one case:
// `reply` roots a conversation on a message that was standalone when it was
// sent, so the root's own record has to learn its conversation. It returns a
// new value — the original is untouched — and it refuses to move a message that
// already belongs somewhere, because that would rewrite history rather than
// record it.
func (m Message) InConvo(convo ID, index int) (Message, error) {
	if !m.env.convo.Zero() {
		return Message{}, fault.Conflict{
			Path:   m.env.id.String(),
			Reason: fmt.Sprintf("already belongs to conversation %s", m.env.convo),
		}
	}
	out := m
	out.env.to = slices.Clone(m.env.to)
	out.env.cc = slices.Clone(m.env.cc)
	out.body = slices.Clone(m.body)
	out.env.convo = convo
	out.env.index = index
	if err := out.validate(); err != nil {
		return Message{}, err
	}
	return out, nil
}

// reasonOf pulls the human part out of an Internal error, so a validation
// failure inside a parse helper reads as a parse problem rather than as a bug
// report. The distinction matters: bad bytes on disk are not a defect in
// Mailman.
func reasonOf(err error) string {
	var internal fault.Internal
	if ok := asInternal(err, &internal); ok {
		return internal.Detail
	}
	return err.Error()
}

func asInternal(err error, out *fault.Internal) bool {
	if e, ok := err.(fault.Internal); ok {
		*out = e
		return true
	}
	return false
}
