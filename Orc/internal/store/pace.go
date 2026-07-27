package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
)

// How often a fleet is kept moving, as something stored rather than as a flag.
//
// `orc wake --every`, `orc wake --after`, and `orc tend --watch` are flags on
// running processes: read once at startup, and unreachable from anywhere else
// afterwards. That is fine for a cycle somebody starts by hand and impossible for
// one they want to change from a browser — a form cannot set a flag on a process
// that is already running.
//
// So the values live here, and the cycles re-read them at the top of every pass.
// The consequence worth stating is the one that makes it usable: **a change takes
// effect on the next pass**, with nothing to restart and no signal to send. Worst
// case it is one interval late, which for a cycle measured in minutes is what
// "immediately" means.
//
// The layering is the wake message's, deliberately — the identity's, else its
// role's, else the fleet's, else the built-in. That chain already exists, it is
// already tested, and an operator already knows it; a second override scheme beside
// it would be a second thing to explain and to get wrong.

const paceFile = "pace.json"

// Pace is one layer's settings. Empty fields inherit; nothing here has a default,
// because a default written into a layer would be indistinguishable from somebody
// choosing that value and would override the layer below.
type Pace struct {
	// WakeAfter is how long a session may be quiet before it is woken.
	WakeAfter string `json:"wake_after,omitempty"`
	// WakeEvery is how often the cycle looks.
	WakeEvery string `json:"wake_every,omitempty"`
	// TendWatch is how often the worklist is reconciled.
	TendWatch string `json:"tend_watch,omitempty"`
	// WakeOff and TendOff stop a cycle for this layer. They are strings rather
	// than booleans so that "not set" and "set to on" are different things: a
	// `false` in JSON is indistinguishable from an absent field once decoded, and
	// an identity that has deliberately turned *on* what its role turned off must
	// be able to say so.
	WakeOff string `json:"wake_off,omitempty"` // "yes" or "no"
	TendOff string `json:"tend_off,omitempty"`
}

// Empty reports whether the layer says anything at all.
func (p Pace) Empty() bool { return p == Pace{} }

// Setting is one resolved value and where it came from, so a screen can say
// "20m, from the role" rather than a number with no provenance.
type Setting struct {
	Value string
	From  instruct.Kind
}

// Set reports whether any layer set this, as against it falling through to the
// built-in default.
func (s Setting) Set() bool { return s.Value != "" }

// Pacing is every cycle's settings for one identity, resolved.
type Pacing struct {
	WakeAfter Setting
	WakeEvery Setting
	TendWatch Setting
	WakeOff   Setting
	TendOff   Setting
}

// Off reports whether a resolved switch is on or off, treating anything but "yes"
// as on. A misspelled value leaves a cycle *running*, which is the safe direction:
// a fleet that quietly stopped being tended because a file said `off ` with a
// trailing space is a fleet nobody is watching.
func (s Setting) Off() bool { return strings.EqualFold(strings.TrimSpace(s.Value), "yes") }

// Duration parses a resolved interval, falling back to what the caller would have
// used. An unparseable value is the fallback and not an error: this is read on
// every pass of a cycle, and a cycle that stopped because a setting was mistyped
// would be the worst possible response to a typo.
func (s Setting) Duration(fallback time.Duration) time.Duration {
	got, err := time.ParseDuration(strings.TrimSpace(s.Value))
	if err != nil || got <= 0 {
		return fallback
	}
	return got
}

func (s *Store) pacePath(kind instruct.Kind, role model.Name, name user.Name) (string, error) {
	switch kind {
	case instruct.System:
		return filepath.Join(s.root, promptsDir, paceFile), nil
	case instruct.Role:
		if role.Zero() {
			return "", fault.Internal{Where: "store.pacePath", Detail: "a role's pace needs a role"}
		}
		return filepath.Join(s.roleDir(role), paceFile), nil
	case instruct.Identity:
		if name.Zero() {
			return "", fault.Internal{Where: "store.pacePath", Detail: "an identity's pace needs an identity"}
		}
		return filepath.Join(s.identityDir(name), paceFile), nil
	default:
		return "", fault.Internal{Where: "store.pacePath", Detail: "unknown layer " + string(kind)}
	}
}

// Pace reads one layer, exactly as it was written.
//
// A missing file is an empty layer. An unreadable one is *also* an empty layer,
// reported as such rather than as an error: every caller of this is a cycle
// deciding how long to sleep, and none of them should stop because a settings file
// was half-written a moment ago.
func (s *Store) Pace(kind instruct.Kind, role model.Name, name user.Name) (Pace, bool) {
	path, err := s.pacePath(kind, role, name)
	if err != nil {
		return Pace{}, false
	}
	data, err := s.ops.readFile(path)
	if err != nil {
		return Pace{}, false
	}
	var got Pace
	if err := json.Unmarshal(data, &got); err != nil {
		return Pace{}, false
	}
	return got, !got.Empty()
}

// SetPace writes one layer.
func (s *Store) SetPace(kind instruct.Kind, role model.Name, name user.Name, pace Pace) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	path, err := s.pacePath(kind, role, name)
	if err != nil {
		return err
	}
	if pace.Empty() {
		// A layer that says nothing is a layer that is not there. Keeping an empty
		// file would make `orc pace` list a layer somebody had cleared.
		if err := s.ops.remove(path); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: path, Err: err}
		}
		return nil
	}

	if err := s.ops.mkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}
	data, err := json.Marshal(pace)
	if err != nil {
		return fault.Internal{Where: "store.SetPace", Detail: err.Error()}
	}
	return s.writeFile(path, append(data, '\n'))
}

// Pacing resolves every setting for one identity, nearest layer first.
func (s *Store) Pacing(name user.Name, role model.Name) Pacing {
	identity, _ := s.Pace(instruct.Identity, model.Name{}, name)
	var forRole Pace
	if !role.Zero() {
		forRole, _ = s.Pace(instruct.Role, role, user.Name{})
	}
	fleet, _ := s.Pace(instruct.System, model.Name{}, user.Name{})

	pick := func(of func(Pace) string) Setting {
		for _, layer := range []struct {
			pace Pace
			kind instruct.Kind
		}{
			{identity, instruct.Identity},
			{forRole, instruct.Role},
			{fleet, instruct.System},
		} {
			if got := strings.TrimSpace(of(layer.pace)); got != "" {
				return Setting{Value: got, From: layer.kind}
			}
		}
		return Setting{}
	}

	return Pacing{
		WakeAfter: pick(func(p Pace) string { return p.WakeAfter }),
		WakeEvery: pick(func(p Pace) string { return p.WakeEvery }),
		TendWatch: pick(func(p Pace) string { return p.TendWatch }),
		WakeOff:   pick(func(p Pace) string { return p.WakeOff }),
		TendOff:   pick(func(p Pace) string { return p.TendOff }),
	}
}

// FleetPacing resolves the fleet's own layer, for the cycles that are about the
// whole fleet rather than about one agent — `orc tend --watch` reconciles a subtree
// and `orc wake --every` sweeps one.
func (s *Store) FleetPacing() Pacing {
	return s.Pacing(user.Name{}, model.Name{})
}
