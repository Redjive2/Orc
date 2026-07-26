package model

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"orc/common/fault"
)

// Kind is what a pattern governs.
//
// Auth_Perm_Role.md names three builtins — read(path list), write(path list),
// and spawn(agent load) — and says a permission "can include any number of
// specific commands or command patterns". KindOrc is that fourth case: a pattern
// over Orc's own verbs, so a role can be allowed to create identities without
// being allowed to hand out authority.
type Kind uint8

// The kinds, in the order they render. Read before write before spawn is the
// order of increasing consequence, which is the order somebody scanning a card
// wants them in.
const (
	KindUnset Kind = iota
	KindRead
	KindWrite
	KindSpawn
	KindOrc
	// KindShell is which shell commands an agent may run.
	//
	// It is the only kind that is *deny by default*: an identity with no shell
	// clause may still run the handful of commands in Innocuous, and nothing
	// else. Every other kind narrows something agents could otherwise do freely;
	// this one is the reverse, because a shell is every capability at once, and
	// "everything except what somebody thought to forbid" is not a policy.
	//
	// Terms are command names as typed — `shell(ls cat)` — matched against the
	// base name of what a command line actually runs, so `/bin/ls` is `ls`. That
	// is a shape match on an undecidable thing; hook.Commands says exactly what
	// it can and cannot see.
	KindShell
	// KindTool is a named capability in another Orc tool.
	//
	// It exists because containment is by *clause*, not by name — see Contains and
	// Fleet.Holds — and that is right for everything above it: an identity that may
	// write everything may hand on a permission to write one directory. It is wrong
	// for a capability whose meaning is "may run this privileged action", because
	// there is no path glob that honestly describes it and any glob broad enough to
	// describe it would confer it by accident.
	//
	// So `tool(upgrade)` is covered by `tool(upgrade)` and by nothing else. A role
	// with `write(**)` does not get it, which is the whole point: `upgrade` sits at
	// floor 90, and a permission that a floor-70 role could reach through coverage
	// would have no floor at all.
	KindTool
)

// String returns the kind as it is written and stored.
func (k Kind) String() string {
	switch k {
	case KindRead:
		return "read"
	case KindWrite:
		return "write"
	case KindSpawn:
		return "spawn"
	case KindOrc:
		return "orc"
	case KindShell:
		return "shell"
	case KindTool:
		return "tool"
	default:
		return "unset"
	}
}

// Valid reports whether the kind is one this build knows.
func (k Kind) Valid() bool { return k >= KindRead && k <= KindTool }

// ParseKind reads a kind name.
func ParseKind(raw string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "read":
		return KindRead, nil
	case "write":
		return KindWrite, nil
	case "spawn":
		return KindSpawn, nil
	case "orc":
		return KindOrc, nil
	case "shell":
		return KindShell, nil
	case "tool":
		return KindTool, nil
	default:
		return KindUnset, fault.Usage{Reason: fmt.Sprintf(
			"unknown permission kind %q; try read, write, spawn, orc, shell, or tool", raw)}
	}
}

// Kinds lists every kind, for help and for tests that must be total.
func Kinds() []Kind {
	return []Kind{KindRead, KindWrite, KindSpawn, KindOrc, KindShell, KindTool}
}

// Pattern is one clause of a permission: a kind and what it applies to.
//
// The zero value is not usable; construct one with ParsePattern. Patterns are
// values, so two permissions that say the same thing compare equal and a
// rendered list is stable.
type Pattern struct {
	kind Kind
	arg  string
	load int // KindSpawn only: the budget, in the units of §6.4 of the plan
}

// MaxLoad bounds a spawn budget. The heaviest single session is 18 (opus at max
// effort), so this is a fleet far larger than one machine runs — it exists to
// turn a typo into a message rather than an unbounded fleet.
const MaxLoad = 4096

// Except separates what a clause allows from what it takes back out.
//
// A bare word rather than punctuation because a clause is read aloud more often
// than it is typed — `write(** except Docs/**)` says what it does, and
// `write(**,-Docs/**)` has to be decoded. A path that is genuinely called
// `except` is written `./except`, which cleanGlob keeps.
const Except = "except"

// ParsePattern reads a pattern as it is written on the command line and stored.
//
//	read(Anno/**)                       one thing
//	read(Anno/** Dock/**)               several, space separated
//	write(** except Docs/** .git/**)    everything but these
//	orc(new assign)                     two verbs
//	orc(** except remove)               every verb but one
//	spawn(24)                           a budget, which is none of the above
//
// Parentheses are required. A bare `read` would have to mean either "read
// everything" or "read nothing", and a permission whose meaning depends on
// guessing which is one nobody can audit.
//
// Three shapes, one grammar: a list of what is allowed, optionally followed by
// `except` and a list of what is not. Every kind but `spawn` takes it, and every
// term of every kind is a glob — `orc(re*)` and `tool(**)` are patterns in the
// same sense `read(Anno/**)` is. A budget is the exception because it is a
// number: `spawn(24 48)` has no meaning that is not a guess, so it is refused
// rather than resolved.
//
// The result is canonical: terms are sorted and the two lists are rendered in one
// fixed form, so two people who typed the same set in different orders wrote the
// same permission, and `orc edit permission` given what is already there changes
// nothing.
func ParsePattern(raw string) (Pattern, error) {
	text := strings.TrimSpace(raw)
	open := strings.IndexByte(text, '(')
	if open < 0 || !strings.HasSuffix(text, ")") {
		return Pattern{}, fault.Usage{Reason: fmt.Sprintf(
			"pattern %q must be written kind(argument), as in read(Anno/**) or spawn(24)", raw)}
	}

	kind, err := ParseKind(text[:open])
	if err != nil {
		return Pattern{}, err
	}
	body := strings.TrimSpace(text[open+1 : len(text)-1])
	if body == "" {
		return Pattern{}, fault.Usage{Reason: fmt.Sprintf("pattern %q has an empty argument", raw)}
	}

	if kind == KindSpawn {
		load, err := parseInt(body)
		if err != nil || load > MaxLoad {
			return Pattern{}, fault.Usage{Reason: fmt.Sprintf(
				"spawn takes one load budget from 0 to %d, not %q; a budget is a number, "+
					"so it has no list and no %s", MaxLoad, body, Except)}
		}
		return Pattern{kind: kind, arg: itoa(load), load: load}, nil
	}

	allow, deny, err := splitExcept(body, raw)
	if err != nil {
		return Pattern{}, err
	}
	if allow, err = cleanTerms(kind, allow, raw); err != nil {
		return Pattern{}, err
	}
	if deny, err = cleanTerms(kind, deny, raw); err != nil {
		return Pattern{}, err
	}
	return Pattern{kind: kind, arg: joinBody(allow, deny)}, nil
}

// splitExcept divides a clause body at the `except` keyword.
//
// One `except` at most, and neither side may be empty: `read(except a/**)` allows
// nothing at all and `read(a/** except)` is a sentence somebody did not finish.
// Both are far more likely to be a typo than an intention, and a permission that
// silently means nothing is the worst of the three outcomes.
func splitExcept(body, raw string) (allow, deny []string, err error) {
	fields := strings.Fields(body)
	at := -1
	for i, f := range fields {
		if !strings.EqualFold(f, Except) {
			continue
		}
		if at >= 0 {
			return nil, nil, fault.Usage{Reason: fmt.Sprintf(
				"pattern %q says %s twice; one list is taken out, so there is one %s",
				raw, Except, Except)}
		}
		at = i
	}
	if at < 0 {
		return fields, nil, nil
	}
	allow, deny = fields[:at], fields[at+1:]
	if len(allow) == 0 {
		return nil, nil, fault.Usage{Reason: fmt.Sprintf(
			"pattern %q starts with %s, so it allows nothing; say what it allows first, "+
				"as in read(** %s .git/**)", raw, Except, Except)}
	}
	if len(deny) == 0 {
		return nil, nil, fault.Usage{Reason: fmt.Sprintf(
			"pattern %q ends with %s and takes nothing out", raw, Except)}
	}
	return allow, deny, nil
}

// cleanTerms normalises every term of one list and refuses a repeat.
//
// A repeat is an error rather than a quiet de-duplication for the reason
// ParsePatterns gives: the two spellings may not have been meant to be the same
// thing, and a clause is short enough to fix. It says both spellings, because
// `Anno/` and `Anno/**` become the same term and somebody who wrote them has to
// be told why they collided.
func cleanTerms(kind Kind, terms []string, raw string) ([]string, error) {
	out := make([]string, 0, len(terms))
	spelt := make([]string, 0, len(terms))
	for i, term := range terms {
		clean, err := cleanTerm(kind, term)
		if err != nil {
			return nil, err
		}
		for j, seen := range out {
			if seen != clean {
				continue
			}
			if spelt[j] == term {
				return nil, fault.Usage{Reason: fmt.Sprintf("pattern %q repeats %s", raw, term)}
			}
			return nil, fault.Usage{Reason: fmt.Sprintf(
				"pattern %q says %s and %s, which are the same thing", raw, spelt[j], term)}
		}
		out = append(out, clean)
		spelt = append(spelt, terms[i])
	}
	slices.Sort(out)
	return out, nil
}

// cleanTerm normalises one term for its kind.
func cleanTerm(kind Kind, term string) (string, error) {
	switch kind {
	case KindRead, KindWrite:
		return cleanGlob(term)
	default: // KindOrc, KindShell, KindTool
		// A verb and a tool name are single words, so a slash is always a mistake
		// — usually a path clause written under the wrong kind. Wildcards are not:
		// `orc(re*)` is a pattern over verbs exactly as `read(Anno/*)` is a pattern
		// over paths, and `**` means all of them.
		if strings.Contains(term, "/") {
			return "", fault.Usage{Reason: fmt.Sprintf(
				"%s(%s) names verbs, not paths, so a term cannot contain a slash", kind, term)}
		}
		if strings.ContainsAny(term, "()") {
			return "", fault.Usage{Reason: fmt.Sprintf(
				"%s(%s) has a parenthesis in a term; clauses do not nest", kind, term)}
		}
		return strings.ToLower(term), nil
	}
}

// joinBody renders the two lists in the one canonical form.
func joinBody(allow, deny []string) string {
	body := strings.Join(allow, " ")
	if len(deny) > 0 {
		body += " " + Except + " " + strings.Join(deny, " ")
	}
	return body
}

// includes and excludes split a canonical argument back into its two lists.
//
// They take the argument rather than the pattern so that Contains can ask about
// both sides without either allocating a Pattern, and they cannot fail: the only
// arguments that exist came out of ParsePattern.
func includes(arg string) []string {
	fields := strings.Fields(arg)
	for i, f := range fields {
		if f == Except {
			return fields[:i]
		}
	}
	return fields
}

func excludes(arg string) []string {
	fields := strings.Fields(arg)
	for i, f := range fields {
		if f == Except {
			return fields[i+1:]
		}
	}
	return nil
}

// cleanGlob normalises a path pattern and refuses one that escapes.
//
// Patterns are relative to the workspace, always. An absolute pattern would mean
// something different on every machine, and a `..` segment is how a permission
// over one directory becomes a permission over its parent — so both are refused
// here rather than at each place a pattern is matched.
func cleanGlob(arg string) (string, error) {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") {
		return "", fault.Escape{Path: arg, Root: "the workspace"}
	}
	for _, seg := range strings.Split(arg, "/") {
		if seg == ".." {
			return "", fault.Escape{Path: arg, Root: "the workspace"}
		}
	}

	// The trailing form is kept rather than cleaned away: `Anno/` and `Anno/**`
	// are written by different people meaning the same thing, and both have to
	// survive to the matcher as "everything under Anno".
	trailing := strings.HasSuffix(arg, "/")
	clean := path.Clean(arg)
	if clean == "." {
		return "", fault.Usage{Reason: fmt.Sprintf("path pattern %q selects nothing; use ** for everything", arg)}
	}
	if trailing && !strings.HasSuffix(clean, "/**") {
		clean += "/**"
	}
	return clean, nil
}

// Kind returns what the pattern governs.
func (p Pattern) Kind() Kind { return p.kind }

// Arg returns the pattern's argument as it is stored.
func (p Pattern) Arg() string { return p.arg }

// Terms returns what the pattern allows, one glob per entry. A spawn budget has
// none: it is a number, not a set of things.
func (p Pattern) Terms() []string {
	if p.kind == KindSpawn {
		return nil
	}
	return includes(p.arg)
}

// Excepts returns what the pattern takes back out, which is usually nothing.
func (p Pattern) Excepts() []string {
	if p.kind == KindSpawn {
		return nil
	}
	return excludes(p.arg)
}

// Load returns a spawn budget. It is zero for every other kind, which is safe:
// a caller that asked the wrong kind for a budget gets "no budget" rather than a
// number that means something else.
func (p Pattern) Load() int {
	if p.kind != KindSpawn {
		return 0
	}
	return p.load
}

// Zero reports whether the pattern was never constructed.
func (p Pattern) Zero() bool { return p.kind == KindUnset }

// String renders the pattern in the form ParsePattern reads.
func (p Pattern) String() string {
	if p.Zero() {
		return "unset"
	}
	return p.kind.String() + "(" + p.arg + ")"
}

// Equal reports whether two patterns say the same thing.
func (p Pattern) Equal(other Pattern) bool {
	return p.kind == other.kind && p.arg == other.arg
}

// Compare orders patterns by kind then argument, so a rendered card is stable.
func (p Pattern) Compare(other Pattern) int {
	if p.kind != other.kind {
		if p.kind < other.kind {
			return -1
		}
		return 1
	}
	return strings.Compare(p.arg, other.arg)
}

// Matches reports whether this pattern allows a specific target: a path for read
// and write, a verb for orc, a capability name for tool. A spawn pattern matches
// nothing — it is a budget, and asking it about a path is a caller mistake that
// must not answer yes.
//
// A term allows and an exception takes back: the target has to be named by one of
// the terms and by none of the exceptions. The exception wins, always, which is
// the only order that makes `write(** except .git/**)` mean what it says.
func (p Pattern) Matches(target string) bool {
	var clean string
	// root is the workspace itself, which is a legitimate thing to ask about and
	// not a path any glob is written to name.
	//
	// It has to be told apart here because cleanGlob refuses it: as a *pattern*,
	// `.` selects nothing and saying so is right. As a *target* it is the
	// directory the agent works in, and running the two through one function meant
	// an agent holding `read(**)` could not list its own workspace — the broadest
	// permission there is, refused on the one directory it exists to cover.
	root := false
	switch p.kind {
	case KindRead, KindWrite:
		if isRoot(target) {
			root = true
			break
		}
		got, err := cleanGlob(target)
		if err != nil {
			return false
		}
		clean = got
	case KindOrc, KindShell, KindTool:
		clean = strings.ToLower(strings.TrimSpace(target))
		if clean == "" {
			return false
		}
	default:
		return false
	}

	// The root is matched as zero segments, which is the matcher's own rule rather
	// than a new one: `**` already "matches whatever is left, including nothing",
	// so `**` covers the workspace and `Docs/**` does not. An agent granted one
	// directory still may not list the directory above it.
	match := func(glob string) bool {
		if root {
			return matchSegments(strings.Split(glob, "/"), nil)
		}
		return globMatch(glob, clean)
	}

	for _, ex := range excludes(p.arg) {
		if match(ex) {
			return false // an exception refuses, whatever the terms say
		}
	}
	for _, term := range includes(p.arg) {
		if match(term) {
			return true
		}
	}
	return false
}

// isRoot reports whether a target names the workspace itself.
//
// `.` is what filepath.Rel gives for the workspace against itself, and the empty
// string is what a caller that trimmed it produces. Both mean the same directory.
func isRoot(target string) bool {
	t := strings.TrimSpace(target)
	return t == "" || t == "." || t == "./"
}

// Contains reports whether this pattern is provably at least as wide as other.
//
// It is the decision the whole permission model rests on, because the tree caps
// a subagent at its boss (see internal/authz): a child's clause survives only if
// the boss's clause provably covers it. Deciding glob containment in general is
// not tractable, so this is a *conservative* procedure — it answers yes only
// when it can prove yes, and the caller treats an unproven pair as no.
//
// The consequence is stated where it lands rather than left implicit: an
// unusual pair of overlapping globs loses the child's clause rather than keeping
// a permission Orc cannot justify. Failing closed is the only direction that
// keeps "a subagent can only have as high a permission as their boss" true.
//
// Exceptions are the same rule pointed the other way. This pattern is wider only
// if every one of its own exceptions is already out of other's reach — either
// other excludes it too, or it cannot touch anything other allows. Anything less
// certain than that is not proof, so it is a no.
func (p Pattern) Contains(other Pattern) bool {
	if p.kind != other.kind || p.Zero() {
		return false
	}
	if p.arg == other.arg {
		return true
	}

	if p.kind == KindSpawn {
		// A budget is a number, so containment is exact rather than
		// conservative: more load covers less.
		return p.load >= other.load
	}

	mine, theirs := includes(p.arg), includes(other.arg)
	for _, want := range theirs {
		if !slices.ContainsFunc(mine, func(have string) bool { return globContains(have, want) }) {
			return false
		}
	}

	for _, ex := range excludes(p.arg) {
		if slices.ContainsFunc(excludes(other.arg), func(theirs string) bool { return globContains(theirs, ex) }) {
			continue // other takes the same thing out, or more of it
		}
		if slices.ContainsFunc(theirs, func(term string) bool { return !disjoint(ex, term) }) {
			return false // it might bite into something other allows
		}
	}
	return true
}

// globContains reports whether one glob is provably at least as wide as another.
// It is Contains for a single term, and the same conservative bargain applies.
func globContains(wide, narrow string) bool {
	if wide == narrow || wide == "**" {
		return true
	}
	// A literal target is decided by matching it directly.
	if !hasWildcard(narrow) {
		return globMatch(wide, narrow)
	}
	// A prefix pattern covers anything whose fixed leading path is inside it.
	if prefix, ok := strings.CutSuffix(wide, "/**"); ok {
		static := staticPrefix(narrow)
		return static == prefix || strings.HasPrefix(static, prefix+"/")
	}
	return false
}

// disjoint reports whether two globs provably share nothing.
//
// It exists for exceptions, and it is why `read(** except .git/**)` can still
// cover `read(Anno/**)`: `.git/**` and `Anno/**` diverge at their first fixed
// segment, so no path is in both, so taking one out cannot narrow the other.
//
// Conservative in the safe direction: unproven is "they might overlap", which
// costs a containment somebody may have to write out longhand, where the other
// answer would quietly keep a clause the boss had taken away.
func disjoint(a, b string) bool {
	left, right := strings.Split(staticPrefix(a), "/"), strings.Split(staticPrefix(b), "/")
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == "" || right[i] == "" {
			return false
		}
		if left[i] != right[i] {
			return true
		}
	}
	return false
}

// hasWildcard reports whether a pattern is a glob rather than a literal path.
func hasWildcard(s string) bool { return strings.ContainsAny(s, "*?[") }

// staticPrefix returns the leading path of a glob that contains no wildcard —
// the deepest directory the glob is certainly confined to.
func staticPrefix(glob string) string {
	segs := strings.Split(glob, "/")
	for i, seg := range segs {
		if hasWildcard(seg) {
			return strings.Join(segs[:i], "/")
		}
	}
	return glob
}

// globMatch matches a path against a pattern, segment by segment, with `**`
// standing for zero or more segments.
//
// path.Match is used per segment, so `*`, `?`, and character classes mean what
// they mean everywhere else — but it treats `**` as a single `*` that cannot
// cross a separator, and "everything under this directory" is the pattern a
// permission is actually written with. Hence the recursion here.
func globMatch(pattern, target string) bool {
	if pattern == "" || target == "" {
		return false
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(target, "/"))
}

func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Zero segments, then one, then two: the first arrangement that
			// matches decides it. A trailing `**` matches whatever is left,
			// including nothing, so `Anno/**` covers `Anno` itself.
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(seg); i++ {
				if matchSegments(rest, seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			// A malformed pattern matches nothing. It cannot get this far from
			// ParsePattern, and if it ever does, refusing is the safe answer.
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// Narrow returns the widest clause that both a child's pattern and a boss's
// pattern allow, and reports whether anything survives.
//
// This is the pattern-level half of the intersection in internal/authz. Either
// side may be the narrower one: a boss with write(Common/user/**) narrows a
// child's write(Common/**), and the same boss leaves a child's
// write(Common/user/store.go) alone.
// A clause that is a list narrows term by term, which is the whole reason lists
// are worth having: a child allowed two directories under a boss allowed one keeps
// the one, where a clause that could only be kept or dropped whole would lose both.
// Every exception either side names is kept, because taking more out is always the
// safe direction and neither party asked for the other's.
func Narrow(child, boss Pattern) (Pattern, bool) {
	switch {
	case boss.Contains(child):
		return child, true
	case child.Contains(boss):
		return boss, true
	case child.Zero() || boss.Zero() || child.kind != boss.kind || child.kind == KindSpawn:
		// A budget has no terms to intersect, and Contains already answered for
		// it: the smaller number won, and there is nothing between them.
		return Pattern{}, false
	}

	mine, theirs := includes(child.arg), includes(boss.arg)
	kept := make([]string, 0, len(mine)+len(theirs))
	// Each side contributes the terms the other provably covers. A child term
	// inside a boss term survives as itself; a boss term inside a child term is
	// the boss's narrowing of it, and survives as the boss wrote it.
	for _, term := range mine {
		if slices.ContainsFunc(theirs, func(b string) bool { return globContains(b, term) }) {
			kept = append(kept, term)
		}
	}
	for _, term := range theirs {
		if slices.ContainsFunc(mine, func(c string) bool { return globContains(c, term) }) {
			kept = append(kept, term)
		}
	}
	kept = compactSorted(kept)
	if len(kept) == 0 {
		return Pattern{}, false
	}

	out := compactSorted(append(excludes(child.arg), excludes(boss.arg)...))
	return Pattern{kind: child.kind, arg: joinBody(kept, out)}, true
}

// compactSorted sorts and de-duplicates, so a narrowed clause renders the one way
// every other clause does.
func compactSorted(list []string) []string {
	slices.Sort(list)
	return slices.Compact(list)
}

// ParsePatterns reads a list of clauses, refusing duplicates.
//
// A duplicate is an error rather than a silent de-duplication because the two
// spellings a caller typed may not have been meant to be the same thing, and a
// permission list is short enough to fix.
func ParsePatterns(raws []string) ([]Pattern, error) {
	joined, err := JoinClauses(raws)
	if err != nil {
		return nil, err
	}
	out := make([]Pattern, 0, len(joined))
	for i, raw := range joined {
		p, err := ParsePattern(raw)
		if err != nil {
			return nil, err
		}
		for _, seen := range out {
			if seen.Equal(p) {
				return nil, fault.Usage{Reason: fmt.Sprintf(
					"pattern %d repeats %s", i+1, p)}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// JoinClauses puts back together a clause the shell took apart.
//
// A clause may now contain spaces, and a shell splits on spaces, so
// `orc new permission p 50 read(a/** b/**)` arrives as two arguments that are each
// half a clause. Quoting is the right habit and the help says so, but a tool that
// answered an unquoted clause with "read(a/** must be written kind(argument)" would
// be blaming the user for its own arithmetic.
//
// So arguments are rejoined until the parentheses balance. What it will not do is
// guess: an argument with no `(` at all where a clause is expected, or a clause
// left open at the end of the list, is an error that says which one.
func JoinClauses(raws []string) ([]string, error) {
	out := make([]string, 0, len(raws))
	var open string
	for _, raw := range raws {
		piece := strings.TrimSpace(raw)
		if piece == "" {
			continue
		}
		if open != "" {
			open += " " + piece
			if balanced(open) {
				out = append(out, open)
				open = ""
			}
			continue
		}
		if balanced(piece) {
			out = append(out, piece)
			continue
		}
		open = piece
	}
	if open != "" {
		return nil, fault.Usage{Reason: fmt.Sprintf(
			"clause %q is never closed; a clause is kind(argument), and one with spaces in it "+
				"needs quoting, as in \"read(Anno/** Dock/**)\"", open)}
	}
	return out, nil
}

// balanced reports whether a fragment holds as many closing parentheses as
// opening ones, and at least one of each. A fragment with none of either is a
// bare word, which is not a clause and is not half of one either — ParsePattern
// gives it the better message.
func balanced(text string) bool {
	depth := 0
	for _, r := range text {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return true // malformed rather than open; ParsePattern says so
			}
		}
	}
	return depth == 0
}

// PatternStrings renders a list for storage or display.
func PatternStrings(list []Pattern) []string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.String()
	}
	return out
}
