package guess_test

import (
	"testing"

	"orc/common/guess"
)

func TestNearest(t *testing.T) {
	orc := []string{"bootstrap", "new", "assign", "remove", "grant", "revoke", "move",
		"status", "introspect", "check-control", "env", "verify", "owner", "employ",
		"fire", "tend", "attach", "poke", "refresh", "doctor", "help"}

	for _, tc := range []struct {
		typed, want, why string
	}{
		{"statsu", "status", "a transposition"},
		{"stat", "status", "a half-typed word"},
		{"emplyo", "employ", "a transposition in a longer word"},
		{"chek-control", "check-control", "a missing letter"},
		{"DOCTOR", "doctor", "case is not a typo worth refusing over"},
		{"frobnicate", "", "nothing resembles it, so nothing is offered"},
		{"", "", "no input, no guess"},
		{"e", "", "one letter is ambiguous between env and employ"},
	} {
		if got := guess.Nearest(tc.typed, orc); got != tc.want {
			t.Errorf("Nearest(%q) = %q, want %q — %s", tc.typed, got, tc.want, tc.why)
		}
	}

	if got := guess.Nearest("status", nil); got != "" {
		t.Errorf("with no candidates: %q", got)
	}
}

// TestNearestWillNotGuessBetweenTwo: a tool that does not know must say nothing
// rather than pick. Suggesting the wrong one of two equally close verbs is worse
// than suggesting none, because it is followed.
func TestNearestWillNotGuessBetweenTwo(t *testing.T) {
	if got := guess.Nearest("fare", []string{"fire", "care"}); got != "" {
		t.Errorf("it guessed %q between equally close candidates", got)
	}
	// A prefix is the exception, and deliberately beats distance: somebody who
	// typed half a word meant that word, however many edits away it is.
	if got := guess.Nearest("fare", []string{"fire", "fare-well", "care"}); got != "fare-well" {
		t.Errorf("a prefix should win outright, got %q", got)
	}
	// An exact match is not a suggestion either: the caller only asks when the
	// command was not recognised, and echoing it back would be nonsense.
	if got := guess.Nearest("fire", []string{"fire"}); got != "" {
		t.Errorf("it suggested the word itself: %q", got)
	}
}
