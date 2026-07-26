package mint

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/orcprobe/internal/clock"
)

func fixed() clock.Clock {
	return clock.NewFake(time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC), time.Second)
}

// store writes a Mailman-shaped user store with the given mailboxes, each
// carrying a digest for a key this test knows — standing in for the real store,
// whose keys orcprobe never has.
func store(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, usersDir, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		salt := make([]byte, SaltLen)
		for i := range salt {
			salt[i] = byte(i + 1)
		}
		digest, err := derive(name, realKey, salt)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(stored{
			Version: FormatVersion, Name: name, Algo: AlgoHMAC,
			Salt:    base64.StdEncoding.EncodeToString(salt),
			Digest:  base64.StdEncoding.EncodeToString(digest),
			Created: clock.Format(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, userFile), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		// A journal, to prove minting leaves everything but the credential alone.
		if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte("{\"kind\":\"landed\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func read(t *testing.T, root, name string) stored {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, usersDir, name, userFile))
	if err != nil {
		t.Fatal(err)
	}
	var rec stored
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// verifies reproduces Mailman's check: does this key authenticate against this
// record? It is the whole point of minting, so the test does the arithmetic
// rather than trusting the code that wrote it.
func verifies(t *testing.T, rec stored, key string) bool {
	t.Helper()
	salt, err := base64.StdEncoding.DecodeString(rec.Salt)
	if err != nil {
		t.Fatal(err)
	}
	want, err := base64.StdEncoding.DecodeString(rec.Digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := derive(rec.Name, key, salt)
	if err != nil {
		t.Fatal(err)
	}
	return hmac.Equal(got, want)
}

func TestUsersRemintsEveryMailbox(t *testing.T) {
	root := store(t, "alice", "boss")

	res, err := Fleet(root, t.TempDir(), fixed())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Identities) != 3 {
		t.Fatalf("minted %d identities, want alice, boss, and %s", len(res.Identities), God)
	}

	for _, id := range res.Identities {
		rec := read(t, root, id.Name)
		if !verifies(t, rec, id.Key) {
			t.Fatalf("%s's probe key does not authenticate against its record", id.Name)
		}
		if verifies(t, rec, realKey) {
			t.Fatalf("%s still authenticates with the real key; the copy kept a live credential", id.Name)
		}
	}
}

func TestUsersLeavesEverythingButTheCredential(t *testing.T) {
	root := store(t, "alice")
	before := read(t, root, "alice")

	if _, err := Fleet(root, t.TempDir(), fixed()); err != nil {
		t.Fatal(err)
	}

	after := read(t, root, "alice")
	if after.Name != before.Name || after.Created != before.Created {
		t.Fatal("minting changed a mailbox's identity or history")
	}
	if after.Salt == before.Salt || after.Digest == before.Digest {
		t.Fatal("the salt or digest survived; minting must replace both")
	}

	journal, err := os.ReadFile(filepath.Join(root, usersDir, "alice", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(journal) != "{\"kind\":\"landed\"}\n" {
		t.Fatal("the journal was touched; only the credential is orcprobe's business")
	}
}

func TestUsersAddsGodOnlyWhenMissing(t *testing.T) {
	root := store(t, God)
	res, err := Fleet(root, t.TempDir(), fixed())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Identities) != 1 {
		t.Fatalf("minted %d identities; an existing god must be reminted, not duplicated", len(res.Identities))
	}
}

func TestUsersSkipsRecordsItDoesNotUnderstand(t *testing.T) {
	root := store(t, "alice")
	future := filepath.Join(root, usersDir, "future")
	if err := os.MkdirAll(future, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(future, userFile), []byte(`{"version":99,"name":"future"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Fleet(root, t.TempDir(), fixed())
	if err != nil {
		t.Fatalf("one unreadable record failed the whole pass: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "future" {
		t.Fatalf("skipped %v, want exactly the future record — silently minting it would be worse", res.Skipped)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identities.json")
	ids := []Identity{{Name: "alice", Key: "k1", Minted: true}, {Name: God, Key: "k2"}}

	if err := Save(path, "probe-1", ids); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("identities.json is %v; it is the one file holding plaintext keys", perm)
	}

	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Default != God {
		t.Fatalf("default identity is %q, want %q", file.Default, God)
	}
	found, err := file.Find("alice")
	if err != nil {
		t.Fatal(err)
	}
	if found.Key != "k1" {
		t.Fatalf("alice's key came back as %q", found.Key)
	}
	if _, err := file.Find("nobody"); err == nil {
		t.Fatal("Find invented an identity")
	}
}

func TestNewKeyIsLongEnoughForMailman(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	// Mailman refuses a key shorter than 32 bytes rather than stretching it.
	if len(key) < 32 {
		t.Fatalf("minted a %d-byte key; mailman requires at least 32", len(key))
	}
}

// orcStore writes an Orc-shaped identity store: the same user.json every tool
// keeps, plus the plaintext key only Orc has.
func orcStore(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, identitiesDir, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		salt := make([]byte, SaltLen)
		for i := range salt {
			salt[i] = byte(i + 7)
		}
		digest, err := derive(name, realKey, salt)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(stored{
			Version: FormatVersion, Name: name, Algo: AlgoHMAC,
			Salt:    base64.StdEncoding.EncodeToString(salt),
			Digest:  base64.StdEncoding.EncodeToString(digest),
			Created: clock.Format(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, userFile), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		// The thing no other store has: the key itself, in the clear.
		if err := os.WriteFile(filepath.Join(dir, keyFile), []byte(realKey+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{"name":"`+name+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// realKey stands in for a credential that opens the real fleet. It must not
// survive anywhere in a probe.
const realKey = "the-real-key-that-opens-the-real-fleet"

func orcKey(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, identitiesDir, name, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestOrcsPlaintextKeyringIsRewritten is the rule this package exists for,
// applied to the one store it actually bites on.
//
// Every other store keeps a digest, so copying it leaks nothing a probe key can
// use. Orc keeps the key. A probe that carried it across would hold a
// credential that opens the real fleet, in a scratch directory made to be
// broken and thrown away.
func TestOrcsPlaintextKeyringIsRewritten(t *testing.T) {
	orc := orcStore(t, "ember", "ash")

	res, err := Fleet(t.TempDir(), orc, fixed())
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"ember", "ash"} {
		key := orcKey(t, orc, name)
		if strings.TrimSpace(key) == realKey {
			t.Fatalf("%s's real key survived into the probe", name)
		}
		if strings.TrimSpace(key) == "" {
			t.Fatalf("%s has no key at all; orc would refuse to run it", name)
		}

		// The record and the key must agree, or the identity is locked out of
		// its own probe.
		data, err := os.ReadFile(filepath.Join(orc, identitiesDir, name, userFile))
		if err != nil {
			t.Fatal(err)
		}
		var rec stored
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatal(err)
		}
		if !verifies(t, rec, strings.TrimSpace(key)) {
			t.Fatalf("%s's keyring and record disagree; nothing could authenticate as it", name)
		}
		if verifies(t, rec, realKey) {
			t.Fatalf("%s's record still accepts the real key", name)
		}
	}

	// And nothing anywhere in the probe still holds the real key.
	if grepTree(t, orc, realKey) {
		t.Fatal("the real key is still somewhere in the copied store")
	}

	for _, id := range res.Identities {
		if len(id.In) == 0 {
			t.Fatalf("%s was minted into no store at all", id.Name)
		}
	}
}

// TestOneKeyPerAgentAcrossBothStores is the coherence the fleet depends on: an
// agent that exists in Orc and in Mailman gets one key, not two.
func TestOneKeyPerAgentAcrossBothStores(t *testing.T) {
	mail := store(t, "ember")
	orc := orcStore(t, "ember")

	res, err := Fleet(mail, orc, fixed())
	if err != nil {
		t.Fatal(err)
	}

	var ember Identity
	for _, id := range res.Identities {
		if id.Name == "ember" {
			ember = id
		}
	}
	if len(ember.In) != 2 {
		t.Fatalf("ember was minted into %v, want both stores", ember.In)
	}

	// The same key opens both records, and it is the key in Orc's keyring.
	if got := strings.TrimSpace(orcKey(t, orc, "ember")); got != ember.Key {
		t.Fatalf("orc's keyring holds a different key from the one reported")
	}
	if !verifies(t, read(t, mail, "ember"), ember.Key) {
		t.Fatal("the mailbox does not accept the key orc hands out")
	}
}

// grepTree reports whether any file under root contains the needle.
func grepTree(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
