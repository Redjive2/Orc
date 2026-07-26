package mail

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// The header keys, in the order Encode writes them. Order is fixed so that
// encoding is deterministic and a stored message can be diffed against a
// re-encoding of itself.
const (
	keyID      = "id"
	keyKind    = "kind"
	keyFrom    = "from"
	keyTo      = "to"
	keyCC      = "cc"
	keySubject = "subject"
	keyConvo   = "convo"
	keyIndex   = "index"
	keySent    = "sent"
	keyBytes   = "bytes"
)

// Encode renders a message for storage.
//
// The layout is a magic line, then one "key: value" per line, then a blank
// line, then the body verbatim. The final header is always the byte count, and
// it is what makes the body unambiguous: a reader consumes exactly that many
// bytes and never searches for a terminator, so a body containing this format's
// own magic line, or a thousand blank lines, decodes back to itself.
func Encode(m Message) ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	e := m.env

	var b bytes.Buffer
	b.Grow(512 + len(m.body))

	b.WriteString(Format)
	b.WriteByte('\n')

	write := func(key, value string) {
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteByte('\n')
	}

	write(keyID, e.id.String())
	write(keyKind, e.kind.String())
	write(keyFrom, e.from.String())
	write(keyTo, strings.Join(user.Names(e.to), ", "))
	if len(e.cc) > 0 {
		write(keyCC, strings.Join(user.Names(e.cc), ", "))
	}
	write(keySubject, e.subject)
	if !e.convo.Zero() {
		write(keyConvo, e.convo.String())
		write(keyIndex, strconv.Itoa(e.index))
	}
	write(keySent, clock.Format(e.sent))
	write(keyBytes, strconv.Itoa(len(m.body)))

	b.WriteByte('\n')
	b.Write(m.body)

	out := b.Bytes()
	// Encoding is the last chance to notice that a value slipped past
	// validation and produced something that will not read back. Round-tripping
	// here costs one parse per send and makes an unreadable message impossible
	// to write rather than merely unlikely.
	if _, err := Decode("<encoded>", out); err != nil {
		return nil, fault.Internal{Where: "mail.Encode", Detail: fmt.Sprintf("message %s does not decode back: %v", e.id, err)}
	}
	return out, nil
}

// field is one header line as read, kept with its line number so every
// complaint about it can say where it was.
type field struct {
	value string
	line  int
}

// Decode reads a stored message, validating every field.
//
// Everything is refused that could make two readers disagree about what the
// message says: an unknown key, a repeated key, a missing required key, a
// trailing byte after the stated body length, or a carriage return in the
// header (which means something converted the file's line endings and will
// therefore have corrupted the byte count too).
func Decode(path string, data []byte) (Message, error) {
	bad := func(line int, format string, args ...any) error {
		return fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	rest, line, err := expectMagic(path, data)
	if err != nil {
		return Message{}, err
	}

	fields := make(map[string]field, 10)
	var body []byte

	for {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			return Message{}, bad(line, "header ends without a blank line before the body")
		}
		if nl > MaxHeaderLine {
			return Message{}, bad(line, "header line is %d bytes, limit is %d", nl, MaxHeaderLine)
		}
		text := string(rest[:nl])
		rest = rest[nl+1:]

		if text == "" {
			body = rest
			break
		}
		if strings.ContainsRune(text, '\r') {
			return Message{}, bad(line, "header line contains a carriage return; the file's line endings were converted, which also corrupts the body length")
		}

		key, value, ok := strings.Cut(text, ": ")
		if !ok {
			return Message{}, bad(line, "header line %q is not \"key: value\"", text)
		}
		if !known[key] {
			return Message{}, bad(line, "unknown header %q", key)
		}
		if prev, dup := fields[key]; dup {
			return Message{}, bad(line, "header %q appears again, first seen on line %d", key, prev.line)
		}
		fields[key] = field{value: value, line: line}
		line++
	}

	return assemble(path, fields, body)
}

// known is the complete set of header keys. A key outside it means a newer
// Mailman wrote this message, and reading it on a partial understanding would
// be worse than saying so.
var known = map[string]bool{
	keyID: true, keyKind: true, keyFrom: true, keyTo: true, keyCC: true,
	keySubject: true, keyConvo: true, keyIndex: true, keySent: true, keyBytes: true,
}

// required lists the headers every message must carry.
var required = []string{keyID, keyKind, keyFrom, keyTo, keySubject, keySent, keyBytes}

func expectMagic(path string, data []byte) ([]byte, int, error) {
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return nil, 0, fault.Parse{Path: path, Line: 1, Reason: "file is empty or has no newline; this is not a mailman message"}
	}
	if got := string(data[:nl]); got != Format {
		return nil, 0, fault.Parse{Path: path, Line: 1, Reason: fmt.Sprintf(
			"first line is %q, want %q", got, Format)}
	}
	return data[nl+1:], 2, nil
}

// assemble turns validated header text into a Message. Every conversion reports
// the line it came from, so a damaged store can be repaired with an editor.
func assemble(path string, fields map[string]field, body []byte) (Message, error) {
	bad := func(key string, format string, args ...any) error {
		return fault.Parse{Path: path, Line: fields[key].line, Reason: fmt.Sprintf(format, args...)}
	}

	var missing []string
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Message{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"message is missing required header(s): %s", strings.Join(missing, ", "))}
	}

	// The body length is checked first: if it disagrees, nothing else about the
	// file can be trusted either.
	size, err := strconv.Atoi(fields[keyBytes].value)
	if err != nil || size < 0 {
		return Message{}, bad(keyBytes, "body length %q is not a non-negative number", fields[keyBytes].value)
	}
	if size > MaxBody {
		return Message{}, bad(keyBytes, "body length %d exceeds the %d byte limit", size, MaxBody)
	}
	if len(body) != size {
		return Message{}, bad(keyBytes, "header says the body is %d bytes, but %d follow", size, len(body))
	}

	id, err := ParseID(fields[keyID].value)
	if err != nil {
		return Message{}, bad(keyID, "%s", reasonOf(err))
	}
	kind, err := ParseKind(fields[keyKind].value)
	if err != nil {
		return Message{}, bad(keyKind, "%s", reasonOf(err))
	}
	from, err := user.Parse(fields[keyFrom].value)
	if err != nil {
		return Message{}, bad(keyFrom, "sender: %s", err)
	}
	to, err := parseList(fields[keyTo].value)
	if err != nil {
		return Message{}, bad(keyTo, "recipients: %s", err)
	}

	var cc []user.Name
	if f, ok := fields[keyCC]; ok {
		if cc, err = parseList(f.value); err != nil {
			return Message{}, bad(keyCC, "copied recipients: %s", err)
		}
		if len(cc) == 0 {
			return Message{}, bad(keyCC, "the cc header is present but empty; omit it instead")
		}
	}

	subject := fields[keySubject].value
	if err := CheckSubject(subject); err != nil {
		return Message{}, bad(keySubject, "subject: %s", err)
	}

	var convo ID
	index := 0
	_, hasConvo := fields[keyConvo]
	_, hasIndex := fields[keyIndex]
	switch {
	case hasConvo != hasIndex:
		key := keyConvo
		if hasIndex {
			key = keyIndex
		}
		return Message{}, bad(key, "conversation and index must appear together")
	case hasConvo:
		if convo, err = ParseID(fields[keyConvo].value); err != nil {
			return Message{}, bad(keyConvo, "conversation: %s", reasonOf(err))
		}
		if index, err = strconv.Atoi(fields[keyIndex].value); err != nil {
			return Message{}, bad(keyIndex, "index %q is not a number", fields[keyIndex].value)
		}
	}

	sent, err := clock.Parse(fields[keySent].value)
	if err != nil {
		return Message{}, bad(keySent, "%s", reasonOf(err))
	}

	m, err := New(id, kind, from, to, cc, subject, convo, index, sent, body)
	if err != nil {
		return Message{}, fault.Parse{Path: path, Reason: "message is invalid: " + reasonOf(err)}
	}
	return m, nil
}

// parseList reads a comma-separated recipient list. Empty entries are refused
// rather than skipped: "alice,,bob" means the caller's list-building has a hole
// in it, and quietly delivering to two of three recipients is precisely the
// failure this tool must not have.
func parseList(s string) ([]user.Name, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fault.Parse{Reason: "list is empty"}
	}
	parts := strings.Split(s, ",")
	if len(parts) > MaxRecipients {
		return nil, fault.Parse{Reason: fmt.Sprintf("list has %d entries, limit is %d", len(parts), MaxRecipients)}
	}

	out := make([]user.Name, 0, len(parts))
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			return nil, fault.Parse{Reason: fmt.Sprintf("entry %d is empty", i+1)}
		}
		n, err := user.Parse(trimmed)
		if err != nil {
			return nil, fault.Parse{Reason: fmt.Sprintf("entry %d: %s", i+1, err)}
		}
		if user.Contains(out, n) {
			return nil, fault.Parse{Reason: fmt.Sprintf("entry %d repeats %s", i+1, n)}
		}
		out = append(out, n)
	}
	return out, nil
}
