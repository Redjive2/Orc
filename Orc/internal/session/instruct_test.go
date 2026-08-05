package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
)

// supervisorFor builds one without running it. Args() is a pure question about a
// composed session, so nothing here needs a pty, a socket, or a child.
func supervisorFor(t *testing.T, s *store.Store, who user.Name) *session.Supervisor {
	t.Helper()

	sup, err := session.New(s, session.Spec{
		Identity: who, ID: "0000000a-00000001",
		Model: model.ModelSonnet, Effort: model.EffortMedium,
	}, nil, "claude")
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	return sup
}

// writeRaw puts a file in the store behind its own API, for the cases that are
// about a file somebody edited by hand.
func writeRaw(t *testing.T, s *store.Store, rel, body string) {
	t.Helper()

	path := filepath.Join(s.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Delivery: the composed prompt reaches the session as `--append-system-prompt`.
//
// The flag was checked against the real `claude` before this was built
// (Instruct.md §5, §14): 2.1.220 documents it, and an unknown flag errors, so
// acceptance means it is parsed. What is tested here is that Orc passes it and with
// what.

// A fleet that has set nothing still passes one thing: the house writing rule.
//
// This used to assert the opposite — no prompts, no flag — and that was right while
// every layer was optional. The writing rule is not a layer. A fleet where half the
// agents write one way produces documents that read as though several people wrote
// them, so nobody sets it and nobody can turn it off.
func TestAFleetWithNoPromptsStillPassesTheHouseRule(t *testing.T) {
	s, who := fleet(t, "ember")
	sup := supervisorFor(t, s, who)

	args := sup.Args()
	at := -1
	for i, arg := range args {
		if arg == "--append-system-prompt" {
			at = i
		}
	}
	if at < 0 || at+1 >= len(args) {
		t.Fatalf("a fleet with no prompts passed no instructions at all: %v", args)
	}
	got := args[at+1]
	if !strings.Contains(got, "ASD-STE100") {
		t.Errorf("the writing rule did not reach the session: %q", got)
	}
	// And nothing else, because nothing else was set.
	if strings.Contains(got, "# the fleet") || strings.Contains(got, "# the ") {
		t.Errorf("a fleet that set nothing passed a layer heading: %q", got)
	}
}

// TestTheComposedPromptIsPassed, layers and all.
func TestTheComposedPromptReachesTheSession(t *testing.T) {
	s, who := fleet(t, "ember")

	if err := s.WritePrompt(store.FleetPrompt(false), who, "ask before you guess"); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePrompt(store.IdentityPrompt(who, false), who, "you are covering the parser"); err != nil {
		t.Fatal(err)
	}

	args := supervisorFor(t, s, who).Args()

	got := ""
	for i, arg := range args {
		if arg == "--append-system-prompt" && i+1 < len(args) {
			got = args[i+1]
		}
	}
	if got == "" {
		t.Fatalf("the prompt was not passed: %v", args)
	}
	for _, want := range []string{"ask before you guess", "you are covering the parser", "# the fleet"} {
		if !strings.Contains(got, want) {
			t.Errorf("the composed prompt lacks %q:\n%s", want, got)
		}
	}
}

// TestARestartDeliversWhatIsSetNow.
//
// This used to assert the opposite — that a prompt was composed once and a restart
// continued under the instructions the conversation had been following. The
// reasoning was that changing them underneath a live conversation is a surprise.
//
// It was wrong about the thing that decides the question: Claude does not keep a
// system prompt in a session's transcript, so a resumed session is built from the
// flags of whatever invocation resumed it. Composing once therefore did not "keep
// the conversation's instructions" — it re-delivered a stale copy of them on every
// restart, for as long as the supervisor lived, with no way to see that from
// outside. An operator who edits a prompt and watches an agent restart expects the
// edit to be what restarted.
//
// The still-running turn is untouched either way: this is about what the next start
// carries.
func TestARestartComposesTheInstructionsAgain(t *testing.T) {
	s, who := fleet(t, "ember")
	if err := s.WritePrompt(store.FleetPrompt(false), who, "the original"); err != nil {
		t.Fatal(err)
	}

	sup := supervisorFor(t, s, who)
	if before := strings.Join(sup.Args(), " "); !strings.Contains(before, "the original") {
		t.Fatalf("the first composition is missing:\n%s", before)
	}

	if err := s.WritePrompt(store.FleetPrompt(false), who, "edited while it was running"); err != nil {
		t.Fatal(err)
	}

	// What the *next* start would carry, which is what `once` composes.
	got, err := session.ComposeFor(s, who)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "edited while it was running") {
		t.Errorf("a restart would deliver the old instructions:\n%s", got)
	}
}

// A prompt file too large to have been written is refused when it is read, and the
// session still starts — an agent that cannot think is worse than one missing a
// layer somebody added.
func TestABrokenPromptDoesNotStopTheSession(t *testing.T) {
	s, who := fleet(t, "ember")
	writeRaw(t, s, "prompts/system.md", strings.Repeat("x", instruct.MaxLayer+1))

	sup := supervisorFor(t, s, who)
	args := sup.Args()

	for _, arg := range args {
		if arg == "--append-system-prompt" {
			t.Errorf("an unreadable prompt was passed anyway: %v", args)
		}
	}
	// And the session is still a session.
	if !strings.Contains(strings.Join(args, " "), "--session-id") {
		t.Errorf("the session lost its arguments with its prompt: %v", args)
	}
}

// TestTheSessionRecordsWhatItWasStartedWith.
//
// The failure this exists for is silent by construction: an agent that never
// received an instruction behaves exactly like one that received it and chose
// otherwise. Recording it is what makes "were they sent?" answerable without
// reading a running process's command line.
func TestComposeForReportsWhatWouldBeDelivered(t *testing.T) {
	s, who := fleet(t, "ember")

	// Nothing set composes to the house writing rule, which every agent receives
	// whether or not the fleet has ever used the feature.
	got, err := session.ComposeFor(s, who)
	if err != nil {
		t.Fatalf("an unconfigured fleet failed to compose: %v", err)
	}
	if !strings.Contains(got, "ASD-STE100") {
		t.Fatalf("an unconfigured fleet composed %q", got)
	}
	if instruct.Beyond(got) != 0 {
		t.Fatalf("an unconfigured fleet composed a layer of its own: %q", got)
	}

	if err := s.WritePrompt(store.FleetPrompt(false), who, "ask before you guess"); err != nil {
		t.Fatal(err)
	}
	got, err = session.ComposeFor(s, who)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "ask before you guess") {
		t.Errorf("the fleet's layer is not in what would be delivered:\n%s", got)
	}
}

// An identity that cannot be read is an error rather than an identity with no role.
// Swallowing it made the role's layer vanish from the composition silently — an
// agent missing a third of its instructions with nothing anywhere saying so.
func TestComposeRefusesAnIdentityItCannotRead(t *testing.T) {
	s, _ := fleet(t, "ember")
	stranger, err := user.Parse("nobody")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := session.ComposeFor(s, stranger); err == nil {
		t.Error("composing for an identity that does not exist did not say so")
	}
}

// TestARestartIsResumedNotReMinted.
//
// `--session-id` mints a session with an id Orc chose; `--resume` continues one that
// exists. Passing both is refused at argument parsing — so the child died instantly,
// the supervisor restarted it, and it died again until the restart budget was spent.
// From outside that is an agent that will not start, with the reason visible only by
// attaching to it, which is the thing an unattended fleet cannot do.
func TestResumeAndSessionIDAreNeverBothPassed(t *testing.T) {
	s, who := fleet(t, "ember")

	fresh := supervisorFor(t, s, who).Args()
	joined := strings.Join(fresh, " ")
	if !strings.Contains(joined, "--session-id") {
		t.Errorf("a new session does not mint an id: %v", fresh)
	}
	if strings.Contains(joined, "--resume") {
		t.Errorf("a new session tried to resume: %v", fresh)
	}

	sup, err := session.New(s, session.Spec{
		Identity: who, ID: "0000000a-00000001", Resume: true,
		Model: model.ModelSonnet, Effort: model.EffortMedium,
	}, nil, "claude")
	if err != nil {
		t.Fatal(err)
	}
	resumed := sup.Args()
	joined = strings.Join(resumed, " ")
	if !strings.Contains(joined, "--resume") {
		t.Errorf("a restart does not resume: %v", resumed)
	}
	if strings.Contains(joined, "--session-id") {
		t.Errorf("a restart passed both, which claude refuses outright: %v", resumed)
	}
}

// TestASessionThatWentMidTurnSaysSo.
//
// A session that ended *waiting* had finished its turn: resuming it is enough. One
// that ended part-way through a turn was interrupted — the model call it was inside
// never came back — so resuming it alone leaves an agent sitting silently on an
// unfinished thought. That is what a fleet looks like from outside when it stops and
// does not come back, and it is the state a usage limit reached mid-turn leaves.
func TestTheEndingRecordsWhetherItWasMidTurn(t *testing.T) {
	for _, tc := range []struct {
		what    string
		feed    string
		midTurn bool
	}{
		{
			what:    "a turn that had finished",
			feed:    `{"at":"2026-07-26T12:00:00.000Z","session":"s","event":"Stop"}`,
			midTurn: false,
		},
		{
			what:    "a tool call that never returned",
			feed:    `{"at":"2026-07-26T12:00:00.000Z","session":"s","event":"PreToolUse","tool":"Bash","path":"go test ./..."}`,
			midTurn: true,
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			s, who := fleet(t, "ember")
			writeRaw(t, s, "identities/ember/session/events.jsonl", tc.feed+"\n")

			sup := supervisorFor(t, s, who)
			session.RecordEndingFor(sup)

			got, ok := s.LastEnded(who)
			if !ok {
				t.Fatal("nothing was remembered about the session")
			}
			if got.MidTurn != tc.midTurn {
				t.Errorf("mid-turn = %v, want %v", got.MidTurn, tc.midTurn)
			}
			if got.Session == "" {
				t.Error("the record names no session to resume")
			}
		})
	}
}
