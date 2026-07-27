package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
)

// What the fleet charges for thinking.
//
// A record and a journal beside it, exactly as a permission has, and for the same
// reason: this is a *policy* somebody changes, and "what did it used to be, and who
// changed it" is a question that gets asked the day a budget starts refusing things
// it did not refuse yesterday.
//
//	tariff/tariff.json    the prices now
//	tariff/tariff.jsonl   who changed what, and when
//	tariff/lock           one writer at a time
//
// A fleet that has never set one has no directory at all, and pays the built-in
// prices. That is not a migration to do: the absence is the answer.
const tariffDir = "tariff"

const (
	tariffRecord  = "tariff.json"
	tariffJournal = "tariff.jsonl"
)

// storedTariff is the record on disk.
//
// Keyed by the words a person types rather than by the enum's numbers, so the file
// is readable and so a build that renumbers an enum cannot silently reprice a fleet.
type storedTariff struct {
	Models     map[string]int `json:"models,omitempty"`
	Efforts    map[string]int `json:"efforts,omitempty"`
	CrowdBase  int            `json:"crowd_base,omitempty"`
	CrowdScale int            `json:"crowd_scale,omitempty"`
}

// tariffChange is one line of the journal.
type tariffChange struct {
	At      string `json:"at"`
	By      string `json:"by"`
	Setting string `json:"setting"`
	From    int    `json:"from"`
	To      int    `json:"to"`
}

func (s *Store) tariffPath() string    { return filepath.Join(s.root, tariffDir, tariffRecord) }
func (s *Store) tariffLog() string     { return filepath.Join(s.root, tariffDir, tariffJournal) }
func (s *Store) tariffLockDir() string { return filepath.Join(s.root, tariffDir) }

// Tariff reads what the fleet charges.
//
// A missing record is the built-in prices. An unreadable one is *also* the built-in
// prices, because this is on the path of every derivation: a fleet that could not
// answer "may this agent do that" because its price list would not parse would be a
// fleet taken down by a settings file.
func (s *Store) Tariff() model.Tariff {
	data, err := s.ops.readFile(s.tariffPath())
	if err != nil {
		return model.DefaultTariff()
	}
	var stored storedTariff
	if err := json.Unmarshal(data, &stored); err != nil {
		return model.DefaultTariff()
	}

	got := model.Tariff{
		Models:     map[model.Model]int{},
		Efforts:    map[model.Effort]int{},
		CrowdBase:  stored.CrowdBase,
		CrowdScale: stored.CrowdScale,
	}
	for name, weight := range stored.Models {
		if m, err := model.ParseModel(name); err == nil {
			got.Models[m] = weight
		}
	}
	for name, weight := range stored.Efforts {
		if e, err := model.ParseEffort(name); err == nil {
			got.Efforts[e] = weight
		}
	}
	return got.WithDefaults()
}

// SetTariff changes one setting under the lock, and records who did.
//
// One setting at a time rather than the whole list, because that is how it is
// changed and because a whole-list write from a stale form would silently revert
// whatever somebody else had set in between — the same hazard `edit permission`
// answers by carrying the whole permission, in a case where the whole is small
// enough to carry.
func (s *Store) SetTariff(by user.Name, setting model.Setting, value int) (model.Tariff, error) {
	if err := s.refuseWrite(); err != nil {
		return model.Tariff{}, err
	}
	if by.Zero() {
		return model.Tariff{}, fault.Internal{Where: "store.SetTariff", Detail: "nobody named"}
	}

	var out model.Tariff
	err := s.withLock(s.tariffLockDir(), func() error {
		current := s.Tariff()
		was, _ := current.Value(setting)

		changed, err := current.Set(setting, value)
		if err != nil {
			return err
		}

		stored := storedTariff{
			Models: map[string]int{}, Efforts: map[string]int{},
			CrowdBase: changed.CrowdBase, CrowdScale: changed.CrowdScale,
		}
		for m, weight := range changed.Models {
			stored.Models[m.String()] = weight
		}
		for e, weight := range changed.Efforts {
			stored.Efforts[e.String()] = weight
		}
		data, err := json.Marshal(stored)
		if err != nil {
			return fault.Internal{Where: "store.SetTariff", Detail: err.Error()}
		}
		if err := s.writeFile(s.tariffPath(), append(data, '\n')); err != nil {
			return err
		}

		// The journal after the record, so a crash between them loses the *note*
		// rather than the change. A price list that says one thing and a history
		// that says it was never set is recoverable; the other way round is a fleet
		// charging what its history denies.
		line, err := json.Marshal(tariffChange{
			At: clock.Format(s.Now()), By: by.String(),
			Setting: string(setting), From: was, To: value,
		})
		if err != nil {
			return fault.Internal{Where: "store.SetTariff", Detail: err.Error()}
		}
		if err := s.appendLine(s.tariffLog(), line); err != nil {
			return err
		}
		out = changed
		return nil
	})
	return out, err
}

// TariffHistory returns what has been changed, newest last.
//
// Best effort: a line that will not parse is skipped. The history is for a person
// asking why a budget started refusing things, and half of it answers that better
// than an error does.
func (s *Store) TariffHistory() []TariffChange {
	data, err := s.ops.readFile(s.tariffLog())
	if err != nil {
		return nil
	}
	var out []TariffChange
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var got tariffChange
		if err := json.Unmarshal(line, &got); err != nil {
			continue
		}
		at, err := clock.Parse(got.At)
		if err != nil {
			continue
		}
		out = append(out, TariffChange{
			At: at, By: got.By, Setting: model.Setting(got.Setting), From: got.From, To: got.To,
		})
	}
	return out
}

// TariffChange is one recorded change, as a reader wants it.
type TariffChange struct {
	At      time.Time
	By      string
	Setting model.Setting
	From    int
	To      int
}

// ClearTariff removes the record, which returns a fleet to the built-in prices.
func (s *Store) ClearTariff(by user.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	return s.withLock(s.tariffLockDir(), func() error {
		if err := s.ops.remove(s.tariffPath()); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: s.tariffPath(), Err: err}
		}
		line, err := json.Marshal(tariffChange{
			At: clock.Format(s.Now()), By: by.String(), Setting: "all", From: 0, To: 0,
		})
		if err != nil {
			return fault.Internal{Where: "store.ClearTariff", Detail: err.Error()}
		}
		return s.appendLine(s.tariffLog(), line)
	})
}
