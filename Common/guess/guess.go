// Package guess turns a command nobody has into the command they meant.
//
// It exists because every Orc tool answers an unrecognised verb, and the useful
// answer to `orc statsu` is `status` rather than a list of twenty-five verbs to
// read. One copy, because five copies of a distance function would drift in their
// thresholds and the tools would disagree about how close a guess has to be.
//
// Nothing here is clever. A typo is a transposition, a doubled letter, a missing
// one, or a half-typed word, and edit distance with a prefix rule covers all four.
package guess

import "strings"

// Nearest is the candidate the caller probably meant, or "" if none is close
// enough to offer.
//
// Silence is a real answer and the common one: `orc frobnicate` resembles nothing,
// and guessing `fire` at it would be worse than saying nothing. A suggestion is
// only made when it is unambiguously the best — two candidates equally close means
// the tool does not actually know, and says so by not guessing.
func Nearest(typed string, candidates []string) string {
	// The comparison is case-insensitive but the exclusion is not: `DOCTOR` is a
	// command the tool really did not recognise, and `doctor` is exactly the
	// suggestion its author wants. Only a candidate the caller typed *verbatim*
	// is skipped.
	raw := strings.TrimSpace(typed)
	typed = strings.ToLower(raw)
	if typed == "" || len(candidates) == 0 {
		return ""
	}

	// A prefix wins outright, and beats any distance: somebody who typed `stat`
	// meant `status`, however many edits away that is.
	var prefix string
	for _, c := range candidates {
		if strings.HasPrefix(strings.ToLower(c), typed) && c != raw {
			if prefix != "" {
				return "" // more than one; the tool does not know which
			}
			prefix = c
		}
	}
	if prefix != "" {
		return prefix
	}

	// Otherwise the nearest by edit distance, within a budget that scales with
	// the word: two edits in `env` is a different word, and in `check-control`
	// it is a typo.
	budget := len(typed) / 3
	if budget < 2 {
		budget = 2
	}
	best, bestAt, tied := "", budget+1, false
	for _, c := range candidates {
		if c == raw {
			// The caller only asks about a word it did not recognise, so a
			// candidate it typed exactly is a disagreement about the command
			// list — not something to echo back as a suggestion.
			continue
		}
		d := distance(typed, strings.ToLower(c))
		switch {
		case d < bestAt:
			best, bestAt, tied = c, d, false
		case d == bestAt:
			tied = true
		}
	}
	if bestAt > budget || tied {
		return ""
	}
	return best
}

// distance is Levenshtein, with the two-row trick so the whole table is never
// held. The words are command names, so the sizes are tiny and this is a
// readability choice rather than a performance one.
func distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	if len(x) == 0 {
		return len(y)
	}

	previous := make([]int, len(x)+1)
	current := make([]int, len(x)+1)
	for i := range previous {
		previous[i] = i
	}

	for j := 1; j <= len(y); j++ {
		current[0] = j
		for i := 1; i <= len(x); i++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			current[i] = min(previous[i]+1, current[i-1]+1, previous[i-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(x)]
}
