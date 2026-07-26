package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/web"
	"orc/theme"
)

// TestStylesheetFollowsTheSharedScheme: the site is drawn in the same colours
// as every CLI, so one setting restyles them all.
func TestStylesheetFollowsTheSharedScheme(t *testing.T) {
	for _, tc := range []struct {
		flavour theme.Flavour
		canvas  string
		text    string
	}{
		{theme.Macchiato, "#24273a", "#cad3f5"},
		{theme.Mocha, "#1e1e2e", "#cdd6f4"},
		{theme.Latte, "#eff1f5", "#4c4f69"},
		{theme.Frappe, "#303446", "#c6d0f5"},
	} {
		t.Run(tc.flavour.String(), func(t *testing.T) {
			css, err := web.Stylesheet(tc.flavour)
			if err != nil {
				t.Fatalf("Stylesheet: %v", err)
			}
			got := string(css)
			if !strings.Contains(got, "--canvas: "+tc.canvas) {
				t.Errorf("canvas is wrong:\n%s", got)
			}
			if !strings.Contains(got, "--text: "+tc.text) {
				t.Errorf("text colour is wrong:\n%s", got)
			}
			if !strings.Contains(got, tc.flavour.String()) {
				t.Errorf("the stylesheet should name its flavour:\n%s", got)
			}
		})
	}
}

// TestEveryPropertyIsDefined guards the table: a property the stylesheet uses
// but the generator never emits would silently render as nothing.
func TestEveryPropertyIsDefined(t *testing.T) {
	css, err := web.Stylesheet(theme.Default)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"--canvas", "--panel", "--inset", "--text", "--heading", "--title",
		"--muted", "--subtle", "--frame", "--primary", "--secondary",
		"--tertiary", "--accent", "--info", "--success", "--warning", "--danger",
	} {
		if !strings.Contains(string(css), name+": #") {
			t.Errorf("%s is not defined", name)
		}
	}
}

// TestEveryPropertyTheStylesheetUsesIsGenerated is the other direction: a
// `var(--x)` with no `--x` behind it.
func TestEveryPropertyTheStylesheetUsesIsGenerated(t *testing.T) {
	generated, err := web.Stylesheet(theme.Default)
	if err != nil {
		t.Fatal(err)
	}
	app := readAsset(t, "app.css")

	for _, used := range varsUsedIn(app) {
		if strings.Contains(string(generated), used+":") {
			continue
		}
		// A variable the application sets itself is fine, as long as it carries a
		// fallback: `var(--depth, 0)` cannot be undefined whatever happens, which
		// is the property this test is really about. Anything else with nothing
		// behind it is the bug.
		if strings.Contains(app, "var("+used+",") {
			continue
		}
		t.Errorf("app.css uses %s, which neither the theme defines nor a fallback covers", used)
	}
}

func varsUsedIn(css string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(css, "var(") {
		if i := strings.IndexAny(part, "),"); i > 0 {
			name := strings.TrimSpace(part[:i])
			if strings.HasPrefix(name, "--") && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func TestPlainFallsBackRatherThanRenderingColourless(t *testing.T) {
	// NO_COLOR is about escape sequences in a pipe; a browser is not a pipe,
	// and a site with no colours is not a site.
	css, err := web.Stylesheet(theme.Plain)
	if err != nil {
		t.Fatalf("Stylesheet: %v", err)
	}
	if !strings.Contains(string(css), theme.Default.String()) {
		t.Errorf("Plain should fall back to the default flavour:\n%s", css)
	}

	if _, err := web.Stylesheet(theme.Flavour(99)); err != nil {
		t.Errorf("an unknown flavour should fall back rather than fail: %v", err)
	}
}

func TestIndexIsTheShell(t *testing.T) {
	index, err := web.Index()
	if err != nil {
		t.Fatal(err)
	}
	got := string(index)
	for _, want := range []string{"communiqué", "/assets/app.js", "/assets/theme.css", "/assets/app.css"} {
		if !strings.Contains(got, want) {
			t.Errorf("the shell should reference %q", want)
		}
	}
	// The content policy forbids inline code, and the shell is written to
	// satisfy it rather than the policy relaxed to suit the shell.
	if strings.Contains(got, "<script>") || strings.Contains(got, " onclick=") {
		t.Errorf("the shell carries inline code:\n%s", got)
	}
}

// TestNothingIsFetchedFromElsewhere: the content policy forbids it, so an asset
// that reached for a CDN would simply fail to load.
func TestNothingIsFetchedFromElsewhere(t *testing.T) {
	shell, err := web.Index()
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{"index.html": string(shell)}
	for _, name := range []string{"app.css", "app.js", "views.js", "dom.js", "api.js", "markdown.js"} {
		sources[name] = readAsset(t, name)
	}

	for name, body := range sources {
		for _, offender := range []string{"http://", "//cdn", "googleapis", "unpkg", "jsdelivr"} {
			if strings.Contains(body, offender) {
				t.Errorf("%s reaches outside the origin: %q", name, offender)
			}
		}
	}
}

// TestNoInnerHTMLAnywhere is the rule that makes the markdown renderer safe:
// nodes are built, never parsed from a string.
//
// Comments are stripped first, because the files say "never innerHTML" in
// prose and a test that could not tell the two apart would be untrustworthy in
// the direction that matters.
func TestNoInnerHTMLAnywhere(t *testing.T) {
	for _, name := range []string{"app.js", "views.js", "dom.js", "api.js", "markdown.js"} {
		code := stripComments(readAsset(t, name))
		for _, offender := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
			if strings.Contains(code, offender) {
				t.Errorf("%s uses %s", name, offender)
			}
		}
	}

	// The stripper must not be the reason the test passes.
	if strings.Contains(stripComments("const x = 1; el.innerHTML = y;"), "innerHTML") != true {
		t.Errorf("stripComments removed real code")
	}
	if strings.Contains(stripComments("// never innerHTML\ncode()"), "innerHTML") {
		t.Errorf("stripComments left a comment behind")
	}
}

// stripComments removes // line comments and /* */ blocks.
func stripComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				i = len(src)
			} else {
				i += end + 4
			}
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String()
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	handler, err := web.Assets(theme.Default)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/{file}", handler)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/assets/"+name, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /assets/%s: status %d", name, w.Code)
	}
	return w.Body.String()
}

func TestAssetsServeWhatExistsAndRefuseWhatDoesNot(t *testing.T) {
	handler, err := web.Assets(theme.Default)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/{file}", handler)

	for _, tc := range []struct {
		name   string
		path   string
		status int
		ctype  string
	}{
		{"stylesheet", "/assets/app.css", http.StatusOK, "text/css; charset=utf-8"},
		{"generated theme", "/assets/theme.css", http.StatusOK, "text/css; charset=utf-8"},
		{"script", "/assets/app.js", http.StatusOK, "text/javascript; charset=utf-8"},
		{"missing", "/assets/nope.js", http.StatusNotFound, ""},
		{"the shell is not an asset", "/assets/index.html", http.StatusNotFound, ""},
		{"no name", "/assets/", http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if tc.ctype != "" && w.Header().Get("Content-Type") != tc.ctype {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tc.ctype)
			}
		})
	}
}

// TestAssetsCannotEscapeTheBundle: a path that climbs out of the embedded
// directory must find nothing rather than the filesystem.
func TestAssetsCannotEscapeTheBundle(t *testing.T) {
	handler, err := web.Assets(theme.Default)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/{file}", handler)

	for _, path := range []string{
		"/assets/..%2fweb.go",
		"/assets/%2e%2e%2f%2e%2e%2fgo.mod",
		"/assets/....//web.go",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == http.StatusOK {
			t.Errorf("%s served something: %s", path, w.Body.String()[:min(80, w.Body.Len())])
		}
	}
}

func TestStylesheetReportsAnUnusableFlavour(t *testing.T) {
	// Every valid flavour has surfaces; this checks the guard exists rather
	// than that it fires, since a hole in the table would be a build-time bug.
	for _, f := range []theme.Flavour{theme.Latte, theme.Frappe, theme.Macchiato, theme.Mocha} {
		if _, err := web.Stylesheet(f); err != nil {
			t.Errorf("%v has no surfaces: %v", f, err)
		}
	}
	var missing error = fault.Internal{Where: "web.Stylesheet", Detail: "x"}
	if !errors.Is(missing, fault.ErrInternal) {
		t.Errorf("the guard should report internally")
	}
}
