package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"orc/common/clock"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
)

// Where standing instructions live.
//
// Beside the thing they describe, rather than in one central tree: the store's
// existing rule is one directory per thing, so `orc remove role engineer` already
// takes `roles/engineer/` and everything in it. A prompt filed somewhere else would
// outlive the role it belonged to and be an orphan nobody notices for months. The
// two fleet-level files have nowhere else to go, so they get a directory of their
// own.
//
//	<root>/prompts/system.md            the fleet
//	<root>/prompts/wake.md              the fleet's wake message
//	<root>/roles/<role>/prompt.md       this job
//	<root>/roles/<role>/wake.md
//	<root>/identities/<name>/prompt.md  this agent
//	<root>/identities/<name>/wake.md
//
// They are plain files rather than journalled records, and deliberately: a prompt is
// prose an operator edits, with no fields to validate and no invariant to hold. What
// is worth journalling is the *fact* that one changed, which is a later step.
const (
	promptsDir = "prompts"
	promptFile = "prompt.md"
	wakeFile   = "wake.md"
	systemFile = "system.md"
)

// Target names one standing instruction: which kind, whose, and whether it is the
// wake message rather than the prompt.
//
// It is a struct rather than three arguments because a role name and an agent name
// are different types in this tree and should stay that way here — a signature
// taking one name for both would accept `PromptPath(Role, emberTheIdentity)` and
// file a role's prompt under an agent's directory.
type Target struct {
	Kind instruct.Kind
	// Role is set for instruct.Role.
	Role model.Name
	// Identity is set for instruct.Identity.
	Identity user.Name
	// Wake asks for the message rather than the prompt. It is a field rather than
	// a fourth Kind because the *addressing* is the same — a role's wake message
	// lives beside a role's prompt — and only the bound and the composition rule
	// differ.
	Wake bool
}

// FleetPrompt is the fleet's own layer, which names nobody.
func FleetPrompt(wake bool) Target { return Target{Kind: instruct.System, Wake: wake} }

// RolePrompt is one job's.
func RolePrompt(role model.Name, wake bool) Target {
	return Target{Kind: instruct.Role, Role: role, Wake: wake}
}

// IdentityPrompt is one agent's.
func IdentityPrompt(name user.Name, wake bool) Target {
	return Target{Kind: instruct.Identity, Identity: name, Wake: wake}
}

// PromptPath is where one standing instruction lives.
func (s *Store) PromptPath(t Target) (string, error) {
	file := promptFile
	if t.Wake {
		file = wakeFile
	}

	switch t.Kind {
	case instruct.System:
		if !t.Role.Zero() || !t.Identity.Zero() {
			return "", fault.Internal{Where: "store.PromptPath", Detail: "the fleet prompt names nobody"}
		}
		if t.Wake {
			return filepath.Join(s.root, promptsDir, wakeFile), nil
		}
		return filepath.Join(s.root, promptsDir, systemFile), nil

	case instruct.Role:
		if t.Role.Zero() {
			return "", fault.Internal{Where: "store.PromptPath", Detail: "a role prompt needs a role"}
		}
		return filepath.Join(s.roleDir(t.Role), file), nil

	case instruct.Identity:
		if t.Identity.Zero() {
			return "", fault.Internal{Where: "store.PromptPath", Detail: "an identity prompt needs an identity"}
		}
		return filepath.Join(s.identityDir(t.Identity), file), nil

	default:
		return "", fault.Internal{Where: "store.PromptPath", Detail: "unknown kind " + string(t.Kind)}
	}
}

// PromptChange is one recorded edit: who, when, and the digest of what they left.
//
// The digest rather than the text. The file *is* the text, and a journal carrying
// every revision would be a second copy of the prose, diverging from the first the
// moment somebody edits the file by hand — which §3 expects them to. A digest
// answers the question actually asked, which is "did this change since Tuesday".
type PromptChange struct {
	By     user.Name
	At     time.Time
	Digest string
	// Size is the text's length in bytes, so `orc instruct` can show the cost of a
	// layer beside when it last moved.
	Size int
	// Cleared marks the change that removed the layer, which has no digest of its
	// own and is otherwise indistinguishable from an empty edit.
	Cleared bool
}

// promptChange is the on-disk line.
type promptChange struct {
	Version int    `json:"version"`
	By      string `json:"by"`
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Wake    bool   `json:"wake,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Size    int    `json:"size,omitempty"`
	Cleared bool   `json:"cleared,omitempty"`
}

// promptJournal is where changes to a target's prompts are recorded.
//
// One file per owning directory, beside the prompts themselves, so a role's history
// goes when the role does — the same rule that put the prompts there.
//
// §9 said to put these in the entity's existing journal. They are not there, and the
// reason is what those journals are: typed event streams that a fold replays to
// reconstruct an identity or a role. A prompt change reconstructs nothing — the file
// is the state — so an event in that stream would be one every fold had to carry and
// ignore, and one more shape in a vocabulary whose totality is tested. A separate
// append-only file beside the prose keeps both journals meaning exactly one thing.
func (s *Store) promptJournal(t Target) (string, error) {
	switch t.Kind {
	case instruct.System:
		return filepath.Join(s.root, promptsDir, "journal.jsonl"), nil
	case instruct.Role:
		if t.Role.Zero() {
			return "", fault.Internal{Where: "store.promptJournal", Detail: "a role prompt needs a role"}
		}
		return filepath.Join(s.roleDir(t.Role), "prompts.jsonl"), nil
	case instruct.Identity:
		if t.Identity.Zero() {
			return "", fault.Internal{Where: "store.promptJournal", Detail: "an identity prompt needs an identity"}
		}
		return filepath.Join(s.identityDir(t.Identity), "prompts.jsonl"), nil
	default:
		return "", fault.Internal{Where: "store.promptJournal", Detail: "unknown kind " + string(t.Kind)}
	}
}

// recordChange appends one line to the target's prompt journal.
//
// A failure here does not undo the write. The prompt is the thing that matters and
// it is already on disk; losing the note that it changed is worth less than leaving
// an operator with a prompt they were told did not save. It is returned so a caller
// can say so.
func (s *Store) recordChange(t Target, by user.Name, text string, cleared bool) error {
	path, err := s.promptJournal(t)
	if err != nil {
		return err
	}

	line := promptChange{
		Version: Version,
		By:      by.String(),
		At:      clock.Format(s.Now()),
		Kind:    string(t.Kind),
		Wake:    t.Wake,
		Cleared: cleared,
	}
	if !cleared {
		sum := sha256.Sum256([]byte(text))
		line.Digest = hex.EncodeToString(sum[:])
		line.Size = len(text)
	}

	encoded, err := json.Marshal(line)
	if err != nil {
		return fault.Internal{Where: "store.recordChange", Detail: err.Error()}
	}
	return s.appendLine(path, encoded)
}

// LastChange is the most recent recorded edit to one layer.
//
// A journal that will not read is reported rather than treated as "never changed":
// the two are different answers, and the second is the one that makes somebody stop
// looking for the reason an agent's behaviour moved.
func (s *Store) LastChange(t Target) (PromptChange, bool, error) {
	path, err := s.promptJournal(t)
	if err != nil {
		return PromptChange{}, false, err
	}

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PromptChange{}, false, nil
		}
		return PromptChange{}, false, fault.IO{Op: "read", Path: path, Err: err}
	}

	var got PromptChange
	found := false
	for i, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var line promptChange
		if err := json.Unmarshal(raw, &line); err != nil {
			return PromptChange{}, false, fault.Parse{Path: path, Line: i + 1,
				Reason: "prompt change: " + err.Error()}
		}
		// One file holds a directory's prompt *and* its wake message, so the lines
		// are filtered rather than assumed to be about the target asked for.
		if line.Kind != string(t.Kind) || line.Wake != t.Wake {
			continue
		}

		by, err := user.Parse(line.By)
		if err != nil {
			return PromptChange{}, false, fault.Parse{Path: path, Line: i + 1,
				Reason: "prompt change names a bad actor: " + err.Error()}
		}
		at, err := clock.Parse(line.At)
		if err != nil {
			return PromptChange{}, false, fault.Parse{Path: path, Line: i + 1,
				Reason: "prompt change has a bad timestamp: " + err.Error()}
		}
		got = PromptChange{By: by, At: at, Digest: line.Digest, Size: line.Size, Cleared: line.Cleared}
		found = true
	}
	return got, found, nil
}

// Prompt reads one standing instruction, reporting whether there is one.
//
// A prompt that is not there is not an error: most layers are empty most of the
// time, and composing three of them would otherwise mean three error checks for the
// ordinary case. An unreadable one *is* reported — a file that exists and cannot be
// read is a permissions mistake, and returning "no prompt" for it would compose an
// agent without an instruction somebody wrote and believes is in force.
func (s *Store) Prompt(t Target) (string, bool, error) {
	path, err := s.PromptPath(t)
	if err != nil {
		return "", false, err
	}

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fault.IO{Op: "read", Path: path, Err: err}
	}

	// Validated on the way out as well as on the way in. A file an operator edited
	// by hand — which §3 expects, since these are plain files — has not been
	// through WritePrompt, and a prompt that would be refused on write should not
	// be delivered because it arrived another way.
	text := string(data)
	if err := instruct.Check(t.bound(), text); err != nil {
		return "", false, err
	}
	return text, true, nil
}

// WritePrompt replaces one standing instruction.
//
// Empty text removes the file rather than writing an empty one: "no layer" and "a
// layer that says nothing" would compose identically, and two ways to spell one
// state is a state somebody eventually disagrees about.
func (s *Store) WritePrompt(t Target, by user.Name, text string) error {
	if err := instruct.Check(t.bound(), text); err != nil {
		return err
	}
	if by.Zero() {
		return fault.Internal{Where: "store.WritePrompt", Detail: "a change needs somebody who made it"}
	}
	path, err := s.PromptPath(t)
	if err != nil {
		return err
	}

	if text == "" {
		return s.ClearPrompt(t, by)
	}
	if err := s.writeFile(path, []byte(text)); err != nil {
		return err
	}
	return s.recordChange(t, by, text, false)
}

// ClearPrompt removes a layer. Removing one that is not there is not an error: the
// caller's intent — that this layer not exist — is satisfied either way.
func (s *Store) ClearPrompt(t Target, by user.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if by.Zero() {
		return fault.Internal{Where: "store.ClearPrompt", Detail: "a change needs somebody who made it"}
	}
	path, err := s.PromptPath(t)
	if err != nil {
		return err
	}

	if _, err := s.ops.readFile(path); os.IsNotExist(err) {
		// Nothing was there, so nothing changed, so there is nothing to record. A
		// journal line per no-op would make "when did this last move" a question
		// about how often somebody ran the command.
		return nil
	}
	if err := s.ops.remove(path); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove", Path: path, Err: err}
	}
	return s.recordChange(t, by, "", true)
}

// Instructions gathers the three layers that compose into an agent's system prompt.
//
// One call rather than three, because the composition is only meaningful as a set
// and a caller assembling it by hand could get the order wrong — which, for an
// additive composition, is the one mistake that changes what it means.
func (s *Store) Instructions(name user.Name, role model.Name) (instruct.Layers, error) {
	got := instruct.Layers{IdentityName: name.String(), RoleName: role.String()}

	system, _, err := s.Prompt(FleetPrompt(false))
	if err != nil {
		return instruct.Layers{}, err
	}
	got.System = system

	if !role.Zero() {
		text, _, err := s.Prompt(RolePrompt(role, false))
		if err != nil {
			return instruct.Layers{}, err
		}
		got.Role = text
	}

	text, _, err := s.Prompt(IdentityPrompt(name, false))
	if err != nil {
		return instruct.Layers{}, err
	}
	got.Identity = text

	return got, nil
}

// WakeMessage is what to say to an identity that has gone quiet, and where it came
// from.
//
// The override chain from Instruct.md §4: the identity's, else its role's, else the
// fleet's, else `continue`. The source is returned with it because a command about
// to type into somebody's session should be able to say which of four things it is
// sending.
func (s *Store) WakeMessage(name user.Name, role model.Name) (string, instruct.Kind, error) {
	identity, _, err := s.Prompt(IdentityPrompt(name, true))
	if err != nil {
		return "", "", err
	}

	var forRole string
	if !role.Zero() {
		forRole, _, err = s.Prompt(RolePrompt(role, true))
		if err != nil {
			return "", "", err
		}
	}

	fleet, _, err := s.Prompt(FleetPrompt(true))
	if err != nil {
		return "", "", err
	}

	return instruct.WakeFor(identity, forRole, fleet), instruct.Source(identity, forRole, fleet), nil
}

// bound is which limit applies: a wake message is a sentence whatever it is filed
// beside, and a layer is a document.
func (t Target) bound() instruct.Kind {
	if t.Wake {
		return instruct.Wake
	}
	return t.Kind
}
