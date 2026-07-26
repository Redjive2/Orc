package server

import (
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"

	"orc/cq/internal/fault"
)

// The login document is deliberately not the application. An unauthenticated
// visitor receives a password box and nothing else — no bundle, no route map,
// no hint of what is behind it.
//
// Its stylesheet is inline, and allowed by a SHA-256 hash in the content policy
// rather than by 'unsafe-inline'. That keeps the exemption list at three routes
// — a separate stylesheet would need a fourth — while keeping the policy strict.
const loginStyle = `
:root { color-scheme: dark; --base:#24273a; --text:#cad3f5; --muted:#8087a2;
        --frame:#5b6078; --accent:#c6a0f6; --bad:#ed8796; }
* { box-sizing: border-box; }
body { margin:0; min-height:100vh; display:grid; place-items:center;
       background:var(--base); color:var(--text);
       font:14px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; }
form { width:min(28rem, 92vw); padding:1.5rem 1.75rem;
       border:1px solid var(--frame); border-radius:2px; }
h1 { margin:0 0 1.25rem; font-size:1rem; font-weight:600; color:var(--accent);
     letter-spacing:.04em; }
label { display:block; margin-bottom:.4rem; color:var(--muted); font-size:.85rem; }
/* 16px is not a taste: below it, iOS zooms the page when the field takes focus,
   and this is the first thing anybody touches on a phone. min-height is the
   smaller of the two platform tap-target guidelines, so it satisfies both. */
input { width:100%; padding:.55rem .7rem; background:transparent; color:var(--text);
        border:1px solid var(--frame); border-radius:2px; font:inherit;
        font-size:16px; min-height:44px; }
input:focus { outline:none; border-color:var(--accent); }
button { margin-top:1rem; width:100%; padding:.55rem; background:transparent;
         color:var(--accent); border:1px solid var(--accent); border-radius:2px;
         font:inherit; font-size:16px; min-height:44px; cursor:pointer; }
button:hover { background:var(--accent); color:var(--base); }
p.bad { margin:1rem 0 0; color:var(--bad); font-size:.85rem; }
`

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>communiqué</title>
<link rel="icon" href="/favicon.ico" sizes="any">
<style>{{.Style}}</style>
<form method="post" action="/login">
  <h1>communiqué</h1>
  <label for="password">password</label>
  <input id="password" name="password" type="password" autocomplete="current-password"
         autofocus required>
  <button type="submit">enter</button>
  {{if .Failed}}<p class="bad">not authenticated</p>{{end}}
</form>
`))

// styleHash is the content policy token that permits exactly this stylesheet
// and nothing else. Computing it from the text means it cannot drift.
var styleHash = func() string {
	sum := sha256.Sum256([]byte(loginStyle))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

const cookieName = "cq_session"

func cookieValue(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// getLogin serves the password box. An already-valid session is sent onward
// rather than asked to log in twice.
func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if _, err := s.creds.Session(cookieValue(r), s.now()); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, false)
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, failed bool) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src "+styleHash+"; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if failed {
		w.WriteHeader(http.StatusUnauthorized)
	}
	data := struct {
		Style  template.CSS
		Failed bool
	}{Style: template.CSS(loginStyle), Failed: failed}
	if err := loginPage.Execute(w, data); err != nil {
		s.log.Warn("could not write the login page", "error", err)
	}
}

// postLogin exchanges a password for a session.
//
// Failure is uniform: the same message, the same status, whether the password
// was wrong, the record was damaged, or no password has ever been set.
func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	source := clientIP(r)

	if err := r.ParseForm(); err != nil {
		s.limiter.Fail(source, s.now())
		s.renderLogin(w, r, true)
		return
	}
	password := r.PostFormValue("password")

	if err := s.creds.VerifyPassword(password); err != nil {
		s.limiter.Fail(source, s.now())
		s.log.Warn("login failed", "source", source, "reason", err)
		if wantsJSON(r) {
			s.fail(w, r, fault.Unauthenticated{})
			return
		}
		s.renderLogin(w, r, true)
		return
	}

	cookie, sess, err := s.creds.NewSession(s.now(), s.lifetime)
	if err != nil {
		s.fail(w, r, fault.Internal{Where: "server.login", Detail: err.Error()})
		return
	}
	s.limiter.Succeed(source)
	s.log.Info("login", "source", source, "expires", sess.Expires)

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cookie,
		Path:     "/",
		Expires:  sess.Expires,
		HttpOnly: true,
		Secure:   s.secure || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})

	if wantsJSON(r) {
		s.ok(w, r, map[string]any{"csrf": sess.CSRF, "expires": sess.Expires})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// logout destroys the session on the server, so a copied cookie stops working
// rather than merely being discarded by one browser.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.creds.EndSession(cookieValue(r)); err != nil {
		s.fail(w, r, fault.Internal{Where: "server.logout", Detail: err.Error()})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure || r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	s.ok(w, r, map[string]any{"ok": true})
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// app serves the application shell.
func (s *Server) app(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.fail(w, r, fault.NotFound{What: "page", Name: r.URL.Path})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(s.index); err != nil {
		s.log.Warn("could not write the application shell", "error", err)
	}
}

// asset serves the stylesheet, the scripts, and the generated theme.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	s.assets.ServeHTTP(w, r)
}
