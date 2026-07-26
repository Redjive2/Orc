package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
)

// storedEvent is the on-disk shape of one journal line, for both roles and
// identities.
//
// One struct serves both because the two vocabularies do not overlap and the
// fields they need are nearly the same — and because a single codec means one
// place where a field can be added without two decoders drifting. Which fold a
// line belongs to is decided by which journal it was read from, not by its
// contents: a role's journal is in the role's directory.
//
// It mirrors model.RoleEvent and model.IdentityEvent, which own what an event
// *means*; this owns only how it is stored. Keeping them apart is what lets the
// folds be tested without a filesystem and the codec be fuzzed without a fleet.
type storedEvent struct {
	Op   string `json:"op"`
	By   string `json:"by"`
	At   string `json:"at"`
	Line int    `json:"-"`

	Authority   int    `json:"authority,omitempty"`
	Description string `json:"description,omitempty"`
	Permission  string `json:"permission,omitempty"`
	Role        string `json:"role,omitempty"`
	Boss        string `json:"boss,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`

	// Grant fields. Session and Until are mutually exclusive, which
	// model.RestoreGrant enforces on the way back in.
	Session string `json:"session,omitempty"`
	Until   string `json:"until,omitempty"`
	Granted string `json:"granted,omitempty"`
}

// decodeLine reads one journal line into its stored shape, without deciding what
// kind of event it is.
func decodeLine(path string, line int, raw []byte) (storedEvent, error) {
	bad := func(format string, args ...any) (storedEvent, error) {
		return storedEvent{}, fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var stored storedEvent
	if err := dec.Decode(&stored); err != nil {
		return bad("journal event: %s", err)
	}
	if dec.More() {
		return bad("journal line has trailing content")
	}
	if stored.Op == "" {
		return bad("journal event has no op")
	}
	stored.Line = line
	return stored, nil
}

// encodeLine renders a stored event, checking it decodes back before it is
// written: an event that cannot be read is an event that has been lost.
func encodeLine(stored storedEvent) ([]byte, error) {
	line, err := json.Marshal(stored)
	if err != nil {
		return nil, fault.Internal{Where: "store.encodeLine", Detail: err.Error()}
	}
	if bytes.ContainsAny(line, "\n\r") {
		return nil, fault.Internal{Where: "store.encodeLine", Detail: "encoded event contains a newline"}
	}
	if _, err := decodeLine("<new event>", 1, line); err != nil {
		return nil, fault.Internal{Where: "store.encodeLine", Detail: "event does not decode back: " + err.Error()}
	}
	return line, nil
}

// actor reads the `by` and `at` fields every event carries.
func (e storedEvent) actor(path string) (user.Name, error) {
	by, err := user.Parse(e.By)
	if err != nil {
		return user.Name{}, fault.Parse{Path: path, Line: e.Line,
			Reason: "journal event names a bad actor: " + err.Error()}
	}
	return by, nil
}

// encodeRoleEvent renders a role event for storage.
func encodeRoleEvent(e model.RoleEvent) ([]byte, error) {
	stored := storedEvent{
		Op: string(e.Op()),
		By: e.By().String(),
		At: clock.Format(e.At()),
	}
	switch e.Op() {
	case model.OpAuthority:
		stored.Authority = e.Authority().Int()
	case model.OpDescribe:
		stored.Description = e.Description()
	case model.OpPermit, model.OpUnpermit:
		stored.Permission = e.Permission().String()
	default:
		return nil, fault.Internal{Where: "store.encodeRoleEvent", Detail: "unknown op " + string(e.Op())}
	}
	return encodeLine(stored)
}

// decodeRoleEvent rebuilds a role event through its own constructor, so the shape
// rules in model apply to stored bytes exactly as they do to fresh events — a
// hand-edited journal cannot introduce a shape the code never produces.
func decodeRoleEvent(path string, stored storedEvent) (model.RoleEvent, error) {
	bad := func(format string, args ...any) (model.RoleEvent, error) {
		return model.RoleEvent{}, fault.Parse{Path: path, Line: stored.Line,
			Reason: fmt.Sprintf(format, args...)}
	}

	by, err := stored.actor(path)
	if err != nil {
		return model.RoleEvent{}, err
	}
	at, err := clock.Parse(stored.At)
	if err != nil {
		return bad("journal event has a bad timestamp: %s", err)
	}

	op := model.RoleOp(stored.Op)
	if !op.Valid() {
		return bad("unknown role journal operation %q", stored.Op)
	}

	switch op {
	case model.OpAuthority:
		authority, err := model.NewAuthority(stored.Authority)
		if err != nil {
			return bad("journal event has a bad authority: %s", err)
		}
		return wrapRole(model.SetAuthority(by, at, authority))
	case model.OpDescribe:
		return wrapRole(model.Describe(by, at, stored.Description))
	case model.OpPermit, model.OpUnpermit:
		name, err := model.ParseName(stored.Permission)
		if err != nil {
			return bad("journal event names a bad permission: %s", err)
		}
		if op == model.OpPermit {
			return wrapRole(model.Permit(by, at, name))
		}
		return wrapRole(model.Unpermit(by, at, name))
	default:
		return bad("unhandled role journal operation %q", stored.Op)
	}
}

// encodeIdentityEvent renders an identity event for storage.
func encodeIdentityEvent(e model.IdentityEvent) ([]byte, error) {
	stored := storedEvent{
		Op: string(e.Op()),
		By: e.By().String(),
		At: clock.Format(e.At()),
	}
	switch e.Op() {
	case model.OpRole:
		stored.Role = e.Role().String()
	case model.OpMove:
		stored.Boss = e.Boss().String()
	case model.OpRevoke:
		stored.Permission = e.Grant().Permission().String()
	case model.OpGrant:
		g := e.Grant()
		stored.Permission = g.Permission().String()
		stored.Granted = clock.Format(g.Granted())
		stored.Session = g.Session()
		if !g.Until().IsZero() {
			stored.Until = clock.Format(g.Until())
		}
	case model.OpEmploy, model.OpModel:
		stored.Model = e.Model().String()
		stored.Effort = e.Effort().String()
	case model.OpFire:
		// Nothing but who and when: what it was employed at is already in the
		// journal, and repeating it here would be two places to disagree.
	default:
		return nil, fault.Internal{Where: "store.encodeIdentityEvent", Detail: "unknown op " + string(e.Op())}
	}
	return encodeLine(stored)
}

// decodeIdentityEvent rebuilds an identity event through its own constructor.
func decodeIdentityEvent(path string, stored storedEvent) (model.IdentityEvent, error) {
	bad := func(format string, args ...any) (model.IdentityEvent, error) {
		return model.IdentityEvent{}, fault.Parse{Path: path, Line: stored.Line,
			Reason: fmt.Sprintf(format, args...)}
	}

	by, err := stored.actor(path)
	if err != nil {
		return model.IdentityEvent{}, err
	}
	at, err := clock.Parse(stored.At)
	if err != nil {
		return bad("journal event has a bad timestamp: %s", err)
	}

	op := model.IdentityOp(stored.Op)
	if !op.Valid() {
		return bad("unknown identity journal operation %q", stored.Op)
	}

	switch op {
	case model.OpRole:
		role, err := model.ParseName(stored.Role)
		if err != nil {
			return bad("journal event names a bad role: %s", err)
		}
		return wrapIdentity(model.AssignRole(by, at, role))

	case model.OpMove:
		boss, err := user.Parse(stored.Boss)
		if err != nil {
			return bad("journal event names a bad boss: %s", err)
		}
		return wrapIdentity(model.Move(by, at, boss))

	case model.OpRevoke:
		name, err := model.ParseName(stored.Permission)
		if err != nil {
			return bad("journal event names a bad permission: %s", err)
		}
		return wrapIdentity(model.RevokePermission(by, at, name))

	case model.OpGrant:
		name, err := model.ParseName(stored.Permission)
		if err != nil {
			return bad("journal event names a bad permission: %s", err)
		}
		granted, err := clock.Parse(stored.Granted)
		if err != nil {
			return bad("grant event has a bad granted time: %s", err)
		}
		var until time.Time
		if stored.Until != "" {
			if until, err = clock.Parse(stored.Until); err != nil {
				return bad("grant event has a bad expiry: %s", err)
			}
		}
		g, err := model.RestoreGrant(name, stored.By, granted, stored.Session, until)
		if err != nil {
			return bad("grant event is not well formed: %s", err)
		}
		return wrapIdentity(model.GrantPermission(by, at, g))

	case model.OpEmploy, model.OpModel:
		m, err := model.ParseModel(stored.Model)
		if err != nil {
			return bad("%s event names a model orc cannot budget: %s", stored.Op, err)
		}
		effort, err := model.ParseEffort(stored.Effort)
		if err != nil {
			return bad("%s event has a bad effort: %s", stored.Op, err)
		}
		if model.IdentityOp(stored.Op) == model.OpModel {
			return wrapIdentity(model.Retune(by, at, m, effort))
		}
		return wrapIdentity(model.Employ(by, at, m, effort))

	case model.OpFire:
		return wrapIdentity(model.Fire(by, at))

	default:
		return bad("unhandled identity journal operation %q", stored.Op)
	}
}

// wrapRole and wrapIdentity turn a constructor's internal fault into a parse
// fault. Bad bytes on disk are not a defect in Orc, and the exit code should say
// so.

func wrapRole(e model.RoleEvent, err error) (model.RoleEvent, error) {
	if err != nil {
		return model.RoleEvent{}, fault.Parse{Reason: "journal event is not well formed: " + err.Error()}
	}
	return e, nil
}

func wrapIdentity(e model.IdentityEvent, err error) (model.IdentityEvent, error) {
	if err != nil {
		return model.IdentityEvent{}, fault.Parse{Reason: "journal event is not well formed: " + err.Error()}
	}
	return e, nil
}

// splitJournal cuts a journal into lines and reports whether the file ended on a
// complete one.
//
// The recovery rule is the whole reason for the append-only design, and it is
// Mailman's: a process killed mid-append can only damage the *last* line, so an
// unparseable final line is dropped with a count. An unparseable line anywhere
// else is corruption rather than interruption and is a hard error — silently
// skipping one would silently drop an authority change, and a dropped authority
// change is an agent with permissions nobody granted it.
func splitJournal(path string, data []byte) (lines [][]byte, complete bool, err error) {
	if len(data) > MaxJournalSize {
		return nil, false, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"journal is %d bytes, limit is %d", len(data), MaxJournalSize)}
	}
	complete = len(data) == 0 || data[len(data)-1] == '\n'
	lines = bytes.Split(data, []byte("\n"))
	if complete && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	return lines, complete, nil
}

// checkLine applies the bounds every journal line has to clear.
func checkLine(path string, lineNo int, raw []byte, last, complete bool) (skip bool, err error) {
	if len(raw) == 0 {
		if last && !complete {
			return true, nil
		}
		return false, fault.Parse{Path: path, Line: lineNo, Reason: "empty journal line"}
	}
	if len(raw) > MaxJournalLine {
		return false, fault.Parse{Path: path, Line: lineNo, Reason: fmt.Sprintf(
			"journal line is %d bytes, limit is %d", len(raw), MaxJournalLine)}
	}
	return false, nil
}

// FoldRole replays a role's journal onto its creation record, and reports how
// many trailing bytes were dropped as an interrupted append.
//
// It is a pure function of its input so the rules can be fuzzed without a
// filesystem.
func FoldRole(path string, base model.Role, data []byte) (model.Role, int, error) {
	lines, complete, err := splitJournal(path, data)
	if err != nil {
		return model.Role{}, 0, err
	}

	out, skipped := base, 0
	for i, raw := range lines {
		lineNo, last := i+1, i == len(lines)-1

		skip, err := checkLine(path, lineNo, raw, last, complete)
		if err != nil {
			return model.Role{}, 0, err
		}
		if skip {
			continue
		}

		stored, err := decodeLine(path, lineNo, raw)
		if err == nil {
			var ev model.RoleEvent
			if ev, err = decodeRoleEvent(path, stored); err == nil {
				var next model.Role
				if next, err = out.With(ev); err == nil {
					out = next
					continue
				}
				// A journal that folds to an illegal state is corruption, not an
				// interrupted write: the events were legal when they were
				// appended, so something has rewritten them.
				err = fault.Parse{Path: path, Line: lineNo, Reason: fmt.Sprintf(
					"journal event %q cannot apply: %s", ev.Op(), err)}
			}
		}
		if last && !complete {
			return out, len(raw), nil
		}
		return model.Role{}, 0, err
	}
	return out, skipped, nil
}

// FoldIdentity replays an identity's journal onto its creation record.
func FoldIdentity(path string, base model.Identity, data []byte) (model.Identity, int, error) {
	lines, complete, err := splitJournal(path, data)
	if err != nil {
		return model.Identity{}, 0, err
	}

	out, skipped := base, 0
	for i, raw := range lines {
		lineNo, last := i+1, i == len(lines)-1

		skip, err := checkLine(path, lineNo, raw, last, complete)
		if err != nil {
			return model.Identity{}, 0, err
		}
		if skip {
			continue
		}

		stored, err := decodeLine(path, lineNo, raw)
		if err == nil {
			var ev model.IdentityEvent
			if ev, err = decodeIdentityEvent(path, stored); err == nil {
				var next model.Identity
				if next, err = out.With(ev); err == nil {
					out = next
					continue
				}
				err = fault.Parse{Path: path, Line: lineNo, Reason: fmt.Sprintf(
					"journal event %q cannot apply: %s", ev.Op(), err)}
			}
		}
		if last && !complete {
			return out, len(raw), nil
		}
		return model.Identity{}, 0, err
	}
	return out, skipped, nil
}
