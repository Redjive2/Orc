# Finishing Orc — concurrent work plan

Milestones 0–2 are built (see [Plan.md](Plan.md)). What is left is milestones 3, 4,
and 5, plus a short list of gaps. This splits it into **five streams that do not
share a file**, so they can run at once and merge in any order.

One rule makes that true, and it is step 0: **the seams are carved before the
streams start.** I create the empty files, the dispatch wiring, and the store method
signatures each stream needs; after that every stream owns whole files. An agent
that finds itself wanting to edit a file another stream owns should stop and say so
rather than negotiate in the diff.

---

## Step 0 — seam carving (me, before anything else)

Small and mechanical. Nothing in it changes behaviour.

- `internal/cli/attach.go` — the attach verbs moved out of `liveness.go`, so stream
  B owns a whole file and stream C can have `liveness.go`.
- `internal/cli/doctor.go` — a stub `doctor` that still refuses, so stream C owns it.
- `internal/store/enforcement.go` — empty file with the three signatures stream A
  needs (`WriteAuthz`, `ReadAuthz`, `EventsPath`), returning `fault.Internal`.
- `internal/event/event.go` — the event *schema* and its codec, in a leaf package
  that imports nothing of Orc's, so A writes and B reads the same shape without
  waiting for each other. It is its own package rather than part of
  `internal/session` because `store` has to import it and `session` imports `store`.
- Dispatch already routes every verb, so no CLI wiring is needed.

---

## The contracts

Agreed here so no stream has to invent one. Anything not listed is that stream's own
business.

### Session events — `identities/<name>/session/events.jsonl`

One line per hook firing, append-only, same journal discipline as everything else (a
truncated final line is dropped, anything else is corruption).

```json
{"at":"2026-07-25T16:24:48.986Z","session":"<uuid>","event":"PreToolUse",
 "tool":"Edit","path":"Anno/internal/tree.go","turn":14,
 "verdict":"allow|block","reason":"...","transcript":"/path/to/session.jsonl"}
```

- `event` is Claude's own `hook_event_name`, verbatim.
- `path` is set for the file tools and empty otherwise; `tool` is empty for
  lifecycle events.
- `verdict` is set only on `PreToolUse`.
- `transcript` appears on the **first** event of a session and is how the clean view
  finds Claude's transcript without knowing how the path is derived.
- `turn` increments on `UserPromptSubmit`.

### The permission snapshot — `identities/<name>/session/authz.json`

Written once at populate, never locked, read by the hook when the live store cannot
be. Effective clauses only, no credential, no grant expiry logic:

```json
{"identity":"ember","session":"<uuid>","at":"...",
 "clauses":[{"kind":"write","arg":"Anno/internal/**"}],"budget":24}
```

### Compiled settings — `identities/<name>/claude/settings.json`

- `permissionMode: "bypassPermissions"` (Plan.md §7.2, confirmed).
- `permissions.deny`: `Read($ORC_HOME/**)`, the `Agent` tool, and everything not
  covered by an effective `read`/`write` clause that can be expressed as a rule.
- `permissions.allow`: the effective clauses, as `Read(...)`/`Edit(...)`/`Write(...)`.
- `hooks.PreToolUse` → `orc-hook`, matcher
  `Read|Edit|Write|NotebookEdit|MultiEdit|Bash|Agent`.
- `hooks` for the event feed: `UserPromptSubmit|PostToolUse|Notification|Stop|SubagentStop|SessionStart|SessionEnd` → `orc-hook`.
- A settings file Orc cannot parse is **left alone and reported**, never rewritten.

### Exit codes

`orc-hook` uses Claude's contract (`0` proceeds, `2` blocks) and nothing else —
never the shared table. Everything else uses `orc/common/fault`.

---

## Stream A — enforcement: `orc-hook` and the compiled settings

**Owns:** `cmd/orc-hook/**`, `internal/hook/**`, `internal/provision/settings.go`,
`internal/store/enforcement.go`, `internal/session/events.go` (writer half).

**Does:**

1. **First, the empirical check** from Plan.md §7.2: does a `deny` rule still refuse
   under `bypassPermissions`? Write the answer into Plan.md §7.2 as a sentence with
   the date. The design does not depend on the answer — hooks run either way — but
   it decides whether the settings file is a fence or a request, and everything else
   in this stream is written knowing which.
2. Compile settings at populate (contract above), plus `authz.json`.
3. `orc-hook`, with the three-rung fallback: live store → `authz.json` → neither,
   where **reads pass and writes and `Agent` block**. 2-second deadline. Only a
   genuine violation blocks; unparseable input, unknown events, and a missing store
   exit 0 silently. `FuzzRun`: no input produces an exit other than 0 or 2.
4. The event feed, per the schema.

**Done when:** the ladder is tested at each rung; the `Agent` denial is asserted
through the hook and not only through the settings file; a `Read` of `$ORC_HOME` is
blocked and recorded as an escape; the hook never writes to the store (fingerprint
it before and after, as Macmuffin's does).

---

## Stream B — the clean `attach` view

**Owns:** `internal/view/**`, `internal/cli/attach.go`, `internal/render/pane.go`.

`attach.go` already exists and holds the raw proxy; `dial`, `short`, and the worklist
verbs stay in `liveness.go`, which stream C owns — call them, never edit them.

**Does:** the pane from Plan.md §6.2 — event feed on top, transcript prose where it
is available, compose-then-confirm input (`^S` sends, `Enter` is a newline), footer
with unread mail and the task in force, `^\ d` detach, `^]` switches to `--direct`.

Build against a **fixture** `events.jsonl` rather than waiting for stream A: the
schema above is fixed, so a hand-written file is a valid input. Degrade rather than
fail when the transcript is missing or unparseable — it is Claude's format, not
ours — and say so on the way in, naming `--direct`.

**Done when:** the view renders from a fixture with colour on and off byte-identical
once stripped; a missing transcript costs the prose and not the pane; typing never
reaches the session before `^S`; detaching with unsent text warns.

---

## Stream C — `doctor`, `verify`, `tend --watch`

**Owns:** `internal/cli/doctor.go`, `internal/cli/verify.go`,
`internal/cli/liveness.go`.

**Does:**

1. `orc doctor` — every guard, and whether it is **in force**, in the shape
   `orcprobe doctor` uses: the sandbox stamp, `flock` (absent on non-unix), the
   compiled settings, the hook's presence on `PATH`, the keyring's mode, and every
   §7.5 hole printed as a hole. Plus: stray `claude` processes whose parent is an Orc
   session and which Orc does not know about, reported and never killed.
2. `verify` gains the worklist: employed-and-not-running, a session file that will
   not parse, a socket with no state, a supervisor pid that is somebody else's.
3. `tend --watch <dur>` — the backstop loop, `cq sync --watch`'s shape.

**Done when:** `doctor` on a healthy fleet is quiet and on a broken one names the
fix; `--watch` survives a session dying and coming back; `verify` still exits
`6` on damage.

---

## Stream D — Orcprobe

**Owns:** `Orcprobe/**` entirely. Touches nothing in `Orc/`.

**Does:** the four open rows in Plan.md §9 — Orc's state in `source/`, **remint
Orc's keyring** (a probe must never hold a real key, and Orc is the one store with
plaintext ones), copy `identities/*/claude` and the workspaces, cut sockets and pids
and `session.json` so no probe claims a live session, and narrow the `orc` shim from
"refuse everything" to the table in §9: read-only verbs allowed, anything that
populates refused.

**Done when:** the inertness test still passes with an Orc store in the world; a
probe's `orc status` works and its `orc employ` is refused; no probe contains a key
that opens the real fleet.

---

## Stream E — the gaps: tests and docs

**Owns:** `Claude/Docs/Orc/Hooks.md` (new), `internal/provision/provision_test.go`,
`internal/render/render_test.go`, `internal/cli/proxy_test.go`, and a patch to
`Docs/Orc/Reference.md`.

**Does:**

1. `Hooks.md` — the wiring doc, in the shape of `Claude/Docs/Macmuffin/Hooks.md`:
   what fires, what blocks, what the rules are, and what it does not cover.
2. The `attach --direct` proxy loop test, which is the honest gap named at the end of
   Plan.md's milestone 2: a pty on both ends and a fake operator.
3. Tests for `internal/provision` (rollback leaves no half-made identity, the
   Claude config is laid out) and `internal/render` (a degenerate fleet still draws;
   a card with a long path truncates the note rather than the value).
4. `Reference.md`: `employ --model/--effort`, `attach --direct`, `fire --yes`,
   `tend --watch`, and the `--only` fields that now exist.

**Done when:** `go test ./...` covers every package that has behaviour, and
`Reference.md` describes the binary that exists.

---

## Rules for every stream

1. **Never touch the real fleet.** No test and no manual check may write to
   `~/.orc`, `~/.mailman`, or `~/.macmuffin`. Set `ORC_HOME` to a temp dir; point
   `ORC_CLAUDE_BIN` at `internal/fixture/claude`; inject `Provision`, `Populate`,
   and `Depopulate` rather than spawning the real thing.
2. **`gofmt -l .` clean, `go vet ./...` clean, `go test ./...` green** before saying
   done. From inside `Orc/`.
3. **The house voice.** Comments say *why*, not what; every refusal names the way
   forward; colour is a layer and never information; no dead code, no placeholder
   that claims to work.
4. **No panics.** A violated invariant is a `fault.Internal`, returned.
5. **Write down what came out differently.** Each stream appends a short
   "as built" note to Plan.md's milestone section — that is where the tree records
   the decisions the building forced, and it is the one place two streams may both
   append (separate subsections, so a merge is trivial).

## Contention map

| Shared thing | Who may write it |
|---|---|
| `internal/store/**` | A only (in `enforcement.go`) |
| `internal/cli/liveness.go` | C only |
| `internal/cli/attach.go` | B only |
| `internal/event/**` | A and B both read it; the schema above is settled, so neither should need to change it |
| `Plan.md` | all, append-only, one subsection each |
| `Docs/Orc/Reference.md` | E only |
| everything under `Orcprobe/` | D only |
| a shared **test package** | nobody exclusively — see below |

**File ownership is not enough for test files.** Two files in one `_test` package share a
scope, so `internal/provision/settings_test.go` (A) and `provision_test.go` (E) collided
on `epoch` and `mustUser` even though neither touched the other's file. The rule that
fixes it: **prefix your helpers with your subject** — `settingsFleet`, `settingsEpoch` —
and never declare a bare `epoch`, `fresh`, or `mustX` in a package another stream also
writes tests in.

**Merge order:** any. A and D and E are independent; B depends only on the event
schema, which is fixed here; C depends on nothing. If two streams both want a new
`store` method, A adds it and the other calls it.
