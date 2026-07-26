// Package server implements `cq serve`.
//
// The shape that matters is the middleware chain and where the gate sits in it:
//
//	recover → headers → origin → rate limit → authenticate → route
//
// **Authentication runs above the router, not inside the handlers.** A handler
// cannot forget to check a session, because no handler is reached without one.
// The set of things a stranger may see is one short list, `public`, rather than
// a property to be inferred from a dozen registrations — and a route absent from
// every list requires a session, so a new endpoint is protected by default.
package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
	"orc/cq/internal/upgrade"
	"orc/cq/internal/web"
	"orc/theme"
)

// Defaults for the knobs an operator rarely changes.
const (
	// SessionLifetime is how long a login lasts.
	SessionLifetime = 14 * 24 * time.Hour
	// ReadHeaderTimeout bounds how long a slow client may hold a connection
	// before saying what it wants.
	ReadHeaderTimeout = 10 * time.Second
	// MaxRequestBytes bounds an ordinary API request body.
	MaxRequestBytes = protocol.MaxRequestBytes
	// MaxSyncBytes bounds a sync body, which carries a whole snapshot.
	MaxSyncBytes = protocol.MaxSnapshotBytes
	// DefaultHeartbeat is how often an idle event stream reassures the network.
	DefaultHeartbeat = 25 * time.Second
)

// class says what credential a route needs. The zero value is the strictest,
// so a route that names nothing is a route that needs a session.
type class int

const (
	// needSession is the default: a logged-in browser.
	needSession class = iota
	// needNothing is the exemption list, and it is deliberately tiny.
	needNothing
	// needToken is the sync endpoint, which agents reach with a bearer token.
	needToken
	// needEither is a browser *or* a provisioned machine.
	//
	// One route has it: the upgrade. It has to be reachable from the admin panel,
	// which is a session, and from `cq upgrade` on an agent machine, which has a
	// sync token and no password. Both are credentials the operator handed out on
	// purpose, so both are the operator asking.
	//
	// The narrower gate on top of it is Orc's: `cq upgrade` refuses unless the
	// identity running it holds the builtin `upgrade` permission, floor 90. That
	// is the check that stops an *agent* with a shell and the machine's token,
	// which is the case an operator actually worries about.
	needEither
)

func (c class) String() string {
	switch c {
	case needNothing:
		return "public"
	case needToken:
		return "token"
	default:
		return "session"
	}
}

// Options configure a server.
type Options struct {
	// State is the store holding snapshots and the queue.
	State *store.Store
	// Creds is the store holding the password, tokens, and sessions.
	Creds *auth.Store
	// Logger receives diagnostics. Required.
	Logger *slog.Logger
	// Admin controls whether the admin panel is served at all.
	Admin bool
	// Now supplies the current time. Defaults to time.Now.
	Now func() time.Time
	// SessionLifetime overrides the default login duration.
	SessionLifetime time.Duration
	// Secure forces the session cookie's Secure attribute even when the
	// request did not arrive over TLS, for a server behind a TLS proxy.
	Secure bool
	// Heartbeat is how often an idle event stream sends a comment, so a proxy
	// in the middle does not decide the connection is dead. Defaults to
	// DefaultHeartbeat.
	Heartbeat time.Duration
	// Flavour is the colour scheme the site is drawn in. It comes from the
	// same setting as every other Orc tool, so one change restyles them all.
	Flavour theme.Flavour
	// Upgrade says where this machine's checkout and binaries are. Its zero value
	// has no source, and the upgrade endpoint then refuses with that reason —
	// which is right for a server that installs binaries rather than building.
	Upgrade upgrade.Options
	// Restart asks whatever is supervising this process for a new one.
	//
	// Nil means nothing is, and the endpoint says so rather than pretending: a
	// server that exited with nothing to start it again would be a button that
	// takes the site down and leaves it down.
	Restart func()
}

// Server answers HTTP for cq.
type Server struct {
	state    *store.Store
	creds    *auth.Store
	log      *slog.Logger
	admin    bool
	now      func() time.Time
	lifetime time.Duration
	secure   bool

	heartbeat time.Duration
	limiter   *auth.Limiter
	events    *broker

	upgrade upgrade.Options
	restart func()

	assets  http.Handler
	index   []byte
	mux     *http.ServeMux
	classes map[string]class
	handler http.Handler
}

// New builds a server, refusing to start unconfigured.
//
// The refusal is the point: there is no default password, no "set one later",
// and no window in which the site answers without a login.
func New(opts Options) (*Server, error) {
	switch {
	case opts.State == nil:
		return nil, fault.Internal{Where: "server.New", Detail: "no state store"}
	case opts.Creds == nil:
		return nil, fault.Internal{Where: "server.New", Detail: "no credential store"}
	case opts.Logger == nil:
		return nil, fault.Internal{Where: "server.New", Detail: "no logger"}
	}
	if err := opts.Creds.Configured(); err != nil {
		return nil, err
	}

	s := &Server{
		state:    opts.State,
		creds:    opts.Creds,
		log:      opts.Logger,
		admin:    opts.Admin,
		now:      opts.Now,
		lifetime: opts.SessionLifetime,
		secure:   opts.Secure,

		upgrade: opts.Upgrade,
		restart: opts.Restart,

		heartbeat: opts.Heartbeat,
		limiter:   auth.NewLimiter(),
		events:    newBroker(),
		mux:       http.NewServeMux(),
		classes:   map[string]class{},
	}
	if s.now == nil {
		// UTC, like every other clock in Orc. The agent reports in UTC, and one
		// record carrying a local offset beside a Z read as two different
		// notions of time when the two were written side by side.
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.lifetime <= 0 {
		s.lifetime = SessionLifetime
	}
	if s.heartbeat <= 0 {
		s.heartbeat = DefaultHeartbeat
	}

	// The interface is prepared once, at startup: a stylesheet that cannot be
	// generated is a misconfiguration, and the right time to hear about it is
	// before the first request rather than during one.
	assets, err := web.Assets(opts.Flavour)
	if err != nil {
		return nil, err
	}
	index, err := web.Index()
	if err != nil {
		return nil, err
	}
	s.assets, s.index = assets, index

	s.routes()
	s.handler = s.chain()
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Patterns lists every registered route with the credential it requires. The
// route-walking test reads it, which is what makes the exemption list auditable
// rather than a claim.
func (s *Server) Patterns() map[string]string {
	out := make(map[string]string, len(s.classes))
	for pattern, c := range s.classes {
		out[pattern] = c.String()
	}
	return out
}

// Events returns the broker, so a sync can announce that something changed.
func (s *Server) Events() *broker { return s.events }

// route registers a handler and records what it needs. Registration and
// classification happen in one call, so a route cannot exist unclassified.
func (s *Server) route(pattern string, c class, h http.HandlerFunc) {
	s.classes[pattern] = c
	s.mux.HandleFunc(pattern, h)
}

// routes is the whole surface of cq, in one readable place.
func (s *Server) routes() {
	// The exemption list. Everything a stranger may reach is here and nowhere
	// else; three entries, each of which reveals nothing.
	s.route("GET /login", needNothing, s.getLogin)
	s.route("POST /login", needNothing, s.postLogin)
	s.route("GET /api/v1/health", needNothing, s.health)
	// A browser asks for this on its own, with no session and no referrer.
	s.route("GET /favicon.ico", needNothing, s.favicon)

	// The agent's one endpoint.
	s.route("POST /api/v1/sync", needToken, s.sync)

	// Everything below needs a logged-in browser.
	s.route("POST /api/v1/logout", needSession, s.logout)
	s.route("GET /api/v1/session", needSession, s.session)
	s.route("GET /api/v1/machines", needSession, s.machines)
	s.route("GET /api/v1/inbox", needSession, s.inbox)
	s.route("GET /api/v1/archive", needSession, s.archive)
	s.route("GET /api/v1/sent", needSession, s.sent)
	s.route("GET /api/v1/messages/{puid}", needSession, s.message)
	s.route("GET /api/v1/messages/{puid}/check", needSession, s.check)
	s.route("GET /api/v1/convos/{cuid}", needSession, s.convo)
	s.route("GET /api/v1/library", needSession, s.library)
	s.route("GET /api/v1/library/file", needSession, s.libraryFile)
	s.route("GET /api/v1/tasks", needSession, s.tasks)
	s.route("GET /api/v1/tasks/{name}", needSession, s.task)
	s.route("GET /api/v1/queue", needSession, s.queue)
	s.route("GET /api/v1/admin/state", needSession, s.adminState)
	s.route("GET /api/v1/events", needSession, s.stream)

	s.route("POST /api/v1/messages", needSession, s.send)
	s.route("POST /api/v1/messages/{puid}/reply", needSession, s.reply)
	s.route("POST /api/v1/messages/{puid}/read", needSession, s.markRead)
	s.route("POST /api/v1/messages/{puid}/archive", needSession, s.archiveMessage)
	s.route("POST /api/v1/convos/{cuid}/cc", needSession, s.cc)
	s.route("POST /api/v1/queue/{id}/retry", needSession, s.retryAction)
	s.route("DELETE /api/v1/queue/{id}", needSession, s.dropAction)
	s.route("POST /api/v1/queue/clear", needSession, s.clearQueue)

	// Editing the mirrored checkout. Every one queues; none of them writes here.
	s.route("GET /api/v1/fleet", needSession, s.fleets)

	// The fleet verbs, one route per Orc command that changes something. See
	// fleet.go for what is deliberately absent.
	s.route("POST /api/v1/fleet/identities", needSession, s.newIdentity)
	s.route("POST /api/v1/fleet/roles", needSession, s.newRole)
	s.route("POST /api/v1/fleet/permissions", needSession, s.newPermission)
	s.route("POST /api/v1/fleet/identities/{name}/role", needSession, s.assignRole)
	s.route("POST /api/v1/fleet/identities/{name}/move", needSession, s.moveIdentity)
	s.route("POST /api/v1/fleet/identities/{name}/employ", needSession, s.employIdentity)
	s.route("POST /api/v1/fleet/identities/{name}/fire", needSession, s.fireIdentity)
	s.route("POST /api/v1/fleet/identities/{name}/poke", needSession, s.pokeIdentity)
	s.route("POST /api/v1/fleet/identities/{name}/refresh", needSession, s.refreshIdentity)
	s.route("POST /api/v1/fleet/identities/{name}/workspace", needSession, s.setWorkspace)
	s.route("POST /api/v1/fleet/identities/{name}/grant", needSession, s.grantPermission)
	s.route("POST /api/v1/fleet/identities/{name}/revoke", needSession, s.revokePermission)
	s.route("DELETE /api/v1/fleet/identities/{name}", needSession, s.removeIdentity)
	s.route("POST /api/v1/fleet/roles/{name}/authority", needSession, s.assignAuthority)
	s.route("POST /api/v1/fleet/roles/{name}/permissions", needSession, s.assignPermission)
	s.route("POST /api/v1/fleet/roles/{name}/budget", needSession, s.setBudget)
	s.route("DELETE /api/v1/fleet/roles/{name}", needSession, s.removeRole)
	s.route("PATCH /api/v1/fleet/permissions/{name}", needSession, s.editPermission)
	s.route("DELETE /api/v1/fleet/permissions/{name}", needSession, s.removePermission)
	s.route("POST /api/v1/fleet/tend", needSession, s.tendFleet)
	s.route("POST /api/v1/fleet/toolkit", needSession, s.installToolkit)

	// The standing instructions. PUT rather than POST: each one replaces a whole
	// layer, and the same body twice lands in the same place.
	s.route("PUT /api/v1/instruct/system", needSession, s.setInstruct("system", false))
	s.route("DELETE /api/v1/instruct/system", needSession, s.clearInstruct("system", false))
	s.route("PUT /api/v1/instruct/roles/{name}", needSession, s.setInstruct("role", false))
	s.route("DELETE /api/v1/instruct/roles/{name}", needSession, s.clearInstruct("role", false))
	s.route("PUT /api/v1/instruct/identities/{name}", needSession, s.setInstruct("identity", false))
	s.route("DELETE /api/v1/instruct/identities/{name}", needSession, s.clearInstruct("identity", false))
	s.route("PUT /api/v1/instruct/wake", needSession, s.setInstruct("system", true))
	s.route("DELETE /api/v1/instruct/wake", needSession, s.clearInstruct("system", true))
	s.route("PUT /api/v1/instruct/wake/roles/{name}", needSession, s.setInstruct("role", true))
	s.route("DELETE /api/v1/instruct/wake/roles/{name}", needSession, s.clearInstruct("role", true))
	s.route("PUT /api/v1/instruct/wake/identities/{name}", needSession, s.setInstruct("identity", true))
	s.route("DELETE /api/v1/instruct/wake/identities/{name}", needSession, s.clearInstruct("identity", true))

	// Rebuilding and restarting the whole fleet. See upgrade.go for why this is
	// one request that becomes one local upgrade plus one queued action each.
	s.route("POST /api/v1/upgrade", needEither, s.upgradeAll)

	// The task verbs, one route per Macmuffin command that changes something.
	// See tasks.go for why this is a route each rather than one pass-through.
	s.route("POST /api/v1/tasks", needSession, s.createTask)
	s.route("POST /api/v1/tasks/{name}/push", needSession, s.pushTask)
	s.route("POST /api/v1/tasks/{name}/claim", needSession, s.claimTask)
	s.route("POST /api/v1/tasks/{name}/assign", needSession, s.assignTask)
	s.route("POST /api/v1/tasks/{name}/invite", needSession, s.inviteToTask)
	s.route("POST /api/v1/tasks/{name}/kick", needSession, s.kickFromTask)
	s.route("POST /api/v1/tasks/{name}/leave", needSession, s.leaveTask)
	s.route("POST /api/v1/tasks/{name}/scope", needSession, s.scopeTask)
	s.route("POST /api/v1/tasks/{name}/worktree", needSession, s.worktreeTask)
	s.route("POST /api/v1/tasks/{name}/status", needSession, s.statusTask)
	s.route("POST /api/v1/tasks/{name}/subtasks", needSession, s.addSubtask)
	s.route("POST /api/v1/tasks/{name}/complete", needSession, s.completeTask)
	s.route("DELETE /api/v1/tasks/{name}", needSession, s.deleteTask)

	s.route("POST /api/v1/library/write", needSession, s.writeFile)
	s.route("POST /api/v1/library/create", needSession, s.createFile)
	s.route("POST /api/v1/library/delete", needSession, s.deleteFile)
	s.route("POST /api/v1/library/mkdir", needSession, s.makeDir)
	s.route("POST /api/v1/library/rmdir", needSession, s.removeDir)
	s.route("POST /api/v1/library/rmtree", needSession, s.removeTree)

	// The application itself, behind the gate like everything else — the
	// bundle is "inside the cq website".
	s.route("GET /", needSession, s.app)
	s.route("GET /assets/{file}", needSession, s.asset)
}

// chain assembles the middleware, outermost first.
func (s *Server) chain() http.Handler {
	var h http.Handler = s.mux
	h = s.authenticate(h)
	h = s.rateLimit(h)
	h = s.checkOrigin(h)
	h = s.headers(h)
	h = s.recover(h)
	return h
}

// classOf reports what the request's route requires.
//
// A path that matches nothing is treated as needing a session, so a stranger
// cannot map the server by watching which unknown paths 404 and which redirect.
func (s *Server) classOf(r *http.Request) class {
	_, pattern := s.mux.Handler(r)
	if pattern == "" {
		return needSession
	}
	c, ok := s.classes[pattern]
	if !ok {
		return needSession
	}
	return c
}

// recover turns a panic anywhere below into a diagnosed 500 rather than a
// dropped connection.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic serving request",
					"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v))
				s.fail(w, r, fault.Internal{
					Where:  "server",
					Detail: fmt.Sprintf("panic: %v", v),
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// headers applies one policy to every response.
//
// The content policy is strict and the pages are written to satisfy it, rather
// than the policy relaxed to suit the pages: no inline script, no inline style,
// nothing external.
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "private, no-cache")
		next.ServeHTTP(w, r)
	})
}

// checkOrigin refuses a cross-site state-changing request.
//
// Together with a SameSite=Strict cookie and the per-session CSRF token, this
// covers forgery from three directions. It also covers the login form, which
// has no session to carry a token yet.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if safeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if s.crossSite(r) {
			s.log.Warn("cross-site request refused", "method", r.Method, "path", r.URL.Path,
				"origin", r.Header.Get("Origin"), "site", r.Header.Get("Sec-Fetch-Site"))
			s.fail(w, r, fault.Unauthenticated{Reason: "cross-site request"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossSite reports whether a request came from somewhere else.
//
// Sec-Fetch-Site is preferred where present: the browser sets it itself and a
// page cannot forge it, and it is accurate in the cases where Origin is not —
// a sandboxed document posts its own form with `Origin: null`, which is opaque
// and indistinguishable from an attacker's frame. Origin is the fallback for
// clients that send no fetch metadata, and its absence entirely means a
// non-browser caller such as curl, which no page can make a request on behalf
// of.
func (s *Server) crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "cross-site", "same-site":
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	return !sameOrigin(origin, r)
}

// rateLimit slows repeated login failures from one source. It guards only the
// login endpoint: everything else already needs a credential, so there is
// nothing there to guess.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/login" {
			next.ServeHTTP(w, r)
			return
		}
		source := clientIP(r)
		if ok, wait := s.limiter.Allow(source, s.now()); !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
			s.log.Warn("login attempt refused by rate limit", "source", source, "wait", wait)
			s.fail(w, r, fault.Unauthenticated{Reason: "too many attempts"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionKey is how a request carries its verified session to a handler.
type sessionKey struct{}

// authenticate is the gate. Nothing below it runs without the credential its
// route requires.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch s.classOf(r) {
		case needNothing:
			next.ServeHTTP(w, r)

		case needEither:
			// A token if there is one, else the session path below. Tried in that
			// order because a machine presenting a token has no cookie, and a
			// browser has no token — so whichever is present is the one meant.
			if _, err := s.bearer(r); err == nil {
				next.ServeHTTP(w, r)
				return
			}
			sess, err := s.creds.Session(cookieValue(r), s.now())
			if err != nil {
				s.deny(w, r)
				return
			}
			if !safeMethod(r.Method) {
				if err := sess.CheckCSRF(r.Header.Get("X-CSRF-Token")); err != nil {
					s.log.Warn("csrf check failed", "path", r.URL.Path)
					s.fail(w, r, fault.Unauthenticated{Reason: "csrf"})
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(withSession(r, sess)))

		case needToken:
			token, err := s.bearer(r)
			if err != nil {
				s.log.Warn("sync rejected", "path", r.URL.Path, "reason", err)
				s.fail(w, r, fault.Unauthenticated{Reason: "bad token"})
				return
			}
			if err := s.creds.TouchToken(token.ID, s.now()); err != nil {
				// Recording use is bookkeeping; failing to do it is not a
				// reason to refuse a sync that has already authenticated.
				s.log.Warn("could not record token use", "token", token.ID, "error", err)
			}
			next.ServeHTTP(w, r)

		default:
			sess, err := s.creds.Session(cookieValue(r), s.now())
			if err != nil {
				s.deny(w, r)
				return
			}
			if !safeMethod(r.Method) {
				if err := sess.CheckCSRF(r.Header.Get("X-CSRF-Token")); err != nil {
					s.log.Warn("csrf check failed", "path", r.URL.Path)
					s.fail(w, r, fault.Unauthenticated{Reason: "csrf"})
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(withSession(r, sess)))
		}
	})
}

// deny answers an unauthenticated request the way its caller can use: an API
// client gets a status it can branch on, a browser gets the login page.
func (s *Server) deny(w http.ResponseWriter, r *http.Request) {
	if isAPI(r) {
		s.fail(w, r, fault.Unauthenticated{})
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// bearer extracts and verifies an Authorization header.
func (s *Server) bearer(r *http.Request) (auth.Token, error) {
	header := r.Header.Get("Authorization")
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || value == "" {
		return auth.Token{}, fault.Unauthenticated{Reason: "no bearer token"}
	}
	return s.creds.VerifyToken(strings.TrimSpace(value))
}

func safeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func isAPI(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/") }

// sameOrigin compares an Origin header against the host the request arrived on.
func sameOrigin(origin string, r *http.Request) bool {
	host := r.Host
	if host == "" {
		return false
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return trimmed == host
}

// clientIP names the source for rate limiting. It reads the socket address and
// deliberately not X-Forwarded-For: a header a client controls is a header a
// client can use to reset its own limit.
func clientIP(r *http.Request) string {
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", fmt.Errorf("no port in %q", addr)
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr[:i], "["), "]"), addr[i+1:], nil
}
