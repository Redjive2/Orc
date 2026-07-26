package protocol_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// TestEveryFieldIsChecked walks each wire type, breaks one field at a time, and
// asserts the break is caught and named.
//
// The point is not the individual cases but the whole: a field with no case here
// is a field nothing validates, and the snapshot is stored and re-served without
// further inspection. Each case names the field it breaks, so a failure says
// which check went missing rather than only that one did.
func TestEveryFieldIsChecked(t *testing.T) {
	long := strings.Repeat("x", protocol.MaxSubjectRunes+1)
	huge := strings.Repeat("x", protocol.MaxBodyBytes+1)
	badUTF8 := "\xff\xfe"

	for _, tc := range []struct {
		field string
		val   protocol.Validator
		want  string
	}{
		// ConvoRef
		{"ConvoRef.title", protocol.ConvoRef{UID: "u", Title: long}, "characters"},
		{"ConvoRef.index", protocol.ConvoRef{UID: "u", Index: -1}, "negative"},

		// Message
		{"Message.puid", mutate(func(m *protocol.Message) { m.PUID = -1 }), "negative"},
		{"Message.mid empty", mutate(func(m *protocol.Message) { m.MID = "" }), "message id is empty"},
		{"Message.mid long", mutate(func(m *protocol.Message) { m.MID = strings.Repeat("m", 129) }), "characters"},
		{"Message.sent", mutate(func(m *protocol.Message) { m.Sent = time.Time{} }), "send time"},
		{"Message.from", mutate(func(m *protocol.Message) { m.From = "Not Valid" }), "must match"},
		{"Message.to", mutate(func(m *protocol.Message) { m.To = []string{"Not Valid"} }), "must match"},
		{"Message.cc", mutate(func(m *protocol.Message) { m.CC = []string{""} }), "name is empty"},
		{"Message.subject", mutate(func(m *protocol.Message) { m.Subject = long }), "characters"},
		{"Message.convo", mutate(func(m *protocol.Message) { m.Convo = protocol.ConvoRef{Title: "orphan"} }), "without a uid"},
		{"Message.body", mutate(func(m *protocol.Message) { m.Body = huge }), "characters"},

		// Convo
		{"Convo.uid empty", protocol.Convo{}, "uid is empty"},
		{"Convo.uid long", protocol.Convo{UID: strings.Repeat("u", 129)}, "characters"},
		{"Convo.title", protocol.Convo{UID: "u", Title: long}, "characters"},
		{"Convo.members", protocol.Convo{UID: "u", Members: []string{"BAD"}}, "must match"},
		{"Convo.count", protocol.Convo{UID: "u", Count: -1}, "negative"},

		// Receipt
		{"Receipt.recipient", protocol.Receipt{MID: "m", Recipient: "Not Valid"}, "must match"},

		// Task
		{"Task.name", protocol.Task{Name: "Not Valid", Priority: 1, Difficulty: 1, Status: 1}, "must match"},
		{"Task.owner", protocol.Task{Name: "t", Owner: "Not Valid", Priority: 1, Difficulty: 1, Status: 1}, "must match"},
		{"Task.collaborators", protocol.Task{Name: "t", Collaborators: []string{"BAD"}, Priority: 1, Difficulty: 1, Status: 1}, "must match"},
		{"Task.scope entry", protocol.Task{Name: "t", Priority: 1, Difficulty: 1, Status: 1, Scope: []string{"a\x00b"}}, "control character"},
		{"Task.scope count", protocol.Task{Name: "t", Priority: 1, Difficulty: 1, Status: 1,
			Scope: make([]string, protocol.MaxListItems+1)}, "exceeds the limit"},
		{"Task.worktree", protocol.Task{Name: "t", Priority: 1, Difficulty: 1, Status: 1,
			Worktree: strings.Repeat("w", 4097)}, "characters"},

		// AdminUser
		{"AdminUser.name", protocol.AdminUser{Name: "Not Valid"}, "must match"},

		// AdminState
		{"AdminState.users", protocol.AdminState{Users: []protocol.AdminUser{{Name: "BAD"}}}, "must match"},
		{"AdminState.messages", protocol.AdminState{Messages: []protocol.Message{{}}}, "message id is empty"},
		{"AdminState.receipts", protocol.AdminState{Receipts: []protocol.Receipt{{}}}, "message id is empty"},

		// Snapshot
		{"Snapshot.machine", snap(func(s *protocol.Snapshot) { s.Machine = "Not Valid" }), "must match"},
		{"Snapshot.machine empty", snap(func(s *protocol.Snapshot) { s.Machine = "" }), "machine id is empty"},
		{"Snapshot.user", snap(func(s *protocol.Snapshot) { s.User = "" }), "name is empty"},
		{"Snapshot.taken_at", snap(func(s *protocol.Snapshot) { s.TakenAt = time.Time{} }), "capture time"},
		{"Snapshot.inbox", snap(func(s *protocol.Snapshot) { s.Inbox = []protocol.Message{{}} }), "message id is empty"},
		{"Snapshot.archive", snap(func(s *protocol.Snapshot) { s.Archive = []protocol.Message{{}} }), "message id is empty"},
		{"Snapshot.convos", snap(func(s *protocol.Snapshot) { s.Convos = []protocol.Convo{{}} }), "uid is empty"},
		{"Snapshot.tasks", snap(func(s *protocol.Snapshot) { s.Tasks = []protocol.Task{{}} }), "name is empty"},
		{"Snapshot.admin", snap(func(s *protocol.Snapshot) {
			s.Admin = &protocol.AdminState{Users: []protocol.AdminUser{{}}}
		}), "name is empty"},

		// Action argument text
		{"Action.args.to", act(protocol.OpSend, protocol.Args{To: []string{"BAD"}, Subject: "s", Body: "b"}), "must match"},
		{"Action.args.user", act(protocol.OpCC, protocol.Args{ConvoUID: "c", User: "BAD"}), "must match"},
		{"Action.args.subject", act(protocol.OpReply, protocol.Args{PUID: 1, Subject: long, Body: "b"}), "characters"},
		{"Action.args.body", act(protocol.OpReply, protocol.Args{PUID: 1, Subject: "s", Body: huge}), "characters"},
		{"Action.args.convo_uid", act(protocol.OpCC, protocol.Args{ConvoUID: strings.Repeat("c", 129), User: "bob"}), "characters"},

		// Result
		{"Result.action_id", protocol.Result{ActionID: "short", OK: true, At: at}, "32 hex"},

		// SyncRequest
		{"SyncRequest.agent", req(func(r *protocol.SyncRequest) { r.Agent = strings.Repeat("a", 129) }), "characters"},
		{"SyncRequest.sent_at", req(func(r *protocol.SyncRequest) { r.SentAt = time.Time{} }), "send time"},
		{"SyncRequest.results", req(func(r *protocol.SyncRequest) {
			r.Results = []protocol.Result{{ActionID: "nope", OK: true, At: at}}
		}), "32 hex"},

		// SyncResponse
		{"SyncResponse.server_time", protocol.SyncResponse{Protocol: protocol.Version}, "server time"},
		{"SyncResponse.actions", protocol.SyncResponse{Protocol: protocol.Version, ServerTime: at,
			Actions: []protocol.Action{{}}}, "32 hex"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			err := tc.val.Validate()
			if err == nil {
				t.Fatalf("%s is not validated at all", tc.field)
			}
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}

	// Invalid UTF-8 cannot arrive through JSON, which substitutes the
	// replacement rune, but it can arrive from a source adapter reading a file.
	t.Run("text rejects invalid UTF-8", func(t *testing.T) {
		m := message(1, "ok")
		m.Body = badUTF8
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
			t.Errorf("error = %v, want a complaint about encoding", err)
		}
	})
}

// mutate builds a sound message and breaks one thing about it.
func mutate(f func(*protocol.Message)) protocol.Message {
	m := message(1, "subject")
	f(&m)
	return m
}

func snap(f func(*protocol.Snapshot)) protocol.Snapshot {
	s := snapshot()
	f(&s)
	return s
}

func req(f func(*protocol.SyncRequest)) protocol.SyncRequest {
	r := request()
	f(&r)
	return r
}

func act(op protocol.Op, args protocol.Args) protocol.Action {
	return protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1,
		Machine: "studio", Op: op, Args: args, Queued: at,
	}
}

// TestEncodeReportsAFailedWrite covers the one I/O path in the package.
func TestEncodeReportsAFailedWrite(t *testing.T) {
	err := protocol.Encode(failWriter{}, ptr(request()))
	if !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
	if !strings.Contains(err.Error(), "encode") {
		t.Errorf("message %q should name the operation", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("stream gone") }

// TestDecodeReportsAnOversizedBodyAsSuch checks the limit is reported as a limit
// rather than as whatever syntax error truncation happened to produce.
func TestDecodeReportsAnOversizedBodyAsSuch(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"truncated mid-token", `{"agent":"` + strings.Repeat("x", 4096)},
		{"complete but too long", `{"protocol":1,"agent":"` + strings.Repeat("x", 4096) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var into protocol.SyncRequest
			err := protocol.Decode(strings.NewReader(tc.body), 64, &into)
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), "exceeds the limit") {
				t.Errorf("message %q should name the limit", err)
			}
		})
	}

	// A body that fits exactly is not over the limit.
	body := `{"protocol":1,"server_time":"2026-07-24T18:31:04.512Z"}`
	var into protocol.SyncResponse
	if err := protocol.Decode(strings.NewReader(body), int64(len(body)), &into); err != nil {
		t.Errorf("a body of exactly the limit should be accepted: %v", err)
	}
}

// TestRemainingBoundaryChecks reaches the three guards the field table steps
// past, each because an earlier check fires first in the ordinary case.
func TestRemainingBoundaryChecks(t *testing.T) {
	t.Run("an empty scope entry is refused", func(t *testing.T) {
		task := protocol.Task{Name: "t", Priority: 1, Difficulty: 1, Status: 1, Scope: []string{""}}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "value is empty") {
			t.Errorf("error = %v, want a complaint about the empty entry", err)
		}
	})

	t.Run("too many names are refused", func(t *testing.T) {
		m := message(1, "s")
		m.To = make([]string, protocol.MaxListItems+1)
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
			t.Errorf("error = %v, want a complaint about the count", err)
		}
	})

	t.Run("a body one byte over the limit is refused", func(t *testing.T) {
		// Valid JSON that decodes cleanly, sized so the reader is exhausted
		// exactly as the document ends — the case a truncation error misses.
		body := `{"protocol":1,"server_time":"2026-07-24T18:31:04.512Z"}`
		var into protocol.SyncResponse
		err := protocol.Decode(strings.NewReader(body), int64(len(body))-1, &into)
		if !errors.Is(err, fault.ErrParse) {
			t.Fatalf("error = %v, want a parse fault", err)
		}
		if !strings.Contains(err.Error(), "exceeds the limit") {
			t.Errorf("message %q should name the limit", err)
		}
	})
}
