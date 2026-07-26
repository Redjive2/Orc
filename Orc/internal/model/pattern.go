package model

import (
	"fmt"
	"path"
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
	case "tool":
		return KindTool, nil
	default:
		return KindUnset, fault.Usage{Reason: fmt.Sprintf(
			"unknown permission kind %q; try read, write, spawn, orc, or tool", raw)}
	}
}

// Kinds lists every kind, for help and for tests that must be total.
func Kinds() []Kind { return []Kind{KindRead, KindWrite, KindSpawn, KindOrc, KindTool} }

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

// ParsePattern reads a pattern as it is written on the command line and stored.
//
//	read(Anno/**)      write(Anno/internal/**)      spawn(24)      orc(employ)
//
// Parentheses are required. A bare `read` would have to mean either "read
// everything" or "read nothing", and a permission whose meaning depends on
// guessing which is one nobody can audit.
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
	arg := strings.TrimSpace(text[open+1 : len(text)-1])
	if arg == "" {
		return Pattern{}, fault.Usage{Reason: fmt.Sprintf("pattern %q has an empty argument", raw)}
	}

	switch kind {
	case KindSpawn:
		load, err := parseInt(arg)
		if err != nil || load > MaxLoad {
			return Pattern{}, fault.Usage{Reason: fmt.Sprintf(
				"spawn takes a load budget from 0 to %d, not %q", MaxLoad, arg)}
		}
		return Pattern{kind: kind, arg: itoa(load), load: load}, nil

	case KindRead, KindWrite:
		clean, err := cleanGlob(arg)
		if err != nil {
			return Pattern{}, err
		}
		return Pattern{kind: kind, arg: clean}, nil

	default: // KindOrc, KindTool
		if strings.ContainsAny(arg, "/ \t") {
			return Pattern{}, fault.Usage{Reason: fmt.Sprintf(
				"%s(%s) names one thing, so it cannot contain a slash or a space", kind, arg)}
		}
		return Pattern{kind: kind, arg: strings.ToLower(arg)}, nil
	}
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
// and write, a verb for orc. A spawn pattern matches nothing — it is a budget,
// and asking it about a path is a caller mistake that must not answer yes.
func (p Pattern) Matches(target string) bool {
	switch p.kind {
	case KindRead, KindWrite:
		clean, err := cleanGlob(target)
		if err != nil {
			return false
		}
		return globMatch(p.arg, clean)
	case KindOrc:
		return globMatch(p.arg, strings.ToLower(strings.TrimSpace(target)))
	default:
		return false
	}
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

	// Everything.
	if p.arg == "**" {
		return true
	}
	// A literal target is decided by matching it directly.
	if !hasWildcard(other.arg) {
		return globMatch(p.arg, other.arg)
	}
	// A prefix pattern covers anything whose fixed leading path is inside it.
	if prefix, ok := strings.CutSuffix(p.arg, "/**"); ok {
		static := staticPrefix(other.arg)
		return static == prefix || strings.HasPrefix(static, prefix+"/")
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
func Narrow(child, boss Pattern) (Pattern, bool) {
	switch {
	case boss.Contains(child):
		return child, true
	case child.Contains(boss):
		return boss, true
	default:
		return Pattern{}, false
	}
}

// ParsePatterns reads a list, refusing duplicates.
//
// A duplicate is an error rather than a silent de-duplication because the two
// spellings a caller typed may not have been meant to be the same thing, and a
// permission list is short enough to fix.
func ParsePatterns(raws []string) ([]Pattern, error) {
	out := make([]Pattern, 0, len(raws))
	for i, raw := range raws {
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

// PatternStrings renders a list for storage or display.
func PatternStrings(list []Pattern) []string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.String()
	}
	return out
}
