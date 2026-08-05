// Package prose measures writing against the house rule.
//
// The rule is two things, and they are enforced differently because they are
// different kinds of statement.
//
//   - **Some words are never used.** A ban is exact: a word is there or it is not,
//     and one occurrence fails the text. No score, no proportion, no argument.
//   - **Most sentences follow ASD-STE100.** Simplified Technical English is a
//     standard of about sixty writing rules and a dictionary of approved words. It
//     was written so that a maintenance manual reads the same way to every reader,
//     and most of what makes it work is that sentences are short, active, and say
//     one thing.
//
// What this package can measure, and what it cannot, must be clear to anybody who
// reads a score it prints.
//
// It measures the mechanical rules: sentence length, passive voice, and strings of
// subordinate clauses. These need no dictionary and no parser, and they are the
// rules that carry most of STE's benefit. It does **not** check the approved
// vocabulary, which is the other half of the standard and needs the dictionary; nor
// noun clusters, which need to know which words are nouns.
//
// So a score here is a measure of the checkable rules and not a certificate of
// STE100 conformance. The doc comment says so, the command says so, and the name of
// the score says so. A tool that let a reader believe otherwise would be worse than
// no tool: it would put a number on a claim nobody had checked.
package prose

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Threshold is the share of sentences that must break no checkable rule.
//
// Eighty per cent, as the house rule states it. It is a proportion rather than a
// requirement on every sentence because prose has sentences that need the length —
// a definition, a list read as a line — and a rule that failed those would be a rule
// people write around rather than follow.
const Threshold = 0.80

// MaxWords is the longest a sentence may be.
//
// STE100 gives twenty words for a procedure and twenty-five for a description. The
// looser of the two, because this measures prose about software rather than steps to
// carry out, and the tighter number would fail writing the standard permits.
const MaxWords = 25

// MaxClauses is how many subordinate clauses a sentence may carry.
//
// One is ordinary. Two is a sentence holding two thoughts, which STE splits, and it
// is the shape that makes technical writing hard to read for somebody who is tired
// or reading in a second language.
const MaxClauses = 2

// Banned are the words the house rule forbids outright.
//
// Each is banned for the same reason: it claims something about the writing rather
// than saying the thing. "Honestly" implies the rest was not, "caveat" is a word for
// a reservation the writer could simply state, and the other two are decoration
// asserting importance instead of showing it.
var Banned = []string{"honest", "honestly", "caveat", "caveats", "genuine", "genuinely", "load-bearing", "load bearing"}

// Report is what a text came to.
type Report struct {
	// Sentences is how many were found, and Clean how many broke no rule.
	Sentences int
	Clean     int
	// Findings are every rule break, in the order they appear.
	Findings []Finding
}

// Score is the share of sentences that broke no checkable rule. A text with no
// sentences scores 1: there is nothing in it to be wrong.
func (r Report) Score() float64 {
	if r.Sentences == 0 {
		return 1
	}
	return float64(r.Clean) / float64(r.Sentences)
}

// Banned reports whether any forbidden word was found. It is separate from the score
// because it is not a proportion: one is too many.
func (r Report) Banned() bool {
	for _, f := range r.Findings {
		if f.Rule == RuleBanned {
			return true
		}
	}
	return false
}

// OK reports whether the text passes both halves of the rule.
func (r Report) OK() bool { return !r.Banned() && r.Score() >= Threshold }

// Rule names what was broken.
type Rule string

// The rules, as they are printed.
const (
	RuleBanned  Rule = "banned word"
	RuleLength  Rule = "long sentence"
	RulePassive Rule = "passive voice"
	RuleClauses Rule = "stacked clauses"
)

// Finding is one rule broken in one place.
type Finding struct {
	Rule Rule
	// Line is 1-indexed, so a report can be read beside the file.
	Line int
	// Detail says what was found, in the words a writer would use to fix it.
	Detail string
	// Text is the sentence, shortened for a terminal.
	Text string
}

// Check measures a text.
func Check(text string) Report {
	report := Report{}

	// The banned words first, and over the whole text rather than per sentence: one
	// may appear in a heading, a list item, or a fragment that is not a sentence at
	// all, and a ban that only looked inside sentences would let those through.
	for line, raw := range strings.Split(text, "\n") {
		for _, word := range bannedIn(withoutCode(raw)) {
			report.Findings = append(report.Findings, Finding{
				Rule: RuleBanned, Line: line + 1,
				Detail: fmt.Sprintf("%q is never used here", word),
				Text:   shorten(strings.TrimSpace(raw)),
			})
		}
	}

	for _, s := range sentences(text) {
		report.Sentences++
		found := s.check()
		if len(found) == 0 {
			report.Clean++
			continue
		}
		report.Findings = append(report.Findings, found...)
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		return report.Findings[i].Line < report.Findings[j].Line
	})
	return report
}

// bannedIn returns every forbidden word in one line, in the order they appear.
func bannedIn(line string) []string {
	lower := strings.ToLower(line)
	var found []string
	for _, word := range Banned {
		for range bannedPattern(word).FindAllString(lower, -1) {
			found = append(found, word)
		}
	}
	return found
}

// patterns are compiled once. A checker run over a repository looks at a great many
// lines, and recompiling eight expressions for each of them is the whole cost.
var patterns = map[string]*regexp.Regexp{}

func bannedPattern(word string) *regexp.Regexp {
	if got, ok := patterns[word]; ok {
		return got
	}
	// Word boundaries, so "honest" does not match inside "dishonest" and — more to
	// the point — so a ban on "genuine" does not fire on a file called
	// `genuine_test.go` mentioned in passing. A hyphen is not a word character to
	// Go's \b, which is what makes "load-bearing" match as written.
	got := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	patterns[word] = got
	return got
}

// sentence is one sentence and where it came from.
type sentence struct {
	text string
	line int
}

// check returns everything wrong with one sentence.
func (s sentence) check() []Finding {
	var found []Finding
	at := func(rule Rule, detail string) {
		found = append(found, Finding{Rule: rule, Line: s.line, Detail: detail, Text: shorten(s.text)})
	}

	if n := len(strings.Fields(s.text)); n > MaxWords {
		at(RuleLength, fmt.Sprintf("%d words; %d is the limit", n, MaxWords))
	}
	if verb := passiveIn(s.text); verb != "" {
		at(RulePassive, fmt.Sprintf("%q — say who does it", verb))
	}
	if n := clausesIn(s.text); n > MaxClauses {
		at(RuleClauses, fmt.Sprintf("%d subordinate clauses; %d is the limit", n, MaxClauses))
	}
	return found
}

// passive finds "is written", "was decided", "has been read" and their kin.
//
// A form of "to be" followed by a past participle. Participles are recognised by
// their ending plus a list of the common irregular ones, which is an approximation
// and is meant to be: a parser would be a great deal of code to catch the handful of
// cases this misses, and the score is a measure rather than a proof.
var passive = regexp.MustCompile(`\b(?:is|are|was|were|be|been|being)\s+(?:\w+ly\s+)?(\w+(?:ed|en)|` +
	strings.Join(irregular, "|") + `)\b`)

// irregular are past participles that do not end in -ed or -en.
var irregular = []string{"made", "done", "sent", "kept", "held", "read", "built", "brought",
	"caught", "dealt", "felt", "found", "left", "lost", "meant", "met", "paid", "put",
	"said", "set", "sold", "told", "thought", "understood"}

func passiveIn(text string) string {
	got := passive.FindString(strings.ToLower(text))
	return strings.Join(strings.Fields(got), " ")
}

// clauses counts the subordinating words that start a clause.
//
// A comma is not counted: it separates lists as often as clauses, and counting it
// would fail every sentence with three items in it. The words below are the ones
// that reliably begin a subordinate clause in technical prose.
var subordinators = regexp.MustCompile(`\b(?:which|because|although|though|whereas|` +
	`unless|whether|while|since|so that|in order to|rather than|as long as)\b`)

func clausesIn(text string) int {
	return len(subordinators.FindAllString(strings.ToLower(text), -1))
}

// sentences splits a text, keeping the line each one started on.
//
// Markdown structure is skipped rather than measured. A heading is not a sentence, a
// fenced code block is not prose, and a table row is a row — scoring those would put
// most of a document's failures in places where the rule was never meant to apply,
// which is how a checker teaches people to ignore it.
func sentences(text string) []sentence {
	var out []sentence
	fenced := false

	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~"):
			fenced = !fenced
			continue
		case fenced, line == "":
			continue
		case strings.HasPrefix(line, "#"), strings.HasPrefix(line, "|"),
			strings.HasPrefix(line, ">"), strings.HasPrefix(line, "    "):
			continue
		}
		// A list marker is dropped and what follows it is measured: the item is
		// prose even when the bullet is not.
		line = listItem.ReplaceAllString(line, "")
		line = withoutCode(line)

		for _, part := range split(line) {
			part = strings.TrimSpace(part)
			if len(strings.Fields(part)) < 3 {
				// Fewer than three words is a fragment — a label, a cell, half a
				// heading — and every rule here is about sentences.
				continue
			}
			out = append(out, sentence{text: part, line: i + 1})
		}
	}
	return out
}

var listItem = regexp.MustCompile(`^(?:[-*+]|\d+\.)\s+`)

// code is an inline span between backticks.
var code = regexp.MustCompile("`[^`]*`")

// withoutCode replaces inline code with a single word.
//
// What is inside backticks is quoted rather than written. A document that explains a
// rule has to *show* what breaks it — the words it bans, an example of the passive
// voice it rejects — and scoring those would mean the clearest way to state a rule is
// the way that fails it. This file's own house rule was the first text to hit that.
//
// One word rather than nothing, so a sentence carrying six commands is still measured
// as a sentence with six things in it.
func withoutCode(line string) string { return code.ReplaceAllString(line, "code") }

// split breaks a line into sentences at terminal punctuation.
//
// Abbreviations are handled by requiring the following character to be a space and
// the punctuation not to be preceded by a single letter — which keeps "e.g." and an
// initial from ending a sentence. It is not complete, and it does not need to be:
// mis-splitting one sentence in a document changes a score by a fraction of a
// percent.
var breaks = regexp.MustCompile(`(?:[.!?])\s+`)

func split(line string) []string {
	var out []string
	rest := line
	for {
		at := breaks.FindStringIndex(rest)
		if at == nil {
			break
		}
		head, tail := rest[:at[1]], rest[at[1]:]
		if abbreviation(head) {
			// Not a sentence end: glue it to what follows and look again past it.
			next := breaks.FindStringIndex(tail)
			if next == nil {
				break
			}
			out = append(out, head+tail[:next[1]])
			rest = tail[next[1]:]
			continue
		}
		out = append(out, head)
		rest = tail
	}
	return append(out, rest)
}

// abbreviation reports whether a fragment ends in one, which is the common reason a
// full stop is not the end of a sentence.
func abbreviation(head string) bool {
	trimmed := strings.TrimRight(head, " \t")
	if len(trimmed) < 2 {
		return false
	}
	body := strings.TrimRight(trimmed, ".!?")
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	// A single letter is an initial; a short word with a dot inside it is `e.g.`.
	return len(last) == 1 || strings.Contains(last, ".")
}

// shorten trims a sentence to something a terminal can show on one line.
func shorten(s string) string {
	const width = 72
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= width {
		return s
	}
	return s[:width-1] + "…"
}
