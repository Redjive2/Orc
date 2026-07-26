// Package scan classifies the lines of a document.
//
// Dock has no syntax of its own: a section is a markdown heading and a link is
// a markdown link. That makes this the only package that knows anything about
// markdown, and it deliberately knows as little as it can — ATX headings,
// fenced code, inline code spans, HTML comments, and inline links. Not
// emphasis, not lists, not tables.
//
// Even that much is self-defence rather than ambition. Dock's own documentation
// is full of example headings and example links inside code fences, and a
// scanner that could not tell an example from the real thing would index its own
// examples and fill the link graph with them.
//
// Scanning is total: no input is an error, and no input panics. Whether a
// document is well formed is decided downstream in doc, where the numbering
// rules live. That split is what lets this package be fuzzed on the simple
// property that a fence never leaks.
package scan

import (
	"strings"
	"unicode/utf8"
)

// Limits on what one line may contribute. A document is read into memory
// whole, so the only unbounded thing here is how many links a single line can
// declare; a generated file of nothing but links should not turn into an
// unbounded slice without anyone noticing.
const (
	// MaxLinksPerLine bounds link extraction from one line. Real prose cites a
	// handful; a line past this is machine-generated, and the excess is dropped
	// rather than grown into.
	MaxLinksPerLine = 64
)

// Kind is what a line turned out to be.
type Kind int

const (
	// Text is ordinary prose: the default, and most of a document.
	Text Kind = iota
	// Heading is an ATX heading, and therefore a candidate section (§2).
	Heading
	// Fence is a fence delimiter — the line that opens or closes a code block.
	Fence
	// Code is a line inside a fenced block. Nothing in it is ever a heading or
	// a link, however much it looks like one.
	Code
	// Comment is a line wholly inside an HTML comment.
	Comment
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	switch k {
	case Text:
		return "text"
	case Heading:
		return "heading"
	case Fence:
		return "fence"
	case Code:
		return "code"
	case Comment:
		return "comment"
	default:
		return "unknown"
	}
}

// Line is one classified line. The zero value is not meaningful; lines are
// produced only by Scan.
type Line struct {
	num   int
	kind  Kind
	text  string
	level int
	head  string
}

// Num returns the 1-indexed line number.
func (l Line) Num() int { return l.num }

// Kind returns what the line is.
func (l Line) Kind() Kind { return l.kind }

// Text returns the line verbatim, without its terminator.
func (l Line) Text() string { return l.text }

// Level returns an ATX heading's depth, 1 to 6. It is 0 for every other kind,
// which is what makes the depth rule in doc checkable without a second lookup.
func (l Line) Level() int { return l.level }

// Head returns a heading's text with the leading #s, any closing #s, and the
// surrounding space removed. It is empty for every other kind.
func (l Line) Head() string { return l.head }

// Link is an inline markdown link found outside code and comments. Whether its
// destination addresses anything is not this package's business: target decides
// that, and most links in a document are ordinary prose links that Dock ignores.
type Link struct {
	line int
	col  int
	text string
	dest string
}

// Line returns the 1-indexed line the link was found on.
func (l Link) Line() int { return l.line }

// Col returns the 1-indexed rune column of the opening bracket.
func (l Link) Col() int { return l.col }

// Text returns the link text, which Dock uses as the edge's label.
func (l Link) Text() string { return l.text }

// Dest returns the destination verbatim, trimmed of surrounding space.
func (l Link) Dest() string { return l.dest }

// Result is one pass over a document.
type Result struct {
	lines []Line
	links []Link
}

// Lines returns a copy of the classified lines, one per line of input.
func (r Result) Lines() []Line { return append([]Line(nil), r.lines...) }

// Links returns a copy of the links found outside code and comments.
func (r Result) Links() []Link { return append([]Link(nil), r.links...) }

// Scan classifies every line of text and extracts its links.
//
// The two jobs share a pass because they share the state that makes them
// correct: a fence suppresses both, and an HTML comment spanning several lines
// suppresses both for its whole extent.
func Scan(text string) Result {
	raw := split(text)
	res := Result{lines: make([]Line, 0, len(raw))}

	var (
		fenceChar byte // 0 when not inside a fence
		fenceLen  int
		inComment bool
	)

	for i, line := range raw {
		num := i + 1
		body := strings.TrimSuffix(line, "\r")

		// A fence outranks everything: inside one, no line is a heading, a
		// comment, or a link, however much it looks like one.
		if fenceChar != 0 {
			if closesFence(body, fenceChar, fenceLen) {
				fenceChar, fenceLen = 0, 0
				res.lines = append(res.lines, Line{num: num, kind: Fence, text: body})
			} else {
				res.lines = append(res.lines, Line{num: num, kind: Code, text: body})
			}
			continue
		}

		// A fence cannot open inside a comment, so the comment check must come
		// first for lines that begin inside one.
		if !inComment {
			if char, n, ok := opensFence(body); ok {
				fenceChar, fenceLen = char, n
				res.lines = append(res.lines, Line{num: num, kind: Fence, text: body})
				continue
			}
		}

		startedInComment := inComment
		spans, nowInComment := visible(body, inComment)
		inComment = nowInComment

		kind := Text
		level, head := 0, ""
		switch {
		case startedInComment && len(spans) == 0:
			kind = Comment
		case !startedInComment:
			// A heading must begin its line, so it can only be found in a span
			// that starts at offset zero.
			if len(spans) > 0 && spans[0].off == 0 {
				if lv, h, ok := heading(spans[0].text); ok {
					kind, level, head = Heading, lv, h
				}
			}
		}

		res.lines = append(res.lines, Line{num: num, kind: kind, text: body, level: level, head: head})

		// A heading's text may contain a link, and that link belongs to the
		// section the heading opens rather than to the one before it — but that
		// is doc's problem to attribute. Here it is simply a link on this line.
		found := 0
		for _, sp := range spans {
			for _, lk := range links(sp.text) {
				if found >= MaxLinksPerLine {
					break
				}
				found++
				res.links = append(res.links, Link{
					line: num,
					col:  utf8.RuneCountInString(body[:sp.off+lk.off]) + 1,
					text: lk.text,
					dest: lk.dest,
				})
			}
		}
	}

	return res
}

// split breaks text into lines without their terminators, and without inventing
// a trailing empty line for a file that ends in a newline.
func split(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// indent reports the leading-space count, and whether it is small enough for the
// line to still be a fence or a heading. Four spaces starts an indented code
// block in markdown, which is deliberately not a thing Dock reads.
func indent(s string) (int, bool) {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n, n <= 3
}

// opensFence reports whether the line opens a fenced block, and with what.
func opensFence(s string) (char byte, n int, ok bool) {
	i, shallow := indent(s)
	if !shallow || i >= len(s) {
		return 0, 0, false
	}
	c := s[i]
	if c != '`' && c != '~' {
		return 0, 0, false
	}
	run := 0
	for i+run < len(s) && s[i+run] == c {
		run++
	}
	if run < 3 {
		return 0, 0, false
	}
	// A backtick fence's info string may not contain a backtick, which is what
	// keeps a line of inline code from opening a block.
	if c == '`' && strings.ContainsRune(s[i+run:], '`') {
		return 0, 0, false
	}
	return c, run, true
}

// closesFence reports whether the line closes a fence opened with char and n.
// A closing fence is at least as long as its opener and carries nothing else.
func closesFence(s string, char byte, n int) bool {
	i, shallow := indent(s)
	if !shallow {
		return false
	}
	run := 0
	for i+run < len(s) && s[i+run] == char {
		run++
	}
	return run >= n && strings.TrimSpace(s[i+run:]) == ""
}

// heading parses an ATX heading, returning its level and text.
func heading(s string) (level int, text string, ok bool) {
	i, shallow := indent(s)
	if !shallow {
		return 0, "", false
	}
	n := 0
	for i+n < len(s) && s[i+n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0, "", false
	}
	rest := s[i+n:]
	// "#foo" is not a heading; "#" alone is an empty one.
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	rest = strings.TrimSpace(rest)
	// An optional closing run of #s is decoration, not content.
	if t := strings.TrimRight(rest, "#"); t != rest {
		if t == "" || strings.HasSuffix(t, " ") {
			rest = strings.TrimSpace(t)
		}
	}
	return n, rest, true
}

// span is a stretch of a line that is neither commented out nor inside an
// inline code span, carried with its byte offset so a column stays truthful.
type span struct {
	off  int
	text string
}

// visible splits a line into the parts that can hold a heading or a link,
// reporting whether the line ends still inside an HTML comment.
//
// Comments are handled before code spans because a comment may contain a
// backtick and must not be reinterpreted because of it.
func visible(s string, inComment bool) ([]span, bool) {
	var out []span
	i := 0
	for i <= len(s) {
		if inComment {
			end := strings.Index(s[i:], "-->")
			if end < 0 {
				return uncoded(out), true
			}
			i += end + len("-->")
			inComment = false
			continue
		}
		start := strings.Index(s[i:], "<!--")
		if start < 0 {
			out = append(out, span{off: i, text: s[i:]})
			return uncoded(out), false
		}
		out = append(out, span{off: i, text: s[i : i+start]})
		i += start + len("<!--")
		inComment = true
	}
	return uncoded(out), inComment
}

// uncoded removes inline code spans from each span, so a link inside backticks
// is not a link. Backtick runs must match in length, which is what lets “ ` “
// hold a literal backtick.
func uncoded(in []span) []span {
	var out []span
	for _, sp := range in {
		s, base := sp.text, sp.off
		i := 0
		for i < len(s) {
			j := strings.IndexByte(s[i:], '`')
			if j < 0 {
				out = append(out, span{off: base + i, text: s[i:]})
				break
			}
			if j > 0 {
				out = append(out, span{off: base + i, text: s[i : i+j]})
			}
			i += j
			run := 0
			for i+run < len(s) && s[i+run] == '`' {
				run++
			}
			closer := strings.Repeat("`", run)
			rest := s[i+run:]
			end := indexRun(rest, closer)
			if end < 0 {
				// An unterminated run is literal text, not an open code span.
				out = append(out, span{off: base + i, text: s[i:]})
				break
			}
			i += run + end + run
		}
	}
	return out
}

// indexRun finds closer in s where it is not part of a longer backtick run.
func indexRun(s, closer string) int {
	from := 0
	for {
		k := strings.Index(s[from:], closer)
		if k < 0 {
			return -1
		}
		k += from
		after := k + len(closer)
		if after < len(s) && s[after] == '`' {
			// Part of a longer run; skip the whole run and keep looking.
			for after < len(s) && s[after] == '`' {
				after++
			}
			from = after
			continue
		}
		return k
	}
}

// rawLink is a link with a byte offset into the span it was found in.
type rawLink struct {
	off  int
	text string
	dest string
}

// links extracts inline links from one visible span.
//
// Images are skipped: "![alt](src)" addresses a picture, and a picture is not a
// section. Reference-style links are not extracted at all, which is decision 8
// of the plan — supporting them well means resolving ids, and supporting them
// badly means dropping edges silently.
func links(s string) []rawLink {
	var out []rawLink
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // an escaped bracket opens nothing
			continue
		}
		if s[i] != '[' {
			continue
		}
		if i > 0 && s[i-1] == '!' {
			continue
		}
		text, after, ok := bracketed(s, i, '[', ']')
		if !ok {
			continue
		}
		if after >= len(s) || s[after] != '(' {
			continue
		}
		dest, end, ok := destination(s, after)
		if !ok {
			continue
		}
		out = append(out, rawLink{off: i, text: text, dest: dest})
		if len(out) >= MaxLinksPerLine {
			return out
		}
		i = end
	}
	return out
}

// bracketed reads a balanced open/close pair beginning at i, returning the
// content and the offset just past the closer.
func bracketed(s string, i int, open, close byte) (string, int, bool) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[i+1 : j], j + 1, true
			}
		}
	}
	return "", 0, false
}

// destination reads "(dest)" or "(<dest>)", allowing a title after the
// destination and balanced parentheses within it.
func destination(s string, open int) (string, int, bool) {
	inner, end, ok := bracketed(s, open, '(', ')')
	if !ok {
		return "", 0, false
	}
	inner = strings.TrimSpace(inner)
	if strings.HasPrefix(inner, "<") {
		if k := strings.IndexByte(inner, '>'); k >= 0 {
			return strings.TrimSpace(inner[1:k]), end - 1, true
		}
		return "", 0, false
	}
	// A title is separated from the destination by whitespace: take the first
	// field and let the rest be prose.
	if k := strings.IndexAny(inner, " \t"); k >= 0 {
		inner = inner[:k]
	}
	return inner, end - 1, true
}
