// Package web serves the interface.
//
// The whole application is one embedded directory, so `cq serve` is a single
// binary with nothing to deploy beside it. Nothing is fetched from anywhere
// else: the content policy forbids it, and the pages are written to satisfy the
// policy rather than the policy relaxed to suit the pages.
//
// The stylesheet's colours are generated from orc/theme, the scheme every Orc
// tool shares, so one setting restyles the website along with every CLI.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"orc/cq/internal/fault"
	"orc/theme"
)

//go:embed app
var files embed.FS

// ThemePath is the generated stylesheet, which is not a file on disk.
const ThemePath = "theme.css"

// surfaces are the three background colours, per flavour.
//
// orc/theme has no role for them, and correctly so: a Role is painted as
// *foreground* text, and a terminal never sets its own background. A web page
// must. When theme grows a surface accessor these twelve values go away and the
// generator reads them from there like everything else.
var surfaces = map[theme.Flavour]struct{ canvas, panel, inset string }{
	theme.Latte:     {"#eff1f5", "#e6e9ef", "#ccd0da"},
	theme.Frappe:    {"#303446", "#292c3c", "#414559"},
	theme.Macchiato: {"#24273a", "#1e2030", "#363a4f"},
	theme.Mocha:     {"#1e1e2e", "#181825", "#313244"},
}

// roleVars maps each CSS custom property to the shared role it draws from.
// Properties are named for what they *are*, not what colour they hold, so a
// change of flavour is a change of one setting and no stylesheet edits.
var roleVars = []struct {
	name string
	role theme.Role
}{
	{"--text", theme.Text},
	{"--heading", theme.Heading},
	{"--title", theme.Title},
	{"--muted", theme.Muted},
	{"--subtle", theme.Subtle},
	{"--frame", theme.Frame},
	{"--primary", theme.Primary},
	{"--secondary", theme.Secondary},
	{"--tertiary", theme.Tertiary},
	{"--accent", theme.Accent},
	{"--info", theme.Info},
	{"--success", theme.Success},
	{"--warning", theme.Warning},
	{"--danger", theme.Danger},
}

// Stylesheet renders the theme as CSS custom properties.
//
// A flavour cq cannot draw — Plain, which means "no colour at all" — falls back
// to the default, because a website with no colours is not a website. The CLI's
// NO_COLOR is about escape sequences in a pipe; it has nothing to say about a
// browser.
func Stylesheet(flavour theme.Flavour) ([]byte, error) {
	if !flavour.Valid() || flavour == theme.Plain {
		flavour = theme.Default
	}
	surface, ok := surfaces[flavour]
	if !ok {
		return nil, fault.Internal{
			Where:  "web.Stylesheet",
			Detail: fmt.Sprintf("no surface colours for flavour %v", flavour),
		}
	}
	p := theme.New(flavour, theme.TrueColour)

	var b bytes.Buffer
	fmt.Fprintf(&b, "/* generated from orc/theme: %s */\n:root {\n", flavour)
	fmt.Fprintf(&b, "  --canvas: %s;\n  --panel: %s;\n  --inset: %s;\n",
		surface.canvas, surface.panel, surface.inset)
	for _, v := range roleVars {
		colour, ok := p.Colour(v.role)
		if !ok {
			return nil, fault.Internal{
				Where:  "web.Stylesheet",
				Detail: fmt.Sprintf("the scheme has no colour for role %v", v.role),
			}
		}
		fmt.Fprintf(&b, "  %s: %s;\n", v.name, colour.Hex())
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// Index returns the application shell.
func Index() ([]byte, error) {
	data, err := files.ReadFile("app/index.html")
	if err != nil {
		return nil, fault.Internal{Where: "web.Index", Detail: err.Error()}
	}
	return data, nil
}

// Assets serves the embedded files and the generated stylesheet.
//
// It is mounted behind the session gate like everything else: the bundle is
// "inside the cq website", and a stranger receives none of it.
func Assets(flavour theme.Flavour) (http.Handler, error) {
	css, err := Stylesheet(flavour)
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(files, "app")
	if err != nil {
		return nil, fault.Internal{Where: "web.Assets", Detail: err.Error()}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.PathValue("file")), "/")
		switch {
		case name == ThemePath:
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			if _, err := w.Write(css); err != nil {
				return
			}
		case name == "" || name == "index.html":
			http.NotFound(w, r)
		default:
			serveFile(w, r, sub, name)
		}
	}), nil
}

// serveFile writes one embedded file with a content type decided by cq rather
// than sniffed, so a file cannot be served as something it is not.
func serveFile(w http.ResponseWriter, r *http.Request, dir fs.FS, name string) {
	data, err := fs.ReadFile(dir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(name))
	if _, err := w.Write(data); err != nil {
		return
	}
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
