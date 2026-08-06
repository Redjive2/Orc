package web_test

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The site is read from a phone as often as from a desk, and the layout that
// makes that work is verified by measuring it in a browser rather than here.
// What *this* guards is the handful of single points of failure — the lines
// whose removal would undo all of that silently, with nothing failing and
// nothing looking wrong on the developer's own wide screen.

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

// TestTheViewportIsDeclared is the one that matters most.
//
// Without it a phone lays the page out at about 980 pixels and scales the result
// down, so every rule below is irrelevant and the site is a legible-but-tiny
// photograph of a desktop. It is one line, it is easy to lose in a rewrite, and
// nothing else in the tree would notice.
func TestTheViewportIsDeclared(t *testing.T) {
	for _, path := range []string{"app/index.html", "../server/login.go"} {
		body := read(t, path)
		if !strings.Contains(body, `name="viewport"`) {
			t.Errorf("%s declares no viewport", path)
		}
		if !strings.Contains(body, "width=device-width") {
			t.Errorf("%s does not lay out at the device width", path)
		}
	}
}

// TestTextInputsDoNotProvokeAZoom: below 16px, iOS zooms the page when a field
// takes focus, leaving the reader scrolled sideways in a layout that fitted a
// moment earlier. The login field is the first thing anybody touches.
func TestTextInputsDoNotProvokeAZoom(t *testing.T) {
	for _, path := range []string{"app/app.css", "../server/login.go"} {
		body := read(t, path)
		if !strings.Contains(body, "font-size: 16px") && !strings.Contains(body, "font-size:16px") {
			t.Errorf("%s sets no 16px font on its fields, so a phone will zoom on focus", path)
		}
	}
}

// TestThereIsANarrowLayout guards the media query itself. Its absence is not a
// broken build or a failing test anywhere else — it is a site that scrolls
// sideways on the device it exists to be read on.
func TestThereIsANarrowLayout(t *testing.T) {
	css := read(t, "app/app.css")
	if !regexp.MustCompile(`@media\s*\(max-width:`).MatchString(css) {
		t.Fatal("app.css has no narrow-screen layer")
	}
	for _, want := range []string{
		"min-height: 44px", // tap targets
		"flex-wrap: wrap",  // the navigation, which is ten links wide
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css no longer sets %q", want)
		}
	}
}

// TestNothingScrollsThePageSideways: a long path, a long subject, or a wide code
// block belongs in its own scrolling box. The page itself never moves.
func TestNothingScrollsThePageSideways(t *testing.T) {
	css := read(t, "app/app.css")
	if !strings.Contains(css, "overflow-x: hidden") {
		t.Error("the page may scroll sideways")
	}
	if !strings.Contains(css, "overflow-x: auto") {
		t.Error("nothing scrolls inside its own box, so wide content has nowhere to go")
	}
}

// TestTheFoldIndentIsAStylesheetDecision: it used to be an inline padding
// written by library.js, which no media query can reach — so a repository five
// levels deep pushed its own filenames off a phone.
//
// The depth has to travel as a *style object*, which `h` applies through the
// CSSOM. Written as a `style` attribute it never reaches the browser at all: the
// site's content policy has no `unsafe-inline`, so the attribute is discarded
// without a word and every fold in the tree draws at depth zero. That is why the
// check below is for the object form rather than for the property name anywhere
// — the name appearing in a comment, or in a string handed to an attribute, is
// exactly the state this is meant to fail on.
func TestTheFoldIndentIsAStylesheetDecision(t *testing.T) {
	js := read(t, "app/library.js")
	if strings.Contains(js, "padding-left:") {
		t.Error("library.js still writes its own padding, which a media query cannot override")
	}
	if !regexp.MustCompile(`style:\s*\{\s*"--depth"`).MatchString(js) {
		t.Error("library.js no longer hands a fold's depth to the stylesheet as a style object; " +
			"as a style attribute the content policy would discard it and every fold would " +
			"draw at depth zero")
	}
	if !strings.Contains(read(t, "app/app.css"), "var(--depth") {
		t.Error("app.css does not use the depth it is given")
	}
}

// TestTheActivityTablesFoldOnAPhone: both are fixed character grids that add up
// to far more than a phone is wide.
//
// The failure is quiet, which is why it is worth a test. `body` has
// `overflow-x: hidden`, so a row too wide to fit is not a page that scrolls
// sideways — it is a row whose last columns are cut off with nothing to say they
// were there. A reader sees four figures and has no way to know there were six.
func TestTheActivityTablesFoldOnAPhone(t *testing.T) {
	css := read(t, "app/app.css")
	narrow := css[strings.Index(css, "@media (max-width: 40rem)"):]

	for _, row := range []string{".gen-row", ".work-row"} {
		if !strings.Contains(narrow, row+" {") && !strings.Contains(narrow, row+",") {
			t.Errorf("%s keeps its desktop columns on a narrow screen, so its last "+
				"figures are clipped away without a sign", row)
		}
	}
}

// TestALinkActingAsAControlIsSizedLikeOne guards the seam an anchor-as-button has.
//
// `a.button` exists because a control that *navigates* should be a link: middle
// click, open-in-new-tab and copy-link are all things a reader expects of one, and
// a <button> calling location.hash takes every one of them away. The cost is that
// it does not match `button` in a selector, so a rule narrowing the controls on a
// row has to name it too.
//
// That cost has already been paid once. `.controls.row button` shrinks the
// controls on an agent row; the anchor did not match it, kept the full padding and
// font, and stood a third taller than the three buttons beside it. Nothing failed
// — a stylesheet has no way to say a rule missed something — so this asks the
// question directly.
func TestALinkActingAsAControlIsSizedLikeOne(t *testing.T) {
	sheet := read(t, "app/app.css")

	for _, line := range strings.Split(sheet, "\n") {
		selector, _, ok := strings.Cut(line, "{")
		if !ok || !strings.Contains(selector, "button") {
			continue
		}
		// Only where a link-as-control actually lives: a row's controls. Every
		// other `button` rule in the sheet is for a context with no anchor in it,
		// and demanding they all name one would be ceremony that teaches people to
		// add the token without thinking.
		if !strings.Contains(selector, ".controls") {
			continue
		}
		if strings.Contains(selector, "a.button") {
			continue
		}
		// A rule can still be genuinely about buttons — a disabled state an anchor
		// cannot be in — and saying so keeps that a decision rather than an
		// oversight.
		if strings.Contains(line, "/* buttons only") {
			continue
		}
		t.Errorf("this rule sizes buttons in a context and does not name a.button, "+
			"so a link acting as a control there will not match its neighbours:\n  %s\n"+
			"add a.button to the selector, or mark it `/* buttons only */` if no link belongs there",
			strings.TrimSpace(line))
	}
}

// TestTheNavigationFoldsOnAPhone.
//
// The navigation is up to nine links wrapping over three or four lines, and it
// sits above every screen — a third of a small display spent before any content is
// drawn. It folds into one control that says where you are.
//
// The rendering is the same at both widths on purpose: the nav decides what
// exists and the stylesheet decides what is on screen. So this asks the
// stylesheet, and the JavaScript tests ask about the state.
func TestTheNavigationFoldsOnAPhone(t *testing.T) {
	css := read(t, "app/app.css")
	js := read(t, "app/views.js")

	if !strings.Contains(js, `class: "hamburger"`) {
		t.Fatal("there is no control to open the navigation with on a phone")
	}
	// Above the breakpoint it must be gone, or a desktop grows a menu button it
	// has no use for beside a navigation that is already on screen.
	if !strings.Contains(css, "#nav .hamburger { display: none; }") {
		t.Error("the phone menu is not hidden on a wide screen")
	}

	narrow := css[strings.Index(css, "@media (max-width: 40rem)"):]
	for _, want := range []string{
		".hamburger",                       // it appears at all
		`aria-expanded="false"] ~ .majors`, // and the rows fold behind it
	} {
		if !strings.Contains(narrow, want) {
			t.Errorf("the narrow layer does not carry %q, so the navigation does not fold", want)
		}
	}
	// A thumb, like every other control on this screen.
	if !strings.Contains(narrow, ".hamburger {") {
		t.Error("the menu button has no narrow rule, so it is not sized for a thumb")
	}
}

// TestNarrowContentUsesTheWidth: the page gives up its side margins, and the
// grids give up their fixed columns.
//
// Both were spending a 40-character screen on nothing. The margin cost a
// character on each side of every screen; a fixed column cut the last figure off
// a table while empty space sat beside it, and `overflow-x: hidden` meant it
// vanished rather than scrolled — a reader saw four figures with no way to know
// there had been six.
func TestNarrowContentUsesTheWidth(t *testing.T) {
	css := read(t, "app/app.css")
	narrow := css[strings.Index(css, "@media (max-width: 40rem)"):]

	// A gutter, and one decision behind it.
	//
	// This used to demand *no* side padding at all, on the argument that a
	// 40-character screen cannot spare a character a side. That was right about
	// text and wrong about edges: a card's border ran against the bezel, a rounded
	// display clipped it, and a notch took a bite out of whichever side was up in
	// landscape. So the rule is no longer "none" — it is that every edge of the
	// page's column comes from `--gutter`, which is a max() of a character and the
	// device's own inset.
	//
	// Named rather than measured, because nothing here can evaluate that max().
	// What is checkable, and what actually broke before, is three edges drifting
	// apart because each was typed separately.
	for _, edge := range []string{"#chrome { padding:", "main { padding:", "#nav { padding:"} {
		i := strings.Index(narrow, edge)
		if i < 0 {
			t.Errorf("%s has no narrow rule at all", edge)
			continue
		}
		line := narrow[i : i+strings.Index(narrow[i:], "\n")]
		if !strings.Contains(line, "--gutter") {
			t.Errorf("this edge is set by hand rather than from the shared gutter, "+
				"so the three that make the page's column can drift apart:\n  %s", line)
		}
	}
	for _, row := range []string{".event", ".survey", ".grid.users"} {
		if !strings.Contains(narrow, row+" { grid-template-columns: ") {
			t.Errorf("%s keeps its desktop columns on a narrow screen", row)
		}
	}
	// The column that holds the content has to be able to shrink below it, or a
	// long word pushes the grid wider than the screen and the rest is clipped.
	if !strings.Contains(narrow, "minmax(0, 1fr)") {
		t.Error("no grid column can shrink below its content, so a long name still " +
			"pushes the row wider than the phone")
	}
}

// TestNoNarrowRuleIsOverriddenByAnother.
//
// There are two `@media (max-width: 40rem)` blocks, and the second wins. A
// selector written in both is a rule that looks applied and is not — which is how
// `.event` kept a `1fr` column after being given `minmax(0, 1fr)`, and how the
// clipping that change was meant to stop went on happening.
//
// A stylesheet has no way to say a rule was overridden, so this asks directly.
func TestNoNarrowRuleIsOverriddenByAnother(t *testing.T) {
	css := read(t, "app/app.css")
	narrow := css[strings.Index(css, "@media (max-width: 40rem)"):]

	// Per block, not per file. Two rules for one selector *inside* a block both
	// apply — they set different properties and that is ordinary. The pair that
	// bites is one selector in both blocks, where the second decides and the first
	// reads as though it did.
	//
	// One-selector rules only. A grouped selector legitimately restates part of
	// another, and the exact repeat is what is worth catching.
	rule := regexp.MustCompile(`(?m)^\s{2}([.#][A-Za-z0-9_.#>-]*)\s*\{`)
	blocks := strings.Split(narrow, "@media (max-width: 40rem)")
	seen := map[string]int{}
	for _, block := range blocks {
		here := map[string]bool{}
		for _, match := range rule.FindAllStringSubmatch(block, -1) {
			here[match[1]] = true
		}
		for selector := range here {
			seen[selector]++
		}
	}

	var twice []string
	for selector, n := range seen {
		if n > 1 {
			twice = append(twice, fmt.Sprintf("%s (in %d blocks)", selector, n))
		}
	}
	sort.Strings(twice)
	if len(twice) > 0 {
		t.Errorf("these selectors are written in more than one narrow block, so the earlier "+
			"one does nothing:\n  %s\nkeep one definition, in the block that wins",
			strings.Join(twice, "\n  "))
	}
}
