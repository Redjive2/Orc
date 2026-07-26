// Package protocol defines everything that crosses the wire.
//
// Both sides compile against these types, so a change to the format that only
// one side follows will not build. Every type validates itself, and decoding
// rejects unknown fields rather than ignoring them: a field the receiver does
// not understand means the two ends disagree about the format, and continuing
// on a guess is how a mirror silently drops data.
//
// Nothing here touches the network or the disk. It is data and its rules.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"orc/cq/internal/fault"
)

// Version is the wire format version. A request carrying any other value is
// refused with a clear message rather than parsed hopefully.
const Version = 1

// Size limits. A snapshot is markdown and metadata, so these are generous; a
// store that exceeds them means something upstream is wrong, and saying so is
// better than allocating until the machine gives up.
const (
	MaxSnapshotBytes = 32 << 20
	MaxRequestBytes  = 1 << 20
	MaxBodyBytes     = 1 << 20
	MaxSubjectRunes  = 998
	MaxNameRunes     = 64
	MaxListItems     = 4096

	// The fleet's own limits. Orc holds the real ones; these are the bound cq
	// allocates against, set generously enough that only a store already in
	// trouble reaches them.
	MaxNoteRunes    = 4096
	MaxPatternRunes = 512

	// The standing instructions' bounds, and they are Orc's rather than cq's:
	// `instruct.MaxLayer` and `instruct.MaxWake`, repeated here because cq cannot
	// import Orc and a queue that accepted what the agent machine will refuse is a
	// queue that fails after a sync, on a machine nobody is watching.
	//
	// A layer is a document and a wake message is a sentence, which is why there
	// are two numbers rather than one.
	MaxPromptBytes = 16 << 10
	MaxWakeBytes   = 2 << 10
	// A task's description, and it is Macmuffin's bound rather than cq's:
	// `store.MaxDescription`, repeated here for the same reason the two above are.
	// A description over it is refused at the browser, where somebody sees the
	// refusal and shortens it — not after a sync, on a machine nobody is watching.
	MaxTaskDescriptionBytes = 32 << 10
	MaxDescriptionRunes     = 512
	// MaxSpawnLoad matches Orc's model.MaxLoad, which is what the operator's own
	// budget is set to. A queued budget above it would be refused there.
	MaxSpawnLoad = 4096
)

// Validator is anything that can check its own invariants. Every wire type
// implements it, and Decode calls it, so no undecoded value reaches a handler.
type Validator interface {
	Validate() error
}

// name is the shared identifier shape across Orc: Mailman user names, Macmuffin
// task names, and machine ids all normalise to it.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// MachineID names the agent machine a snapshot came from. Snapshots are stored
// per machine and actions are routed back to the machine that owns the mailbox,
// so this is the one field that must be right for a two-machine setup to work.
type MachineID string

// Validate checks the id is usable as both a routing key and a path element.
func (m MachineID) Validate() error {
	if m == "" {
		return fault.Field("MachineID", "", "machine id is empty")
	}
	if !name.MatchString(string(m)) {
		return fault.Field("MachineID", "", "machine id %q must match %s", string(m), name)
	}
	return nil
}

// ActionID identifies one queued user action. The server mints it; the agent
// uses it to make application idempotent.
type ActionID string

// Validate checks the id is a 32-character hex string.
func (a ActionID) Validate() error {
	if !hex32.MatchString(string(a)) {
		return fault.Field("ActionID", "", "action id %q must be 32 hex characters", string(a))
	}
	return nil
}

// Op is what a queued action asks the agent to do. Each maps to exactly one
// Mailman command, because cq is a mirror of that API and inventing a verb here
// would mean inventing a behaviour Mailman does not have.
type Op string

const (
	OpSend    Op = "send"    // mailman send
	OpReply   Op = "reply"   // mailman reply
	OpRead    Op = "read"    // mailman read
	OpArchive Op = "archive" // mailman archive <query>
	OpCC      Op = "cc"      // mailman cc

	// The library verbs. Unlike the five above, these do not go through another
	// tool: they write the agent machine's filesystem, inside the mirrored
	// checkout and nowhere else.
	//
	// Every one of them carries Base, the digest of what the operator was
	// looking at. The agent refuses if the file no longer matches, which is what
	// makes a mirror safe to edit from: a snapshot is minutes old by the time
	// somebody acts on it, and writing back a whole file edited from a stale
	// copy would silently discard whatever changed in between.
	//
	// It also makes these self-guarding against being applied twice. After a
	// successful write the file no longer hashes to Base, so a repeat refuses
	// rather than overwriting — which matters most for the one action that
	// cannot be undone.
	OpWrite     Op = "write"  // replace a file's contents
	OpCreate    Op = "create" // make a file that does not exist
	OpDelete    Op = "delete" // remove a file
	OpMakeDir   Op = "mkdir"  // make a directory
	OpRemoveDir Op = "rmdir"  // remove an empty directory

	// OpRemoveTree removes a directory and everything under it.
	//
	// It is the one action here that cannot be undone and cannot be checked
	// afterwards, so it carries the whole of what the operator was looking at:
	// every file path the mirror showed inside that directory. The agent walks
	// the real one and refuses if it finds anything the list does not name.
	//
	// That is what makes it safe from a snapshot minutes old. A folder somebody
	// filed work into after the mirror was taken is a folder this refuses, and
	// the operator is told so rather than losing the work.
	OpRemoveTree Op = "rmtree"

	// The task verbs, one per Macmuffin command that changes something.
	//
	// They are namespaced because `create` and `delete` were already taken by the
	// library, and an operation whose meaning depends on which fields happen to be
	// set is one the queue cannot report on honestly.
	//
	// Every mutating `muff` verb is here. The ones that are missing — `pool`,
	// `info`, `check-scope`, `verify` — are reads or local diagnostics: the first
	// two arrive in every snapshot already, and the last two answer about the agent
	// machine's own filesystem, where there is nobody to read the answer.
	OpTaskCreate   Op = "task.create"   // muff create <task> <priority> <difficulty>
	OpTaskPush     Op = "task.push"     // muff push <task>
	OpTaskClaim    Op = "task.claim"    // muff claim <task>
	OpTaskAssign   Op = "task.assign"   // muff assign <agent> <task>
	OpTaskInvite   Op = "task.invite"   // muff invite <agent> <task>
	OpTaskKick     Op = "task.kick"     // muff kick <agent> <task>
	OpTaskLeave    Op = "task.leave"    // muff leave <task>
	OpTaskScope    Op = "task.scope"    // muff scope <task> <paths...>
	OpTaskWorktree Op = "task.worktree" // muff worktree <task> <path>
	OpTaskStatus   Op = "task.status"   // muff status <task> <1..4>
	OpTaskSubtask  Op = "task.subtask"  // muff create <task> --sub <name>
	OpTaskComplete Op = "task.complete" // muff complete <task> [--sub <name>] [--force]
	OpTaskDelete   Op = "task.delete"   // muff delete <task> [--sub <name>] --yes
	// The description: what the work is, in markdown. Two operations rather than
	// one with an empty text, for the same reason the standing instructions have
	// two — clearing a specification and setting it to nothing are the same outcome
	// reached by different intents, and a queue whose account of what it is about
	// depends on whether a field is empty is one nobody can read.
	OpTaskDescribe      Op = "task.describe"       // muff describe <task> --set -
	OpTaskDescribeClear Op = "task.describe.clear" // muff describe <task> --clear
)

// TaskOps are the verbs that go through Macmuffin rather than Mailman.
var TaskOps = []Op{
	OpTaskCreate, OpTaskPush, OpTaskClaim, OpTaskAssign, OpTaskInvite, OpTaskKick,
	OpTaskLeave, OpTaskScope, OpTaskWorktree, OpTaskStatus, OpTaskSubtask,
	OpTaskComplete, OpTaskDelete, OpTaskDescribe, OpTaskDescribeClear,
}

// TouchesTasks reports whether an operation changes the task pool.
func (o Op) TouchesTasks() bool { return slices.Contains(TaskOps, o) }

// LibraryOps are the verbs that touch the checkout rather than the mailbox.
var LibraryOps = []Op{OpWrite, OpCreate, OpDelete, OpMakeDir, OpRemoveDir, OpRemoveTree}

// TouchesLibrary reports whether an operation writes the mirrored checkout.
func (o Op) TouchesLibrary() bool { return slices.Contains(LibraryOps, o) }

// Idempotent reports whether applying the operation twice has the same effect
// as applying it once.
//
// It exists for one decision: whether an action whose outcome is unknown may be
// tried again. Marking mail read twice is marking it read. Sending twice is a
// second message to a real person, who reads it twice and answers once.
func (o Op) Idempotent() bool {
	switch o {
	case OpMakeDir:
		// Making a directory that exists is making a directory.
		return true
	case OpWrite, OpCreate, OpDelete, OpRemoveDir:
		// Not idempotent, but self-guarding: each carries the digest of what it
		// expected to find, so a second application refuses rather than repeats.
		// An interrupted one is still never retried blindly — a delete that may
		// already have happened is exactly the case that rule exists for.
		return false
	case OpRemoveTree:
		// Not idempotent — the second application finds nothing and refuses —
		// but safe to repeat where it left off, which is the question `retryable`
		// actually asks. Its check is that nothing *unexpected* is there, so a
		// half-finished removal has fewer files than the list, not more, and
		// finishing the job is exactly what a retry should do.
		return false
	case OpRead, OpArchive, OpCC:
		// cc is idempotent in Mailman: adding a participant who is already in a
		// conversation is not an error and changes nothing.
		return true
	case OpSend, OpReply:
		return false
	case OpTaskDescribe, OpTaskDescribeClear:
		// A description is a value: writing the same words twice lands in the same
		// place, and clearing one twice is cleared.
		return true
	case OpTaskScope, OpTaskWorktree, OpTaskStatus, OpTaskAssign, OpTaskInvite:
		// Each sets a value to what was asked for rather than stepping it, so
		// doing it twice lands in the same place. Macmuffin accepts assigning a
		// task to whoever already owns it, and inviting a collaborator who is
		// already one — both say so and change nothing.
		return true
	case OpOrcWorkspace:
		// Two operations behind one verb. Adopting points a value at a directory
		// that is already there, which lands in the same place however many times
		// it is applied. Relocating copies files, and the second application finds
		// the source where it left it and the target already made — so it is not
		// idempotent, and does not need to be: `from` is the guard, exactly as
		// `base` is for a file write. A retry whose `from` no longer matches is
		// refused rather than repeated.
		return false
	case OpOrcAssignRole, OpOrcAssignAuthority, OpOrcAssignPerm, OpOrcMove,
		OpOrcBudget, OpOrcTend, OpOrcToolkit, OpOrcFire, OpOrcRevoke, OpOrcEditPermission,
		OpOrcInstructSet, OpOrcInstructClear:
		// Each sets a state to what was asked for rather than stepping it. An
		// identity already under that boss stays there; a role already at that
		// authority is unchanged; `tend` reconciles to the same place however many
		// times it runs — that is what a reconciler is. Firing what is already
		// fired and revoking what is already revoked both land where they were
		// asked to land.
		return true
	case OpUpgrade:
		// Pulling and rebuilding twice lands on the same revision with the same
		// binaries. It is the one operation here whose repeat is not merely
		// harmless but routine — a machine that missed a round gets the next one.
		return true
	case OpOrcNewIdentity, OpOrcNewRole, OpOrcNewPermission,
		OpOrcRemoveIdentity, OpOrcRemoveRole, OpOrcRemovePerm,
		OpOrcGrant, OpOrcEmploy, OpOrcPoke, OpOrcRefresh:
		// Creating twice is a conflict, and removing twice finds nothing. Employ
		// spends a budget and refuses when it is exhausted, so a repeat is not free.
		// A grant re-run extends a lapse; poke says a thing to somebody twice; and
		// refresh throws away a *second* conversation — the one started by the
		// first refresh, which may have been an hour of work.
		return false
	case OpTaskCreate, OpTaskSubtask, OpTaskPush, OpTaskClaim, OpTaskKick,
		OpTaskLeave, OpTaskComplete, OpTaskDelete:
		// Creating twice is a conflict, and the rest are transitions with a
		// precondition: a second push, claim, or complete finds the task already
		// there and refuses. Refusing is the right answer, but it is not the same
		// answer, so none of these may be retried blindly after an unknown outcome.
		return false
	default:
		// An operation cq does not recognise is assumed to have consequences.
		return false
	}
}

// Ops lists every defined operation.
var Ops = slices.Concat([]Op{OpSend, OpReply, OpRead, OpArchive, OpCC,
	OpWrite, OpCreate, OpDelete, OpMakeDir, OpRemoveDir, OpRemoveTree, OpUpgrade}, TaskOps, FleetOps)

// Valid reports whether o is one of the defined operations.
func (o Op) Valid() bool { return slices.Contains(Ops, o) }

// Validate checks the operation is one cq knows how to apply.
func (o Op) Validate() error {
	if !o.Valid() {
		return fault.Field("Op", "", "unknown operation %q; expected one of %s", string(o), joinOps())
	}
	return nil
}

func joinOps() string {
	out := make([]string, len(Ops))
	for i, o := range Ops {
		out[i] = string(o)
	}
	return strings.Join(out, ", ")
}

// ConvoRef locates a message within its conversation.
type ConvoRef struct {
	UID   string `json:"uid"`
	Title string `json:"title,omitempty"`
	Index int    `json:"index"`
}

// Validate checks the reference is either wholly absent or internally coherent.
func (c ConvoRef) Validate() error {
	if c.UID == "" {
		if c.Title != "" || c.Index != 0 {
			return fault.Field("ConvoRef", "uid", "conversation title or index given without a uid")
		}
		return nil
	}
	if err := checkText("ConvoRef", "title", c.Title, MaxSubjectRunes, true); err != nil {
		return err
	}
	if c.Index < 0 {
		return fault.Field("ConvoRef", "index", "index %d is negative", c.Index)
	}
	return nil
}

// Message is one piece of mail, as Mailman reports it.
//
// Body is empty when the snapshot was taken in metadata-only mode, which is
// indistinguishable from a genuinely empty body — deliberately, since neither
// gives the reader anything to show.
type Message struct {
	PUID     int       `json:"puid"`
	MID      string    `json:"mid"`
	Sent     time.Time `json:"sent"`
	From     string    `json:"from"`
	To       []string  `json:"to,omitempty"`
	CC       []string  `json:"cc,omitempty"`
	Subject  string    `json:"subject"`
	Convo    ConvoRef  `json:"convo"`
	Read     bool      `json:"read"`
	Archived bool      `json:"archived"`
	Body     string    `json:"body,omitempty"`
}

// Validate checks every field a renderer or a router will rely on.
func (m Message) Validate() error {
	if m.PUID < 0 {
		return fault.Field("Message", "puid", "puid %d is negative", m.PUID)
	}
	if m.MID == "" {
		return fault.Field("Message", "mid", "message id is empty")
	}
	if err := checkText("Message", "mid", m.MID, 128, false); err != nil {
		return err
	}
	if m.Sent.IsZero() {
		return fault.Field("Message", "sent", "send time is missing")
	}
	if err := checkName("Message", "from", m.From); err != nil {
		return err
	}
	if err := checkNames("Message", "to", m.To); err != nil {
		return err
	}
	if err := checkNames("Message", "cc", m.CC); err != nil {
		return err
	}
	if err := checkText("Message", "subject", m.Subject, MaxSubjectRunes, true); err != nil {
		return err
	}
	if err := m.Convo.Validate(); err != nil {
		return err
	}
	return checkText("Message", "body", m.Body, MaxBodyBytes, true)
}

// Convo is a conversation as a whole.
type Convo struct {
	UID     string   `json:"uid"`
	Title   string   `json:"title,omitempty"`
	Members []string `json:"members,omitempty"`
	Count   int      `json:"count"`
}

// Validate checks the conversation is addressable and internally consistent.
func (c Convo) Validate() error {
	if c.UID == "" {
		return fault.Field("Convo", "uid", "conversation uid is empty")
	}
	if err := checkText("Convo", "uid", c.UID, 128, false); err != nil {
		return err
	}
	if err := checkText("Convo", "title", c.Title, MaxSubjectRunes, true); err != nil {
		return err
	}
	if err := checkNames("Convo", "members", c.Members); err != nil {
		return err
	}
	if c.Count < 0 {
		return fault.Field("Convo", "count", "count %d is negative", c.Count)
	}
	return nil
}

// Receipt records whether one recipient has read one message. It is what the
// admin panel answers "who has seen this" with.
// At is a pointer because `omitempty` does nothing for a time.Time: a struct is
// never "empty" to encoding/json, so an unread receipt encoded the zero time and
// told every reader the message was read in the year 1.
type Receipt struct {
	MID       string     `json:"mid"`
	Recipient string     `json:"recipient"`
	Read      bool       `json:"read"`
	At        *time.Time `json:"at,omitempty"`
}

// Validate checks the receipt names a message and a recipient, and that a read
// receipt carries the time it was read.
func (r Receipt) Validate() error {
	if r.MID == "" {
		return fault.Field("Receipt", "mid", "message id is empty")
	}
	if err := checkName("Receipt", "recipient", r.Recipient); err != nil {
		return err
	}
	if r.Read && (r.At == nil || r.At.IsZero()) {
		return fault.Field("Receipt", "at", "message is marked read but carries no read time")
	}
	if !r.Read && r.At != nil {
		return fault.Field("Receipt", "at", "message is unread but carries a read time")
	}
	return nil
}

// Task is one Macmuffin task, as `muff pool` reports it.
type Task struct {
	Name          string   `json:"name"`
	Owner         string   `json:"owner,omitempty"`
	Collaborators []string `json:"collaborators,omitempty"`
	Priority      int      `json:"priority"`
	Difficulty    int      `json:"difficulty"`
	Status        int      `json:"status"`
	Done          int      `json:"done"`
	Total         int      `json:"total"`
	Draft         bool     `json:"draft"`
	Scope         []string `json:"scope,omitempty"`
	Worktree      string   `json:"worktree,omitempty"`
	// Subtasks are the steps, by name, so the browser can complete or delete one
	// rather than only see how many are done. Macmuffin's board omits them and its
	// `info` carries them, which is why the agent asks twice — see source.tasks.
	Subtasks []Subtask `json:"subtasks,omitempty"`

	// Description is what the work actually is, in markdown.
	//
	// It travels with the mirror rather than being fetched when somebody opens the
	// task, because the server cannot reach the agent machine: a description not in
	// the snapshot is one the browser cannot show at all. The same reason the
	// standing instructions travel with the fleet.
	Description string `json:"description,omitempty"`
	// Described is what the board says, and is the reason the text can be absent
	// without meaning "there is none": a task the agent could not read the
	// description of is described-but-empty, and a panel that guessed would offer
	// to write a first one over the top of something.
	Described   bool   `json:"described,omitempty"`
	DescribedBy string `json:"described_by,omitempty"`
	DescribedAt string `json:"described_at,omitempty"`
}

// Subtask is one step of one task.
type Subtask struct {
	Name string `json:"name"`
	Done bool   `json:"done"`
}

// Validate checks a subtask carries a name Macmuffin would accept.
func (s Subtask) Validate() error { return checkName("Subtask", "name", s.Name) }

// Validate checks the scales Macmuffin defines: priority and difficulty run 1
// to 5, status 1 to 4, and completed subtasks cannot exceed the total.
func (t Task) Validate() error {
	if err := checkName("Task", "name", t.Name); err != nil {
		return err
	}
	if t.Owner != "" {
		if err := checkName("Task", "owner", t.Owner); err != nil {
			return err
		}
	}
	if err := checkNames("Task", "collaborators", t.Collaborators); err != nil {
		return err
	}
	if err := inRange("Task", "priority", t.Priority, 1, 5); err != nil {
		return err
	}
	if err := inRange("Task", "difficulty", t.Difficulty, 1, 5); err != nil {
		return err
	}
	if err := inRange("Task", "status", t.Status, 1, 4); err != nil {
		return err
	}
	if t.Done < 0 || t.Total < 0 {
		return fault.Field("Task", "done", "subtask counts %d/%d are negative", t.Done, t.Total)
	}
	if len(t.Subtasks) > MaxListItems {
		return fault.Field("Task", "subtasks", "%d subtasks exceeds the limit of %d",
			len(t.Subtasks), MaxListItems)
	}
	for i, sub := range t.Subtasks {
		if err := sub.Validate(); err != nil {
			return fault.Field("Task", fmt.Sprintf("subtasks[%d]", i), "%v", err)
		}
	}
	if t.Done > t.Total {
		return fault.Field("Task", "done", "%d subtasks done of %d", t.Done, t.Total)
	}
	if len(t.Scope) > MaxListItems {
		return fault.Field("Task", "scope", "%d scope entries exceeds the limit of %d", len(t.Scope), MaxListItems)
	}
	for i, p := range t.Scope {
		if err := checkText("Task", fmt.Sprintf("scope[%d]", i), p, 4096, false); err != nil {
			return err
		}
	}
	return checkText("Task", "worktree", t.Worktree, 4096, true)
}

// AdminUser is a Mailman account as the admin panel lists it.
//
// A name and nothing else, because a name is all Mailman keeps. cq once carried
// a creation time here and it was always zero: an account's registry entry
// records that it exists, not when it started to. A field that is never filled
// in is worse than no field, because it invites the interface to display it.
type AdminUser struct {
	Name string `json:"name"`
}

// Validate checks the account has a usable name.
func (u AdminUser) Validate() error {
	return checkName("AdminUser", "name", u.Name)
}

// AdminState is the whole-Mailman view. It is absent from a snapshot taken with
// --no-admin, and carries messages without bodies under --admin-metadata-only.
type AdminState struct {
	Users        []AdminUser `json:"users"`
	Messages     []Message   `json:"messages"`
	Receipts     []Receipt   `json:"receipts"`
	MetadataOnly bool        `json:"metadata_only"`
}

// Validate checks every member, and that a metadata-only state really carries
// no bodies — a mislabelled snapshot would send bodies the operator asked to
// withhold.
func (a AdminState) Validate() error {
	if err := validateEach("AdminState", "users", a.Users); err != nil {
		return err
	}
	if err := validateEach("AdminState", "messages", a.Messages); err != nil {
		return err
	}
	if err := validateEach("AdminState", "receipts", a.Receipts); err != nil {
		return err
	}
	if a.MetadataOnly {
		for i, m := range a.Messages {
			if m.Body != "" {
				return fault.Field("AdminState", fmt.Sprintf("messages[%d].body", i),
					"snapshot is marked metadata-only but message %q carries a body", m.MID)
			}
		}
	}
	return nil
}

// Snapshot is one machine's complete state at a moment. It replaces its
// predecessor wholesale on the server, so it must stand alone.
type Snapshot struct {
	Machine MachineID   `json:"machine"`
	User    string      `json:"user"`
	TakenAt time.Time   `json:"taken_at"`
	Inbox   []Message   `json:"inbox"`
	Archive []Message   `json:"archive"`
	Sent    []Message   `json:"sent"`
	Convos  []Convo     `json:"convos"`
	Tasks   []Task      `json:"tasks"`
	Admin   *AdminState `json:"admin,omitempty"`
	// Library is the repository as something to read. Absent when the machine
	// was not asked to mirror one.
	Library *Library `json:"library,omitempty"`
	// Fleet is the machine's Orc store. Absent when the machine runs no agents,
	// which is most machines that mirror a mailbox.
	Fleet *Fleet `json:"fleet,omitempty"`
}

// Validate checks the snapshot names its machine and owner and that every
// member is sound.
func (s Snapshot) Validate() error {
	if err := s.Machine.Validate(); err != nil {
		return err
	}
	if err := checkName("Snapshot", "user", s.User); err != nil {
		return err
	}
	if s.TakenAt.IsZero() {
		return fault.Field("Snapshot", "taken_at", "capture time is missing")
	}
	if err := validateEach("Snapshot", "inbox", s.Inbox); err != nil {
		return err
	}
	if err := validateEach("Snapshot", "sent", s.Sent); err != nil {
		return err
	}
	if err := validateEach("Snapshot", "archive", s.Archive); err != nil {
		return err
	}
	if err := validateEach("Snapshot", "convos", s.Convos); err != nil {
		return err
	}
	if err := validateEach("Snapshot", "tasks", s.Tasks); err != nil {
		return err
	}
	if s.Fleet != nil {
		if err := s.Fleet.Validate(); err != nil {
			return err
		}
	}
	if s.Library != nil {
		if err := s.Library.Validate(); err != nil {
			return err
		}
	}
	if s.Admin != nil {
		return s.Admin.Validate()
	}
	return nil
}

// Args carries an action's operands. One struct rather than a union per op,
// because the set is small and a table of which fields each op requires is
// easier to read — and to check exhaustively — than five near-identical types.
type Args struct {
	PUID     int      `json:"puid,omitempty"`
	ConvoUID string   `json:"convo_uid,omitempty"`
	To       []string `json:"to,omitempty"`
	User     string   `json:"user,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Body     string   `json:"body,omitempty"`

	// The library verbs' arguments.
	//
	// Path is relative to the mirrored checkout and may not climb out of it;
	// Text is a file's whole new contents; Base is the SHA-256, in hex, of what
	// the operator was editing — empty only for a create, which expects nothing
	// to be there at all.
	Path string `json:"path,omitempty"`
	Text string `json:"text,omitempty"`
	Base string `json:"base,omitempty"`

	// The task verbs' arguments.
	//
	// Task names the task and Sub the step within it, when there is one. Paths is
	// the scope, a whole replacement rather than an addition, because that is what
	// `muff scope` does.
	//
	// Priority, Difficulty, and Status are Macmuffin's own 1-to-5 and 1-to-4
	// scales; they are validated here as well as there so a value the pool would
	// refuse never reaches the queue. User carries the agent for assign, invite,
	// and kick, which is the same field `cc` uses for the same kind of thing.
	Task       string   `json:"task,omitempty"`
	Sub        string   `json:"sub,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Priority   int      `json:"priority,omitempty"`
	Difficulty int      `json:"difficulty,omitempty"`
	Status     int      `json:"status,omitempty"`
	// Force is `muff complete --force`: finish a task whose subtasks are not all
	// done. It is an operand rather than a separate operation because it changes
	// what the command tolerates, not what it does.
	Force bool `json:"force,omitempty"`

	// The fleet verbs' arguments.
	//
	// Separate fields rather than reuse: `User` already means a mail recipient and
	// a task collaborator, and a third meaning on the same field would make the
	// queue's own report of what it is about depend on which operation is beside
	// it. Identity, Role, Permission, and Boss are all Orc names.
	Identity    string   `json:"identity,omitempty"`
	Role        string   `json:"role,omitempty"`
	Permission  string   `json:"permission,omitempty"`
	Boss        string   `json:"boss,omitempty"`
	Authority   int      `json:"authority,omitempty"`
	Floor       int      `json:"floor,omitempty"`
	Patterns    []string `json:"patterns,omitempty"`
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`
	Effort      string   `json:"effort,omitempty"`
	Until       string   `json:"until,omitempty"`
	// Load is a spawn budget. Zero is a real value — a budget of nothing refuses
	// every employ — so it is present-and-meaningful rather than absent.
	Load    int    `json:"load,omitempty"`
	Message string `json:"message,omitempty"`

	// `orc workspace`'s operands.
	//
	// Workspace is the absolute path to work in, and is deliberately not `Path`:
	// that field means "relative to the mirrored checkout and may not climb out of
	// it", and two meanings on one field would make the queue's own report of what
	// an action is about depend on which operation sat beside it.
	//
	// From is where the operator saw the identity working. Adopt says the directory
	// is expected to be there already, which is the difference between working in
	// somebody's checkout and moving an agent's files to a new path.
	Workspace string `json:"workspace,omitempty"`
	From      string `json:"from,omitempty"`
	Adopt     bool   `json:"adopt,omitempty"`

	// `orc instruct`'s operands. Prompt is which layer — "system", "role", or
	// "identity" — and PromptName whose, empty for the fleet's own. Wake asks for
	// the message rather than the prompt, which is a different bound and the
	// opposite composition rule.
	//
	// The text itself reuses `Text`, the library's field, because it is the same
	// kind of thing — a whole replacement, not an addition — and its rule already
	// bounds and validates one. `Path` is *not* reused for the same reason it was
	// not for a workspace: that field means "relative to the mirrored checkout",
	// and a prompt is not in the checkout at all.
	Prompt     string `json:"prompt,omitempty"`
	PromptName string `json:"prompt_name,omitempty"`
	Wake       bool   `json:"wake,omitempty"`
}

// Action is one thing the user did in the browser, waiting to be applied on the
// machine that owns the mailbox.
type Action struct {
	ID      ActionID  `json:"id"`
	Seq     uint64    `json:"seq"`
	Machine MachineID `json:"machine"`
	Op      Op        `json:"op"`
	Args    Args      `json:"args"`
	Queued  time.Time `json:"queued"`
}

// argRule says which operands an operation requires. Fields not listed must be
// empty, so a `read` carrying a body is refused rather than silently ignored —
// which is what would let a client believe it had sent something it had not.
type argRule struct {
	puid     bool
	convoUID bool
	to       bool
	user     bool
	subject  bool
	body     bool
	path     bool
	text     bool
	// base is the digest of what the operator was editing. It is separate from
	// `text` because a create expects nothing to be there and so has none, while
	// every other library verb must have one.
	base bool

	// The task operands. `scales` covers priority and difficulty together,
	// because the one command that takes either takes both.
	task     bool
	sub      bool
	paths    bool
	optPaths bool // a list of paths is allowed and may be empty
	scales   bool
	status   bool
	optSub   bool // a sub name is allowed but not required
	optForce bool // --force is allowed

	// The fleet operands. See fleetRules in fleet.go.
	identity    bool
	role        bool
	permission  bool
	boss        bool
	authority   bool
	floor       bool
	patterns    bool
	description bool
	load        bool
	optRole     bool // `--from <role>` narrows instead of deleting
	optUntil    bool
	optMessage  bool
	optSession  bool // --model and --effort are allowed
	// workspace and from are `orc workspace`'s operands: where to, and where the
	// operator believed it was. optAdopt is whether the directory is expected to
	// exist already.
	workspace bool
	from      bool
	optAdopt  bool
	// prompt is `orc instruct`'s target: the layer's kind, its name, and whether it
	// is the wake message. What the layer becomes travels in `text`, the library's
	// own operand.
	prompt bool
}

var argRules = map[Op]argRule{
	OpSend:    {to: true, subject: true, body: true},
	OpReply:   {puid: true, subject: true, body: true},
	OpRead:    {puid: true},
	OpArchive: {puid: true},
	OpCC:      {convoUID: true, user: true},

	// A create carries no base: it expects the path to be empty, and a digest of
	// nothing is not a precondition, it is a guess.
	OpWrite:      {path: true, text: true, base: true},
	OpCreate:     {path: true, text: true},
	OpDelete:     {path: true, base: true},
	OpMakeDir:    {path: true},
	OpRemoveDir:  {path: true},
	OpRemoveTree: {path: true, optPaths: true},

	OpTaskCreate: {task: true, scales: true},
	// `text` is the markdown. The task is in the path; the prose is the body.
	OpTaskDescribe:      {task: true, text: true},
	OpTaskDescribeClear: {task: true},
	OpTaskPush:          {task: true},
	OpTaskClaim:         {task: true},
	OpTaskAssign:        {task: true, user: true},
	OpTaskInvite:        {task: true, user: true},
	OpTaskKick:          {task: true, user: true},
	OpTaskLeave:         {task: true},
	OpTaskScope:         {task: true, paths: true},
	OpTaskWorktree:      {task: true, path: true},
	OpTaskStatus:        {task: true, status: true},
	OpTaskSubtask:       {task: true, sub: true},
	// Both take an optional sub: without one they act on the whole task, which is
	// a different thing rather than a missing operand.
	OpTaskComplete: {task: true, optSub: true, optForce: true},
	OpTaskDelete:   {task: true, optSub: true},

	// The upgrade takes nothing. What to pull and where to build it are the
	// machine's own settings, not something a browser on another machine gets to
	// name — a path arriving over the wire and handed to a build script is the
	// shape of every remote-execution hole there has ever been.
	OpUpgrade: {},
}

// The fleet verbs live in fleet.go and are folded in here, so there is still one
// table an operation must appear in to be valid.
func init() {
	for op, rule := range fleetRules {
		argRules[op] = rule
	}
}

// Validate checks the action is addressable and that its operands match its
// operation exactly — no missing field, and no extra one.
func (a Action) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return err
	}
	if err := a.Machine.Validate(); err != nil {
		return err
	}
	if err := a.Op.Validate(); err != nil {
		return err
	}
	if a.Queued.IsZero() {
		return fault.Field("Action", "queued", "queue time is missing")
	}

	rule, ok := argRules[a.Op]
	if !ok {
		return fault.Internal{Where: "protocol.Action.Validate", Detail: "no argument rule for operation " + string(a.Op)}
	}

	// The PUID default of zero is a legitimate puid, so presence is decided by
	// the rule rather than by the value.
	if !rule.puid && a.Args.PUID != 0 {
		return unexpected(a.Op, "puid")
	}
	if rule.puid && a.Args.PUID < 0 {
		return fault.Field("Action", "args.puid", "puid %d is negative", a.Args.PUID)
	}

	for _, c := range []struct {
		want  bool
		field string
		empty bool
	}{
		{rule.convoUID, "convo_uid", a.Args.ConvoUID == ""},
		{rule.to, "to", len(a.Args.To) == 0},
		{rule.user, "user", a.Args.User == ""},
		{rule.subject, "subject", a.Args.Subject == ""},
		{rule.body, "body", a.Args.Body == ""},
		{rule.path, "path", a.Args.Path == ""},
		{rule.base, "base", a.Args.Base == ""},
		{rule.task, "task", a.Args.Task == ""},
		// `sub` and `paths` are not here: a sub is optional for two operations, and
		// the required/forbidden pair this loop checks cannot express that. Both
		// are owned by validateTaskArgs below.
	} {
		switch {
		case c.want && c.empty:
			return fault.Field("Action", "args."+c.field, "%s requires %s", a.Op, c.field)
		case !c.want && !c.empty:
			return unexpected(a.Op, c.field)
		}
	}

	// Text is not in the loop above, because an empty file is a real file and
	// "created it empty" must not be refused as "forgot the contents".
	if !rule.text && a.Args.Text != "" {
		return unexpected(a.Op, "text")
	}
	if rule.text {
		if err := checkText("Action", "args.text", a.Args.Text, MaxFileBytes, true); err != nil {
			return err
		}
		if err := a.checkDescriptionText(); err != nil {
			return err
		}
	}
	if rule.path {
		if err := checkPath("Action", "args.path", a.Args.Path); err != nil {
			return err
		}
	}
	if rule.base {
		if err := checkDigest("Action", "args.base", a.Args.Base); err != nil {
			return err
		}
	}
	if rule.to {
		if err := checkNames("Action", "args.to", a.Args.To); err != nil {
			return err
		}
	}
	if rule.user {
		if err := checkName("Action", "args.user", a.Args.User); err != nil {
			return err
		}
	}
	if rule.subject {
		if err := checkText("Action", "args.subject", a.Args.Subject, MaxSubjectRunes, false); err != nil {
			return err
		}
	}
	if rule.body {
		if err := checkText("Action", "args.body", a.Args.Body, MaxBodyBytes, false); err != nil {
			return err
		}
	}
	if rule.convoUID {
		if err := checkText("Action", "args.convo_uid", a.Args.ConvoUID, 128, false); err != nil {
			return err
		}
	}
	if err := a.validatePaths(rule); err != nil {
		return err
	}
	if err := a.validateTaskArgs(rule); err != nil {
		return err
	}
	return a.validateFleetArgs(rule)
}

// validatePaths checks a list of repository-relative paths.
//
// Two operations carry one and they mean different things by it, which is why
// emptiness is decided by the rule rather than here. `task.scope` takes at least
// one — `muff scope` does, and a scope of nothing is a missing operand rather
// than a wide one. `rmtree` carries the files the operator was shown inside a
// directory, and a directory holding only empty subdirectories shows none, so
// an empty list there is a fact rather than an omission.
func (a Action) validatePaths(rule argRule) error {
	switch {
	case !rule.paths && !rule.optPaths:
		if len(a.Args.Paths) > 0 {
			return unexpected(a.Op, "paths")
		}
		return nil
	case rule.paths && len(a.Args.Paths) == 0:
		return fault.Field("Action", "args.paths", "%s requires paths", a.Op)
	}

	if len(a.Args.Paths) > MaxListItems {
		return fault.Field("Action", "args.paths", "%d paths exceeds the limit of %d",
			len(a.Args.Paths), MaxListItems)
	}
	for i, path := range a.Args.Paths {
		if err := checkPath("Action", fmt.Sprintf("args.paths[%d]", i), path); err != nil {
			return err
		}
	}
	return nil
}

// validateTaskArgs checks the Macmuffin operands.
//
// Separate from the loop above because these are numbers and optional strings
// rather than "present or absent" strings: a status of 0 is absent and a status
// of 9 is present and wrong, and the two need different messages.
func (a Action) validateTaskArgs(rule argRule) error {
	if rule.task {
		if err := checkName("Action", "args.task", a.Args.Task); err != nil {
			return err
		}
	}
	// An optional sub is checked when it is there and allowed to be absent. An
	// operation that takes none at all must not carry one.
	switch {
	case (rule.sub || rule.optSub) && a.Args.Sub != "":
		if err := checkName("Action", "args.sub", a.Args.Sub); err != nil {
			return err
		}
	case !rule.sub && !rule.optSub && a.Args.Sub != "":
		return unexpected(a.Op, "sub")
	}

	// The scales, held to Macmuffin's own ranges here so the queue never carries a
	// value the pool would refuse. A zero is absence, which is why the check is on
	// the rule rather than on the value.
	for _, c := range []struct {
		want     bool
		field    string
		got      int
		lo, hi   int
		required bool
	}{
		{rule.scales, "priority", a.Args.Priority, 1, 5, true},
		{rule.scales, "difficulty", a.Args.Difficulty, 1, 5, true},
		{rule.status, "status", a.Args.Status, 1, 4, true},
	} {
		if !c.want {
			if c.got != 0 {
				return unexpected(a.Op, c.field)
			}
			continue
		}
		if c.required && c.got == 0 {
			return fault.Field("Action", "args."+c.field, "%s requires %s", a.Op, c.field)
		}
		if err := inRange("Action", "args."+c.field, c.got, c.lo, c.hi); err != nil {
			return err
		}
	}

	if !rule.optForce && a.Args.Force {
		return unexpected(a.Op, "force")
	}
	return nil
}

func unexpected(op Op, field string) error {
	return fault.Field("Action", "args."+field, "%s takes no %s", op, field)
}

// Result reports what became of one action on the agent machine.
type Result struct {
	ActionID ActionID  `json:"action_id"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
	// InDoubt marks an action that was started and whose end was never
	// recorded — the agent died mid-apply — so it may or may not have happened.
	//
	// It is a field of its own rather than a shape of Error because the two
	// demand opposite responses. A refused action did not happen and can simply
	// be tried again; an action in doubt may have sent a real message to a real
	// person, and offering "retry" for it without knowing the difference is how
	// somebody gets the same mail twice. Nothing may distinguish these by
	// reading prose.
	InDoubt bool `json:"in_doubt,omitempty"`
}

// Validate checks that a failure explains itself and a success does not pretend
// to. A result that is neither is the one shape the UI cannot render honestly.
func (r Result) Validate() error {
	if err := r.ActionID.Validate(); err != nil {
		return err
	}
	if r.At.IsZero() {
		return fault.Field("Result", "at", "completion time is missing")
	}
	if r.OK && r.Error != "" {
		return fault.Field("Result", "error", "a successful result carries an error message")
	}
	if !r.OK && r.Error == "" {
		return fault.Field("Result", "error", "a failed result carries no reason")
	}
	if r.OK && r.InDoubt {
		return fault.Field("Result", "in_doubt", "a successful result is not in doubt")
	}
	return checkText("Result", "error", r.Error, 4096, true)
}

// SyncRequest is what the agent posts: the whole of its state, plus what became
// of the actions it was given last time.
type SyncRequest struct {
	Protocol int       `json:"protocol"`
	Agent    string    `json:"agent"`
	SentAt   time.Time `json:"sent_at"`
	Results  []Result  `json:"results,omitempty"`
	Snapshot Snapshot  `json:"snapshot"`
}

// Validate checks the version first, so a mismatch is reported as a mismatch
// rather than as whichever field happens to have changed shape.
func (r SyncRequest) Validate() error {
	if err := checkVersion("SyncRequest", r.Protocol); err != nil {
		return err
	}
	if r.SentAt.IsZero() {
		return fault.Field("SyncRequest", "sent_at", "send time is missing")
	}
	if err := checkText("SyncRequest", "agent", r.Agent, 128, true); err != nil {
		return err
	}
	if err := validateEach("SyncRequest", "results", r.Results); err != nil {
		return err
	}
	if err := r.Snapshot.Validate(); err != nil {
		return err
	}
	seen := make(map[ActionID]struct{}, len(r.Results))
	for i, res := range r.Results {
		if _, dup := seen[res.ActionID]; dup {
			return fault.Field("SyncRequest", fmt.Sprintf("results[%d]", i),
				"action %s is reported twice", res.ActionID)
		}
		seen[res.ActionID] = struct{}{}
	}
	return nil
}

// SyncResponse is what the server answers: the actions this machine should
// apply, in the order they were queued.
type SyncResponse struct {
	Protocol   int       `json:"protocol"`
	ServerTime time.Time `json:"server_time"`
	Actions    []Action  `json:"actions,omitempty"`
}

// Validate checks the version, then that the batch is ordered and unique —
// applying actions out of order would let a reply precede the read it follows.
func (r SyncResponse) Validate() error {
	if err := checkVersion("SyncResponse", r.Protocol); err != nil {
		return err
	}
	if r.ServerTime.IsZero() {
		return fault.Field("SyncResponse", "server_time", "server time is missing")
	}
	if err := validateEach("SyncResponse", "actions", r.Actions); err != nil {
		return err
	}
	seen := make(map[ActionID]struct{}, len(r.Actions))
	for i, a := range r.Actions {
		if i > 0 && a.Seq <= r.Actions[i-1].Seq {
			return fault.Field("SyncResponse", fmt.Sprintf("actions[%d].seq", i),
				"sequence %d does not follow %d", a.Seq, r.Actions[i-1].Seq)
		}
		if _, dup := seen[a.ID]; dup {
			return fault.Field("SyncResponse", fmt.Sprintf("actions[%d]", i),
				"action %s appears twice", a.ID)
		}
		seen[a.ID] = struct{}{}
	}
	return nil
}

// ErrorBody is the one error shape every endpoint answers with.
type ErrorBody struct {
	Code    fault.Code `json:"code"`
	Message string     `json:"message"`
}

// APIError wraps an error body, so a client can tell an error document from a
// successful one by shape alone.
type APIError struct {
	Error ErrorBody `json:"error"`
}

// Validate checks the code is one cq defines, so a client can trust it enough
// to branch on.
func (e APIError) Validate() error {
	if !e.Error.Code.Valid() {
		return fault.Field("APIError", "error.code", "unknown code %q", string(e.Error.Code))
	}
	if e.Error.Message == "" {
		return fault.Field("APIError", "error.message", "error message is empty")
	}
	return nil
}

// NewAPIError renders an error as a wire document, with the message already
// reduced to what a client may see.
func NewAPIError(err error) APIError {
	code := fault.Classify(err)
	if code == "" {
		code = fault.CodeInternal
	}
	msg := fault.Public(err)
	if msg == "" {
		msg = string(code)
	}
	return APIError{Error: ErrorBody{Code: code, Message: msg}}
}

// Decode reads exactly one JSON value from r into v, then validates it.
//
// Three refusals matter and each has a reason: input beyond limit is refused so
// a caller cannot exhaust memory; an unknown field is refused because it means
// the ends disagree about the format; and trailing data after the value is
// refused because a request carrying two documents is not a request cq
// understands, whichever one it acted on.
func Decode(r io.Reader, limit int64, v Validator) error {
	if r == nil {
		return fault.Internal{Where: "protocol.Decode", Detail: "nil reader"}
	}
	if v == nil {
		return fault.Internal{Where: "protocol.Decode", Detail: "nil destination"}
	}
	if limit <= 0 {
		return fault.Internal{Where: "protocol.Decode", Detail: fmt.Sprintf("limit %d is not positive", limit)}
	}

	// One byte past the limit, so a body that ends exactly at the limit is
	// distinguishable from one that ran past it: the reader is exhausted only in
	// the second case, which is what the check below reads.
	limited := &io.LimitedReader{R: r, N: limit + 1}
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		if limited.N <= 0 {
			return fault.Parse{Where: "request", Reason: fmt.Sprintf("body exceeds the limit of %d bytes", limit)}
		}
		if errors.Is(err, io.EOF) {
			return fault.Parse{Where: "request", Reason: "body is empty"}
		}
		return fault.Parse{Where: "request", Reason: err.Error()}
	}
	if limited.N <= 0 {
		return fault.Parse{Where: "request", Reason: fmt.Sprintf("body exceeds the limit of %d bytes", limit)}
	}
	if dec.More() {
		return fault.Parse{Where: "request", Reason: "body carries more than one JSON document"}
	}
	return v.Validate()
}

// Encode writes v as a single JSON document, validating it first so cq cannot
// send something it would refuse to receive.
func Encode(w io.Writer, v Validator) error {
	if w == nil {
		return fault.Internal{Where: "protocol.Encode", Detail: "nil writer"}
	}
	if v == nil {
		return fault.Internal{Where: "protocol.Encode", Detail: "nil value"}
	}
	if err := v.Validate(); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		return fault.IO{Op: "encode", Subject: fmt.Sprintf("%T", v), Err: err}
	}
	return nil
}

// --- shared checks -------------------------------------------------------

func checkVersion(typ string, got int) error {
	if got != Version {
		return fault.Field(typ, "protocol",
			"protocol version %d is not supported; this build speaks version %d", got, Version)
	}
	return nil
}

func validateEach[T Validator](typ, field string, items []T) error {
	if len(items) > MaxListItems {
		return fault.Field(typ, field, "%d items exceeds the limit of %d", len(items), MaxListItems)
	}
	for i, item := range items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("%s.%s[%d]: %w", typ, field, i, err)
		}
	}
	return nil
}

func checkName(typ, field, value string) error {
	if value == "" {
		return fault.Field(typ, field, "name is empty")
	}
	if !name.MatchString(value) {
		return fault.Field(typ, field, "name %q must match %s", value, name)
	}
	return nil
}

func checkNames(typ, field string, values []string) error {
	if len(values) > MaxListItems {
		return fault.Field(typ, field, "%d names exceeds the limit of %d", len(values), MaxListItems)
	}
	for i, v := range values {
		if err := checkName(typ, fmt.Sprintf("%s[%d]", field, i), v); err != nil {
			return err
		}
	}
	return nil
}

// checkText rejects text that is too long, not valid UTF-8, or carrying control
// characters. Control characters are refused because this text is rendered into
// a terminal-styled page and read back out of a table: a stray escape sequence
// in a subject line is a way to forge either.
// checkDescriptionText holds a task's description to Macmuffin's bound.
//
// `checkText` above already refused invalid UTF-8 and control characters, and both
// of those are Macmuffin's rules too. What it does not know is that a description is
// bounded far tighter than a library file: 32 KiB against a megabyte. Queuing one in
// between would produce an action that is valid here and refused there — which is
// the shape of failure this whole layer exists to prevent.
//
// Bytes rather than runes, because what is bounded is what the sync carries.
func (a Action) checkDescriptionText() error {
	if a.Op != OpTaskDescribe {
		return nil
	}
	if a.Args.Text == "" {
		return fault.Field("Action", "args.text",
			"setting a description to nothing is %s, which says what it means", OpTaskDescribeClear)
	}
	if len(a.Args.Text) > MaxTaskDescriptionBytes {
		return fault.Field("Action", "args.text",
			"a description is %d bytes and the limit is %d; it would be refused on the agent machine after a sync",
			len(a.Args.Text), MaxTaskDescriptionBytes)
	}
	return nil
}

func checkText(typ, field, value string, max int, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fault.Field(typ, field, "value is empty")
	}
	if !utf8.ValidString(value) {
		return fault.Field(typ, field, "value is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(value); n > max {
		return fault.Field(typ, field, "value is %d characters, limit is %d", n, max)
	}
	if i := ControlRune(value); i >= 0 {
		r, _ := utf8.DecodeRuneInString(value[i:])
		return fault.Field(typ, field, "value carries a control character (%#U) at byte %d", r, i)
	}
	return nil
}

// ControlRune reports the byte offset of the first character no text may carry,
// or -1.
//
// Tab, newline and **carriage return** are text; everything else below a space
// is what tells a binary from a source file, whatever the extension claims.
//
// The carriage return is not a nicety. Every file in a checkout on Windows ends
// its lines with one, so a rule that refused it would refuse every file on that
// machine — and since one bad file fails the whole snapshot, it would refuse the
// entire mirror rather than a file of it.
//
// This is exported because the collector has to make the same judgement before
// it puts a file on the wire, and the two disagreeing is what a wire refusing
// something a collector carried looks like: a whole sync lost to one file.
func ControlRune(value string) int {
	for i, r := range value {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return i
		}
	}
	return -1
}

func inRange(typ, field string, got, lo, hi int) error {
	if got < lo || got > hi {
		return fault.Field(typ, field, "%d is outside the range %d to %d", got, lo, hi)
	}
	return nil
}
