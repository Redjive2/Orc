# Mailman — Implementation Plan (Go)

Derived from [Vision.md](../../../Docs/Mailman/Vision.md) and
[Reference.md](../../../Docs/Mailman/Reference.md), and written to the
conventions [Anno](../Anno/Plan.md) already establishes for this tree.

Guiding constraints, in priority order:

1. **Robust** — every error is handled and carries position; no panics, no
   silent truncation, no partial writes, no lost mail. A mailbox is other
   agents' only channel: losing one message is worse than refusing ten.
2. **Immutable** — a sent message is never edited. Mutable state (read flags,
   archive, puid assignment) is an append-only journal replayed into a frozen
   view. Mutation is confined to short builder scopes that yield frozen values.
3. **Simple** — small packages, one job each, no frameworks, stdlib only.
4. **Readable** — the spec's vocabulary (inbox, convo, puid, query, receipt) is
   the code's vocabulary.

Mailman differs from Anno in one structural way, and it drives most of what
follows: **Anno is single-process over files the user owns; Mailman is
multi-process over one shared store.** Several agents run concurrently, in
different working directories, and none of them coordinate. Concurrency safety
is therefore not a hardening pass at the end — it is the shape of the storage
design in §3.

---

## 1. Semantics recovered from the spec

The reference is a CLI shape, not a specification. These are the behaviours it
implies but does not state, resolved here so they are decided once.

**Mail is per-recipient, not global.** `inbox` shows unread messages, `read`
marks them read "visible for all recipients", and `check` reports who has and
has not read. So a message has one immutable body and *N* per-recipient states.
Read state must be readable by every recipient, not just its owner.

**`puid` is per-user and permanent.** The reference lists messages "by
persistent unique identifier (puid)" and shows `id="0"`, a small integer. A
global counter would leak other users' traffic volume and would need a global
lock on every send. So: each user assigns puids from its own monotone counter as
mail lands, starting at 0, and never reuses one — a puid stays valid as a way to
name a message even after it is archived, and `prune` retires it permanently
rather than freeing it.

**A message's recipient set is fixed at send time; `cc` extends the
conversation.** The reference is explicit that `cc` "works via a special email,
so mailman check works on it". So `cc` appends a real message of kind `cc` to
the conversation, addressed to the added user and to the existing participants
(non-blind — everyone sees the addition). That cc notice is what lands in the
new user's inbox with its own puid; the conversation's *prior* messages become
readable through `convo`, but do not retroactively appear in their inbox.
Backfilling an inbox with history a user never received would make the unread
count meaningless.

This only works if conversation membership is a **stored set** rather than
something re-derived from each message's recipients. The two differ exactly when
someone is cc'd in, and deriving it per message means the new participant
silently drops out of the next reply that answers an older message. So a
conversation records its participants, `cc` appends to them, `reply` addresses
them, and `convo` requires membership in them — a non-member being told the
conversation does not exist, rather than that they may not see it, so the
command cannot enumerate what threads are going on.

**A conversation is created by `reply`, not by `send`.** `send` produces a
standalone message. `reply` "starts a conversation rooted on the given message,
if need be" — so the root message is retrofitted into a new conversation the
first time someone replies to it, and subsequent replies join that one. A
message therefore belongs to zero or one conversation, and its index within the
conversation is its position in send order.

**`open` picks the most recent match; every other query command applies to all
matches.** `mailman open from="boss"` is documented as "most recent message from
boss" — so `open` deliberately narrows where Anno's `read` would refuse as
ambiguous. That asymmetry is correct here (mail is inherently time-ordered) but
it is dangerous when silent, so `open` prints a one-line note on stderr when it
had more than one candidate, naming how to see the rest. `archive`, `prune`,
`read`, `check`, and `cc` operate on the **whole** match set. `cc` is the
exception that needs care: it resolves the query to a *conversation*, and
refuses if the matches span more than one.

**Auth is per-invocation, not ambient trust.** "Authentication happens on every
request via a privately stored key" — so every command except `help` resolves a
credential, verifies it against the store, and fails closed. There is no
"current user" that survives a failed check, and no session cache that could
outlive a revoked account.

**Bodies are markdown, and markdown is untrusted text.** A body may contain
anything, including text that looks like Mailman's own frontmatter delimiters.
Storage must be unambiguous under adversarial bodies (§3), not merely under
polite ones.

---

## 2. Identity

`Vision.md` places account control outside this tool: *"User accounts are
controlled via Orc remote auth."* `Reference.md` has no `auth` command. So
Mailman **consumes** an identity, it does not issue one, and it holds no session
state at all — the sentence "authentication happens on every request" is taken
literally.

That leaves a boundary to define rather than a mechanism to build, since Orc's
remote auth is not yet specified. The boundary is `internal/identity`, and it is
deliberately the smallest thing that can be swapped: **`$ORC_USER` and
`$ORC_KEY`, and nothing else.**

One source, not a search path. Orc spawns the agent, so Orc controls the
agent's environment, and a single source means there is no precedence order in
which a typo in one place silently authenticates as whoever another place names.
Both variables must be present together; a half-set environment names the
missing half rather than falling through, because there is nothing to fall
through to. Nothing in this package touches the filesystem, which is why it has
no injected operations and no test that needs a temporary directory.

An earlier draft also accepted a `0600` credential file and `--user`/`--key`
flags. Both are gone: the file added a mode-check path and a precedence rule to
protect a secret that Orc is already placing in the environment, and the flags
would have put a key in argv, where `ps` can read it.

### 2.1 Users

A user name is normalised before use: NFC, trimmed, lowercased, and validated
against `^[a-z0-9][a-z0-9._-]{0,63}$`. Reserved names (`all`, `system`,
`mailman`, `.`, `..`) are rejected. Normalisation happens exactly once, in
`user.Parse`, and the rest of the program only ever handles a `user.Name` value
that cannot be constructed any other way — which is also what makes a name safe
to use as a path element.

### 2.2 Keys

Keys are never stored. `users/<name>/user.json` holds a 32-byte random salt and
`HMAC-SHA256(key, salt)`, compared with `crypto/subtle.ConstantTimeCompare`. A
user whose record is malformed, truncated, or unreadable fails authentication;
there is no path through the verifier on which a damaged record becomes "no key
required".

**Why HMAC and not PBKDF2.** A password KDF exists to make guessing a
*low-entropy human secret* expensive. This key is not one: Orc mints it, a
process stores it, and no human types it. Against a 256-bit random key, 200 000
iterations buy nothing an attacker would notice — and they would cost that much
work on *every single command*, since there is no session to amortise them over.
So the record carries an explicit `algo` tag, keys are required to be at least
32 bytes, and the reasoning is written down rather than left as an apparent
oversight. If a human-chosen key ever becomes a case, `algo` is where a real KDF
goes.

Keys are accepted from the environment only, never from argv, because argv is
world-readable in `ps`. This is a departure from Anno's habit of taking
everything as an argument: here the argument form is a leak, so it does not
exist.

### 2.3 Provisioning

Orc owns account creation, and Orc's auth does not exist yet, so Mailman would
be untestable and unusable in the meantime. The gap is filled by one command
outside the reference's CLI — `mailman admin user add|remove|list` — which
writes exactly the records described above and nothing else. It is marked in
help as a stand-in, and when Orc writes those records directly the command can
be deleted without touching any other package. Listed in §11 to confirm.

---

## 3. Storage

Root is `$MAILMAN_HOME`, else `$XDG_DATA_HOME/mailman`, else `~/.mailman`.

```
<root>/
  version                      format version; an unknown one is a hard, clear error
  users/<name>/user.json       algo, salt, key digest, created
  users/<name>/journal.jsonl   append-only per-user state (§3.2)
  messages/<ab>/<mid>.msg      immutable message, write-once (§3.1)
  messages/<ab>/<mid>.rcpt/    read receipts, one file per recipient (§3.3)
  convos/<cuid>.jsonl          append-only conversation membership and order
  lock                         advisory lock, held only for appends
```

`<ab>` is the first two hex characters of the message id — a flat directory of
tens of thousands of files is slow to list on every `inbox`.

A message id is `<unix-micros-hex>-<8 random hex>`: sortable by send time
without a lookup, unique across processes without coordination, and carrying no
sender identity in its name.

### 3.1 The message file

Write-once, never edited, never rewritten. Format is a length-prefixed header
followed by the raw body:

```
mailman/1
id: 019a3f…-7c2e91b4
from: boss
to: alice, bob
cc: carol
subject: RE: work
convo: 019a3f…-0000
index: 3
sent: 2026-07-24T18:31:04.512Z
bytes: 1284

<1284 bytes of markdown, verbatim>
```

The `bytes:` count is what makes the body unambiguous: the reader consumes
exactly that many bytes and does not scan for a delimiter, so a body containing
`mailman/1` or a blank line or anything else cannot forge a header. Header
values are escaped (`\n`, `\\`) and validated on parse; a header line without a
colon, an unknown key, a duplicated key, or a byte count that does not match the
file's remaining length is a `fault.Parse` naming the line.

Written through Anno's commit sequence exactly: temp file in the same directory,
write, `fsync`, `chmod`, `rename`, `fsync` the directory, remove the temp file
on every failure path. Since messages are write-once, a `rename` onto an
existing id is a conflict, not an overwrite.

### 3.2 The journal

Per-user mutable state is an append-only JSONL file. One event per line:

```
{"op":"deliver","mid":"019a3f…","puid":41,"at":"2026-07-24T18:31:04.512Z"}
{"op":"read","mid":"019a3f…","at":"…"}
{"op":"archive","mid":"019a3f…","at":"…"}
{"op":"prune","mid":"019a3f…","at":"…"}
```

Appending is the only write. State is a left fold over the journal, computed
fresh on each command — mailboxes are small, and a derived cache is a second
source of truth that can disagree with the first.

This is chosen for its failure mode. A process killed mid-append leaves a
truncated final line; replay drops an unparseable **final** line with a note on
stderr and continues, because an interrupted append can only damage the tail. An
unparseable line anywhere *else* is corruption, not interruption, and is a hard
error — silently skipping it would silently drop mail. A rewrite-in-place design
has no equivalent of "only the tail can be wrong".

Appends take the advisory lock (`flock` on unix, `O_CREATE|O_EXCL` lock file
elsewhere, behind one interface with a `//go:build unix` split, following Anno's
`fifo_unix_test.go` precedent), open with `O_APPEND`, write one line, `fsync`.
Reads take no lock: a torn tail is already handled, and blocking readers behind
writers is how a mail tool becomes the thing agents avoid using.

Puid assignment is the one operation needing real mutual exclusion, and it is
covered: the puid is chosen while holding the lock, as `max(existing)+1` read
from the journal being appended to.

### 3.3 Receipts

A read receipt is `messages/<ab>/<mid>.rcpt/<user>.json`, written once by that
user with the timestamp. Receipts live with the message rather than with the
user because `check` must read *other* users' state, and this makes that a
directory listing instead of a scan of every user's journal. Each user writes
only their own file, so two recipients marking the same message read never
contend.

Read state is therefore recorded twice — in the reader's journal and as a
receipt. That is deliberate redundancy with a defined precedence: the receipt
directory is authoritative for `check`, the journal for the reader's own
`inbox`, and a `verify` command (§4) reports any divergence rather than papering
over it.

---

## 4. Commands

| Command | Behaviour |
|---|---|
| `inbox [--all]` | Unread messages, `*`-marked; `--all` includes read. Excludes archived. |
| `open <query>` | Most recent match, header then body. Notes on stderr when it narrowed. |
| `convo <cuid> [--all]` | The inbox table, restricted to one conversation, in index order. |
| `send <subject> <to...> <content>` | One message to every recipient. `-` reads the body from stdin. |
| `reply <query> <subject> <content>` | Root a conversation if needed, then append. Recipients are the parent's participants. |
| `archive [query]` | Archive every match; with no query, show the archive table. |
| `prune <query>` | Delete archived matches permanently. Refuses a query matching non-archived mail. |
| `read <query>` | Mark every match read. |
| `check <query>` | Per-message table of who has and has not read. |
| `cc <query> <user>` | Add a user to the matched conversation via a `cc` message. |
| `admin user …` | Provisioning stand-in until Orc remote auth lands (§2.3). |
| `verify` | Not in the reference; see below. |

Every command but `help` and `admin` authenticates first (§2), so a failure to
resolve a credential is reported before any argument is even parsed — an agent
with no identity should be told that, not told its query is malformed.

`verify` walks the store, re-parsing every message, replaying every journal, and
reconciling receipts, and reports what is wrong without changing anything. It
exists because a store that several unsupervised agents write to needs a way to
answer "is this healthy?" that is not "read the source". It is additive and
optional — listed in §11 as a decision to confirm.

Recipient lists are deduplicated, normalised, and must be non-empty; sending to
a non-existent user is `ErrNotFound`, never a silent drop. Sending to oneself is
allowed — agents use it as a scratch note.

**Destructive commands.** `prune` is the only irreversible operation. It refuses
an empty query, refuses matches outside the archive, prints the full list of what
it will delete, and requires `--yes` when stdin is not a terminal — which for
every agent is always. Deleted message files are removed only after the prune
event is journaled, so a crash mid-prune leaves the message unreferenced rather
than leaving a dangling reference.

Exit codes, extending Anno's so hooks can branch uniformly:
`0` ok · `1` usage · `2` not found · `3` ambiguous · `4` parse · `5` i/o ·
`6` conflict · `7` auth · `70` internal. Diagnostics to stderr, output to stdout.

---

## 4a. Presentation

`AGENTS.md` asks every Orc tool for colour, vertical alignment, tables, box
drawings, and diagrams. Mailman's output is almost entirely tabular, so this is
a first-class concern rather than a coat of paint.

**Box-drawn tables.** Every listing (`inbox`, `convo`, `archive`, `check`) is a
Unicode box table with a titled header bar, a column rule, and per-column
alignment — numbers right, text left, timestamps in a fixed width so they form a
column the eye can scan. Layout is two passes, as in Anno's `render`: measure
every cell, then draw fixed-width rows. Every width is clamped to a sane minimum
so a degenerate input (no mail, empty subject, a user with no name width) still
produces a well-formed table.

**A message is a card**; a conversation is a **thread diagram** with connective
gutters, so the shape of a reply chain is visible without reading it.

**Colour is a layer, never information.** Every colour is redundant with a
glyph or a word: unread is `*` *and* bright, read is `·` *and* dim. A pipe
through `grep` therefore loses nothing. Golden tests run with colour off, and a
separate test asserts the escape sequences appear when it is on — so the table
layout is pinned by goldens that are readable in a diff.

The colours themselves are not Mailman's. They come from `orc/theme`, the
Catppuccin scheme every Orc tool shares, and `internal/style` only decides what
is a heading rather than what a heading looks like. Colour is enabled only when
the stream is a terminal, and disabled by `NO_COLOR`, `TERM=dumb`,
`ORC_THEME=none`, `--no-color`, or `ORC_AGENT` — which is absolute, because an
agent's output is another program's input. See [Theme.md](../Theme.md).

**Overlong cells are truncated with `…`, never wrapped**, so a row is always one
line and columns stay aligned; the full value is always available from `open`.
Width is measured in runes, and East Asian wide runes count as two, so a subject
in CJK does not shear the table.

This lives in two packages: `internal/style` (palette, capability detection,
width measurement, truncation) and `internal/render` (layout and drawing).
`style` is the only package that knows an escape sequence exists.

---

## 5. The query language

The reference gives `field="value"`, `&`, `|`, and grouping by precedence in
`'from="boss" & subject="RE: work"'`. Formalised:

```
query := or
or    := and { "|" and }
and   := term { "&" term }
term  := "(" query ")" | "!" term | predicate
pred  := field op value
op    := "=" | "!=" | "~"          (~ is substring, case-folded)
value := '"' … '"' | "'" … "'" | bare
```

`|` binds loosest, then `&`, then `!`. Parens, `!`, and `~` are additions to the
reference — the documented forms parse identically under this grammar, so
nothing breaks, and mark them in §11.

Fields: `id` (puid), `mid`, `from`, `to`, `cc`, `subject`, `body`, `convo`,
`title`, `index`, `unread`, `archived`, `before`, `after`, `kind`. An unknown
field is a parse error listing the valid ones — never a predicate that silently
matches nothing, which is how a `prune` typo becomes a no-op and a `read` typo
becomes a lie.

`before`/`after` take RFC3339 or a relative form (`2h`, `7d`). User-valued
fields compare on normalised names. `subject` and `body` are case-sensitive
under `=` and case-folded under `~`.

The parser is a hand-written lexer plus recursive descent producing an immutable
AST; every error carries a byte column into the query string, and the CLI prints
a caret line under the offending character. Evaluation is a pure function of
`(AST, message view)` with no I/O, which is what makes the whole language
exhaustively table-testable and fuzzable.

Bare values are permitted (`from=boss`) but stop at whitespace and operators;
anything else requires quoting. Unterminated quotes, trailing operators, and
empty groups are all distinct, named parse errors.

---

## 6. Package layout

Module at `Orc/Mailman/go.mod`, module path `orc/mailman` — matching `orc/anno`.

```
Mailman/
  go.mod
  cmd/mailman/main.go     thin: build App from os streams, dispatch, exit
  internal/fault/         error vocabulary; Anno's, plus Auth
  internal/clock/         injectable time; the real one and a deterministic fake
  internal/user/          name normalisation, key digest, verification
  internal/identity/      the Orc credential boundary (§2)
  internal/mail/          Message, Envelope, Body — immutable values + codec (§3.1)
  internal/query/         lexer, parser, immutable AST, pure evaluator
  internal/store/         paths, locking, atomic write, journal append and replay
  internal/view/          store → frozen inbox / convo / archive / receipt views
  internal/style/         roles onto orc/theme, plus width and truncation (§4a)
  internal/render/        the tables, cards, and thread diagrams; pure, two-pass
  internal/cli/           one method per command, each (args) → error
  internal/fixture/       the golden corpus (§8)
```

Only `store`, `identity`, and `cli` touch the filesystem; only `clock` touches
time; only `user` and `mail` touch `crypto/rand`. Everything in `mail`, `query`,
`view`, and `render` is a pure function of its input. That boundary is what makes
the test suite cheap, and it is the same boundary Anno draws.

---

## 7. Validation discipline

The house rule, applied without exception:

- **No meaningful zero values.** Every domain type has unexported fields and a
  `New…(…) (T, error)` constructor that validates. A zero `Message` cannot be
  produced outside `mail`.
- **A private `validate() error` on every type with invariants**, run at
  construction *and* again on any value that arrives by decoding — because a
  constructor proves nothing about bytes that came off a disk another process
  wrote.
- **Entry guards and exit guards.** A function validates its arguments on entry;
  a function computing derived data re-checks the postcondition before returning
  it. `fault.Check(cond, where, format, args…)` is the assertion primitive, and
  it *returns* an `Internal` error — assertions report, they do not abort.
  `source.File.validate` in Anno is the model: a defect surfaces at construction
  rather than as corruption much later.
- **Decoders are strict.** `json.Decoder` with `DisallowUnknownFields`, every
  field range-checked, every timestamp parsed and bounded, every user name
  re-normalised. An unknown field means a newer Mailman wrote this store, and
  guessing is worse than saying so.
- **Bounds on everything from outside.** Body size (16 MiB), subject length,
  recipient count, query length and nesting depth, journal line length. Each is
  a named constant with the reason in its doc comment.
- **No `panic` outside genuinely impossible states**, and none of those; `cli`
  recovers at the top and converts any panic into `Internal` + exit 70, exactly
  as Anno's `cli.Main` does.
- **Write errors are errors too.** Stdout is written through one `say` helper
  that returns its error, as Anno's `cli.say` does, so a closed pipe fails the
  command instead of being dropped. The only discards are explicit `_, _ =`,
  each with a comment saying why nothing can be done: the top-level diagnostic
  write (stderr is where a stderr failure would be reported), the directory
  `fsync` after a successful rename, and temp-file cleanup on an
  already-failing path. No bare `_ = err` anywhere.
- **Every `os`/`io` error wrapped** with `%w`, the operation, and the path.

Errors are typed and carry position, unwrapping to sentinels so exit codes are
derived mechanically in exactly one place. Mailman adds `ErrAuth` /
`fault.Auth{User, Reason}` to Anno's set; the rest are reused verbatim so the two
tools' exit codes mean the same things. Multi-problem inputs report **all**
problems via `errors.Join`, so an agent fixing a store gets one round trip.

Auth failures are deliberately vague to the user (`authentication failed`) and
specific in the store's own audit line — distinguishing "no such user" from
"wrong key" is an enumeration oracle, and agents share a machine.

---

## 8. Testing

Testing is the point of this plan, so it is specified before it is written.
Conventions follow Anno's: `package foo_test` for behaviour, `export_test.go`
where an internal guard is unreachable through the public API, table tests as the
default shape, and `internal/fixture` as the single source of golden data.

**Table tests** — every parser, every predicate, every command's argument
handling, every exit-code mapping. The grid for `query` is the whole grammar of
§5 crossed with malformed forms: unterminated quotes, unknown fields, trailing
operators, empty groups, depth overflow, non-UTF-8, embedded NUL.

**Golden tests** — `fixture` holds a small store (four users, two conversations,
a cc, an archived message, a pruned puid) and the exact rendered output of
`inbox`, `inbox --all`, `convo`, `archive`, and `check` against it. A change to
the table layout breaks exactly one constant, as Anno's `fixture.ExampleGo` does.

**Fuzz targets** — `query.Parse` (no panic, no hang, errors carry an in-range
column), `mail.Decode` (no panic; decode∘encode is identity on anything that
decodes), `user.Parse` (output always re-parses to itself), `user.Decode`,
`style` width and truncation, and the journal fold. The journal's recovery rules
are split out of `Replay` as a pure function over bytes precisely so they can be
fuzzed as one: a target that built a store per iteration managed a few hundred
inputs a second, and the pure one manages hundreds of thousands.

**Property tests**
- `Decode(Encode(m)) == m` over generated messages, including bodies containing
  the header delimiter, CRLF, lone CR, and no trailing newline.
- Replaying a journal is deterministic, and order-independent for commuting
  events — every permutation of marks on distinct messages folds to the same
  mailbox, so a mailbox cannot depend on how two agents happened to interleave.
  Puids are strictly increasing and never reused across archive/prune.
- `send` then `inbox` shows the message for every recipient and nobody else.
- Any prefix of a journal replays without error — the crash-consistency property
  stated as a test rather than a hope.

**Fault injection** — the filesystem is an `ops` struct, exactly as
`edit.commit` does it in Anno, with `export_test.go` setters. Every call gets a
test that fails it and asserts: the store is unchanged, the error is the right
sentinel, and no temp file survives. Same for a full disk (short write), a
read-only store, a lock held by someone else, and a `rename` that fails after a
successful `fsync`.

**Concurrency** — the tests Mailman exists to pass:
- 64 goroutines sending to one user: every message arrives exactly once, puids
  are unique and contiguous, the journal parses.
- 8 real subprocesses (`go test` re-executing the test binary) doing the same,
  which is the only way to test `flock` honestly.
- Two recipients marking the same message read simultaneously: both receipts
  land, `check` reports both.
- `prune` racing `open` on the same message: `open` either succeeds or returns
  `ErrNotFound`, never a partial read.

**End-to-end CLI tests** — drive `cli.Main` with `bytes.Buffer` streams and a
`t.TempDir()` store, asserting stdout, stderr, and exit code together. Every
example invocation in `Reference.md` is a test case that must reproduce.

**Hygiene** — `go test -race ./...` on every package, `-count=2` to catch state
leaking between runs, and the fuzz corpora committed. No test reads the real
`$HOME`; `store.Root` is resolved from an injected environment.

---

## 8a. What implementation changed

Five things turned out differently once the code existed. Each is recorded here
rather than quietly folded into the sections above, so the plan stays honest
about what was designed and what was learned.

**A sender keeps a copy of their own outgoing mail.** `check` is supposed to
answer "who has read what I sent", and it could not: a sender is not a
recipient, so a sent message entered nobody's journal but its recipients'. The
sender now gets a copy, filed already-read and excluded from the inbox by
`view.Row.Mine`. A self-addressed note is deliberately *not* excluded — agents
use those as scratch notes, and it is mail you do have to read.

**The user's name is bound into the key digest.** Without it, copying alice's
`user.json` into bob's directory produced a bob who authenticated with alice's
key. The digest is now `HMAC(salt, name ‖ 0x00 ‖ key)`, and the store also
checks that a record names the directory it was found in.

**Every query command reports an empty match set as not-found.** `open` was
returning an internal fault and exit 70 through `view.Latest`; `archive` and
`read` were reporting "archived 0 messages". Both are the silent-no-op failure
§5 warns about, so all of them now exit 2.

**`verify` distinguishes a pruned message from a lost one.** A conversation
naming mail that `prune` deleted is the expected outcome of pruning, not
damage, and reporting it made a healthy store look broken after any tidying up.

**Tables widen to fit their own title.** Column widths follow the cells, so a
one-column list of user names rendered as `│ mail…2 │`. A table is now grown to
its heading's width, up to the terminal's.

**Conversation membership became a stored set.** The plan claimed a cc'd user
could read the thread through `convo`; they could not, because `view.Thread`
read the reader's own mailbox and theirs held only the notice. Worse, `reply`
addressed the *parent message's* recipients, so answering an older message
silently dropped whoever had been cc'd in since. Conversations now carry a
participant list (a `join` event in the conversation file), `reply` addresses
it, and `convo` reads by membership. See §1.

**`inbox --sent` exists.** The `Sent` scope was implemented and tested but no
command reached it, which is dead code with a test to make it look alive.

Two deliberate non-changes worth naming: a table whose columns cannot fit the
terminal **overruns rather than collapsing** into a column of ellipses, and
`Latest` still refuses an empty set rather than returning a zero row — the
caller checks first.

## 9. Milestones

| # | Deliverable | Done when |
|---|---|---|
| 1 | `fault`, `clock`, `user` | Name normalisation fuzzes clean; key verify/reject table-tested; no test touches `$HOME`. |
| 2 | `mail` codec | Round-trip property holds over the adversarial body corpus; every malformed header is a distinct, positioned error. |
| 3 | `store` + `identity` | Journal prefix-replay property; all three credential sources tested, including the refusal of a loose-moded file; fault injection covers every fs call. |
| 4 | `query` | Grammar table complete, fuzz target clean, every `Reference.md` query parses to the expected AST. |
| 5 | `view` + `render` + `inbox`/`open` | Golden tables byte-exact; first genuinely useful checkpoint. |
| 6 | `send`, `reply`, `convo` | Conversation rooting tested; concurrency suite green under `-race`. |
| 7 | `read`, `check`, `cc` | Receipt precedence and divergence reporting tested; cc visibility rules pinned by golden output. |
| 8 | `archive`, `prune`, `verify` | Every prune refusal path tested; crash-mid-prune leaves no dangling reference. |
| 9 | Orc / Claude hook integration | Config in `Docs/Mailman/`, exercised end-to-end with two live agents. |

Milestones 1–3 are one coherent sitting and are where the resilience actually
lives; 4–5 make it usable.

**Status: 1–8 implemented.** ~8 050 lines of code and ~6 500 of tests: 197 test
functions over roughly 1 200 cases, 6 fuzz targets, green under `go test -race`
and `-count=2`. Milestone 9 is not started — it needs Orc's hooks, which do not
exist yet.

---

## 10. What is deliberately not built

- No delivery daemon, no sockets, no ports. The filesystem is the transport.
- No search index. Mailboxes are small; a full scan is fast and cannot disagree
  with the source of truth.
- No message editing or unsend. Immutable means immutable.
- No attachments. Markdown bodies only, as the vision says.
- No quotas or retention policy beyond `prune`.

Each of these is cheap to add later and expensive to remove once agents depend
on it.

---

## 11. Decisions

### Confirmed

1. **The Orc credential contract** (§2) — `$ORC_USER` and `$ORC_KEY`, and no
   other source. The credential file and the `--user`/`--key` flags were
   dropped. `internal/identity` remains the one file to rewrite when Orc's
   remote auth is specified.
2. **HMAC rather than a password KDF** (§2.2) — correct for a machine-minted
   key and ~100 ms/command cheaper. The stored `algo` tag is where a real KDF
   goes if human-chosen keys ever become a case.
3. **`admin user` and `verify` both stay**, and are documented in
   `Docs/Mailman/Reference.md` rather than left as undeclared extras. `admin
   user` is still deletable once Orc writes user records itself.
4. **Query extensions** — parens, `!`, `~`, and `!=` are kept, and are now in
   the reference. Every documented form parses identically either way.

### Still assumed

5. **Module path** — `orc/mailman`, matching `orc/anno`.
6. **`cc` does not backfill the inbox** (§1) — prior messages become visible via
   `convo`, but only the cc notice is delivered.
7. **`open` narrows to most-recent with a stderr note** (§1) — the reference
   says most-recent; the note is added so the narrowing is never silent.
8. **`prune --yes` for non-interactive callers** (§4) — a guard the reference
   does not ask for, on the only irreversible command.
9. **Receipt redundancy** (§3.3) — read state in both the journal and the
   receipt directory, with `check` authoritative on receipts. The alternative is
   one location and a slower `check`.
10. **A sender keeps a copy of their own sent mail** (§8a) — needed to make
   `check` answerable at all. It is hidden from the inbox but reachable by
   query.
