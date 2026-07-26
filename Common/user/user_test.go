package user_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

var testTime = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// fixedEntropy is a deterministic byte source, so a test can pin a salt without
// reaching for the real random pool.
type fixedEntropy struct{ b byte }

func (e *fixedEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
		e.b++
	}
	return len(p), nil
}

// shortEntropy delivers fewer bytes than asked for and then fails, which is the
// case a salt must never be built from.
type shortEntropy struct{ left int }

func (e *shortEntropy) Read(p []byte) (int, error) {
	if e.left <= 0 {
		return 0, errors.New("entropy pool exhausted")
	}
	n := min(len(p), e.left)
	e.left -= n
	return n, nil
}

func testKey(t *testing.T) string {
	t.Helper()
	key, err := user.NewKey(rand.Reader)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

func TestParseNormalises(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "alice", "alice"},
		{"uppercase", "ALICE", "alice"},
		{"mixed case", "AlIcE", "alice"},
		{"surrounding space", "  alice  ", "alice"},
		{"digits", "agent7", "agent7"},
		{"leading digit", "7agent", "7agent"},
		{"dots and dashes", "a.b-c_d", "a.b-c_d"},
		{"single character", "a", "a"},
		{"longest allowed", strings.Repeat("a", 64), strings.Repeat("a", 64)},
		// Trailing whitespace is trimmed deliberately: a name read from a file
		// or an environment variable routinely arrives with a newline attached,
		// and refusing that would be a papercut with no security value.
		{"trailing newline", "alice\n", "alice"},
		{"trailing carriage return", "alice\r\n", "alice"},
		{"surrounding tabs", "\talice\t", "alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := user.Parse(tc.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.raw, err)
			}
			if got.String() != tc.want {
				t.Errorf("Parse(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if got.Zero() {
				t.Error("a parsed name should not be zero")
			}
		})
	}
}

// TestParseRejects covers every way a name could become dangerous as a path
// element or ambiguous as a query value.
func TestParseRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"only space", "   "},
		{"traversal", ".."},
		{"single dot", "."},
		{"reserved all", "all"},
		{"reserved system", "system"},
		{"reserved mailman", "mailman"},
		{"reserved uppercase", "SYSTEM"},
		{"slash", "a/b"},
		{"backslash", `a\b`},
		{"leading dot", ".alice"},
		{"leading dash", "-alice"},
		{"leading underscore", "_alice"},
		{"space inside", "al ice"},
		{"newline inside", "al\nice"},
		{"tab inside", "al\tice"},
		{"nul", "alice\x00"},
		{"at sign", "alice@host"},
		{"comma", "a,b"},
		{"quote", `a"b`},
		{"too long", strings.Repeat("a", 65)},
		{"non-ascii letter", "álice"},
		{"invalid utf8", "\xff\xfe"},
		{"colon", "a:b"},
		{"asterisk", "a*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := user.Parse(tc.raw); !errors.Is(err, fault.ErrUsage) {
				t.Errorf("Parse(%q) = %q, %v; want a usage fault", tc.raw, got, err)
			}
		})
	}
}

// TestParseIsIdempotent is the property that makes a Name safe to re-normalise
// anywhere: parsing an already-parsed name must not change it.
func TestParseIsIdempotent(t *testing.T) {
	for _, raw := range []string{"alice", "ALICE", " Bob ", "a.b-c_d", "7x"} {
		first, err := user.Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		second, err := user.Parse(first.String())
		if err != nil {
			t.Fatalf("re-parsing %q: %v", first, err)
		}
		if first.String() != second.String() {
			t.Errorf("Parse is not idempotent: %q -> %q -> %q", raw, first, second)
		}
	}
}

func TestParseList(t *testing.T) {
	got, err := user.ParseList([]string{"Bob", "alice", "bob", "  CAROL  "})
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	// Duplicates drop, first-mention order survives.
	if want := []string{"bob", "alice", "carol"}; strings.Join(user.Names(got), ",") != strings.Join(want, ",") {
		t.Errorf("ParseList = %v, want %v", user.Names(got), want)
	}
}

// TestParseListReportsEveryBadName: an agent fixing a recipient list should get
// one round trip, not one per typo.
func TestParseListReportsEveryBadName(t *testing.T) {
	_, err := user.ParseList([]string{"alice", "", "b ob", "carol"})
	if !errors.Is(err, fault.ErrUsage) {
		t.Fatalf("ParseList = %v, want a usage fault", err)
	}
	for _, want := range []string{"recipient 2", "recipient 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q should mention %q", err, want)
		}
	}
}

func TestParseListOnEmptyInput(t *testing.T) {
	got, err := user.ParseList(nil)
	if err != nil {
		t.Fatalf("ParseList(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ParseList(nil) = %v, want empty", got)
	}
}

func TestContainsAndCompare(t *testing.T) {
	list, err := user.ParseList([]string{"alice", "bob"})
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := user.MustParse("alice")
	carol, _ := user.MustParse("carol")

	if !user.Contains(list, alice) {
		t.Error("Contains should find alice")
	}
	if user.Contains(list, carol) {
		t.Error("Contains should not find carol")
	}
	if alice.Compare(carol) >= 0 {
		t.Error("alice should sort before carol")
	}
	if alice.Compare(alice) != 0 {
		t.Error("a name should compare equal to itself")
	}
}

func TestNewKeyIsUsableAndUnique(t *testing.T) {
	seen := make(map[string]bool, 32)
	for range 32 {
		key, err := user.NewKey(rand.Reader)
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		if err := user.CheckKey(key); err != nil {
			t.Fatalf("NewKey produced a key that fails CheckKey: %v", err)
		}
		if len(key) < user.MinKeyLen {
			t.Fatalf("NewKey produced %d bytes, below the %d minimum", len(key), user.MinKeyLen)
		}
		if seen[key] {
			t.Fatal("NewKey repeated a key")
		}
		seen[key] = true
	}
}

func TestNewKeyRefusesShortEntropy(t *testing.T) {
	if _, err := user.NewKey(&shortEntropy{left: 4}); !errors.Is(err, fault.ErrIO) {
		t.Errorf("NewKey with exhausted entropy = %v, want an i/o fault", err)
	}
}

func TestCheckKey(t *testing.T) {
	good := strings.Repeat("k", 32)
	if err := user.CheckKey(good); err != nil {
		t.Errorf("CheckKey(32 bytes) = %v, want nil", err)
	}
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"one byte short", strings.Repeat("k", 31)},
		{"too long", strings.Repeat("k", 1025)},
		{"control character", strings.Repeat("k", 31) + "\n"},
		{"tab", strings.Repeat("k", 31) + "\t"},
		{"nul", strings.Repeat("k", 31) + "\x00"},
		{"invalid utf8", strings.Repeat("k", 31) + "\xff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := user.CheckKey(tc.key); !errors.Is(err, fault.ErrUsage) {
				t.Errorf("CheckKey = %v, want a usage fault", err)
			}
		})
	}
}

func TestRecordVerifies(t *testing.T) {
	name, _ := user.MustParse("alice")
	key := testKey(t)

	rec, err := user.NewRecord(name, key, testTime, rand.Reader)
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if rec.Name().String() != "alice" {
		t.Errorf("Name() = %q", rec.Name())
	}
	if !rec.Created().Equal(clock.Normalise(testTime)) {
		t.Errorf("Created() = %s, want %s", rec.Created(), testTime)
	}
	if err := rec.Verify(key); err != nil {
		t.Errorf("Verify with the right key = %v, want nil", err)
	}
}

// changeFirst returns the key with its first character genuinely different.
//
// The obvious "X"+key[1:] is a no-op whenever a randomly generated key already
// starts with X — about one run in sixty-four, which is exactly often enough to
// fail somebody else's unrelated change and be dismissed as a fluke.
func changeFirst(key string) string {
	if key == "" {
		return "X"
	}
	if key[0] == 'X' {
		return "Y" + key[1:]
	}
	return "X" + key[1:]
}

func TestRecordRejectsWrongKeys(t *testing.T) {
	name, _ := user.MustParse("alice")
	key := testKey(t)
	rec, err := user.NewRecord(name, key, testTime, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		key  string
	}{
		{"other key", testKey(t)},
		{"empty", ""},
		{"too short", "abc"},
		{"one byte changed", changeFirst(key)},
		{"truncated", key[:len(key)-1]},
		{"extended", key + "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := rec.Verify(tc.key)
			if !errors.Is(err, fault.ErrAuth) {
				t.Fatalf("Verify(%s) = %v, want an auth fault", tc.name, err)
			}
			// The visible message must not say which way it failed.
			if got := err.Error(); got != "authentication failed" {
				t.Errorf("Verify message = %q, want the generic text", got)
			}
		})
	}
}

// TestDistinctSaltsGiveDistinctDigests is what stops a digest lifted from one
// store working against another.
func TestDistinctSaltsGiveDistinctDigests(t *testing.T) {
	name, _ := user.MustParse("alice")
	key := testKey(t)

	first, err := user.NewRecord(name, key, testTime, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := user.NewRecord(name, key, testTime, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	a, err := first.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Error("two records for the same key should differ; the salt is not random")
	}
	// Both must still verify against the same key.
	for i, rec := range []user.Record{first, second} {
		if err := rec.Verify(key); err != nil {
			t.Errorf("record %d does not verify: %v", i, err)
		}
	}
}

// TestRecordIsBoundToItsName is the reason the user's name goes into the
// digest. Without it, copying one user's record into another's directory would
// produce an account that opens with the first user's key.
func TestRecordIsBoundToItsName(t *testing.T) {
	alice, _ := user.MustParse("alice")
	bob, _ := user.MustParse("bob")
	key := testKey(t)

	rec, err := user.NewRecord(alice, key, testTime, &fixedEntropy{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the record as though it had been copied to bob's directory. The
	// salt and digest are untouched; only the name changes.
	stolen := bytes.ReplaceAll(data, []byte(`"alice"`), []byte(`"bob"`))
	if bytes.Equal(stolen, data) {
		t.Fatal("the test did not actually rewrite the name")
	}

	moved, err := user.Decode("user.json", stolen)
	if err != nil {
		t.Fatalf("the rewritten record should still decode: %v", err)
	}
	if moved.Name().String() != "bob" {
		t.Fatalf("rewritten record names %q", moved.Name())
	}
	if err := moved.Verify(key); !errors.Is(err, fault.ErrAuth) {
		t.Error("alice's record, renamed to bob, verified with alice's key")
	}

	// The same key under a record genuinely made for bob must of course work.
	real, err := user.NewRecord(bob, key, testTime, &fixedEntropy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := real.Verify(key); err != nil {
		t.Errorf("bob's own record does not verify: %v", err)
	}
}

func TestNewRecordRejectsBadInput(t *testing.T) {
	name, _ := user.MustParse("alice")

	if _, err := user.NewRecord(name, "short", testTime, rand.Reader); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("NewRecord with a short key = %v, want a usage fault", err)
	}
	if _, err := user.NewRecord(user.Name{}, testKey(t), testTime, rand.Reader); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("NewRecord with a zero name = %v, want an internal fault", err)
	}
	if _, err := user.NewRecord(name, testKey(t), testTime, &shortEntropy{left: 4}); !errors.Is(err, fault.ErrIO) {
		t.Errorf("NewRecord with exhausted entropy = %v, want an i/o fault", err)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	name, _ := user.MustParse("alice")
	key := testKey(t)
	rec, err := user.NewRecord(name, key, testTime, &fixedEntropy{})
	if err != nil {
		t.Fatal(err)
	}

	data, err := rec.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := user.Decode("user.json", data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if back.Name().String() != rec.Name().String() {
		t.Errorf("name did not survive: %q vs %q", back.Name(), rec.Name())
	}
	if !back.Created().Equal(rec.Created()) {
		t.Errorf("created did not survive: %s vs %s", back.Created(), rec.Created())
	}
	if err := back.Verify(key); err != nil {
		t.Errorf("decoded record does not verify: %v", err)
	}

	// Re-encoding must be byte-identical, so a store can be diffed.
	again, err := back.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Errorf("re-encoding changed the bytes:\n%s\nvs\n%s", data, again)
	}
}

func TestDecodeRejectsBadRecords(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"version": 1,
			"name":    "alice",
			"algo":    user.AlgoHMAC,
			"salt":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, user.SaltLen)),
			"digest":  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, user.DigestLen)),
			"created": clock.Format(testTime),
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown version", func(m map[string]any) { m["version"] = 2 }},
		{"missing version", func(m map[string]any) { delete(m, "version") }},
		{"bad name", func(m map[string]any) { m["name"] = "../etc" }},
		{"missing name", func(m map[string]any) { delete(m, "name") }},
		{"unknown algo", func(m map[string]any) { m["algo"] = "rot13" }},
		{"missing algo", func(m map[string]any) { delete(m, "algo") }},
		{"salt not base64", func(m map[string]any) { m["salt"] = "!!!" }},
		{"salt too short", func(m map[string]any) { m["salt"] = base64.StdEncoding.EncodeToString([]byte{1}) }},
		{"zero salt", func(m map[string]any) {
			m["salt"] = base64.StdEncoding.EncodeToString(make([]byte, user.SaltLen))
		}},
		{"digest too short", func(m map[string]any) { m["digest"] = base64.StdEncoding.EncodeToString([]byte{1}) }},
		{"bad created", func(m map[string]any) { m["created"] = "yesterday" }},
		{"created out of range", func(m map[string]any) { m["created"] = "1970-01-01T00:00:00.000Z" }},
		{"unknown field", func(m map[string]any) { m["admin"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := valid()
			tc.mutate(m)
			data := mustJSON(t, m)
			if _, err := user.Decode("user.json", data); !errors.Is(err, fault.ErrParse) {
				t.Errorf("Decode = %v, want a parse fault", err)
			}
		})
	}

	// The unmutated record must decode, or the table above proves nothing.
	if _, err := user.Decode("user.json", mustJSON(t, valid())); err != nil {
		t.Errorf("the baseline record should decode, got %v", err)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"truncated", `{"version": 1`},
		{"not an object", `[1,2,3]`},
		{"trailing content", `{"version":1,"name":"a","algo":"hmac-sha256","salt":"","digest":"","created":""}{}`},
		{"nul bytes", "\x00\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := user.Decode("user.json", []byte(tc.data)); !errors.Is(err, fault.ErrParse) {
				t.Errorf("Decode(%q) = %v, want a parse fault", tc.data, err)
			}
		})
	}
}

// TestCorruptRecordFailsClosed is the security property: a damaged user.json
// must never become "no key required".
func TestCorruptRecordFailsClosed(t *testing.T) {
	name, _ := user.MustParse("alice")
	key := testKey(t)
	rec, err := user.NewRecord(name, key, testTime, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}

	// Every single-byte corruption must either fail to decode or fail to verify.
	// Neither outcome may be "authenticated".
	for i := range data {
		mutated := bytes.Clone(data)
		mutated[i] ^= 0xff

		back, err := user.Decode("user.json", mutated)
		if err != nil {
			continue // refused at the door, which is fine
		}
		if err := back.Verify(key); err == nil {
			t.Fatalf("flipping byte %d produced a record that still verifies", i)
		}
	}

	// A zero-value record must not verify anything either.
	if err := (user.Record{}).Verify(key); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("the zero Record verified: %v", err)
	}
	if err := (user.Record{}).Verify(""); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("the zero Record verified an empty key: %v", err)
	}
}

func TestZeroRecordCannotEncode(t *testing.T) {
	if _, err := (user.Record{}).Encode(); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("encoding the zero Record = %v, want an internal fault", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{"alice", "ALICE", "", "..", "a/b", "a.b-c_d", "\xff", strings.Repeat("a", 70)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		name, err := user.Parse(raw)
		if err != nil {
			if !errors.Is(err, fault.ErrUsage) && !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("Parse(%q) failed with an unclassified error: %v", raw, err)
			}
			return
		}

		s := name.String()
		// Whatever came in, what comes out must be safe as a path element and
		// stable under re-parsing.
		if strings.ContainsAny(s, `/\:*?"<>| `+"\x00\n\r\t") {
			t.Fatalf("Parse(%q) produced an unsafe name %q", raw, s)
		}
		if s == "." || s == ".." || s == "" {
			t.Fatalf("Parse(%q) produced %q", raw, s)
		}
		again, err := user.Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q) produced %q, which does not re-parse: %v", raw, s, err)
		}
		if again.String() != s {
			t.Fatalf("Parse is not idempotent: %q -> %q -> %q", raw, s, again)
		}
	})
}

func FuzzDecode(f *testing.F) {
	name, _ := user.MustParse("alice")
	rec, err := user.NewRecord(name, strings.Repeat("k", 32), testTime, &fixedEntropy{})
	if err != nil {
		f.Fatal(err)
	}
	data, err := rec.Encode()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data)
	f.Add([]byte("{}"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := user.Decode("user.json", data)
		if err != nil {
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Decode failed with an unclassified error: %v", err)
			}
			return
		}
		// Anything that decodes must be internally consistent enough to
		// re-encode and decode again to the same thing.
		encoded, err := got.Encode()
		if err != nil {
			t.Fatalf("a decoded record failed to encode: %v", err)
		}
		again, err := user.Decode("user.json", encoded)
		if err != nil {
			t.Fatalf("a re-encoded record failed to decode: %v", err)
		}
		if again.Name().String() != got.Name().String() {
			t.Fatalf("name changed across a round trip: %q -> %q", got.Name(), again.Name())
		}
	})
}

func mustJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshalling the test record: %v", err)
	}
	return data
}
