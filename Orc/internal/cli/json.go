package cli

import (
	"encoding/json"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/activity"
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
	"orc/orc/internal/store"
)

// The JSON shapes are a contract, not a rendering: Communiqué will mirror the
// fleet through them, and a script that branches on a field should not have to be
// rewritten when a column moves on a card.
//
// One rule governs every shape here, and it is the reason this file is separate
// from the drawing code: **no shape has a field for a key.** `orc env` is the only
// command that discloses one, and it prints shell exports rather than JSON, so
// there is no path on which a credential reaches a program that logs its input.

type jsonClause struct {
	Kind       string `json:"kind"`
	Arg        string `json:"arg"`
	Permission string `json:"permission"`
	Source     string `json:"source"`
	Capped     bool   `json:"capped"`
	Asked      string `json:"asked,omitempty"`
	Lapses     string `json:"lapses,omitempty"`
}

type jsonGrant struct {
	Permission string `json:"permission"`
	By         string `json:"by,omitempty"`
	Granted    string `json:"granted"`
	Until      string `json:"until,omitempty"`
	Session    string `json:"session,omitempty"`
	Live       bool   `json:"live"`
}

type jsonIdentity struct {
	Name         string       `json:"name"`
	ID           string       `json:"id"`
	Created      string       `json:"created"`
	Operator     bool         `json:"operator"`
	Boss         string       `json:"boss,omitempty"`
	Chain        []string     `json:"chain,omitempty"`
	Role         string       `json:"role,omitempty"`
	Authority    int          `json:"authority"`
	AskedFor     int          `json:"asked_for"`
	Capped       bool         `json:"capped"`
	Clauses      []jsonClause `json:"clauses"`
	Grants       []jsonGrant  `json:"grants"`
	Subordinates []string     `json:"subordinates,omitempty"`
	Workspace    string       `json:"workspace"`
	Budget       int          `json:"spawn_budget"`
	HasBudget    bool         `json:"has_spawn_budget"`

	// The worklist half, and then what is actually running. They are separate
	// fields because the states where they disagree are the ones worth mirroring:
	// employed with no session is an agent something keeps killing.
	Employed bool   `json:"employed"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Load     int    `json:"load"`

	Populated bool   `json:"populated"`
	Session   string `json:"session,omitempty"`
	Restarts  int    `json:"restarts,omitempty"`
	// Instructed is how many bytes of standing instruction this session started
	// with, and InstructError why there are none when there should have been. cq
	// mirrors these so the browser can answer the same question a terminal can.
	Instructed    int    `json:"instructed,omitempty"`
	InstructError string `json:"instruct_error,omitempty"`
	Started       string `json:"started,omitempty"`
	LastExit      string `json:"last_exit,omitempty"`

	// What it is doing, as one word, with the turn it is on and the last few
	// things it did. Derived here rather than left to a reader: "employed and not
	// running" is three situations wearing one label, and a screen inferring them
	// from two booleans gets it half right. See cli/activity.go.
	Activity string    `json:"activity"`
	Turn     int       `json:"turn,omitempty"`
	Since    string    `json:"since,omitempty"`
	Why      string    `json:"why,omitempty"`
	Doing    []jsonRow `json:"doing,omitempty"`
	FeedRead bool      `json:"feed_read,omitempty"`

	// What it has cost and touched, by the hour. Bounded — see SyncWindow.
	Buckets []jsonBucket `json:"buckets,omitempty"`
	// How often this one is woken and tended, resolved through its layers, with
	// where each value came from. A browser showing "20m" over a value somebody
	// did not set on this agent would send them looking in the wrong place.
	Pace jsonPace `json:"pace,omitzero"`
}

// jsonPace is one identity's resolved cycles.
type jsonPace struct {
	WakeAfter string `json:"wake_after,omitempty"`
	WakeEvery string `json:"wake_every,omitempty"`
	TendWatch string `json:"tend_watch,omitempty"`
	WakeOff   bool   `json:"wake_off,omitempty"`
	TendOff   bool   `json:"tend_off,omitempty"`
	// From says which layer set each value: `system`, `role`, or `identity`.
	// Absent where nothing set it and the built-in stands.
	From map[string]string `json:"from,omitempty"`
}

// jsonBucket is one hour of one identity's work, on one model at one effort.
//
// A delta-folded total: the rollup on disk holds one line per read and this is the
// sum, so the number only ever grows. That is what makes it safe for a mirror to
// receive the same bucket twice — it writes the same value — and what lets a
// machine that missed a sync catch up whole rather than in order.
type jsonBucket struct {
	At     string          `json:"at"`
	Model  string          `json:"model,omitempty"`
	Effort string          `json:"effort,omitempty"`
	Turns  int             `json:"turns,omitempty"`
	Tokens activity.Tokens `json:"tokens,omitzero"`
	Files  activity.Files  `json:"files,omitzero"`
}

// jsonRow is one line of a session's event feed, as a screen shows it.
//
// The path and never the content, as everywhere else that touches this feed: a
// mirror carrying the text of every edit would be a second copy of the repository
// on a machine that has no business holding one.
type jsonRow struct {
	At     string `json:"at"`
	Turn   int    `json:"turn,omitempty"`
	Kind   string `json:"kind"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Blocked and Reason are set on refusals only, which are the rows anybody
	// reading this at a glance is looking for.
	Blocked bool   `json:"blocked,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type jsonRole struct {
	Name        string   `json:"name"`
	Authority   int      `json:"authority"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Created     string   `json:"created"`
	HeldBy      []string `json:"held_by,omitempty"`
}

type jsonPermission struct {
	Name     string   `json:"name"`
	Floor    int      `json:"floor"`
	Patterns []string `json:"patterns"`
	Created  string   `json:"created"`
}

// jsonVocabulary is the words a clause may be written with.
//
// It travels with the fleet because the only other way for cq's browser to offer
// them is to keep its own copy, and a copy of a privilege list is a copy that goes
// stale silently — offering a verb this build stopped checking, or omitting one it
// started. The fleet a browser is looking at is the authority on what it accepts.
type jsonWord struct {
	Word string `json:"word"`
	Does string `json:"does"`
	// In names the tool that checks it, and is empty for an orc verb: orc checks
	// its own.
	In string `json:"in,omitempty"`
}

type jsonVocabulary struct {
	Verbs []jsonWord `json:"verbs"`
	Tools []jsonWord `json:"tools"`
	// Innocuous is what `shell` allows with no clause at all.
	//
	// It travels for the opposite reason to the two above. Those are words a
	// clause may *use*; this is what an identity already has without one — and a
	// browser showing a permission list without it shows a fleet as more
	// restricted than it is, because the commands nobody had to ask for are the
	// ones nothing mentions.
	Innocuous []string `json:"innocuous"`
}

// jsonToolkitEntry is one toolkit permission as this build defines it, and whether
// the fleet has it.
//
// It travels for the same reason jsonVocabulary does: the toolkit is a table inside
// the binary, and any other program that wanted to show it would have to keep a copy
// — one that goes stale silently, offering a permission this build dropped or
// omitting one it added. The fleet a browser is looking at is the authority.
//
// `have` is the field that matters. `orc bootstrap` installs the toolkit and is safe
// to run again, so a fleet made before a permission existed simply does not have it —
// and until something says so, the only symptom is a screen that is missing rows
// nobody knew to expect.
type jsonToolkitEntry struct {
	Name     string   `json:"name"`
	Floor    int      `json:"floor"`
	Patterns []string `json:"patterns"`
	Why      string   `json:"why"`
	Have     bool     `json:"have"`
}

type jsonFleet struct {
	// Pace is the fleet's own layer, which is what a browser edits when it is
	// setting a default rather than an exception.
	Pace jsonPace `json:"pace,omitzero"`
	// Tariff is what this fleet charges, and what measurement suggests instead.
	//
	// The suggestion travels rather than being worked out in the browser from the
	// same buckets: a second implementation of the normalisation would be a second
	// opinion about what a fleet should charge, and the two would drift the first
	// time either rounded differently.
	Tariff []jsonTariff `json:"tariff,omitempty"`

	Root        string             `json:"root"`
	Operator    string             `json:"operator"`
	Identities  []jsonIdentity     `json:"identities"`
	Roles       []jsonRole         `json:"roles"`
	Permissions []jsonPermission   `json:"permissions"`
	Toolkit     []jsonToolkitEntry `json:"toolkit"`
	Vocabulary  jsonVocabulary     `json:"vocabulary"`
	Problems    []string           `json:"problems,omitempty"`
}

// vocabulary renders the two lists this build knows.
func vocabulary() jsonVocabulary {
	out := jsonVocabulary{}
	for _, v := range model.OrcVerbs() {
		out.Verbs = append(out.Verbs, jsonWord{Word: v.Verb, Does: v.Does})
	}
	for _, t := range model.Tools() {
		out.Tools = append(out.Tools, jsonWord{Word: t.Name, Does: t.Does, In: t.In})
	}
	out.Innocuous = model.InnocuousWords()
	return out
}

// emitJSON writes a value as indented JSON with a trailing newline.
func (a App) emitJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fault.Internal{Where: "cli.emitJSON", Detail: err.Error()}
	}
	return a.say(string(data))
}

func (a App) emitIdentityJSON(s caller, who user.Name) error {
	shape, err := s.identityJSON(who)
	if err != nil {
		return err
	}
	return a.emitJSON(shape)
}

func (a App) emitFleetJSON(s caller) error {
	out := jsonFleet{
		Pace:       paceJSON(s.store.FleetPacing()),
		Tariff:     tariffJSON(s),
		Root:       s.store.Root(),
		Operator:   s.fleet.Operator().String(),
		Vocabulary: vocabulary(),
		Problems:   s.fleet.Problems(),
	}
	for _, name := range s.fleet.Subtree(s.who) {
		shape, err := s.identityJSON(name)
		if err != nil {
			return err
		}
		out.Identities = append(out.Identities, shape)
	}
	for _, r := range s.fleet.Roles() {
		out.Roles = append(out.Roles, jsonRole{
			Name:        r.Name().String(),
			Authority:   r.Authority().Int(),
			Description: r.Description(),
			Permissions: model.Names(r.Permissions()),
			Created:     clock.Format(r.Created()),
			HeldBy:      user.Names(s.fleet.UsesRole(r.Name())),
		})
	}
	kit, err := toolkitJSON(s)
	if err != nil {
		return err
	}
	out.Toolkit = kit

	for _, p := range s.fleet.Permissions() {
		out.Permissions = append(out.Permissions, jsonPermission{
			Name:     p.Name().String(),
			Floor:    p.Floor().Int(),
			Patterns: model.PatternStrings(p.Patterns()),
			Created:  clock.Format(p.Created()),
		})
	}
	return a.emitJSON(out)
}

// toolkitJSON is the toolkit, with what this fleet has of it.
func toolkitJSON(s caller) ([]jsonToolkitEntry, error) {
	want, err := store.Toolkit()
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, p := range s.fleet.Permissions() {
		have[p.Name().String()] = true
	}

	out := make([]jsonToolkitEntry, 0, len(want))
	for _, p := range want {
		out = append(out, jsonToolkitEntry{
			Name:     p.Name.String(),
			Floor:    p.Floor.Int(),
			Patterns: model.PatternStrings(p.Patterns),
			Why:      p.Why,
			Have:     have[p.Name.String()],
		})
	}
	return out, nil
}

func (s caller) identityJSON(who user.Name) (jsonIdentity, error) {
	i, err := s.fleet.Identity(who)
	if err != nil {
		return jsonIdentity{}, err
	}
	effective, asked := s.fleet.Authority(who)
	budget, hasBudget := s.fleet.Budget(who)

	out := jsonIdentity{
		Name:         who.String(),
		ID:           i.ID(),
		Created:      clock.Format(i.Created()),
		Operator:     i.IsOperator(),
		Boss:         i.Boss().String(),
		Chain:        user.Names(s.fleet.Chain(who)),
		Role:         i.Role().String(),
		Authority:    effective.Int(),
		AskedFor:     asked.Int(),
		Capped:       !asked.Zero() && asked.Int() != effective.Int(),
		Clauses:      []jsonClause{},
		Grants:       []jsonGrant{},
		Subordinates: user.Names(s.fleet.Children(who)),
		Workspace:    s.store.WorkspaceDir(who),
		Budget:       budget,
		HasBudget:    hasBudget,
		Employed:     i.Employed(),
		Load:         s.fleet.LoadOf(who),
	}
	if i.Model().Valid() {
		out.Model = i.Model().String()
	}
	if i.Effort().Valid() {
		out.Effort = i.Effort().String()
	}

	// A session state that cannot be read leaves Populated false rather than
	// failing the whole shape: a mirror asking about a fleet should still get the
	// nine other identities.
	if state, live, err := s.store.Session(who); err == nil && live {
		out.Populated = true
		out.Session = state.ID
		out.Restarts = state.Restarts
		out.Instructed = state.Instructed
		out.InstructError = state.InstructError
		out.Started = state.Started
		out.LastExit = state.LastExit
	}

	// The rollup, bounded to what a sync should carry.
	//
	// Not everything: a fleet of twenty with a year of history would put megabytes
	// through every sync, for a series the far end has already got. What travels is
	// a window wide enough to cover an ordinary outage, and the mirror keeps the
	// long history at its end.
	if buckets, err := s.store.Activity(who, s.store.Now().Add(-SyncWindow)); err == nil {
		if over := len(buckets) - MaxSyncBuckets; over > 0 {
			// Newest kept: an old bucket the far end is missing is a hole in a
			// chart, and a recent one it is missing is the chart being wrong now.
			buckets = buckets[over:]
		}
		for _, b := range buckets {
			out.Buckets = append(out.Buckets, jsonBucket{
				At: clock.Format(b.At), Model: b.Model, Effort: b.Effort,
				Turns: b.Turns, Tokens: b.Tokens, Files: b.Files,
			})
		}
	}

	out.Pace = paceJSON(s.store.Pacing(who, i.Role()))

	doing := s.doing(who)
	out.Activity = string(doing.State)
	out.Turn = doing.Turn
	out.Why = doing.Why
	out.FeedRead = doing.Feed
	if !doing.Since.IsZero() {
		out.Since = clock.Format(doing.Since)
	}
	for _, row := range doing.Rows {
		out.Doing = append(out.Doing, jsonRow{
			At:      clock.Format(row.At),
			Turn:    row.Turn,
			Kind:    row.Kind.String(),
			Tool:    row.Tool,
			Detail:  row.Detail,
			Blocked: row.Blocked(),
			Reason:  row.Reason,
		})
	}

	for _, c := range s.fleet.Clauses(who) {
		shape := jsonClause{
			Kind:       c.Pattern.Kind().String(),
			Arg:        c.Pattern.Arg(),
			Permission: c.Permission.String(),
			Source:     c.Source.String(),
			Capped:     c.Capped,
		}
		if c.Capped {
			shape.Asked = c.Asked.String()
		}
		if c.Source == authz.FromGrant {
			shape.Lapses = c.Grant.Lapse(s.fleet.Now())
		}
		out.Clauses = append(out.Clauses, shape)
	}

	// Every grant, live or lapsed, with a flag rather than a filtered list: a
	// mirror showing why a permission disappeared needs the lapsed one too.
	for _, g := range i.Grants() {
		out.Grants = append(out.Grants, s.grantJSON(who, g))
	}
	return out, nil
}

// grantJSON is one grant's shape. It is a function of its own because `orc list
// grants` emits the same one, and two copies of a wire shape is how a field gets
// added to one of them.
func (s caller) grantJSON(who user.Name, g model.Grant) jsonGrant {
	shape := jsonGrant{
		Permission: g.Permission().String(),
		By:         g.By(),
		Granted:    clock.Format(g.Granted()),
		Session:    g.Session(),
		Live:       g.Live(s.fleet.Now(), s.fleet.Session(who)),
	}
	if !g.Until().IsZero() {
		shape.Until = clock.Format(g.Until())
	}
	return shape
}

// What a sync carries of the rollup.
//
// The trade is between a mirror that can fill a gap and a body that fits on a phone.
// Two days covers a machine that slept overnight or a sync that could not reach the
// server for an afternoon, which is the ordinary outage; a longer one loses the
// older buckets from the *mirror's* series and never from Orc's own, so
// `orc activity` on the agent machine still has them and a rebuilt mirror is a
// question about history rather than a loss of it.
const (
	// SyncWindow is how far back a snapshot reaches.
	SyncWindow = 48 * time.Hour
	// MaxSyncBuckets bounds it whatever the window says. An identity that changed
	// model and effort every hour would otherwise multiply the count by the number
	// of combinations it used.
	MaxSyncBuckets = 240
)

// jsonTariff is one thing a tariff prices.
type jsonTariff struct {
	Setting string `json:"setting"`
	Weight  int    `json:"weight"`
	// Suggested and Measured are absent where nothing was observed, which is not
	// the same as a suggestion of zero: a combination nobody ran proposes nothing.
	Suggested int     `json:"suggested,omitempty"`
	Measured  float64 `json:"measured,omitempty"`
	Turns     int     `json:"turns,omitempty"`
}

// paceJSON renders resolved pacing, with provenance for the values a layer set.
func paceJSON(got store.Pacing) jsonPace {
	out := jsonPace{
		WakeAfter: got.WakeAfter.Value,
		WakeEvery: got.WakeEvery.Value,
		TendWatch: got.TendWatch.Value,
		WakeOff:   got.WakeOff.Off(),
		TendOff:   got.TendOff.Off(),
		From:      map[string]string{},
	}
	for name, setting := range map[string]store.Setting{
		"wake_after": got.WakeAfter,
		"wake_every": got.WakeEvery,
		"tend_watch": got.TendWatch,
		"wake_off":   got.WakeOff,
		"tend_off":   got.TendOff,
	} {
		if setting.Set() {
			out.From[name] = string(setting.From)
		}
	}
	if len(out.From) == 0 {
		out.From = nil
	}
	return out
}

// tariffJSON renders the price list, with what measurement suggests beside it.
func tariffJSON(s caller) []jsonTariff {
	got := s.store.Tariff()
	proposals := s.proposals()

	out := make([]jsonTariff, 0, len(model.Settings()))
	for _, setting := range model.Settings() {
		weight, ok := got.Value(setting)
		if !ok {
			continue
		}
		row := jsonTariff{Setting: string(setting), Weight: weight}
		if p, seen := proposals[setting]; seen {
			row.Suggested, row.Measured, row.Turns = p.Suggested, p.Measured, p.Turns
		}
		out = append(out, row)
	}
	return out
}
