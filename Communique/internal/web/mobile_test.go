package web_test

import (
	"os"
	"regexp"
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
func TestTheFoldIndentIsAStylesheetDecision(t *testing.T) {
	js := read(t, "app/library.js")
	if strings.Contains(js, "padding-left:") {
		t.Error("library.js still writes its own padding, which a media query cannot override")
	}
	if !strings.Contains(js, "--depth:") {
		t.Error("library.js no longer states a fold's depth for the stylesheet")
	}
	if !strings.Contains(read(t, "app/app.css"), "var(--depth") {
		t.Error("app.css does not use the depth it is given")
	}
}
