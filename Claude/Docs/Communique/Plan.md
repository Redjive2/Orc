# Communiqué — Implementation Plan (Go + vanilla SPA)

Derived from [Vision.md](../../../Docs/Vision.md)'s one-line brief — *"a tool for
communicating with the user via a remote web server"* — and from the two tools it
mirrors, [Mailman](../Mailman/Plan.md) and [Macmuffin](../Macmuffin/Plan.md).
Written to the conventions [Anno](../Anno/Plan.md) established for this tree.

Guiding constraints, in priority order:

1. **Robust** — every error handled, no panics, no partial writes. A queued reply
   is the user's words: losing one is worse than refusing ten.
2. **Honest** — the UI is a mirror of state that lives elsewhere. It must always
   say how stale it is and never show a queued action as a done one.
3. **Simple** — stdlib Go, vanilla HTML/CSS/JS, no build step, no dependencies.
4. **Readable** — mailman's vocabulary (inbox, convo, puid, receipt) is cq's
   vocabulary, in the API and in the UI.

Two facts about the tools cq mirrors shape everything below, and neither is
negotiable:

- **Mailman has no network surface.** Its plan is explicit: *"No delivery daemon,
  no sockets, no ports. The filesystem is the transport."* Macmuffin is the same.
  So the state cq shows lives as files on an agent machine, and something must
  carry it to the web.
- **Both stores are per-machine.** A user with agents on two machines has two
  inboxes. cq is the only place they are ever seen as one.

---

## 1. Semantics recovered from the brief

The brief is an architecture sketch. These are the questions it raises but does
not answer, resolved here so they are decided once.

**"Mirror" is not read-only.** The tool's stated purpose is *communicating* with
the user, and the user "has an inbox like everyone else". A board the user can
only read is a notification feed, not communication. So cq carries mail in both
directions: the mirror flows agent → user, and replies, sends, archives and read
marks flow user → agent.

**The agent machine is the client in both directions.** `cq sync` is specified as
an HTTP client on the agent PC and `cq serve` as the server on the server PC.
That is the correct way round — the server is the reachable side, the agent
machine is typically behind NAT — and it has a consequence the brief does not
state: **the server can never initiate anything.** Everything the user does is
queued on the server and collected by the next sync. The UI must therefore show
pending actions as pending (§9), because between the click and the next sync the
action has not happened.

**Mailman nudges cq after every action.** A mirror that refreshes on a timer is
always a little wrong; one that refreshes when the thing it mirrors changes is
wrong only while the network is. So a state-changing mailman or macmuffin command
fires a sync on completion, as a property of those tools rather than a cron job
someone remembered to set up (§5.2). This makes the agent → user direction
effectively instant.

It does **not** make the reverse direction instant, and the distinction matters:
a nudge happens when an *agent* acts, but the user's replies need collecting even
when every agent is idle. So the periodic sync stays, as the pull path, at a
relaxed interval. Nudges carry freshness; the timer carries latency.

**Sync is a whole-state replacement, not a diff.** Mailman's own reasoning
applies unchanged: mailboxes are small, a full scan is fast, and *"a derived
cache is a second source of truth that can disagree with the first."* Each sync
posts a complete snapshot that replaces the server's copy for that machine. The
server never merges, so it can never be subtly wrong.

**cq reads the other tools through their CLIs, not their files.** Three ways were
available: import their Go packages (impossible across modules for `internal/`,
and coupling if made possible), read their store files directly (a second parser
that will drift from the first), or invoke their commands. The third keeps each
tool's on-disk format private, reuses their validated read paths, and inherits
their authentication for free. It costs one addition to each tool, named in §5.1
and listed in §16.

**The admin panel shows everything.** The brief asks for "the whole mailman
state", and the author reports that server-side exposure is handled by their
hardware and domain setup. So other users' bodies are included, and the flag is
the other way round: `--admin-metadata-only` narrows it for anyone whose
deployment is less contained.

---

## 2. Topology

```
        agent machine                          server machine
  ┌──────────────────────────┐          ┌──────────────────────────┐
  │  mailman store  (files)  │          │   cq serve  :8080        │
  │  macmuffin store (files) │          │   ├── SPA (embedded)     │
  │            ▲             │          │   ├── /api/v1/…          │
  │            │ CLI         │          │   └── state/ (§7)        │
  │  ┌─────────┴──────────┐  │  HTTPS   │            ▲             │
  │  │   cq sync          │──┼─────────▶│  POST /api/v1/sync       │
  │  │                    │◀─┼──────────│  ← queued user actions   │
  │  └────────────────────┘  │          └──────────────────────────┘
  └──────────────────────────┘                       ▲
                                                     │ browser
                                              ┌──────┴───────┐
                                              │   the user   │
                                              └──────────────┘
```

One HTTP round trip carries both directions: the request holds the snapshot and
the results of the previous batch of actions; the response holds the next batch.
Nothing else crosses the wire.

Syncs are triggered two ways, and the pair is what makes the mirror feel live
without polling hard:

- **Nudged**, by mailman or macmuffin, immediately after any command that changed
  something (§5.2). This is the push path and it is effectively instant.
- **Timed**, by `cq sync --watch <interval>`, default five minutes. This is the
  pull path: it collects the user's replies when no agent has acted, and it is
  the backstop for every nudge that was dropped because the network was down.

`cq sync` stays a one-shot command with the loop outside it, so the process is
restartable and the tool is testable.

---

## 3. Trust

The server holds everyone's mail. The author reports that network exposure is
already handled by their hardware and domain setup, so this section is shorter
than the first draft: it covers the one thing that setup cannot cover, which is
who is allowed to look once they reach the site.

**Nothing is visible without logging in.** Not the inbox, not the task board, not
the admin panel, and not the SPA itself. Exactly three routes answer without a
session — `GET /login`, `POST /login`, and `GET /api/v1/health`, which returns
liveness and no data. Everything else, including every static asset of the
application bundle, is behind the session check. The login page is a small
self-contained document with its own inline-free CSS, not the application shell,
so an unauthenticated visitor never receives a byte of the app.

**cq serve refuses to start without credentials configured.** No default
password, no "set one later", no unauthenticated first-run window. A server with
no sync token and no operator password record exits with a usage fault saying
which command sets them. A login gate with a blank default is not a gate.

**Two credentials, two purposes.**

| Credential | Who uses it | Verified by |
|---|---|---|
| sync token | `cq sync`, as `Authorization: Bearer …` | HMAC-SHA256 over a stored salt, constant-time compare |
| operator password | the browser, once, at `/login` | PBKDF2-HMAC-SHA512, then a session cookie |

Both are stored as digests; neither is recoverable from the store. The password
uses a real KDF because it is the one human-chosen secret in Orc, and it is
verified once per session rather than once per request, so the cost is paid
somewhere it is never noticed. `crypto/pbkdf2` has been in the standard library
since Go 1.24, which keeps Orc's zero-dependency rule intact — the first draft of
this plan proposed Argon2id and `golang.org/x/crypto`, and that is no longer
worth a dependency for a threat model the author has already contained.

This is the opposite conclusion to Mailman's, from the same reasoning, and the
two are worth reading together: Mailman argues a password KDF is wrong for a
machine-minted 256-bit key, because guessing is already infeasible and the cost
lands on every command. Both halves invert here.

**Sessions.** A random 32-byte id, stored server-side with an expiry, sent as a
cookie that is `HttpOnly`, `SameSite=Strict`, `Path=/`, and `Secure` when the
request arrived over TLS. `SameSite=Strict` plus a per-session CSRF token on
every state-changing request covers cross-site forgery. Login attempts are
rate-limited per source address, and the failure message never distinguishes a
wrong password from an unconfigured one.

**Transport.** `cq serve` binds `:8080` as the brief specifies and accepts
`--tls-cert`/`--tls-key` to terminate TLS itself, or sits behind a proxy with
`--trusted-proxy` set so the rate limiter sees real client addresses.

**Headers.** `Content-Security-Policy: default-src 'self'` with no
`unsafe-inline`, plus `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, and `X-Frame-Options: DENY`. The SPA is written
to satisfy that policy rather than the policy relaxed to suit the SPA: no inline
scripts, no inline handlers, no external anything (§10).

### 3.1 What crosses the wire

| Data | Default | Flag |
|---|---|---|
| The user's own inbox, archive, conversations — with bodies | included | — |
| Macmuffin pool and task detail | included | — |
| Other users' messages, with bodies, plus receipts and the user list | included | `--admin-metadata-only`, or `--no-admin` to omit entirely |

---

## 4. Commands

`cq <command> <args…>`, matching every other Orc tool.

| Command | Behaviour |
|---|---|
| `cq serve` | Serve the SPA and API on `:8080`. `--addr`, `--tls-cert`, `--tls-key`, `--state`, `--no-admin`. |
| `cq sync` | One sync round trip against `--server`. `--watch <dur>` repeats it (default 5m). `--nudge` is the coalescing form mailman calls (§5.2). `--dry-run` prints what would be sent. |
| `cq status` | Print local sync state: last success, queue depth, last error. No network. |
| `cq admin operator set` | Set or change the operator password, writing the Argon2id record. Reads the password from a prompt or `$CQ_PASSWORD`, never from argv. |
| `cq admin token new` | Mint a sync token, print it once, store only its digest. |

Exit codes follow Anno's table: `0` ok, `1` usage, `2` not found, `4` parse,
`5` i/o, `6` conflict, `70` internal — plus `7` unauthenticated and `8` server
unreachable, which are the two failures unique to a networked tool.

---

## 5. The agent side: `cq sync`

One round trip, five steps, each of which can fail without damaging the others:

1. **Collect.** Read the local mailman and macmuffin state through the source
   adapter (§5.1).
2. **Report.** Attach the results of the previous batch of actions, read from the
   local journal.
3. **Post.** `POST /api/v1/sync` with snapshot and results.
4. **Apply.** Execute the actions in the response, in `seq` order, recording each
   outcome in the local journal before moving to the next.
5. **Record.** Write the new sync cursor. The next sync reports the results.

Applying before recording, and recording each outcome before the next action, is
what makes a crash mid-batch safe: on restart the journal says exactly how far it
got, and actions already applied are skipped by id.

**Delivery is at-least-once on the wire and exactly-once in effect.** The server
re-sends any action it has not seen a result for; the agent keeps applied action
ids in `applied.jsonl` and treats a repeat as a no-op that re-reports the
original result. A failed action reports its error text and is not retried
automatically — a reply that failed because the recipient no longer exists will
fail identically forever, and the user should see why rather than watch it
retry. `cq sync --retry <id>` exists for the cases where retrying is right.

**Local state** lives under `$CQ_HOME`, else `$XDG_STATE_HOME/cq`, else
`~/.cq`:

```
<root>/
  version          format version; an unknown one is a hard, clear error
  applied.jsonl    append-only: action id → outcome, replayed on start
  cursor.json      last successful sync, server-assigned watermark
```

Same journal discipline as Mailman and Macmuffin, for the same reason and with
the same failure rule: a truncated **final** line is an interrupted append and is
dropped with a note; an unparseable line anywhere else is corruption and a hard
error.

### 5.2 Nudges

A state-changing mailman or macmuffin command runs `cq sync --nudge` on
completion. Four rules keep that from turning a mail tool into a fragile one, and
each exists because the alternative is a way for mailman to get worse:

1. **It never blocks.** The child is spawned detached, with its streams closed,
   and the parent does not wait. `mailman send` returns at the speed it always
   did, whatever the network is doing.
2. **It never fails the parent.** A missing `cq` binary, a spawn error, a server
   that is down — all are ignored in the parent entirely. Mail was already
   delivered when the nudge fires; the mirror being late is not a reason to tell
   an agent its message failed. `$CQ_NO_NUDGE=1` turns the whole thing off for
   sandboxes and tests.
3. **It coalesces.** `--nudge` tries the sync lock without blocking. If a sync is
   already running it writes a `pending` marker and exits 0; the running sync
   checks that marker before it finishes and, if set, clears it and goes round
   once more. So a burst of twenty commands produces two syncs, not twenty, and
   the last one always reflects the final state.
4. **It is never the only path.** A dropped nudge is invisible by design, so the
   `--watch` timer is what guarantees convergence. Nudges make the mirror fast;
   the timer makes it correct.

**This is a change to Mailman and Macmuffin, not only to cq.** Both need a
post-command notify step. Macmuffin already has the shape for it — its `outbox/`
mechanism exists precisely to make an external notification durable — and the
cleanest form is a small `internal/notify` package in the common module both
tools already share, invoked from the one place each of them commits a journal
append. It is a dozen lines in each tool and belongs in their plans; §16 records
it.

### 5.1 The source adapter

`internal/source` is the boundary between cq and the tools it mirrors, and it is
deliberately the smallest swappable thing — the same move `internal/identity` is
in Mailman's plan.

```go
type Source interface {
    Inbox(ctx context.Context, opts Scope) (Mailbox, error)
    Convos(ctx context.Context) ([]Convo, error)
    Tasks(ctx context.Context) (Pool, error)
    Admin(ctx context.Context, bodies bool) (AdminState, error)
    Apply(ctx context.Context, a Action) error
}
```

The shipping implementation shells out to `mailman` and `muff`. **This requires
one addition to each tool: a `--json` output mode.** Parsing their human-facing
tables would be building on a presentation format, which is exactly the coupling
the box-drawn output in both plans makes fragile. `--json` is cheap to add now,
while both tools are still plans, and gives cq a contract that will not move.
The JSON shapes cq needs are the obvious projections of each tool's own model:

```
mailman inbox --all --json   → [{puid, mid, ts, from, to[], cc[], subject,
                                 convo{uid,title,index}, read, archived, body}]
mailman check <query> --json → [{mid, recipient, read, at}]
mailman admin users --json   → [{name, created}]
muff pool --json             → [{name, owner, collaborators[], priority,
                                 difficulty, status, subs{done,total}, draft,
                                 scope[], worktree}]
muff info <task> --json      → the above plus steps[]{name, subs[]{name,done}}
```

If either tool instead grows a Go library API, this interface is the one file
that changes. A recorded-fixture implementation backs the tests, so cq's suite
never needs a real mailman installed.

---

## 6. The server side: `cq serve`

Stdlib `net/http` with `http.ServeMux`'s method-and-pattern routing. No router
dependency, no middleware framework — a handful of `func(http.Handler)
http.Handler` wrappers, applied in one visible chain:

```
recover → log → security headers → rate limit → authenticate → CSRF → route
```

`recover` is first so a panic in any later layer becomes a 500 with a request id
rather than a dropped connection, mirroring `cli.Main`'s discipline in Anno.

**`authenticate` sits above the router, not inside the handlers**, and that
placement is the whole of §3's login rule. A handler cannot forget to check a
session, because no handler is reached without one. The exemption list is three
literal paths — `GET /login`, `POST /login`, `GET /api/v1/health` — held in one
slice next to the middleware that reads it, so the set of things visible to a
stranger is one short list rather than a property to be inferred from a dozen
route registrations. A test walks every registered route and asserts that
anything absent from that list answers 401 or 303 without a session; a new
endpoint is therefore protected by default and a deliberately public one has to
be added to a list a reviewer will see.

The SPA is served from an `embed.FS` — behind the same gate, since the bundle is
"inside the cq website" — so `cq serve` is a single binary with nothing to deploy
beside it. Assets carry a content hash in their `ETag` with
`Cache-Control: private, no-cache`, so a browser revalidates, never serves a
stale bundle after an upgrade, and no shared cache keeps a copy.

**Live updates use Server-Sent Events** on `GET /api/v1/events`: one-way,
text/event-stream, stdlib-only, and reconnects on its own. Every sync that
changes anything emits an event, and the SPA refetches. A `Last-Event-ID` header
resumes. Polling every 15 s is the fallback when the stream cannot be
established, so the UI degrades rather than freezes.

---

## 7. Server storage

Root is `--state`, else `$CQ_STATE`, else `/var/lib/cq`, else `~/.cq-server`.

```
<root>/
  version
  operator.json          argon2id params, salt, digest
  tokens/<id>.json       sync token digests, label, created, last-seen
  machines/<id>/snapshot.json   last snapshot from that machine (§5)
  machines/<id>/meta.json       last sync time, protocol version, agent version
  queue/<seq>.json       user actions awaiting collection
  queue/cursor.json      next sequence number
  sessions/<id>.json     browser sessions with expiry
```

Snapshots are written through Anno's commit sequence exactly — temp file in the
same directory, write, `fsync`, `chmod`, `rename`, `fsync` the directory, temp
removed on every failure path. A snapshot replaces its predecessor wholesale, so
there is no merge to get wrong and a half-written snapshot is impossible.

The queue is one file per action, named by sequence, so an append needs no lock
and a collected action is removed by unlink. Actions carry `state`
(`queued` → `sent` → `done` | `failed`), the sync that took them, and the result
text when they finish — which is what lets the UI say *"failed: no such user
`carol`"* instead of silently forgetting.

Snapshots are capped at 32 MiB and rejected with a clear fault above it,
following `source.MaxSize` in Anno. Bodies are markdown only, so the cap is
generous; a store that exceeds it means something is wrong upstream.

---

## 8. The API

Versioned under `/api/v1`. JSON in, JSON out, one error shape everywhere:

```json
{ "error": { "code": "ambiguous", "message": "…", "detail": {…} } }
```

`code` is the Orc fault vocabulary — `usage`, `not_found`, `ambiguous`, `parse`,
`io`, `conflict`, `unauthenticated`, `internal` — so a client can branch on it
without reading prose, and the HTTP status carries the same meaning.

**Unauthenticated — the entire list**

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/login` | The login document. Self-contained, not the app bundle. |
| `POST` | `/login` | Password in, session cookie out. Rate-limited. |
| `GET` | `/api/v1/health` | Liveness. No data, no state. |

Everything below requires a session; everything not in the table above is behind
the gate, including `/`, the SPA bundle, and every asset.

**Sync (token auth)**

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/sync` | Snapshot + previous results in; queued actions out. |

**User (session auth).** Shaped on mailman's verbs, because the brief asks for a
mirror of that API and because agents and the user should be describing the same
operations with the same words.

| Method | Path | Mailman equivalent |
|---|---|---|
| `GET` | `/api/v1/inbox?all=&machine=` | `mailman inbox [--all]` |
| `GET` | `/api/v1/messages/{puid}` | `mailman open id=…` |
| `GET` | `/api/v1/convos/{cuid}?all=` | `mailman convo` |
| `GET` | `/api/v1/archive` | `mailman archive` |
| `POST` | `/api/v1/messages` | `mailman send` |
| `POST` | `/api/v1/messages/{puid}/reply` | `mailman reply` |
| `POST` | `/api/v1/messages/{puid}/read` | `mailman read` |
| `POST` | `/api/v1/messages/{puid}/archive` | `mailman archive <query>` |
| `POST` | `/api/v1/convos/{cuid}/cc` | `mailman cc` |
| `GET` | `/api/v1/messages/{puid}/check` | `mailman check` |
| `GET` | `/api/v1/tasks` | `muff pool` |
| `GET` | `/api/v1/tasks/{name}` | `muff info` |
| `GET` | `/api/v1/admin/state` | the whole mailman state (§3.1) |
| `GET` | `/api/v1/events` | SSE change stream |
| `POST` | `/api/v1/logout` | Destroy the session server-side |

Every `POST` in the user table **enqueues** rather than performs, and returns
`202 Accepted` with the action id and its queue position. Returning `200 OK` for
work that has not happened yet would be the single easiest way to make this tool
lie, so the status code carries the truth and the UI shows it (§9).

---

## 9. The SPA

Five views, hash-routed so the server needs no catch-all rewrite:

| Route | View |
|---|---|
| `#/inbox` | The mailbox. Unread marked `*`, exactly as `mailman inbox` does. |
| `#/message/:puid` | One message, its conversation thread, and reply. |
| `#/archive` | Archived mail, same table. |
| `#/tasks` | Macmuffin pool, with a task card on selection. |
| `#/admin` | Users, all messages, receipts, queue health. |

A sixth document sits outside the SPA entirely: `/login`. It is plain HTML with
its own stylesheet, no JavaScript beyond the form post, and it never loads the
application bundle — an unauthenticated visitor gets a password box and nothing
else. A session that expires mid-use turns the next API call into a 401, and the
SPA redirects to it rather than rendering an empty inbox.

Three rules make the mirror honest, and each is a visible element rather than a
convention:

1. **A staleness clock, always on screen.** `synced 12s ago` in the header,
   turning amber past two minutes and red past ten. A mirror that looks live when
   it is an hour old is worse than no mirror.
2. **Pending actions look pending.** A sent reply appears immediately in the
   thread, dimmed, marked `queued`, and it stays that way until a sync reports
   the result. On failure it turns to `failed` with the error text and a retry
   control. Nothing is ever shown as done because the browser thinks it should be.
3. **Machine provenance is shown when there is more than one.** With a single
   agent machine the column is hidden; with two it appears, because a message's
   inbox is a fact about where it lives.

Vanilla JS, ES modules, no build step, no framework, no dependencies. State is
one immutable store object replaced on each update with a render pass over it —
the same discipline the Go side uses, for the same reason. Roughly: `api.js`
(fetch + error shape), `store.js` (state), `router.js`, `views/*.js`, `dom.js`
(a tiny `h()` helper). No innerHTML with server data anywhere; message bodies are
markdown and are rendered by a small, explicitly-limited renderer that emits
text, emphasis, code, lists and links only, with everything else escaped. That
renderer is the highest-risk code in the SPA and is fuzzed (§13).

---

## 10. Design language

The brief asks for calm, pastel, ultra-minimal, clean, terminal, **dark only**,
at Catppuccin Macchiato or Latte intensity — "nothing crazy". `AGENTS.md` asks
every Orc tool for colour, vertical alignment, tables, box drawing. These agree,
and the CLI tools have set the house style: cq should look like `anno index`
rendered in a browser.

**One theme: Catppuccin Macchiato.** No light mode, no toggle, no
`prefers-color-scheme` branch — which removes an entire class of "correct in one
theme, unreadable in the other" bug, and halves the surface every view has to be
checked against. Macchiato rather than Mocha because Mocha's near-black is the
"crazy" end; Macchiato's `#24273a` base is the calm one, and it is what the brief
named.

| Role | Catppuccin | Hex |
|---|---|---|
| page background | base | `#24273a` |
| panel, frame fill | mantle | `#1e2030` |
| rules, borders | surface0 | `#363a4f` |
| body text | text | `#cad3f5` |
| secondary text | subtext0 | `#a5adcb` |
| muted, timestamps | overlay1 | `#8087a2` |
| unread, success | green | `#a6da95` |
| senders, links | blue | `#8aadf4` |
| conversations | mauve | `#c6a0f6` |
| queued, warnings | yellow | `#eed49f` |
| stale, overdue | peach | `#f5a97f` |
| failed, errors | red | `#ed8796` |

Declared once as custom properties on `:root`, named for their *role* rather than
their colour — `--unread`, not `--green` — so a palette change is one block and
no view mentions a hue.

"Nothing crazy" is a rule the CSS can be held to, so it is written as one: **the
page is `text` on `base`, and an accent appears only where it carries state.** A
sender is blue because sender identity is worth scanning for; a failed action is
red because it needs finding. Nothing is coloured for decoration, no gradients,
no shadows beyond a single `surface0` hairline, and never more than three accents
visible in one view.

- **Type.** One monospace stack throughout, no proportional text anywhere. Three
  sizes. Generous line height — a terminal is dense; a calm terminal is not.
- **Layout.** A fixed character grid, content in `ch` units, columns aligned by
  padding exactly as the CLI aligns them. Box drawing (`╭─╮│╰─╯├┤`) for frames,
  matching Macmuffin's `info` card.
- **Accessibility.** Colour is never the only signal — unread is `*` as well as
  green, failure is the word `failed` as well as red — the same rule the CLI
  follows for `NO_COLOR`. Every pairing above is checked against WCAG AA for body
  text; Macchiato's accents are chosen to clear it on `base`, and any that do not
  are used for glyphs and borders rather than prose. Motion is limited to 120 ms
  opacity fades, dropped under `prefers-reduced-motion`.

A sketch of the inbox, which is the view everything else is judged against:

```
╭─ communiqué ───────────────────────────────── synced 12s ago ─╮
│  inbox 3    archive    tasks    admin                  logout │
╰───────────────────────────────────────────────────────────────╯

   *  41  18:31  boss     RE: work                    parser · 3
   *  40  17:02  alice    scope for fix-the-parser              —
      39  16:44  muff     task assigned: fix-the-parser         —
      38  15:20  carol    re: review                  review · 7

   ╭─ 41 · RE: work ─────────────────────────── boss → you, bob ─╮
   │ The parser change is in. Can you look at the span rules      │
   │ before I merge?                                              │
   ├──────────────────────────────────────────────────────────────┤
   │ ▸ your reply             queued · will send on next sync      │
   ╰──────────────────────────────────────────────────────────────╯
```

`*` and the puid are green, senders blue, the convo column mauve, the timestamp
and the `—` overlay1, `queued` yellow, the frames surface0. Everything else is
`text` on `base`. That is the whole visual system.

---

## 11. Package layout

Module `orc/cq`, matching `orc/anno`.

```
Communique/
  go.mod
  cmd/cq/main.go            thin: parse argv, dispatch, map fault → exit code
  internal/fault/           the Orc error vocabulary, plus unauthenticated
  internal/protocol/        wire types, shared by both sides; the contract
  internal/source/          the mailman/macmuffin adapter (§5.1)
  internal/agent/           cq sync: collect, post, apply, journal
  internal/server/          cq serve: handlers, middleware, SSE
  internal/store/           server-side storage (§7)
  internal/auth/            tokens, operator password, sessions, CSRF
  internal/web/             embed.FS + the SPA
  internal/web/app/         index.html, app.css, *.js
```

`internal/protocol` existing as its own package is the point: both sides compile
against the same types, so a change to the wire format that only one side
follows will not build.

---

## 12. Validation discipline

Carried over from Anno unchanged, because the reasoning did not change:

- Typed faults with position, `errors.Is`-classifiable, mapped to exit codes and
  HTTP statuses in exactly one place each.
- Every constructor validates; no zero value is meaningful.
- Assertions **return** `fault.Internal`, never panic. The one `recover` on each
  side turns a defect into a diagnosed 500 or a diagnosed exit, never a crash.
- Every incoming byte is untrusted: request bodies are size-capped and decoded
  with `DisallowUnknownFields`, path parameters are parsed and range-checked, and
  the snapshot is validated against the protocol version before a single field of
  it is stored.

---

## 13. Testing

- **Unit**, table-driven, per package, as in Anno.
- **Protocol round trip** — every wire type marshals and unmarshals to itself,
  and an unknown field is rejected rather than ignored.
- **Sync integration** — a fake `Source` and an in-process `httptest.Server`,
  driven through: snapshot, queue, collect, apply, report. The properties under
  test are that a crash between any two steps loses nothing, and that redelivery
  of an already-applied action is a no-op.
- **Server integration** — `httptest` against the real handler chain, asserting
  the auth matrix exhaustively: every endpoint, with no credential, a bad
  credential, a token where a session is needed and the reverse.
- **Fuzz** — the markdown renderer (no HTML may ever escape it), the protocol
  decoder, and the query-string parsers. Corpora are checked in, as Anno's are.
- **Browser** — the SPA's pure modules (router, markdown, formatting) are tested
  headlessly by running them under `node --test` if node is present, and skipped
  with a note if not; the view layer is verified by hand against the sketch.

---

## 14. Milestones

| # | Deliverable | Done when |
|---|---|---|
| 1 | `protocol` + `fault` | Wire types round trip; unknown fields rejected; fuzz clean. |
| 2 | `store` + `auth` | Snapshot survives a crash mid-write; the route-walking test proves nothing outside the three-path exemption list answers without a session; `cq serve` refuses to start unconfigured. |
| 3 | `serve` API, no UI | Every endpoint answers correctly under `httptest`, `curl` can drive a whole session. |
| 4 | `source` + `sync` | Against a fake source: snapshot up, actions down, applied exactly once, crash-safe at every step. `--nudge` coalesces a burst of twenty into two syncs and never blocks its caller. |
| 5 | The SPA | Inbox, message, reply, archive against a live server; staleness clock and pending state visible. |
| 6 | Tasks + admin views | Macmuffin pool and the mailman state panel. |
| 7 | Real mailman | Swap the fake source for the CLI adapter once `--json` lands; end-to-end on two machines. |

Milestones 1–4 are useful without a line of UI: at the end of 4 the mirror works
and `curl` is the client. That is the right order for a tool whose risk is all in
the protocol, not the pixels.

---

## 15. What is deliberately not built

- **No push to the agent machine.** The server never initiates; §1 explains why.
- **No multi-user cq.** cq is the operator's window. Agents use mailman directly.
- **No search index.** Same reasoning as Mailman: mailboxes are small.
- **No attachments, no HTML mail.** Markdown only, as Mailman's vision says.
- **No offline write queue in the browser.** An action is queued server-side or
  it did not happen; a second queue in `localStorage` would be a third source of
  truth.
- **No notifications, email or push.** Worth having, but it is a separate
  concern and the staleness clock is the honest minimum.

Each is cheap to add and expensive to remove once relied on.

---

## 16. Decisions to confirm

1. **`--json` on mailman and macmuffin** (§5.1). cq needs a machine-readable
   contract from both. This is the one item that is a dependency on another
   tool's plan rather than a choice inside this one, and it is best settled
   before either is built. The alternative — cq parsing box-drawn tables — is
   not one I would recommend.
2. **The notify step in Mailman and Macmuffin** (§5.2). Nudging is now a
   property of those tools, so both plans need a paragraph and about a dozen
   lines each. Best placed in the common module as `internal/notify`, called
   from the one point where each tool commits a journal append. This and item 1
   are the two things cq needs from its neighbours.
3. **Settled: server-side credentials are not hardened further.** The author
   reports hardware and domain setup already contains the exposure, so §3 drops
   Argon2id and `golang.org/x/crypto` for stdlib `crypto/pbkdf2`, keeping Orc
   dependency-free. Revisit only if the server is ever moved somewhere less
   contained.
4. **Settled: nothing is visible without a login**, including the SPA bundle
   itself, enforced above the router rather than per handler (§6).
5. **Settled: one theme, Catppuccin Macchiato** (§10). No light mode, no toggle.
6. **Multi-machine support from day one** (§1). Costs one field in the protocol
   and one column in the UI. Dropping it now would be cheap; adding it later
   would change the wire format and the store layout.
7. **Actions queue rather than apply** (§8). A consequence of the topology, not a
   preference. Nudges do not help here — they fire on agent activity, and the
   user's replies need collecting when agents are idle — so the `--watch`
   interval is what the user feels when they hit send. Five minutes is the
   suggested default; a minute costs little if that wait is annoying.
8. **SSE rather than WebSocket** (§6). One-way is all this needs, and it is
   stdlib-only. A WebSocket would be required only if the UI ever drives the
   agent machine live, which §15 rules out.
