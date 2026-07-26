// Package user holds identity: the name of an agent and the secret that proves
// it.
//
// A Name is normalised exactly once, here, and every other package in every Orc
// tool handles only values of this type. That is what makes a name safe to use as a path element
// without a second thought at each call site: a Name cannot be constructed
// without passing the validation in Parse, and Parse rejects everything that
// could escape a directory or collide under case folding.
//
// Keys are never stored. A Record keeps a salt and an HMAC digest, and
// verification is a constant-time comparison, so neither the store nor a timing
// measurement gives a key back.
package user

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"orc/common/clock"
	"orc/common/fault"
)

// Limits on a name. The upper bound is well under any filesystem's, since a
// name becomes one path element among several.
const (
	MinNameLen = 1
	MaxNameLen = 64
)

// AlgoHMAC names the digest this package computes. It is stored with every
// record so a future scheme can be introduced without guessing what an existing
// record used.
const AlgoHMAC = "hmac-sha256"

// Key parameters.
//
// The digest is HMAC-SHA256 rather than a password KDF, and that is a decision
// rather than an oversight. A password KDF exists to make guessing a
// *low-entropy human secret* expensive. This key is not one: Orc mints it, a
// process stores it, and no human ever types it. Against a 256-bit random key,
// two hundred thousand iterations buy nothing an attacker would notice — and
// Mailman authenticates on *every* command with no session to amortise the cost
// over, so they would buy that nothing a hundred milliseconds at a time.
//
// MinKeyLen is what makes the reasoning hold, so it is enforced on the way in
// and on the way out. If human-chosen keys ever become a case, the Algo field
// is where a real KDF goes.
const (
	SaltLen   = 32
	DigestLen = 32

	// MinKeyLen is 32 bytes because that is the entropy the digest choice above
	// assumes. A shorter key is refused rather than stretched.
	MinKeyLen = 32
	MaxKeyLen = 1024
)

// reserved names would be ambiguous as path elements or as query values.
var reserved = map[string]bool{
	".": true, "..": true,
	"all": true, "any": true, "none": true,
	"system": true, "mailman": true,
}

// Name is a validated, normalised mailbox name. The zero value is not usable;
// construct one with Parse.
type Name struct {
	s string
}

// Parse normalises and validates a name.
//
// Normalisation is trim then lowercase. Case folding is what stops "Alice" and
// "alice" being two mailboxes on a case-sensitive filesystem and one mailbox on
// a case-insensitive one — the same store must not mean different things on
// macOS and Linux.
func Parse(raw string) (Name, error) {
	if !utf8.ValidString(raw) {
		return Name{}, fault.Usage{Reason: "user name is not valid UTF-8"}
	}
	s := strings.ToLower(strings.TrimSpace(raw))

	switch {
	case s == "":
		return Name{}, fault.Usage{Reason: "user name is empty"}
	case len(s) < MinNameLen:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("user name %q is shorter than %d characters", raw, MinNameLen)}
	case len(s) > MaxNameLen:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("user name %q is longer than %d characters", raw, MaxNameLen)}
	case reserved[s]:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("user name %q is reserved", s)}
	}

	for i, r := range s {
		if !allowed(r) {
			return Name{}, fault.Usage{Reason: fmt.Sprintf(
				"user name %q contains %q at position %d; use letters, digits, and . _ -", raw, r, i+1)}
		}
	}
	if !isAlphanumeric(rune(s[0])) {
		return Name{}, fault.Usage{Reason: fmt.Sprintf("user name %q must start with a letter or digit", raw)}
	}

	n := Name{s: s}
	if err := n.validate(); err != nil {
		return Name{}, err
	}
	return n, nil
}

// MustParse is Parse for package-level test data and constants. It returns the
// zero Name on failure rather than panicking, and callers that use it are
// expected to be checked by a test — Mailman has no panicking constructors.
func MustParse(raw string) (Name, bool) {
	n, err := Parse(raw)
	return n, err == nil
}

func allowed(r rune) bool {
	return isAlphanumeric(r) || r == '.' || r == '_' || r == '-'
}

func isAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// validate re-derives what Parse is expected to guarantee. It runs on every
// constructed Name so a defect in Parse surfaces here rather than as a path
// traversal much later.
func (n Name) validate() error {
	const where = "user.Name"
	if err := fault.Check(n.s != "", where, "name is empty"); err != nil {
		return err
	}
	if err := fault.Check(len(n.s) <= MaxNameLen, where, "name %q is %d bytes", n.s, len(n.s)); err != nil {
		return err
	}
	if err := fault.Check(!reserved[n.s], where, "name %q is reserved", n.s); err != nil {
		return err
	}
	if err := fault.Check(n.s == strings.ToLower(n.s), where, "name %q is not lowercased", n.s); err != nil {
		return err
	}
	for _, r := range n.s {
		if err := fault.Check(allowed(r), where, "name %q contains %q", n.s, r); err != nil {
			return err
		}
	}
	return fault.Check(isAlphanumeric(rune(n.s[0])), where, "name %q does not start alphanumerically", n.s)
}

// String returns the normalised name.
func (n Name) String() string { return n.s }

// Zero reports whether the name was never constructed.
func (n Name) Zero() bool { return n.s == "" }

// Compare orders names, so a rendered list is stable.
func (n Name) Compare(other Name) int { return strings.Compare(n.s, other.s) }

// ParseList normalises a list of names, dropping duplicates and preserving
// first-mention order. An empty entry is an error rather than a silent skip: a
// recipient list with a hole in it means the caller built it wrong, and mail
// that goes to fewer people than intended is the failure this whole tool exists
// to avoid.
func ParseList(raws []string) ([]Name, error) {
	out := make([]Name, 0, len(raws))
	seen := make(map[string]bool, len(raws))
	var problems []error

	for i, raw := range raws {
		n, err := Parse(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("recipient %d: %w", i+1, err))
			continue
		}
		if seen[n.s] {
			continue
		}
		seen[n.s] = true
		out = append(out, n)
	}
	if len(problems) > 0 {
		// Report every bad name at once; an agent fixing a recipient list should
		// get one round trip, not one per typo.
		return nil, joinUsage(problems)
	}
	return out, nil
}

// joinUsage folds several name problems into one usage fault, so the result
// still classifies as ErrUsage rather than degrading to an untyped error.
func joinUsage(problems []error) error {
	msgs := make([]string, 0, len(problems))
	for _, p := range problems {
		msgs = append(msgs, p.Error())
	}
	return fault.Usage{Reason: strings.Join(msgs, "; ")}
}

// Names renders a list for storage or display.
func Names(list []Name) []string {
	out := make([]string, len(list))
	for i, n := range list {
		out[i] = n.s
	}
	return out
}

// Contains reports whether list includes name.
func Contains(list []Name, name Name) bool {
	return slices.ContainsFunc(list, func(n Name) bool { return n.s == name.s })
}

// Record is a user's stored identity: who they are and what proves it. The
// zero value is not usable; construct one with NewRecord or Decode.
type Record struct {
	name    Name
	algo    string
	salt    []byte
	digest  []byte
	created time.Time
}

// NewKey mints a fresh key: 32 random bytes, base64-encoded so it survives an
// environment variable and a JSON file intact.
//
// Provisioning belongs to Orc, but Orc has to mint the key with the same
// properties this package verifies against, so the minting lives here next to
// the requirements rather than in whatever calls it.
func NewKey(entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(entropy, raw); err != nil {
		return "", fault.IO{Op: "read entropy for", Path: "new key", Err: err}
	}
	key := base64.RawStdEncoding.EncodeToString(raw)
	if err := CheckKey(key); err != nil {
		return "", fault.Internal{Where: "user.NewKey", Detail: "minted a key that fails validation: " + err.Error()}
	}
	return key, nil
}

// NewRecord derives a fresh record for name from key.
//
// entropy is injected so a test can pin the salt; it must be a cryptographic
// source in production. A short read from it is a hard failure — deriving a key
// from a partly-filled salt is exactly the silent weakening this package exists
// to prevent.
func NewRecord(name Name, key string, at time.Time, entropy io.Reader) (Record, error) {
	if err := name.validate(); err != nil {
		return Record{}, err
	}
	if err := CheckKey(key); err != nil {
		return Record{}, err
	}
	if entropy == nil {
		entropy = rand.Reader
	}

	salt := make([]byte, SaltLen)
	if _, err := io.ReadFull(entropy, salt); err != nil {
		return Record{}, fault.IO{Op: "read entropy for", Path: name.String(), Err: err}
	}

	digest, err := derive(AlgoHMAC, name, key, salt)
	if err != nil {
		return Record{}, err
	}

	r := Record{
		name:    name,
		algo:    AlgoHMAC,
		salt:    salt,
		digest:  digest,
		created: clock.Normalise(at),
	}
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}

// CheckKey validates key material before it is used or stored.
func CheckKey(key string) error {
	switch {
	case key == "":
		return fault.Usage{Reason: "key is empty"}
	case len(key) < MinKeyLen:
		return fault.Usage{Reason: fmt.Sprintf("key is shorter than %d bytes", MinKeyLen)}
	case len(key) > MaxKeyLen:
		return fault.Usage{Reason: fmt.Sprintf("key is longer than %d bytes", MaxKeyLen)}
	case !utf8.ValidString(key):
		return fault.Usage{Reason: "key is not valid UTF-8"}
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fault.Usage{Reason: "key contains a control character"}
		}
	}
	return nil
}

// derive computes a record's digest.
//
// The salt is the HMAC key, so two users who somehow share a key still have
// unrelated digests and a digest lifted from one store is useless against
// another.
//
// The user's *name* is part of the message, ahead of the key and separated from
// it by a NUL that neither can contain. That binding is what stops a record
// being moved: copying alice's user.json into bob's directory would otherwise
// produce a bob who authenticates with alice's key, since nothing else about
// the digest mentions who it belongs to.
func derive(algo string, name Name, key string, salt []byte) ([]byte, error) {
	const where = "user.derive"
	if err := fault.Check(algo == AlgoHMAC, where, "unsupported algorithm %q", algo); err != nil {
		return nil, err
	}
	if err := fault.Check(len(salt) == SaltLen, where, "salt is %d bytes, want %d", len(salt), SaltLen); err != nil {
		return nil, err
	}
	if err := name.validate(); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, salt)
	// Writing to a hash cannot fail, but the errors are checked rather than
	// discarded: the house rule has no exception for "cannot happen".
	if _, err := mac.Write([]byte(name.String())); err != nil {
		return nil, fault.Internal{Where: where, Detail: "hmac write: " + err.Error()}
	}
	if _, err := mac.Write([]byte{0}); err != nil {
		return nil, fault.Internal{Where: where, Detail: "hmac write: " + err.Error()}
	}
	if _, err := mac.Write([]byte(key)); err != nil {
		return nil, fault.Internal{Where: where, Detail: "hmac write: " + err.Error()}
	}
	digest := mac.Sum(nil)
	if err := fault.Check(len(digest) == DigestLen, where, "digest is %d bytes, want %d", len(digest), DigestLen); err != nil {
		return nil, err
	}
	return digest, nil
}

func (r Record) validate() error {
	const where = "user.Record"
	if err := r.name.validate(); err != nil {
		return err
	}
	if err := fault.Check(len(r.salt) == SaltLen, where, "salt is %d bytes, want %d", len(r.salt), SaltLen); err != nil {
		return err
	}
	if err := fault.Check(len(r.digest) == DigestLen, where, "digest is %d bytes, want %d", len(r.digest), DigestLen); err != nil {
		return err
	}
	if err := fault.Check(r.algo == AlgoHMAC, where, "unsupported algorithm %q", r.algo); err != nil {
		return err
	}
	if err := fault.Check(!bytes.Equal(r.salt, make([]byte, SaltLen)), where, "salt is all zeroes"); err != nil {
		return err
	}
	return fault.Check(!r.created.IsZero(), where, "created time is zero")
}

// Name returns whose record this is.
func (r Record) Name() Name { return r.name }

// Created returns when the record was made.
func (r Record) Created() time.Time { return r.created }

// Verify reports whether key opens this record.
//
// The comparison is constant-time, and a malformed record fails closed: there
// is no path through this function on which a damaged or truncated user.json
// results in "no key required".
func (r Record) Verify(key string) error {
	if err := r.validate(); err != nil {
		// A corrupt record is an internal-shaped problem, but the caller must not
		// be let in, so it is reported as an authentication failure with the real
		// cause carried in Detail.
		return fault.Auth{Reason: "authentication failed", Detail: "stored record is invalid: " + err.Error()}
	}
	if err := CheckKey(key); err != nil {
		return fault.Auth{Reason: "authentication failed", Detail: "offered key is invalid: " + err.Error()}
	}

	got, err := derive(r.algo, r.name, key, r.salt)
	if err != nil {
		return fault.Auth{Reason: "authentication failed", Detail: err.Error()}
	}
	if subtle.ConstantTimeCompare(got, r.digest) != 1 {
		return fault.Auth{Reason: "authentication failed", Detail: "key does not match"}
	}
	return nil
}

// stored is the on-disk shape of a Record. It is separate from Record so the
// domain type keeps unexported fields and no JSON tags, and so decoding has an
// explicit validation step rather than trusting whatever json.Unmarshal built.
type stored struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Algo    string `json:"algo"`
	Salt    string `json:"salt"`
	Digest  string `json:"digest"`
	Created string `json:"created"`
}

// FormatVersion is the record layout this package writes.
const FormatVersion = 1

// Encode renders a record for storage.
func (r Record) Encode() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(stored{
		Version: FormatVersion,
		Name:    r.name.String(),
		Algo:    r.algo,
		Salt:    base64.StdEncoding.EncodeToString(r.salt),
		Digest:  base64.StdEncoding.EncodeToString(r.digest),
		Created: clock.Format(r.created),
	}, "", "  ")
	if err != nil {
		return nil, fault.Internal{Where: "user.Record.Encode", Detail: err.Error()}
	}
	return append(data, '\n'), nil
}

// Decode reads a stored record, validating every field.
//
// Unknown fields are refused rather than ignored: a field this version does not
// understand means a newer Mailman wrote the store, and proceeding on a partial
// understanding of someone's credentials is the wrong kind of forgiving.
func Decode(path string, data []byte) (Record, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var s stored
	if err := dec.Decode(&s); err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "user record: " + err.Error()}
	}
	if dec.More() {
		return Record{}, fault.Parse{Path: path, Reason: "user record has trailing content"}
	}

	if s.Version != FormatVersion {
		return Record{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"user record is version %d, this mailman writes version %d", s.Version, FormatVersion)}
	}

	name, err := Parse(s.Name)
	if err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "user record name: " + err.Error()}
	}
	salt, err := base64.StdEncoding.DecodeString(s.Salt)
	if err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "user record salt is not base64"}
	}
	digest, err := base64.StdEncoding.DecodeString(s.Digest)
	if err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "user record digest is not base64"}
	}
	created, err := clock.Parse(s.Created)
	if err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "user record created: " + err.Error()}
	}

	r := Record{name: name, algo: s.Algo, salt: salt, digest: digest, created: created}
	if err := r.validate(); err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "user record is invalid: " + err.Error()}
	}
	return r, nil
}
