// Package mint replaces the credentials in a copied Mailman store with
// probe-local ones.
//
// This is the reason a probe can hand out any identity instantly, and the
// reason a leaked probe discloses nothing. Mailman stores a salt and an
// HMAC-SHA256 digest, never a key, so the real keys are not in the copy to
// begin with — what minting does is *replace* each digest with one orcprobe
// knows the key for. The consequences are all wanted:
//
//   - the operator can act as anybody in the probe, with no password;
//   - a probe committed or copied by accident leaks no real credential;
//   - a probe key is worthless against the real store, whose digests are
//     unchanged and unknown here.
//
// The record format is Mailman's, reproduced field for field. That coupling is
// deliberate and is the price of being able to write a store another tool
// reads: if Mailman's format moves, this package must move with it, and the
// version check below is what turns that into a clear refusal rather than a
// mailbox nobody can open.
package mint

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// Mailman's user record, reproduced. FormatVersion is checked on every record
// read: a record this build does not understand is left alone and reported,
// never rewritten on a guess.
const (
	FormatVersion = 1
	AlgoHMAC      = "hmac-sha256"
	SaltLen       = 32
	// KeyBytes is the entropy in a minted key. Base64 of 32 bytes is 44
	// characters, comfortably past Mailman's 32-byte minimum.
	KeyBytes = 32

	usersDir = "users"
	userFile = "user.json"

	// Orc's layout, from Orc/internal/store/store.go. It keeps the same
	// user.json every other tool does *and* a plaintext key beside it, because
	// Orc is the thing that hands credentials out. That file is the reason this
	// package matters more for Orc than for anything else: every other store
	// holds a digest a probe key is useless against, and this one holds the key
	// itself.
	identitiesDir = "identities"
	keyFile       = "key"
)

// God is the synthetic identity a probe shell uses when no other is named. It
// is a real mailbox — a recipient of nothing, holder of every capability the
// tools grant a single user. The name is plain because Mailman's names are:
// lowercase letters, digits, and . _ - only, so "@god" is not a name a mailbox
// can have.
const God = "god"

// Identity is one mailbox and the key that proves it, inside a probe.
type Identity struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	// Minted distinguishes a mailbox that existed in the real store and had its
	// credential replaced from one orcprobe created (god).
	Minted bool `json:"minted"`
	// In names the stores this key was written into — "mailman", "orc", or
	// both. One name can exist in either or in each, and a probe where the two
	// disagreed about a credential would be a probe where `orc introspect` and
	// `mailman inbox` tell different stories about the same agent.
	In []string `json:"in,omitempty"`
}

// Result is what a minting pass amounted to.
type Result struct {
	Identities []Identity
	// Skipped names records orcprobe would not rewrite, with why. A mailbox
	// that quietly did not get a probe key would look like a mailbox the
	// operator simply cannot use, which is a confusing way to say "this record
	// is in a format I do not understand".
	Skipped []Skip
}

// Skip is one record left alone.
type Skip struct {
	Name string
	Why  string
}

type stored struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Algo    string `json:"algo"`
	Salt    string `json:"salt"`
	Digest  string `json:"digest"`
	Created string `json:"created"`
}

// Fleet remints every credential in a probe: Mailman's mailboxes and Orc's
// identities, one key per name.
//
// One key per *name*, not per store, and that is the point. An agent usually
// exists in both — Orc issued the credential, Mailman holds a mailbox under the
// same name — and a probe that minted two different keys for one agent would be
// a probe where `orc introspect` proves an identity that `mailman inbox` then
// refuses. The two stores have to agree, so they are reminted together.
//
// Both roots are the *probe's* copies. This package never opens a real store: it
// is given paths inside a probe and would be as happy rewriting a temporary
// directory, which is what makes it testable without a fleet at all.
func Fleet(mailmanRoot, orcRoot string, c clock.Clock) (Result, error) {
	var res Result

	mailboxes, skipped, err := names(filepath.Join(mailmanRoot, usersDir), userFile)
	if err != nil {
		return res, err
	}
	res.Skipped = append(res.Skipped, skipped...)

	identities, skipped, err := names(filepath.Join(orcRoot, identitiesDir), userFile)
	if err != nil {
		return res, err
	}
	res.Skipped = append(res.Skipped, skipped...)

	for _, name := range union(mailboxes, identities) {
		key, err := NewKey()
		if err != nil {
			return res, err
		}
		id := Identity{Name: name, Key: key, Minted: true}

		if mailboxes[name] {
			created, err := rewrite(filepath.Join(mailmanRoot, usersDir, name, userFile), name, key, c, false)
			if err != nil {
				var parse fault.Parse
				if asParse(err, &parse) {
					res.Skipped = append(res.Skipped, Skip{Name: name, Why: parse.Reason})
				} else {
					return res, err
				}
			} else {
				_ = created
				id.In = append(id.In, "mailman")
			}
		}

		if identities[name] {
			// The record and the plaintext key are written together. Orc reads
			// both and would refuse a pair that disagreed — and a probe holding
			// a real key beside a probe digest is exactly the leak this whole
			// package exists to prevent.
			dir := filepath.Join(orcRoot, identitiesDir, name)
			if _, err := rewrite(filepath.Join(dir, userFile), name, key, c, false); err != nil {
				var parse fault.Parse
				if asParse(err, &parse) {
					res.Skipped = append(res.Skipped, Skip{Name: name, Why: parse.Reason})
				} else {
					return res, err
				}
			} else {
				if err := writeKey(filepath.Join(dir, keyFile), key); err != nil {
					return res, err
				}
				id.In = append(id.In, "orc")
			}
		}

		if len(id.In) > 0 {
			res.Identities = append(res.Identities, id)
		}
	}

	dir := filepath.Join(mailmanRoot, usersDir)

	// God comes last so it sorts with everything else and so a store that
	// already has a mailbox called god keeps it — reminted like any other,
	// rather than replaced by a fresh one.
	if !has(res.Identities, God) {
		id, err := create(filepath.Join(dir, God, userFile), God, c)
		if err != nil {
			return res, err
		}
		res.Identities = append(res.Identities, id)
	}

	sort.Slice(res.Identities, func(i, j int) bool { return res.Identities[i].Name < res.Identities[j].Name })
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Name < res.Skipped[j].Name })
	return res, nil
}

// names lists the entities under a store's directory that carry a record,
// reporting the ones it will not touch rather than passing over them.
func names(dir, record string) (map[string]bool, []Skip, error) {
	found := map[string]bool{}
	var skipped []Skip

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return found, nil, nil // that tool has no store in this probe
		}
		return nil, nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if err := checkName(name); err != nil {
			skipped = append(skipped, Skip{Name: name, Why: "not a valid name"})
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name, record)); err != nil {
			if os.IsNotExist(err) {
				skipped = append(skipped, Skip{Name: name, Why: "has no " + record})
				continue
			}
			return nil, nil, fault.IO{Op: "look at", Path: filepath.Join(dir, name, record), Err: err}
		}
		found[name] = true
	}
	return found, skipped, nil
}

// union returns every name in either store, in order, so one key is minted per
// agent rather than per store.
func union(left, right map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, set := range []map[string]bool{left, right} {
		for name := range set {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// rewrite replaces one record's salt and digest with a probe key's, keeping its
// name and creation time. Everything else about the entity — its journal, its
// mail, its history — is untouched: the credential is the only thing that must
// not survive the copy.
func rewrite(path, name, key string, c clock.Clock, fresh bool) (Identity, error) {
	created := clock.Format(c.Now())

	if !fresh {
		data, err := os.ReadFile(path)
		if err != nil {
			return Identity{}, fault.IO{Op: "read", Path: path, Err: err}
		}

		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var rec stored
		if err := dec.Decode(&rec); err != nil {
			return Identity{}, fault.Parse{Path: path, Reason: "user record: " + err.Error()}
		}
		if rec.Version != FormatVersion {
			return Identity{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
				"user record is version %d, this orcprobe mints version %d", rec.Version, FormatVersion)}
		}
		if rec.Name != name {
			return Identity{}, fault.Parse{Path: path, Reason: "record names " + quote(rec.Name) + " but sits in " + quote(name)}
		}
		if rec.Algo != AlgoHMAC {
			return Identity{}, fault.Parse{Path: path,
				Reason: "record uses algorithm " + quote(rec.Algo) + ", which orcprobe cannot mint for"}
		}
		if _, err := clock.Parse(rec.Created); err == nil {
			created = rec.Created
		}
	}

	return write(path, name, key, created)
}

// writeKey stores a plaintext key, owner-readable only.
//
// Orc is the one store that keeps these. A probe's copy must hold a key that
// opens the probe and nothing else, so this is written in the same breath as
// the record that verifies it — a pair that disagreed would be a fleet where
// every identity is locked out of its own probe.
func writeKey(path, key string) error {
	if err := os.WriteFile(path, []byte(key+"\n"), snapshot.FileMode); err != nil {
		return fault.IO{Op: "write", Path: path, Err: err}
	}
	return nil
}

// create makes a mailbox that was not in the real store.
func create(path, name string, c clock.Clock) (Identity, error) {
	if err := os.MkdirAll(filepath.Dir(path), snapshot.DirMode); err != nil {
		return Identity{}, fault.IO{Op: "create", Path: filepath.Dir(path), Err: err}
	}
	key, err := NewKey()
	if err != nil {
		return Identity{}, err
	}
	id, err := rewrite(path, name, key, c, true)
	if err != nil {
		return Identity{}, err
	}
	id.Minted = false
	id.In = []string{"mailman"}
	return id, nil
}

// write stores the record that verifies a key.
//
// Written through a temporary file and renamed, like every other record in this
// tree: a half-written user.json is an identity nobody can be.
func write(path, name, key, created string) (Identity, error) {
	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return Identity{}, fault.Internal{Where: "mint.write", Detail: "no entropy: " + err.Error()}
	}

	digest, err := derive(name, key, salt)
	if err != nil {
		return Identity{}, err
	}
	data, err := json.MarshalIndent(stored{
		Version: FormatVersion,
		Name:    name,
		Algo:    AlgoHMAC,
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Digest:  base64.StdEncoding.EncodeToString(digest),
		Created: created,
	}, "", "  ")
	if err != nil {
		return Identity{}, fault.Internal{Where: "mint.write", Detail: err.Error()}
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return Identity{}, fault.IO{Op: "create a temporary file beside", Path: path, Err: err}
	}
	tmpName := tmp.Name()
	abandon := func(op string, cause error) (Identity, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return Identity{}, fault.IO{Op: op, Path: path, Err: cause}
	}
	if _, err := tmp.Write(data); err != nil {
		return abandon("write", err)
	}
	if err := tmp.Sync(); err != nil {
		return abandon("flush", err)
	}
	if err := tmp.Chmod(snapshot.FileMode); err != nil {
		return abandon("set permissions on", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return Identity{}, fault.IO{Op: "close", Path: path, Err: err}
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return Identity{}, fault.IO{Op: "replace", Path: path, Err: err}
	}
	return Identity{Name: name, Key: key, Minted: true}, nil
}

// NewKey generates probe key material.
func NewKey() (string, error) {
	buf := make([]byte, KeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fault.Internal{Where: "mint.NewKey", Detail: "no entropy: " + err.Error()}
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// derive computes a record's digest exactly as Mailman does: the salt is the
// HMAC key, and the message is the user's name, a NUL, then the key. The name
// is in the message so a record cannot be moved between mailboxes.
func derive(name, key string, salt []byte) ([]byte, error) {
	if len(salt) != SaltLen {
		return nil, fault.Internal{Where: "mint.derive", Detail: fmt.Sprintf("salt is %d bytes, want %d", len(salt), SaltLen)}
	}
	mac := hmac.New(sha256.New, salt)
	for _, part := range [][]byte{[]byte(name), {0}, []byte(key)} {
		if _, err := mac.Write(part); err != nil {
			return nil, fault.Internal{Where: "mint.derive", Detail: "hmac write: " + err.Error()}
		}
	}
	return mac.Sum(nil), nil
}

// File is the on-disk shape of identities.json.
type File struct {
	Version int `json:"version"`
	// Probe is the id of the probe these keys belong to, so a file copied out of
	// one probe and into another is recognisably wrong.
	Probe      string     `json:"probe"`
	Default    string     `json:"default"`
	Identities []Identity `json:"identities"`
}

// Save writes the key file, readable only by its owner. This is the one file in
// a probe that holds plaintext keys, which is why it is written whole and never
// appended to.
func Save(path, probeID string, ids []Identity) error {
	data, err := json.MarshalIndent(File{
		Version:    FormatVersion,
		Probe:      probeID,
		Default:    God,
		Identities: ids,
	}, "", "  ")
	if err != nil {
		return fault.Internal{Where: "mint.Save", Detail: err.Error()}
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), snapshot.DirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}
	if err := os.WriteFile(path, data, snapshot.FileMode); err != nil {
		return fault.IO{Op: "write", Path: path, Err: err}
	}
	return nil
}

// Load reads a probe's keys.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, fault.NotFound{Target: path,
				Near: []string{"this probe has no identities file; it may predate minting"}}
		}
		return File{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return File{}, fault.Parse{Path: path, Reason: "identities: " + err.Error()}
	}
	if f.Version != FormatVersion {
		return File{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"identities file is version %d, this orcprobe writes version %d", f.Version, FormatVersion)}
	}
	return f, nil
}

// Find returns one identity by name.
func (f File) Find(name string) (Identity, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	names := make([]string, 0, len(f.Identities))
	for _, id := range f.Identities {
		if id.Name == want {
			return id, nil
		}
		names = append(names, id.Name)
	}
	return Identity{}, fault.NotFound{Target: name, Near: names}
}

func has(ids []Identity, name string) bool {
	for _, id := range ids {
		if id.Name == name {
			return true
		}
	}
	return false
}

// checkName applies Mailman's name rules, so orcprobe never writes a record
// Mailman would refuse to read.
func checkName(name string) error {
	if name == "" || len(name) > 64 {
		return fault.Usage{Reason: "bad mailbox name"}
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fault.Usage{Reason: "bad mailbox name"}
		}
	}
	first := rune(name[0])
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return fault.Usage{Reason: "bad mailbox name"}
	}
	return nil
}

func asParse(err error, out *fault.Parse) bool {
	p, ok := err.(fault.Parse)
	if ok {
		*out = p
	}
	return ok
}

func quote(s string) string { return `"` + s + `"` }
