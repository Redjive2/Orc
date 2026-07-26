package cli

import (
	"strings"
	"testing"
)

// strip removes SGR escape sequences, so a coloured rendering can be compared
// with the plain one it must be a layer over. It is Macmuffin's, deliberately:
// the two tools assert the same property and should measure it the same way.
func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// readOnlyScreens is every screen that can be drawn twice and produce the same
// bytes: the whole view surface, plus a refusal, a usage error, and a
// not-found.
//
// `destroy` without --yes is deliberately absent. It behaves differently on a
// terminal — where it prompts — from a pipe, where it is a usage error, so the
// two renderings differ for a reason that has nothing to do with colour.
//
// The comparison runs both renderings against *one* harness rather than two,
// because a probe's id is random and a harness's paths are a temporary
// directory — two rigs would differ in ways that have nothing to do with
// colour. Mutating screens are covered separately below, for the same reason.
func readOnlyScreens() [][]string {
	return [][]string{
		{"help"},
		{"list"},
		{"world"},
		{"mail"},
		{"mail", `from="boss"`},
		{"tasks"},
		{"journal", "refactor"},
		{"journal", "alice"},
		{"timeline"},
		{"manifest"},
		{"diff", "--source"},
		{"doctor"},
		{"as", "god", "--", "cq", "sync"}, // a refusal
		{"nonsense"},                      // a usage error, which prints the help too
		{"journal", "nothing-like-this"},  // a not-found
	}
}

// TestColourStripsToPlain is the house rule made mechanical: every screen,
// stripped of its escape sequences, must be byte-for-byte the plain rendering.
//
// If it ever is not, colour has become information — and a pipe, a NO_COLOR
// terminal, or an agent would silently lose part of the answer.
func TestColourStripsToPlain(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.runColour(false, "create", "scratch"); code != CodeOK {
		t.Fatalf("create exited %d\n%s", code, out)
	}

	for _, args := range readOnlyScreens() {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			plainCode, plainOut, plainErr := h.runColour(false, args...)
			colourCode, colourOut, colourErr := h.runColour(true, args...)

			if plainCode != colourCode {
				t.Fatalf("exit codes differ: plain %d, coloured %d", plainCode, colourCode)
			}
			if got := strip(colourOut); got != plainOut {
				t.Fatalf("stdout differs once stripped:\nplain:\n%s\nstripped:\n%s", plainOut, got)
			}
			if got := strip(colourErr); got != plainErr {
				t.Fatalf("stderr differs once stripped:\nplain:\n%s\nstripped:\n%s", plainErr, got)
			}
		})
	}
}

// TestMutatingScreensStripToPlain covers the screens that cannot be drawn
// twice — creating, saving, rewinding, destroying.
//
// They get a weaker check by necessity: the same command on two harnesses
// prints different temporary paths and a different probe id, so the assertion
// is that stripping leaves no escape residue and that the words are the same
// once the volatile parts are gone. Weaker than byte-equality, and still enough
// to catch colour that carries meaning.
func TestMutatingScreensStripToPlain(t *testing.T) {
	for _, args := range [][]string{
		{"create", "scratch"},
		{"save", "point"},
		{"restore", "point", "--yes"},
		{"destroy", "scratch", "--yes"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			plain, colour := newHarness(t), newHarness(t)
			for _, h := range []*harness{plain, colour} {
				if strings.Join(args, " ") != "create scratch" {
					if code, _, _ := h.runColour(false, "create", "scratch"); code != CodeOK {
						t.Fatal("create failed")
					}
				}
				if args[0] == "restore" || args[0] == "destroy" {
					if code, _, _ := h.runColour(false, "save", "point"); code != CodeOK {
						t.Fatal("save failed")
					}
				}
			}

			_, plainOut, _ := plain.runColour(false, args...)
			_, colourOut, _ := colour.runColour(true, args...)

			stripped := strip(colourOut)
			if strings.Contains(stripped, "\x1b") {
				t.Fatalf("escape sequences survived stripping:\n%q", stripped)
			}
			if words(stripped) != words(plainOut) {
				t.Fatalf("the coloured screen says something different:\nplain:\n%s\nstripped:\n%s", plainOut, stripped)
			}
		})
	}
}

// words reduces a screen to the words it contains, dropping the volatile parts
// a temporary harness produces: absolute paths, ids, sizes, and counts.
func words(s string) string {
	var kept []string
	for _, field := range strings.Fields(s) {
		switch {
		case strings.ContainsAny(field, "/"): // a path
		case strings.ContainsAny(field, "0123456789"): // an id, a size, a count
		default:
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}

// TestColourActuallyColours guards the other direction. A strip-equivalence
// test passes trivially if nothing is ever painted, so something has to assert
// the escapes are there in the first place.
func TestColourActuallyColours(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.runColour(true, "create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}

	// help is first because it is the screen that was plain for five milestones:
	// it was a constant rather than a rendering, so nothing painted it.
	for _, args := range [][]string{{"help"}, {"list"}, {"world"}, {"doctor"}, {"mail"}, {"tasks"}, {"timeline"}} {
		_, out, _ := h.runColour(true, args...)
		if !strings.Contains(out, "\x1b[") {
			t.Fatalf("%v produced no colour at all:\n%s", args, out)
		}
	}
}

// TestPlainStaysPlain covers every way colour is turned off. Each is a promise
// to somebody: a pipe to a person, NO_COLOR to an operator, ORC_AGENT to Orc.
func TestPlainStaysPlain(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		terminal bool
		args     []string
	}{
		{"not a terminal", nil, false, []string{"list"}},
		{"--no-color beats a terminal", nil, true, []string{"list", "--no-color"}},
		{"--no-colour spelled the other way", nil, true, []string{"list", "--no-colour"}},
		{"NO_COLOR", map[string]string{"NO_COLOR": ""}, true, []string{"list"}},
		{"ORC_AGENT", map[string]string{"ORC_AGENT": "1"}, true, []string{"list"}},
		{"ORC_THEME=none", map[string]string{"ORC_THEME": "none"}, true, []string{"list"}},
		{"a dumb terminal", map[string]string{"TERM": "dumb"}, true, []string{"list"}},

		// The ones that matter most: ORC_AGENT and NO_COLOR turn colour off for
		// every tool at once, and a single command must not defeat them.
		{"ORC_AGENT beats --color", map[string]string{"ORC_AGENT": "1"}, false, []string{"list", "--color"}},
		{"NO_COLOR beats --color", map[string]string{"NO_COLOR": ""}, false, []string{"list", "--color"}},
		{"--no-color beats --color", nil, true, []string{"list", "--color", "--no-color"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if code, _, _ := h.run("create", "scratch"); code != CodeOK {
				t.Fatal("create failed")
			}
			_, out, errs := h.runWith(c.env, c.terminal, c.args...)
			if strings.Contains(out+errs, "\x1b[") {
				t.Fatalf("colour survived %s:\n%s%s", c.name, out, errs)
			}
		})
	}
}

// TestColourForcesOntoAPipe is what --color is for: a person paging coloured
// output. The stream is not a terminal, and the escapes appear anyway.
func TestColourForcesOntoAPipe(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}

	if _, plain, _ := h.run("list"); strings.Contains(plain, "\x1b[") {
		t.Fatal("a pipe was coloured without being asked")
	}
	_, forced, _ := h.run("list", "--color")
	if !strings.Contains(forced, "\x1b[") {
		t.Fatalf("--color did not colour a pipe:\n%s", forced)
	}
}

// TestEachStreamIsAskedSeparately is the bug the two palettes exist to prevent.
//
// `orcprobe shell > log` paints its banner for a terminal while stdout is a
// file; `2> log` is the reverse. Deciding both from stdout would either drop
// the colour where a person is reading it or write escapes where nobody is.
func TestEachStreamIsAskedSeparately(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("create", "scratch"); code != CodeOK {
		t.Fatal("create failed")
	}
	// `list` warns on stderr about this, so both streams have something on them.
	plantUnfinished(t, h)

	_, out, errs := h.runStreams(true, false, "list")
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("stdout was a terminal and got no colour:\n%s", out)
	}
	if strings.Contains(errs, "\x1b[") {
		t.Fatalf("stderr was a pipe and got escape codes:\n%q", errs)
	}

	if _, out, _ = h.runStreams(false, true, "list"); strings.Contains(out, "\x1b[") {
		t.Fatalf("stdout was a pipe and got escape codes:\n%s", out)
	}
}

// TestBadThemeIsReported: a setting that silently does nothing is one the
// operator concludes is broken.
func TestBadThemeIsReported(t *testing.T) {
	h := newHarness(t)
	code, _, errs := h.runWith(map[string]string{"ORC_THEME": "nonsense"}, false, "list")
	if code != CodeUsage {
		t.Fatalf("a misspelled ORC_THEME exited %d, want %d", code, CodeUsage)
	}
	if !strings.Contains(errs, "theme") {
		t.Fatalf("the error does not name the setting:\n%s", errs)
	}
}
