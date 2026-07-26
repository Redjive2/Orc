package mail_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
)

// adversarialBodies are the bodies a length-prefixed codec exists for. Every
// one of them contains something that a delimiter-scanning reader would
// mistake for structure.
var adversarialBodies = []string{
	"",
	"plain text",
	"no trailing newline",
	"trailing newline\n",
	"two trailing newlines\n\n",
	"\n",
	"\n\n\n",
	"leading blank\n\nand more",
	mail.Format,
	mail.Format + "\nid: forged\n\nfake body",
	"\n" + mail.Format + "\n",
	"bytes: 0\n\nnot really",
	"subject: not a header\n",
	"a\r\nb\r\n",
	"lone \r carriage return",
	"unicode → ✓ 日本語 🚀",
	"combining é and ﬁ ligature",
	"tabs\tand\tspaces   ",
	strings.Repeat("long line ", 500),
	strings.Repeat("many\nlines\n", 200),
	"---\nfrontmatter: yes\n---\n# markdown\n\n- a\n- b\n",
	"```\nid: 0\nbytes: 99\n```\n",
}

func TestEncodeDecodeRoundTripsAdversarialBodies(t *testing.T) {
	for i, body := range adversarialBodies {
		t.Run(fmt.Sprintf("body-%d", i), func(t *testing.T) {
			m := build(t, withBody(body))

			data, err := mail.Encode(m)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			back, err := mail.Decode("m.msg", data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			if got := back.BodyString(); got != body {
				t.Errorf("body did not survive:\n got %q\nwant %q", got, body)
			}
			if back.ID().String() != m.ID().String() {
				t.Errorf("id %q became %q", m.ID(), back.ID())
			}
			if back.Subject() != m.Subject() {
				t.Errorf("subject %q became %q", m.Subject(), back.Subject())
			}
			if !back.Sent().Equal(m.Sent()) {
				t.Errorf("sent %s became %s", m.Sent(), back.Sent())
			}

			// Re-encoding must be byte-identical, which is what makes a stored
			// message diffable against a fresh encoding of itself.
			again, err := mail.Encode(back)
			if err != nil {
				t.Fatalf("re-Encode: %v", err)
			}
			if !bytes.Equal(data, again) {
				t.Errorf("re-encoding changed the bytes")
			}
		})
	}
}

// TestEncodeIsDeterministic: two encodings of the same message must be equal,
// or nothing downstream can compare stored bytes.
func TestEncodeIsDeterministic(t *testing.T) {
	m := build(t, withCC("carol"), inConvo(3))
	first, err := mail.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, err := mail.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("Encode is not deterministic")
		}
	}
}

// TestEncodedShape pins the wire format, so a change to it is a deliberate act
// that breaks one test rather than a surprise in a stored corpus.
func TestEncodedShape(t *testing.T) {
	m := build(t, withCC("carol"), inConvo(3), withBody("Ship it.\n"))
	data, err := mail.Encode(m)
	if err != nil {
		t.Fatal(err)
	}

	header, body, found := bytes.Cut(data, []byte("\n\n"))
	if !found {
		t.Fatal("no blank line between header and body")
	}
	if string(body) != "Ship it.\n" {
		t.Errorf("body = %q", body)
	}

	lines := strings.Split(string(header), "\n")
	if lines[0] != mail.Format {
		t.Errorf("first line = %q, want %q", lines[0], mail.Format)
	}

	var keys []string
	for _, line := range lines[1:] {
		key, _, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("header line %q is not \"key: value\"", line)
		}
		keys = append(keys, key)
	}
	want := []string{"id", "kind", "from", "to", "cc", "subject", "convo", "index", "sent", "bytes"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("header keys = %v, want %v", keys, want)
	}
	// The byte count must be last: it is what terminates the header.
	if keys[len(keys)-1] != "bytes" {
		t.Error("the byte count must be the final header")
	}
}

// TestOptionalHeadersAreOmitted keeps a standalone message from carrying empty
// fields that Decode would then have to interpret.
func TestOptionalHeadersAreOmitted(t *testing.T) {
	data, err := mail.Encode(build(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cc:", "convo:", "index:"} {
		if bytes.Contains(data, []byte("\n"+key)) {
			t.Errorf("a standalone message should not carry a %q header:\n%s", key, data)
		}
	}
}

// TestABodyCannotForgeAHeader is the property the byte count buys. A body that
// looks exactly like a complete message must decode as a *body*.
func TestABodyCannotForgeAHeader(t *testing.T) {
	inner := build(t, withSubject("INNOCENT"), withBody("inner"))
	innerBytes, err := mail.Encode(inner)
	if err != nil {
		t.Fatal(err)
	}

	outer := build(t, withSubject("REAL"), withBody(string(innerBytes)))
	outerBytes, err := mail.Encode(outer)
	if err != nil {
		t.Fatal(err)
	}

	back, err := mail.Decode("m.msg", outerBytes)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if back.Subject() != "REAL" {
		t.Errorf("the nested message won: subject = %q", back.Subject())
	}
	if back.BodyString() != string(innerBytes) {
		t.Error("the nested message was not preserved verbatim as a body")
	}
}

func TestDecodeRejectsMalformedMessages(t *testing.T) {
	good, err := mail.Encode(build(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(good)

	for _, tc := range []struct {
		name string
		data string
		says string
	}{
		{"empty", "", "not a mailman message"},
		{"no newline", "mailman/1", "not a mailman message"},
		{"wrong magic", "mailman/2\n" + text[len(mail.Format)+1:], "want"},
		{"no blank line", strings.ReplaceAll(text, "\n\n", "\n"), ""},
		{"unknown header", strings.Replace(text, "kind:", "urgency:", 1), "unknown header"},
		{"repeated header", strings.Replace(text, "kind: mail\n", "kind: mail\nkind: cc\n", 1), "appears again"},
		{"no colon", strings.Replace(text, "kind: mail", "kind mail", 1), "key: value"},
		{"carriage return", strings.Replace(text, "kind: mail", "kind: mail\r", 1), "carriage return"},
		{"body longer than stated", text + "extra", "bytes"},
		{"body shorter than stated", text[:len(text)-3], "bytes"},
		{"bad byte count", strings.Replace(text, "bytes: 9", "bytes: nine", 1), "not a non-negative number"},
		{"negative byte count", strings.Replace(text, "bytes: 9", "bytes: -1", 1), "non-negative"},
		{"bad id", strings.Replace(text, "id: ", "id: zzz", 1), "id"},
		{"bad kind", strings.Replace(text, "kind: mail", "kind: shouting", 1), "kind"},
		{"bad sender", strings.Replace(text, "from: boss", "from: ../root", 1), "sender"},
		{"empty recipients", strings.Replace(text, "to: alice, bob", "to: ", 1), ""},
		{"hole in recipients", strings.Replace(text, "to: alice, bob", "to: alice,,bob", 1), "empty"},
		{"repeated recipient", strings.Replace(text, "to: alice, bob", "to: alice, alice", 1), "repeats"},
		{"empty subject", strings.Replace(text, "subject: RE: work", "subject: ", 1), "subject"},
		{"bad timestamp", strings.Replace(text, "sent: 2026", "sent: yolo", 1), "timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mail.Decode("m.msg", []byte(tc.data))
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Decode = %v, want a parse fault", err)
			}
			if tc.says != "" && !strings.Contains(err.Error(), tc.says) {
				t.Errorf("message %q should mention %q", err, tc.says)
			}
		})
	}
}

func TestDecodeRejectsMissingRequiredHeaders(t *testing.T) {
	good, err := mail.Encode(build(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"id", "kind", "from", "to", "subject", "sent", "bytes"} {
		t.Run(key, func(t *testing.T) {
			var kept []string
			for _, line := range strings.Split(string(good), "\n") {
				if strings.HasPrefix(line, key+": ") {
					continue
				}
				kept = append(kept, line)
			}
			_, err := mail.Decode("m.msg", []byte(strings.Join(kept, "\n")))
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Decode without %q = %v, want a parse fault", key, err)
			}
		})
	}
}

// TestConvoAndIndexTravelTogether: one without the other cannot be resolved or
// ordered, so it is refused rather than half-honoured.
func TestConvoAndIndexTravelTogether(t *testing.T) {
	good, err := mail.Encode(build(t, inConvo(2)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(good)

	for _, drop := range []string{"convo", "index"} {
		t.Run("without "+drop, func(t *testing.T) {
			var kept []string
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, drop+": ") {
					continue
				}
				kept = append(kept, line)
			}
			_, err := mail.Decode("m.msg", []byte(strings.Join(kept, "\n")))
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Decode without %q = %v, want a parse fault", drop, err)
			}
			if !strings.Contains(err.Error(), "together") {
				t.Errorf("message %q should say they must appear together", err)
			}
		})
	}
}

// TestEmptyCCHeaderIsRefused: an empty cc is indistinguishable from no cc, and
// two encodings of one message must not exist.
func TestEmptyCCHeaderIsRefused(t *testing.T) {
	good, err := mail.Encode(build(t))
	if err != nil {
		t.Fatal(err)
	}
	withEmpty := strings.Replace(string(good), "subject:", "cc: \nsubject:", 1)
	_, err = mail.Decode("m.msg", []byte(withEmpty))
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("Decode with an empty cc = %v, want a parse fault", err)
	}
}

// TestDecodeErrorsCarryALine is what makes a damaged store repairable by hand.
func TestDecodeErrorsCarryALine(t *testing.T) {
	good, err := mail.Encode(build(t))
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(good), "kind: mail", "kind: shouting", 1)

	_, err = mail.Decode("alice/m.msg", []byte(broken))
	var pf fault.Parse
	if !errors.As(err, &pf) {
		t.Fatalf("Decode = %v, want a fault.Parse", err)
	}
	if pf.Path != "alice/m.msg" {
		t.Errorf("Path = %q, want the path it was given", pf.Path)
	}
	if pf.Line != 3 {
		t.Errorf("Line = %d, want 3 (the kind header)", pf.Line)
	}
}

// TestOversizedHeaderLineIsRefused stops a damaged file being read into memory
// as one enormous line.
func TestOversizedHeaderLineIsRefused(t *testing.T) {
	huge := mail.Format + "\n" + strings.Repeat("x", mail.MaxHeaderLine+10) + "\n\n"
	_, err := mail.Decode("m.msg", []byte(huge))
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("Decode = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("message %q should mention the limit", err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, body := range adversarialBodies[:8] {
		m, err := mail.Encode(mustBuild(f, body))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(m)
	}
	f.Add([]byte(""))
	f.Add([]byte("mailman/1\n\n"))
	f.Add([]byte("mailman/1\nbytes: 0\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := mail.Decode("fuzz.msg", data)
		if err != nil {
			// Every refusal must be classified, so the CLI can map it to an exit
			// code. An unclassified error would exit 70 and read as a crash.
			if !errors.Is(err, fault.ErrParse) && !errors.Is(err, fault.ErrUsage) {
				t.Fatalf("Decode failed with an unclassified error: %v", err)
			}
			return
		}

		// Anything that decodes must re-encode to bytes that decode identically.
		// This is the property that stops a message being readable once and
		// unreadable the next time.
		encoded, err := mail.Encode(m)
		if err != nil {
			t.Fatalf("a decoded message failed to encode: %v", err)
		}
		again, err := mail.Decode("fuzz.msg", encoded)
		if err != nil {
			t.Fatalf("a re-encoded message failed to decode: %v", err)
		}
		if again.BodyString() != m.BodyString() {
			t.Fatalf("body changed across a round trip")
		}
		if again.ID().String() != m.ID().String() {
			t.Fatalf("id changed across a round trip: %q -> %q", m.ID(), again.ID())
		}
		if again.Subject() != m.Subject() {
			t.Fatalf("subject changed across a round trip: %q -> %q", m.Subject(), again.Subject())
		}

		twice, err := mail.Encode(again)
		if err != nil {
			t.Fatalf("encoding twice failed: %v", err)
		}
		if !bytes.Equal(encoded, twice) {
			t.Fatalf("encoding is not stable across a round trip")
		}
	})
}

// mustBuild is build for a *testing.F, which has no *testing.T to hand.
func mustBuild(f *testing.F, body string) mail.Message {
	f.Helper()
	id, err := mail.NewID(testTime, &countingEntropy{})
	if err != nil {
		f.Fatal(err)
	}
	from, err := user.Parse("boss")
	if err != nil {
		f.Fatal(err)
	}
	to, err := user.ParseList([]string{"alice", "bob"})
	if err != nil {
		f.Fatal(err)
	}
	m, err := mail.New(id, mail.Ordinary, from, to, nil, "RE: work", mail.ID{}, 0, testTime, []byte(body))
	if err != nil {
		f.Fatal(err)
	}
	return m
}
