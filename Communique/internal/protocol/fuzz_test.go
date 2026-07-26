package protocol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// FuzzDecodeSyncRequest holds the properties the wire boundary rests on: no
// input can crash the decoder, no input produces an unclassified error, and
// anything that decodes successfully is genuinely valid — so a handler that
// receives a decoded value may use it without re-checking.
func FuzzDecodeSyncRequest(f *testing.F) {
	var seed bytes.Buffer
	if err := protocol.Encode(&seed, ptr(request())); err != nil {
		f.Fatal(err)
	}
	f.Add(seed.String())

	for _, s := range []string{
		"", "{}", "null", "[]", `{"protocol":1}`,
		`{"protocol":0,"snapshot":{}}`,
		`{"protocol":1,"agent":"x","sent_at":"2026-07-24T18:31:04Z","snapshot":{"machine":"m","user":"u","taken_at":"2026-07-24T18:31:04Z","inbox":[],"archive":[],"convos":[],"tasks":[]}}`,
		`{"protocol":1,"snapshot":{"machine":"../etc","user":"u"}}`,
		strings.Repeat(`{"a":`, 200),
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		var got protocol.SyncRequest
		err := protocol.Decode(strings.NewReader(in), protocol.MaxSnapshotBytes, &got)
		if err != nil {
			classified(t, err)
			return
		}

		// A value that decoded must satisfy its own rules.
		if verr := got.Validate(); verr != nil {
			t.Fatalf("Decode accepted a value its own Validate rejects: %v", verr)
		}

		// And it must survive a round trip unchanged, since the server stores
		// what it received and the agent reads back what it stored.
		var first bytes.Buffer
		if err := protocol.Encode(&first, &got); err != nil {
			t.Fatalf("a decoded value would not re-encode: %v", err)
		}
		var again protocol.SyncRequest
		if err := protocol.Decode(bytes.NewReader(first.Bytes()), protocol.MaxSnapshotBytes, &again); err != nil {
			t.Fatalf("a re-encoded value would not decode: %v", err)
		}
		var second bytes.Buffer
		if err := protocol.Encode(&second, &again); err != nil {
			t.Fatalf("a twice-decoded value would not re-encode: %v", err)
		}
		if first.String() != second.String() {
			t.Fatalf("round trip is not stable:\n first %s\nsecond %s", first.String(), second.String())
		}
	})
}

// FuzzDecodeSyncResponse does the same for the direction the agent trusts least:
// the server tells it what to do, and a malformed batch must be refused rather
// than half-applied.
func FuzzDecodeSyncResponse(f *testing.F) {
	var seed bytes.Buffer
	if err := protocol.Encode(&seed, ptr(response())); err != nil {
		f.Fatal(err)
	}
	f.Add(seed.String())

	for _, s := range []string{
		"", "{}", `{"protocol":1,"server_time":"2026-07-24T18:31:04Z"}`,
		`{"protocol":1,"server_time":"2026-07-24T18:31:04Z","actions":[{}]}`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		var got protocol.SyncResponse
		err := protocol.Decode(strings.NewReader(in), protocol.MaxSnapshotBytes, &got)
		if err != nil {
			classified(t, err)
			return
		}
		if verr := got.Validate(); verr != nil {
			t.Fatalf("Decode accepted a response its own Validate rejects: %v", verr)
		}
		// Every accepted action must be applicable: a known op, whose operands
		// match it. Anything else would reach the agent's Apply as a surprise.
		for i, a := range got.Actions {
			if !a.Op.Valid() {
				t.Fatalf("action %d carries unknown op %q", i, a.Op)
			}
			if err := a.Validate(); err != nil {
				t.Fatalf("action %d is invalid after decoding: %v", i, err)
			}
		}
	})
}

// classified asserts a failure is one cq can report, so no input produces a bare
// error that would classify as internal and read as a bug in cq.
func classified(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, fault.ErrParse) {
		return
	}
	t.Fatalf("unclassified decode error (%v): %v", fault.Classify(err), err)
}
