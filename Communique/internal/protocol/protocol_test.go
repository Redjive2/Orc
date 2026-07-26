package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// at is a fixed UTC instant. UTC and no monotonic reading are what make a
// time.Time survive a JSON round trip unchanged.
var at = time.Date(2026, 7, 24, 18, 31, 4, 512000000, time.UTC)

func message(puid int, subject string) protocol.Message {
	return protocol.Message{
		PUID:    puid,
		MID:     "019a3f7c-2e91b4",
		Sent:    at,
		From:    "boss",
		To:      []string{"redjive", "bob"},
		CC:      []string{"carol"},
		Subject: subject,
		Convo:   protocol.ConvoRef{UID: "019a3f-0000", Title: "parser", Index: 3},
		Read:    false,
		Body:    "The parser change is in.\n",
	}
}

func snapshot() protocol.Snapshot {
	return protocol.Snapshot{
		Machine: "studio",
		User:    "redjive",
		TakenAt: at,
		Inbox:   []protocol.Message{message(41, "RE: work")},
		Archive: []protocol.Message{},
		Convos:  []protocol.Convo{{UID: "019a3f-0000", Title: "parser", Members: []string{"boss", "redjive"}, Count: 4}},
		Tasks: []protocol.Task{{
			Name: "fix-the-parser", Owner: "bob", Collaborators: []string{"alice"},
			Priority: 4, Difficulty: 3, Status: 3, Done: 5, Total: 8,
			Scope: []string{"internal/tree/"}, Worktree: "../orc-parser",
		}},
		Admin: &protocol.AdminState{
			Users:    []protocol.AdminUser{{Name: "boss"}},
			Messages: []protocol.Message{message(7, "internal")},
			Receipts: []protocol.Receipt{{MID: "019a3f7c-2e91b4", Recipient: "bob", Read: true, At: &at}},
		},
	}
}

func request() protocol.SyncRequest {
	return protocol.SyncRequest{
		Protocol: protocol.Version,
		Agent:    "cq/0.1",
		SentAt:   at,
		Results: []protocol.Result{
			{ActionID: protocol.ActionID(strings.Repeat("a", 32)), OK: true, At: at},
			{ActionID: protocol.ActionID(strings.Repeat("b", 32)), OK: false, Error: "no such user \"carol\"", At: at},
		},
		Snapshot: snapshot(),
	}
}

func response() protocol.SyncResponse {
	return protocol.SyncResponse{
		Protocol:   protocol.Version,
		ServerTime: at,
		Actions: []protocol.Action{
			{ID: protocol.ActionID(strings.Repeat("c", 32)), Seq: 1, Machine: "studio", Op: protocol.OpRead,
				Args: protocol.Args{PUID: 41}, Queued: at},
			{ID: protocol.ActionID(strings.Repeat("d", 32)), Seq: 2, Machine: "studio", Op: protocol.OpReply,
				Args: protocol.Args{PUID: 41, Subject: "RE: work", Body: "looks good"}, Queued: at},
		},
	}
}

// TestRoundTrip is the milestone gate: every wire type marshals, decodes, and
// marshals again to the identical bytes.
func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  protocol.Validator
		into func() protocol.Validator
	}{
		{"sync request", ptr(request()), func() protocol.Validator { return &protocol.SyncRequest{} }},
		{"sync response", ptr(response()), func() protocol.Validator { return &protocol.SyncResponse{} }},
		{"api error", ptr(protocol.NewAPIError(fault.Usage{Reason: "bad"})), func() protocol.Validator { return &protocol.APIError{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first bytes.Buffer
			if err := protocol.Encode(&first, tc.val); err != nil {
				t.Fatalf("Encode: %v", err)
			}

			got := tc.into()
			if err := protocol.Decode(bytes.NewReader(first.Bytes()), protocol.MaxSnapshotBytes, got); err != nil {
				t.Fatalf("Decode: %v", err)
			}

			var second bytes.Buffer
			if err := protocol.Encode(&second, got); err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if first.String() != second.String() {
				t.Errorf("round trip changed the document:\n first %s\nsecond %s", first.String(), second.String())
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// TestDecodeRejectsUnknownFields is why the ends cannot silently disagree.
// TestAnUnreadReceiptHasNoReadTime pins the encoding, not the Go value.
//
// The bug this replaces was invisible from Go: the struct said Read: false and
// a zero At, Validate was satisfied, and every test passed — while the JSON on
// the wire carried "at":"0001-01-01T00:00:00Z" and told the interface the
// message had been read in the year 1. `omitempty` does nothing for a
// time.Time, because a struct is never empty to encoding/json.
func TestAnUnreadReceiptHasNoReadTime(t *testing.T) {
	unread, err := json.Marshal(protocol.Receipt{MID: "m", Recipient: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unread), "at") {
		t.Errorf("an unread receipt carries a read time: %s", unread)
	}

	read, err := json.Marshal(protocol.Receipt{MID: "m", Recipient: "bob", Read: true, At: &at})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(read), `"at":`) {
		t.Errorf("a read receipt lost its read time: %s", read)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	var buf bytes.Buffer
	if err := protocol.Encode(&buf, ptr(request())); err != nil {
		t.Fatal(err)
	}

	var generic map[string]any
	if err := json.Unmarshal(buf.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	generic["invented_by_a_newer_build"] = true
	tampered, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}

	var into protocol.SyncRequest
	err = protocol.Decode(bytes.NewReader(tampered), protocol.MaxSnapshotBytes, &into)
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "invented_by_a_newer_build") {
		t.Errorf("message %q should name the unknown field", err)
	}
}

func TestDecodeRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"empty", "", "empty"},
		{"truncated", `{"protocol":1`, "unexpected"},
		{"not an object", `["a"]`, "cannot unmarshal"},
		{"two documents", `{"protocol":1,"agent":"x","sent_at":"2026-07-24T18:31:04Z","snapshot":{"machine":"m","user":"u","taken_at":"2026-07-24T18:31:04Z","inbox":[],"archive":[],"convos":[],"tasks":[]}} {"protocol":1}`, "more than one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var into protocol.SyncRequest
			err := protocol.Decode(strings.NewReader(tc.body), protocol.MaxSnapshotBytes, &into)
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestDecodeEnforcesItsLimit(t *testing.T) {
	body := `{"protocol":1,"agent":"` + strings.Repeat("x", 4096) + `"}`
	var into protocol.SyncRequest
	err := protocol.Decode(strings.NewReader(body), 64, &into)
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
}

func TestDecodeGuardsItsOwnArguments(t *testing.T) {
	var into protocol.SyncRequest
	if err := protocol.Decode(nil, 10, &into); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a nil reader should be internal, got %v", err)
	}
	if err := protocol.Decode(strings.NewReader("{}"), 10, nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a nil destination should be internal, got %v", err)
	}
	if err := protocol.Decode(strings.NewReader("{}"), 0, &into); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a non-positive limit should be internal, got %v", err)
	}
}

func TestEncodeRefusesToSendWhatItWouldRefuseToReceive(t *testing.T) {
	bad := request()
	bad.Snapshot.User = "Not A Valid Name"
	var buf bytes.Buffer
	if err := protocol.Encode(&buf, &bad); !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should have been written, got %q", buf.String())
	}

	if err := protocol.Encode(nil, ptr(request())); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a nil writer should be internal, got %v", err)
	}
	if err := protocol.Encode(&buf, nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a nil value should be internal, got %v", err)
	}
}

func TestVersionMismatchIsReportedAsSuch(t *testing.T) {
	r := request()
	r.Protocol = protocol.Version + 1
	err := r.Validate()
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "protocol version") {
		t.Errorf("message %q should name the version, not a field that happened to change", err)
	}
}

// TestActionArgumentRules checks both halves of every operation's contract: the
// operands it requires, and the ones it must not carry.
// digest stands in for the SHA-256 of a file, in the form the wire requires.
const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestActionArgumentRules(t *testing.T) {
	base := func(op protocol.Op, args protocol.Args) protocol.Action {
		return protocol.Action{ID: protocol.ActionID(strings.Repeat("e", 32)), Seq: 1, Machine: "studio", Op: op, Args: args, Queued: at}
	}

	valid := map[protocol.Op]protocol.Args{
		protocol.OpSend:    {To: []string{"bob"}, Subject: "hello", Body: "hi"},
		protocol.OpReply:   {PUID: 41, Subject: "RE: hello", Body: "hi"},
		protocol.OpRead:    {PUID: 41},
		protocol.OpArchive: {PUID: 41},
		protocol.OpCC:      {ConvoUID: "019a-0000", User: "carol"},

		// The library verbs. Every one that expects to find something carries the
		// digest of what it expected — which is what stops an edit made from a
		// minutes-old mirror overwriting whatever arrived in between.
		protocol.OpWrite:     {Path: "Docs/Vision.md", Text: "new\n", Base: digest},
		protocol.OpCreate:    {Path: "Docs/New.md", Text: "fresh\n"},
		protocol.OpDelete:    {Path: "Docs/Old.md", Base: digest},
		protocol.OpMakeDir:   {Path: "Docs/Ideas"},
		protocol.OpRemoveDir: {Path: "Docs/Ideas"},
		protocol.OpRemoveTree: {Path: "Docs/Old",
			Paths: []string{"Docs/Old/a.md", "Docs/Old/deep/b.md"}},

		// The task verbs. Every mutating `muff` command has one, and this map is
		// what makes that checkable: the loop below walks protocol.Ops, so an
		// operation added without a sample here fails as "requires task".
		protocol.OpTaskCreate:   {Task: "fix-the-parser", Priority: 4, Difficulty: 3},
		protocol.OpTaskPush:     {Task: "fix-the-parser"},
		protocol.OpTaskClaim:    {Task: "fix-the-parser"},
		protocol.OpTaskAssign:   {Task: "fix-the-parser", User: "bob"},
		protocol.OpTaskInvite:   {Task: "fix-the-parser", User: "bob"},
		protocol.OpTaskKick:     {Task: "fix-the-parser", User: "bob"},
		protocol.OpTaskLeave:    {Task: "fix-the-parser"},
		protocol.OpTaskScope:    {Task: "fix-the-parser", Paths: []string{"internal/tree"}},
		protocol.OpTaskWorktree: {Task: "fix-the-parser", Path: "work/parser"},
		protocol.OpTaskStatus:   {Task: "fix-the-parser", Status: 2},
		protocol.OpTaskSubtask:  {Task: "fix-the-parser", Sub: "write-the-tests"},
		protocol.OpTaskComplete: {Task: "fix-the-parser"},
		protocol.OpTaskDelete:   {Task: "fix-the-parser"},

		// The fleet verbs. As above, the loop walks protocol.Ops, so a verb added
		// without a sample here fails rather than being silently unexercised.
		protocol.OpOrcNewIdentity:     {Identity: "atlas"},
		protocol.OpOrcNewRole:         {Role: "engineer", Authority: 60, Description: "writes the code"},
		protocol.OpOrcNewPermission:   {Permission: "edit-anno", Floor: 40, Patterns: []string{"read(Anno/**)"}},
		protocol.OpOrcEditPermission:  {Permission: "edit-anno", Floor: 40, Patterns: []string{"read(Anno/**)"}},
		protocol.OpOrcAssignRole:      {Identity: "atlas", Role: "engineer"},
		protocol.OpOrcAssignAuthority: {Role: "engineer", Authority: 55},
		protocol.OpOrcAssignPerm:      {Role: "engineer", Permission: "edit-anno"},
		protocol.OpOrcRemoveIdentity:  {Identity: "atlas"},
		protocol.OpOrcRemoveRole:      {Role: "engineer"},
		protocol.OpOrcRemovePerm:      {Permission: "edit-anno"},
		protocol.OpOrcGrant:           {Identity: "atlas", Permission: "edit-anno"},
		protocol.OpOrcRevoke:          {Identity: "atlas", Permission: "edit-anno"},
		protocol.OpOrcMove:            {Identity: "atlas", Boss: "boss"},
		protocol.OpOrcEmploy:          {Identity: "atlas"},
		protocol.OpOrcFire:            {Identity: "atlas"},
		protocol.OpOrcBudget:          {Role: "engineer", Load: 24},
		protocol.OpOrcPoke:            {Identity: "atlas"},
		protocol.OpOrcRefresh:         {Identity: "atlas"},
		protocol.OpOrcWorkspace:       {Identity: "atlas", Workspace: "/trees/parser", From: "/old/workspace"},
		protocol.OpOrcInstructSet:     {Prompt: "identity", PromptName: "atlas", Text: "you write the parser"},
		protocol.OpOrcInstructClear:   {Prompt: "system"},
		protocol.OpTaskDescribe:       {Task: "fix-the-parser", Text: "# what to do\n"},
		protocol.OpTaskDescribeClear:  {Task: "fix-the-parser"},
		protocol.OpOrcTend:            {},
		protocol.OpOrcToolkit:         {Identity: "boss"},
		protocol.OpUpgrade:            {},
	}

	// The optional operands, which the loop above cannot cover because it takes
	// one sample per operation: both of these are the *same* operation carrying
	// something it is allowed but not required to carry.
	for _, tc := range []struct {
		name string
		op   protocol.Op
		args protocol.Args
	}{
		{"complete one subtask", protocol.OpTaskComplete,
			protocol.Args{Task: "fix-the-parser", Sub: "write-the-tests"}},
		{"complete a task whose subtasks are not all done", protocol.OpTaskComplete,
			protocol.Args{Task: "fix-the-parser", Force: true}},
		{"delete one subtask", protocol.OpTaskDelete,
			protocol.Args{Task: "fix-the-parser", Sub: "write-the-tests"}},

		// The fleet verbs' optional operands, for the same reason.
		{"narrow one role instead of deleting the permission", protocol.OpOrcRemovePerm,
			protocol.Args{Permission: "edit-anno", Role: "engineer"}},
		{"a grant with a wall-clock expiry", protocol.OpOrcGrant,
			protocol.Args{Identity: "atlas", Permission: "edit-anno", Until: "2h"}},
		{"employ at a chosen model and effort", protocol.OpOrcEmploy,
			protocol.Args{Identity: "atlas", Model: "opus", Effort: "high"}},
		{"poke with something to say", protocol.OpOrcPoke,
			protocol.Args{Identity: "atlas", Message: "the tests are failing"}},
		{"a budget of nothing, which refuses every employ", protocol.OpOrcBudget,
			protocol.Args{Role: "engineer", Load: 0}},
	} {
		if err := base(tc.op, tc.args).Validate(); err != nil {
			t.Errorf("%s was rejected: %v", tc.name, err)
		}
	}

	for _, op := range protocol.Ops {
		t.Run(string(op)+" accepts its own arguments", func(t *testing.T) {
			if err := base(op, valid[op]).Validate(); err != nil {
				t.Errorf("valid %s rejected: %v", op, err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		op   protocol.Op
		args protocol.Args
		want string
	}{
		{"send without recipients", protocol.OpSend, protocol.Args{Subject: "s", Body: "b"}, "requires to"},
		{"send without a subject", protocol.OpSend, protocol.Args{To: []string{"bob"}, Body: "b"}, "requires subject"},
		{"send without a body", protocol.OpSend, protocol.Args{To: []string{"bob"}, Subject: "s"}, "requires body"},
		{"send carrying a puid", protocol.OpSend, protocol.Args{To: []string{"bob"}, Subject: "s", Body: "b", PUID: 3}, "takes no puid"},

		// The library verbs, and the mistakes that would make one dangerous.
		{"a write with no path", protocol.OpWrite, protocol.Args{Text: "x", Base: digest}, "requires path"},
		{"a write with no base", protocol.OpWrite, protocol.Args{Path: "a.md", Text: "x"}, "requires base"},
		{"a write with a base that is not a digest", protocol.OpWrite,
			protocol.Args{Path: "a.md", Text: "x", Base: "nope"}, "64 hex"},
		{"a delete with no base", protocol.OpDelete, protocol.Args{Path: "a.md"}, "requires base"},
		{"a create carrying a base it cannot have", protocol.OpCreate,
			protocol.Args{Path: "a.md", Text: "x", Base: digest}, "takes no base"},
		{"a mkdir carrying contents", protocol.OpMakeDir,
			protocol.Args{Path: "d", Text: "x"}, "takes no text"},
		{"a path that climbs out of the checkout", protocol.OpWrite,
			protocol.Args{Path: "../../etc/passwd", Text: "x", Base: digest}, "climbs out"},
		// The task verbs, and the mistakes that would put a value in the queue that
		// Macmuffin would then refuse — which is a refusal nobody sees until the
		// next sync, on a machine nobody is looking at.
		{"a task create with no name", protocol.OpTaskCreate,
			protocol.Args{Priority: 3, Difficulty: 3}, "requires task"},
		{"a task create with no priority", protocol.OpTaskCreate,
			protocol.Args{Task: "t", Difficulty: 3}, "requires priority"},
		{"a priority off the scale", protocol.OpTaskCreate,
			protocol.Args{Task: "t", Priority: 9, Difficulty: 3}, "outside the range 1 to 5"},
		{"a status off the scale", protocol.OpTaskStatus,
			protocol.Args{Task: "t", Status: 7}, "outside the range 1 to 4"},
		{"a push carrying a status it does not take", protocol.OpTaskPush,
			protocol.Args{Task: "t", Status: 2}, "takes no status"},
		{"a claim carrying a subtask it does not take", protocol.OpTaskClaim,
			protocol.Args{Task: "t", Sub: "s"}, "takes no sub"},
		{"a delete asking to be forced", protocol.OpTaskDelete,
			protocol.Args{Task: "t", Force: true}, "takes no force"},
		{"a scope with no paths", protocol.OpTaskScope, protocol.Args{Task: "t"}, "requires paths"},
		{"a scope path that climbs out", protocol.OpTaskScope,
			protocol.Args{Task: "t", Paths: []string{"../etc"}}, "climbs out"},
		{"a leave carrying paths it does not take", protocol.OpTaskLeave,
			protocol.Args{Task: "t", Paths: []string{"x"}}, "takes no paths"},

		// The fleet verbs, and the values Orc itself would refuse.
		{"a role at the operator's level", protocol.OpOrcNewRole,
			protocol.Args{Role: "boss-like", Authority: 100, Description: "no"}, "outside the range 1 to 99"},
		{"a role with no description", protocol.OpOrcNewRole,
			protocol.Args{Role: "engineer", Authority: 60}, "requires description"},
		{"a permission with no clauses", protocol.OpOrcNewPermission,
			protocol.Args{Permission: "empty", Floor: 40}, "requires patterns"},
		{"a move with no boss", protocol.OpOrcMove,
			protocol.Args{Identity: "atlas"}, "requires boss"},
		{"a budget above what orc will hold", protocol.OpOrcBudget,
			protocol.Args{Role: "engineer", Load: 99999}, "outside the range"},
		{"an identity name that is not a name", protocol.OpOrcFire,
			protocol.Args{Identity: "not a name"}, "must match"},
		{"tend carrying an identity it does not take", protocol.OpOrcTend,
			protocol.Args{Identity: "atlas"}, "takes no identity"},
		{"a fire asking for a model", protocol.OpOrcFire,
			protocol.Args{Identity: "atlas", Model: "opus"}, "takes no model"},
		{"a revoke with an expiry it does not take", protocol.OpOrcRevoke,
			protocol.Args{Identity: "atlas", Permission: "p", Until: "2h"}, "takes no until"},

		{"an absolute path", protocol.OpWrite,
			protocol.Args{Path: "/etc/passwd", Text: "x", Base: digest}, "absolute"},
		{"a mail verb carrying a path", protocol.OpRead,
			protocol.Args{PUID: 1, Path: "a.md"}, "takes no path"},
		{"reply without a puid target", protocol.OpReply, protocol.Args{Subject: "s", Body: "b", PUID: -1}, "negative"},
		{"read carrying a body", protocol.OpRead, protocol.Args{PUID: 1, Body: "b"}, "takes no body"},
		{"read carrying recipients", protocol.OpRead, protocol.Args{PUID: 1, To: []string{"bob"}}, "takes no to"},
		{"archive carrying a user", protocol.OpArchive, protocol.Args{PUID: 1, User: "bob"}, "takes no user"},
		{"cc without a user", protocol.OpCC, protocol.Args{ConvoUID: "019a"}, "requires user"},
		{"cc without a conversation", protocol.OpCC, protocol.Args{User: "carol"}, "requires convo_uid"},
		{"cc carrying a puid", protocol.OpCC, protocol.Args{ConvoUID: "019a", User: "carol", PUID: 2}, "takes no puid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.op, tc.args).Validate()
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestActionRejectsBadIdentifiers(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*protocol.Action)
		want string
	}{
		{"short id", func(a *protocol.Action) { a.ID = "abc" }, "32 hex"},
		{"uppercase id", func(a *protocol.Action) { a.ID = protocol.ActionID(strings.Repeat("A", 32)) }, "32 hex"},
		{"empty machine", func(a *protocol.Action) { a.Machine = "" }, "machine id is empty"},
		{"machine with a slash", func(a *protocol.Action) { a.Machine = "a/b" }, "must match"},
		{"unknown op", func(a *protocol.Action) { a.Op = "detonate" }, "unknown operation"},
		{"no queue time", func(a *protocol.Action) { a.Queued = time.Time{} }, "queue time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := protocol.Action{ID: protocol.ActionID(strings.Repeat("f", 32)), Seq: 1, Machine: "studio",
				Op: protocol.OpRead, Args: protocol.Args{PUID: 1}, Queued: at}
			tc.mut(&a)
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected a fault")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestResultMustBeHonest(t *testing.T) {
	id := protocol.ActionID(strings.Repeat("a", 32))
	for _, tc := range []struct {
		name string
		r    protocol.Result
		want string
	}{
		{"success carrying an error", protocol.Result{ActionID: id, OK: true, Error: "hmm", At: at}, "carries an error"},
		{"failure with no reason", protocol.Result{ActionID: id, OK: false, At: at}, "carries no reason"},
		{"no completion time", protocol.Result{ActionID: id, OK: true}, "completion time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.Validate()
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestResponseRequiresOrderedUniqueActions(t *testing.T) {
	out := response()
	out.Actions[1].Seq = out.Actions[0].Seq
	if err := out.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a non-increasing sequence should be refused, got %v", err)
	}

	out = response()
	out.Actions[1].ID = out.Actions[0].ID
	if err := out.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a duplicated action id should be refused, got %v", err)
	}
}

func TestRequestRejectsDuplicateResults(t *testing.T) {
	r := request()
	r.Results[1].ActionID = r.Results[0].ActionID
	err := r.Validate()
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "reported twice") {
		t.Errorf("message %q should say the action is duplicated", err)
	}
}

// TestMetadataOnlyIsHonest guards the one mislabelling that would send bodies
// the operator asked to withhold.
func TestMetadataOnlyIsHonest(t *testing.T) {
	a := protocol.AdminState{
		Messages:     []protocol.Message{message(1, "s")},
		MetadataOnly: true,
	}
	err := a.Validate()
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "metadata-only") {
		t.Errorf("message %q should name the mislabelling", err)
	}

	a.Messages[0].Body = ""
	if err := a.Validate(); err != nil {
		t.Errorf("a genuinely body-free state should pass: %v", err)
	}
}

func TestTaskScales(t *testing.T) {
	sound := protocol.Task{Name: "t", Priority: 3, Difficulty: 3, Status: 3, Done: 1, Total: 2}
	if err := sound.Validate(); err != nil {
		t.Fatalf("a sound task was rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*protocol.Task)
		want string
	}{
		{"priority too high", func(t *protocol.Task) { t.Priority = 6 }, "range 1 to 5"},
		{"priority too low", func(t *protocol.Task) { t.Priority = 0 }, "range 1 to 5"},
		{"difficulty out of range", func(t *protocol.Task) { t.Difficulty = 9 }, "range 1 to 5"},
		{"status out of range", func(t *protocol.Task) { t.Status = 5 }, "range 1 to 4"},
		{"more done than exist", func(t *protocol.Task) { t.Done, t.Total = 3, 2 }, "3 subtasks done of 2"},
		{"negative counts", func(t *protocol.Task) { t.Done = -1 }, "negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := sound
			tc.mut(&task)
			err := task.Validate()
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestReceiptConsistency(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    protocol.Receipt
		want string
	}{
		{"read with no time", protocol.Receipt{MID: "m", Recipient: "bob", Read: true}, "no read time"},
		{"unread with a time", protocol.Receipt{MID: "m", Recipient: "bob", At: &at}, "carries a read time"},
		{"no message", protocol.Receipt{Recipient: "bob"}, "message id is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.r.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, should mention %q", err, tc.want)
			}
		})
	}
}

// TestTextRejectsControlCharacters is what keeps an escape sequence out of a
// subject line, in a page styled to look like a terminal.
func TestTextRejectsControlCharacters(t *testing.T) {
	m := message(1, "RE: \x1b[31mred\x1b[0m")
	err := m.Validate()
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "control character") {
		t.Errorf("message %q should name the problem", err)
	}

	// Newlines and tabs are ordinary content in a markdown body.
	m = message(1, "plain")
	m.Body = "line one\n\tindented\n"
	if err := m.Validate(); err != nil {
		t.Errorf("a body with newlines and tabs should pass: %v", err)
	}
}

func TestConvoRefCoherence(t *testing.T) {
	if err := (protocol.ConvoRef{}).Validate(); err != nil {
		t.Errorf("an absent conversation reference is legitimate: %v", err)
	}
	err := protocol.ConvoRef{Title: "orphan"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "without a uid") {
		t.Errorf("error = %v, want a complaint about the missing uid", err)
	}
}

func TestNewAPIErrorAlwaysProducesAValidDocument(t *testing.T) {
	for _, err := range []error{
		fault.Usage{Reason: "bad"},
		fault.Internal{Where: "w", Detail: "secret path"},
		fault.Unauthenticated{Reason: "digest mismatch"},
		errors.New("unclassified"),
		nil,
	} {
		doc := protocol.NewAPIError(err)
		if verr := doc.Validate(); verr != nil {
			t.Errorf("NewAPIError(%v) produced an invalid document: %v", err, verr)
		}
		if strings.Contains(doc.Error.Message, "secret path") || strings.Contains(doc.Error.Message, "digest") {
			t.Errorf("NewAPIError leaked internal detail: %q", doc.Error.Message)
		}
	}
}

func TestAPIErrorRejectsUnknownCodes(t *testing.T) {
	doc := protocol.APIError{Error: protocol.ErrorBody{Code: "invented", Message: "m"}}
	if err := doc.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("an unknown code should be refused, got %v", err)
	}
	doc = protocol.APIError{Error: protocol.ErrorBody{Code: fault.CodeUsage}}
	if err := doc.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("an empty message should be refused, got %v", err)
	}
}

func TestOversizedListsAreRefused(t *testing.T) {
	s := snapshot()
	s.Inbox = make([]protocol.Message, protocol.MaxListItems+1)
	err := s.Validate()
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "exceeds the limit") {
		t.Errorf("message %q should name the limit", err)
	}
}

// TestADescriptionTooBigIsRefusedBeforeItIsQueued. Macmuffin's bound is 32 KiB and
// cq's generic text limit is a megabyte, so without this an oversized description
// would be accepted at the browser and refused hours later on a machine nobody is
// watching — which is the failure this whole layer exists to prevent.
func TestOversizedDescriptionIsRefusedAtTheBrowser(t *testing.T) {
	base := func(text string) protocol.Action {
		return protocol.Action{
			ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
			Op: protocol.OpTaskDescribe, Queued: at,
			Args: protocol.Args{Task: "fix-the-parser", Text: text},
		}
	}

	// At the bound it is fine.
	if err := base(strings.Repeat("x", protocol.MaxTaskDescriptionBytes)).Validate(); err != nil {
		t.Errorf("a description at the limit was refused: %v", err)
	}

	err := base(strings.Repeat("x", protocol.MaxTaskDescriptionBytes+1)).Validate()
	if err == nil {
		t.Fatal("an oversized description was queued")
	}
	// The refusal gives the arithmetic and says where it would otherwise have gone
	// wrong, because the fix is to shorten it and the person has to know by how much.
	for _, want := range []string{"limit", "agent machine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// Setting a description to nothing is clearing it, and there is an operation that
// says so. Two spellings of one intent leave the queue unable to report which
// happened.
func TestAnEmptyDescriptionIsRefusedInFavourOfClear(t *testing.T) {
	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpTaskDescribe, Queued: at,
		Args: protocol.Args{Task: "fix-the-parser", Text: ""},
	}
	err := action.Validate()
	if err == nil {
		t.Fatal("an empty description was queued as a write")
	}
	if !strings.Contains(err.Error(), string(protocol.OpTaskDescribeClear)) {
		t.Errorf("the refusal does not name the operation that means it: %v", err)
	}
}

// A description carrying an escape sequence is refused here as well as in Macmuffin:
// it is printed to a terminal by `muff describe` at the far end.
func TestADescriptionWithControlCharactersIsRefused(t *testing.T) {
	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpTaskDescribe, Queued: at,
		Args: protocol.Args{Task: "fix-the-parser", Text: "before\x1b[31m after"},
	}
	if err := action.Validate(); err == nil {
		t.Error("an escape sequence was queued into a description")
	}
}
