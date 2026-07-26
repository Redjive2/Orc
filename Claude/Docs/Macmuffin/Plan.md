# Macmuffin — Implementation Plan (Go)

Derived from [Vision.md](../../../Docs/Macmuffin/Vision.md) and
[Reference.md](../../../Docs/Macmuffin/Reference.md), and written to the
conventions [Anno](../Anno/Plan.md) and [Mailman](../Mailman/Plan.md) already
establish for this tree.

Guiding constraints, in priority order:

1. **Robust** — every error is handled and carries position; no panics, no silent
   truncation, no partial writes, no lost claims. Two agents racing for the same
   task is the normal case, not the edge case.
2. **Immutable** — a task's history is never rewritten. Mutable state (owner,
   collaborators, status, completion, scope) is an append-only journal replayed
   into a frozen view. Mutation is confined to short builder scopes that yield
   frozen values.
3. **Simple** — small packages, one job each, no frameworks, stdlib only.
4. **Readable** — the spec's vocabulary (pool, task, subtask, scope, claim,
   status) is the code's vocabulary.

Macmuffin sits where Anno and Mailman meet, and that placement drives most of
what follows. Like Mailman it is **multi-process over one shared store**, so §4's
storage design is concurrency-shaped from the start rather than hardened
afterwards. Unlike Mailman it is also **authoritative over other tools**: a scope
is not advice, it is a Claude hook that refuses an edit, including an edit made
through Anno. A task tracker whose scope check is wrong does not merely
misreport — it blocks work that should proceed, or permits work that should not.
§8 is therefore as much of this plan as §4 is.

This plan is written after a round of decisions with the author; §14 records all
fifteen, and none are left open. Two of those answers shape everything below
and are worth stating up front: the shared packages the three tools have been
copying between themselves are **extracted into a common module first** (§10),
and **`assign` is not built yet** (§2.3).

---

## 1. Semantics recovered from the spec

The reference is a CLI shape, not a specification. These are the behaviours it
implies but does not state, resolved here so they are decided once.

**A task has two lives: draft and pooled.** `create` makes a task and `push`
"pushes it to the task pool" — so creation does not publish. A drafted task is
visible only to its author, can be shaped freely, and is nobody else's business;
a pooled task is visible to every agent, appears in `pool`, and is claimable.
`push` is one-way. This is what makes `create` cheap: an agent can sketch a task,
give it scope and subtasks, and only then expose it to the others.

**Scope is the gate on everything that matters.** The reference is explicit:
a task "cannot be edited or completed, only claimed or deleted, until a scope is
added." So a scopeless task is a *stub*. `claim` and `delete` work on it,
`--sub`, `status`, `complete`, `push`, and `worktree` do not. The reasoning is
visible in the vision — scope is what makes a workflow "highly readable to those
outside the workflow", and a task with no declared surface tells an onlooker
nothing about what is about to change under them.

**`status` is a health signal, not progress.** The four values (`1` not working,
`2` slow/problematic, `3` nominal, `4` done/basically done) are separate from
`complete`, which is terminal, and separate again from subtask counts. A task can
be status 4 and incomplete — that is precisely the state the scale exists to
report, and collapsing it into completion would delete the tool's most useful
signal. `pool` shows all three: status glyph, `n/m` subtasks, completion.

**Subtasks are a flat list.** The vision asks for subtasks "arranged in groups as
steps"; the reference gives no syntax for a group, and grouping is deliberately
deferred (§13). A subtask is a name and a done flag, ordered by creation.

**Ownership is one owner and N collaborators.** `claim` takes a task "as your
own"; `invite`/`kick`/`leave` manage collaborators. So exactly one owner, a set
of collaborators, and a permission table (§3) that says which of them may do
what. An unowned pooled task is the normal resting state — that is what a pool
*is*.

**`claim` is a compare-and-set, not a write.** Two agents scanning the same pool
will claim the same task within milliseconds of each other. The second must lose,
loudly, naming the winner — not overwrite, and not silently no-op. This is the
one operation whose correctness the whole tool rests on.

**Membership changes are announced through Mailman.** The reference says
assignment "notifies them via mailman automatically with a CC for you". The same
reasoning covers `invite` and `kick`: an agent added to or removed from a task
needs to hear about it. A notice the recipient never receives is worse than a
refused command, so delivery is journaled and retried (§7) rather than attempted
once and shrugged off.

**Auth is per-invocation.** As in Mailman: *"User accounts are controlled via Orc
remote auth."* Every command resolves a credential, verifies it, and fails
closed. There is no ambient current user.

---

## 2. Names and identity

### 2.1 Task names

A task name is the tool's only handle — every command but `pool` takes one — so
it is normalised exactly once, in `task.ParseName`, and the rest of the program
only ever handles a `task.Name` that cannot be constructed another way. This is
Mailman's `user.Parse` discipline applied to the type that gets used as a path
element.

Normalisation is trim, lowercase, and every run of whitespace, `_`, and `-`
collapsed to a single `-`. (NFC was planned, but it needs `golang.org/x/text`
and every Orc tool is stdlib only. Names are restricted to ASCII instead, which
makes normalisation total rather than approximate and sidesteps the question of
whether two names that look identical are the same task.) The result must match `^[a-z0-9][a-z0-9.-]{0,79}$`, must not be a reserved
word (`all`, `pool`, `none`, `any`, `.`, `..`), and must not look like a flag —
checked against the *raw* spelling, before normalisation, since normalisation
would otherwise turn `--force` into the perfectly valid name `force`. Names are
globally unique across the store; `create` on an existing name is `ErrConflict`
naming the owner, never a second task with the same handle.

Commands accept an unnormalised name and normalise before lookup, so
`muff info "Fix The Parser"` finds `fix-the-parser`. A name that normalises to
something the caller did not obviously intend is echoed back in the output
header, so the mapping is never invisible.

Subtask names normalise the same way, but uniquely per-task rather than globally.

### 2.2 Agents

Agents are Mailman users — the same namespace, since membership changes mail
them.

**Not yet verified.** As of milestone 3, Macmuffin *resolves* a credential and
fails closed without one, but does not check the key against a user record: the
records live in Mailman's store, and reading another tool's store is coupling
this plan has not designed. Until it is, `$ORC_KEY` is an assertion rather than
a proof, and a well-formed key is accepted. That is fine while the store's 0700
permissions are the real boundary — every agent on the machine can already read
it — and it is the first thing Orc's real auth replaces. Recorded here rather
than left to be discovered. So `user` and `identity` are the common module's packages (§10), and the
credential contract is Mailman's unchanged: `--user`/`--key`, then
`$ORC_USER`/`$ORC_KEY`, then a `0600` file at `$ORC_CREDENTIALS` or
`~/.orc/credentials`.

### 2.3 `assign`, and why it is not built yet

`assign <agent> <task>` is permitted only "given you control `<agent name>`",
which is a fact Macmuffin cannot know: agent parentage belongs to Orc, which does
not exist yet. Every way of proceeding without it is bad in a different way —
inventing a roster file guesses at an interface Orc has not defined, and
permitting any assignment teaches agents that assignment is unrestricted, which
is a lesson that cannot be untaught once workflows depend on it.

So `assign` is **not implemented**. It is the one documented command this plan
does not deliver, and the omission is deliberate rather than an oversight:
`muff assign` exits `1` with a message saying the command is waiting on Orc's
agent-control contract, and pointing at `claim` (an agent takes the task itself)
and `invite` (the owner adds them as a collaborator), which need no proof of
control. When Orc lands, `assign` is a permission check, a journal event already
shaped like `claim`'s, and one notification through the machinery §7 builds for
`invite` — a day's work, not a redesign.

---

## 3. The permission table

Written as a table because it is implemented as one — a single
`policy.Allows(actor, task, action) error` consulted by every command, rather
than an `if` scattered through twelve of them.

| Action | Owner | Collaborator | Anyone | Notes |
|---|:--:|:--:|:--:|---|
| `info`, `pool` | ✓ | ✓ | ✓ | Drafts are visible only to their author. |
| `claim` | — | ✓ | ✓ | Only if unowned. Owner claiming their own task is a no-op, reported as one. |
| `invite`, `kick` | ✓ | — | — | |
| `leave` | — | ✓ | — | An owner cannot `leave`; a task is never orphaned by accident. |
| `status` | ✓ | ✓ | — | |
| `create --sub`, `complete --sub` | ✓ | ✓ | — | |
| `scope` | ✓ | — | — | Widening the editable surface while collaborators work is the owner's call. |
| `complete`, `delete`, `push`, `worktree` | ✓ | — | — | `delete` on an unowned draft is permitted to its author. |

An unowned pooled task has no owner, so owner-only actions are refused with
"claim it first" rather than "permission denied" — the distinction is the whole
difference between a dead end and a next step.

Refusals are `fault.Denied` (exit 8) and name the owner, so an agent that wanted
a task knows who to mail.

---

## 4. Storage

Root is `$MACMUFFIN_HOME`, else `$XDG_DATA_HOME/macmuffin`, else `~/.macmuffin`.
A store is per-machine and shared by every agent on it, exactly as Mailman's is.

```
<root>/
  version                       format version; an unknown one is a hard, clear error
  tasks/<name>/task.json        immutable creation record: name, author, created, id
  tasks/<name>/journal.jsonl    append-only task state (§4.1)
  tasks/<name>/lock             advisory lock, held only for appends
  worktrees/<hash>.json         worktree path → task name, for the hook's cwd lookup
  outbox/<id>.json              pending Mailman notifications (§7)
  tombstones.jsonl              deletions, so verify can name a half-erased task
```

One directory per task, one lock per task. Contention is naturally per-task — two
agents race for *a* task, not for the store — so a global lock would serialise
the pool for no gain. `pool` reads every task directory and takes no lock at all.

`task.json` is written once at creation, through Anno's commit sequence exactly:
temp file in the same directory, write, `fsync`, `chmod`, `rename`, `fsync` the
directory, temp removed on every failure path. A `rename` onto an existing name
is the uniqueness check from §2.1 — enforced by the filesystem, not by a
read-then-write that another process can interleave.

### 4.1 The journal

Everything mutable is an append-only JSONL file. One event per line, one line per
command:

```
{"op":"scope","by":"alice","paths":["internal/tree/","cmd/anno/main.go"],"at":"…"}
{"op":"sub.add","by":"alice","name":"fuzz-the-parser","at":"…"}
{"op":"push","by":"alice","at":"…"}
{"op":"claim","by":"bob","at":"…"}
{"op":"status","by":"bob","value":2,"at":"…"}
{"op":"sub.done","by":"bob","name":"fuzz-the-parser","at":"…"}
```

State is a left fold over the journal, computed fresh on every command. Tasks are
small and few; a derived cache is a second source of truth that can disagree with
the first.

The failure mode is why this shape is chosen, and the reasoning is Mailman's: a
process killed mid-append leaves a truncated final line, so replay drops an
unparseable **final** line with a note on stderr and continues. An unparseable
line anywhere else is corruption rather than interruption, and is a hard error —
silently skipping it would silently drop a claim, and a dropped claim is two
agents editing the same files.

Appends take the task's advisory lock (`flock` on unix, `O_CREATE|O_EXCL` lock
file elsewhere, behind one interface with a `//go:build unix` split, following
Anno's `fifo_unix_test.go` precedent), open with `O_APPEND`, write one line,
`fsync`.

### 4.2 Conditional appends

`claim` — and every other operation whose legality depends on current state — is
a **read-under-lock, decide, append, release**. The lock is taken, the journal is
replayed, the permission table is consulted against *that* state, and the event
is appended before the lock is released. The check and the write are never
separated, which is what makes the claim race resolve rather than interleave.

Every conditional operation is expressed as one function,
`store.Apply(name, func(state) (event, error))`, so there is exactly one place
where the lock is held and exactly one place where a decision can be made against
stale state. A command that wanted to check first and write later cannot: the API
does not offer it.

---

## 5. Commands

| Command | Behaviour |
|---|---|
| `create <name> <priority> <difficulty>` | New draft task. Both scores are `1..5`; out of range is a usage error naming the scale. |
| `create <name> --sub <sub>` | Add a subtask. Requires scope (§1). |
| `pool [--all]` | The board: every *active* pooled task, plus the caller's own drafts, marked. `--all` adds completed ones. |
| `info <name>` | Full card: scores, owner, collaborators, status, scope, subtasks, history. |
| `status <name> <1..4>` | Set the health signal. Prints the previous value, so a change is visible. |
| `assign <agent> <name>` | **Not implemented** — waiting on Orc's agent-control contract (§2.3). Exits `1` with the reason and the alternatives. |
| `claim <name>` | Take an unowned task (§4.2). |
| `invite <agent> <name>` | Add a collaborator, and notify them with the caller CC'd (§7). |
| `leave <name>` | Drop collaboration. |
| `kick <agent> <name>` | Remove a collaborator, and notify them. |
| `complete <name> [--sub <sub>]` | Mark done. Requires scope; a task with incomplete subtasks refuses and lists them. |
| `delete <name> [--sub <sub>]` | Remove. Irreversible; see below. |
| `scope <name> <paths...>` | Declare the editable surface, and enforce it (§8). |
| `push <name>` | Publish a draft to the pool. One-way. Requires scope. |
| `worktree <name> <path>` | Bind the task to a git worktree of the main repo (§9). |
| `check-scope <paths...>` | Exit-code only, no output: `0` in scope, `9` outside. The contract Anno calls (§8.3). |
| `verify` | Walks the store and reports what is wrong without changing anything. Not in the reference (§14). |

Every command but `help` authenticates first, so an agent with no identity is
told that rather than told its arguments are malformed.

**Destructive commands.** `delete` is the only irreversible operation. It refuses
a pooled task that anyone has claimed unless the caller is the owner, prints what
it will delete (including subtask count and collaborators, who lose the task
without warning otherwise), and requires `--yes` when stdin is not a terminal —
which for every agent is always. The task directory is removed only after the
deletion is journaled to `tombstones.jsonl`, so a crash mid-delete leaves a task
`verify` can name rather than a half-erased directory. A worktree binding goes
with the task: one pointing at a task that no longer exists would make the hook
enforce a scope nobody owns.

**`complete` refuses a task with unfinished subtasks**, listing them. `--force`
completes anyway and journals `complete.forced` with the skipped list — the point
of a tracker is that shortcuts stay visible, so the override exists and leaves a
mark.

Exit codes, extending Anno's and Mailman's so hooks branch uniformly:
`0` ok · `1` usage · `2` not found · `3` ambiguous · `4` parse · `5` i/o ·
`6` conflict · `7` auth · `8` denied · `9` out of scope · `70` internal.
Diagnostics to stderr, output to stdout.

The numbering is no longer per-tool. The sentinel → code mapping is **one table
in `common/fault`** (§10.1), documented in `Claude/Docs/ExitCodes.md`, so a tool
cannot assign a code that already means something else somewhere else. Adding a
code is an edit to that table and that doc, in one commit, for all tools at once.

`9` is Macmuffin's own contribution: it is what `check-scope` and the hook exit
with, and it is distinct from `8` because "you may not do this at all" and "you
may do this, but not to that file" are different problems with different fixes.

---

## 6. Presentation

`AGENTS.md` asks every Orc tool for colour, vertical alignment, tables, box
drawing, and diagrams. Macmuffin's whole purpose is to make a workflow "highly
readable while it is going on to those outside the workflow", so this section is
the feature, not the finish.

**`pool` is a board.** A box-drawn table, one row per task, with a titled header
bar and per-column alignment — scores and counts right, names and owners left.
Progress is a compact meter (`▓▓▓▓░░░ 4/7`) that reads at a glance and still
carries its own numbers for when colour is gone. Status is a glyph *and* a word
(`✗ broken`, `~ slow`, `● nominal`, `✓ done`). Drafts are marked `draft` and
dimmed. Sort is priority descending, then difficulty descending, then age.

Completed tasks leave the board by default and come back under `--all`, dimmed
and struck through, below the active ones. The board is the thing agents look at
most often, so it has to stay readable as the store ages; nothing is deleted to
achieve that, it is one flag away.

**`info` is a card.** Metadata in a titled box, scope as an aligned list, and
subtasks as a checklist, so the state of the work is visible without reading it:

```
╭─ fix-the-parser ─────────────────── P4  D3  ● nominal  5/8 ──╮
│ owner  bob            created  2026-07-24 18:31              │
│ collab alice, carol   worktree ../orc-parser                 │
├─ scope ──────────────────────────────────────────────────────┤
│ internal/tree/            internal/marker/                   │
│ cmd/anno/main.go                                             │
├─ subtasks ───────────────────────────────────────────────────┤
│ ✓ recover-the-grammar     ✓ table-the-sigils                 │
│ ✓ pin-the-example         ✓ golden-the-index                 │
│ ✓ classify-every-sigil    ○ fuzz-the-parser                  │
│ ○ wire-the-hook           ○ document-the-closers             │
╰──────────────────────────────────────────────────────────────╯
```

**Colour is a layer, never information.** Every colour is redundant with a glyph
or a word, so a pipe through `grep` loses nothing. Colour is on only when the
stream is a terminal, and off under `NO_COLOR`, `TERM=dumb`, or `--no-color`.
Golden tests run with colour off; a separate small test asserts the escape
sequences appear when it is on, and a third strips them and compares
byte-for-byte against the plain rendering, so a coloured table can never be a
misaligned one.

**Overlong cells truncate with `…`, never wrap**, so a row is one line and
columns stay aligned. Width is measured in runes with East Asian wide runes
counting as two, so a CJK task name does not shear the table.

Layout is two passes as in Anno's `render`: measure every cell, then draw
fixed-width rows, with every width clamped to a sane minimum so a degenerate
input (no tasks, no scope, a task with no subtasks) still produces a well-formed
frame. The common module's `style` is the only package that knows an escape
sequence exists; `internal/render` is pure.

---

## 7. Mailman integration

`invite` and `kick` notify, and `assign` will when it lands. Macmuffin does not
reimplement mail — it shells out to `mailman send`, passing the body on stdin,
with the affected agent as recipient and the caller CC'd, exactly as the
reference describes.

The coupling is one package, `internal/notify`, with the exec behind an interface
so every test runs against a recorder rather than a real binary.

Delivery is journaled, not fired and forgotten:

1. The task event (`invite`) is appended first. Membership is the fact; the mail
   is the announcement.
2. A notification is written to `outbox/<id>.json`.
3. `mailman send` is executed. On success the outbox entry is removed.
4. On failure the entry stays, a warning goes to stderr, and the *next* Macmuffin
   command in any process retries the outbox before doing its own work.

So a Mailman that is missing, misconfigured, or momentarily broken delays a
notification rather than losing one, and never fails a membership change that
already happened. An outbox entry that has failed ten times stops being retried
and starts being reported by `verify` — a retry loop that never gives up is a
retry loop that eventually hides the actual problem.

---

## 8. Scope enforcement

The vision's hard requirement: scope "enforces editing (even via Anno) only on
files in scope via Claude Hook". This is the part of Macmuffin that can break
someone's session, so it is designed under Anno's hook rule (`Hooks.md`): **only
a genuine violation may block; everything unexpected exits 0 silently.**

### 8.1 Which task is in force

The hook must answer "what task is this agent working on?" without being told on
every call. In order:

1. `$MUFF_TASK`, if set.
2. The worktree binding (§9): the cwd's git worktree root, looked up in
   `worktrees/`.
3. Otherwise **no task is in force, and nothing is enforced.** An agent that
   never opted in is never blocked.

### 8.2 The check

`muff-hook` runs on `PreToolUse` — not `PostToolUse`, because a scope violation
must be prevented, not reported after the write. It matches
`Edit|Write|NotebookEdit|MultiEdit`, extracts the target path, and denies with
exit 2 (Claude's block code) when the path is outside scope:

```
muff: internal/render/render.go is outside the scope of fix-the-parser.

  in scope:  internal/tree/  internal/marker/  cmd/anno/main.go

Add it with `muff scope fix-the-parser internal/render/render.go`, or work on a
task that covers it.
```

Path matching is: normalise to a path relative to the worktree root, reject any
that escapes it, then match against each scope entry as an exact file, a
directory prefix (entry ends in `/`), or a glob (`path.Match` semantics, no
`**`). Symlinks are resolved before matching, because a scope check that a
symlink walks around is decoration.

Two refinements from building it. A path that escapes the root is `ErrEscape`
(exit 11), not `ErrScope` (9): an ordinary out-of-scope edit is routine, and a
path resolving outside the worktree is a containment failure worth alarming on.
And the trailing slash is required in the *matcher* but optional at the command
line — `muff scope x internal/tree` adds `internal/tree/` when that is really a
directory, because making a caller remember the slash is a papercut with no
safety value.

Scopes are independent: a path may be in two tasks' scopes at once, and
Macmuffin says nothing about it. Two tasks legitimately touch one file, and a
warning that fires on the legitimate case is a warning agents learn to ignore.

### 8.3 Anno, and the general problem of Bash

Anno writes through `anno write`, which reaches the filesystem as a `Bash` call,
and deciding what an arbitrary shell command will write is undecidable. The fix
is on Anno's side, where it is decidable:

- **`muff check-scope <paths...>`** exits `0` or `9` and prints nothing. Anno's
  write path calls it when `$MUFF_TASK` or a worktree binding is present, and
  refuses the write on `9`, reporting Macmuffin's message. This is the mechanism
  the vision's "even via Anno" names, and it needs a change to Anno — the one
  item in this plan that touches another tool.
- The hook *also* parses `Bash` commands for a leading `anno write <target>`
  (including after `cd … &&`) and checks the path half. This is belt-and-braces
  for the window before Anno is changed, and for an `anno` binary older than the
  change.

Anything else through `Bash` is out of reach, and the docs say so plainly rather
than implying a guarantee that does not hold.

### 8.4 The rules that keep it safe

Each is a test, mirroring Anno's `TestNothingUnexpectedEverBlocks`:

1. **Only a genuine violation blocks.** Unparseable JSON on stdin, an unknown
   event, a tool Macmuffin does not care about, a missing path, no task in force,
   an unreadable store, a store that does not exist — all exit 0 silently.
2. **A scopeless task never blocks.** Scope is opt-in per task, and a task
   without one enforces nothing.
3. **The hook never writes.** It reads the store and decides. A hook that
   journals on every tool call would make the journal a log of keystrokes and
   would put a lock in the path of every edit.
4. **A slow or broken store must not stall a session.** The check is bounded by a
   hard deadline; on timeout, an unreadable store, or a corrupt journal it exits
   0 with a stderr note. Failing open is right here and failing closed is right
   in §3 — the difference is that a permission check is asked a question it can
   always answer, and a hook is a bystander. The cost is real and is stated
   rather than hidden: while the store is broken, a violation gets through.

---

## 9. Worktrees

`worktree <name> <path>` binds a task to a git worktree of the main repository.
The binding is what makes §8.1 work without an environment variable, and what
makes `info` able to say where a task is being done.

`internal/repo` resolves a directory to its worktree root and its main repository
by reading `.git` (a directory for the main tree, a `gitdir:` file for a linked
one) — no `git` subprocess, so it cannot be defeated by a hook, an alias, or a
`git` that is not on `PATH`. The bind is refused when the path is not a worktree,
when it belongs to a different main repository than the task's other bindings, or
when it is already bound to another *active* task, because an ambiguous cwd → task
lookup would silently enforce the wrong scope.

Bindings are stored under `worktrees/<hash>.json`, keyed by the hash of the
resolved absolute path, so the hook's lookup is one stat and one read rather than
a scan.

---

## 10. The common module, and package layout

### 10.1 `Orc/Common`

Anno and Mailman have each grown their own `fault`, `clock`, and `style`, and
Mailman a `user` and `identity` that Macmuffin needs verbatim. A third copy is
the point at which copying stops being cheaper than sharing, so the shared
packages are **extracted before Macmuffin is written**, not after.

```
Common/
  go.mod                   module orc/common
  fault/                   the whole error vocabulary, all sentinels, Check,
                           and the single sentinel → exit code table (§5)
  clock/                   injectable time; the real one and a deterministic fake
  user/                    name normalisation, key digest, verification
  identity/                the Orc credential boundary
  source/                  file load, validation (UTF-8, NUL, size cap), hashing
  commit/                  the atomic write sequence, alone
```

**`style` is not in this list, because it already exists elsewhere.** Between
this plan being written and milestone 0 starting, the colour scheme was
extracted as its own module, `orc/theme` at `Orc/Theme/` — Catppuccin, four
flavours, session-configurable, and plain for agents. Four modules already
depend on it, two of them (`orc/cq` and `orc/orcprobe`) built by other agents.

Folding it into `Common` now would mean rewriting two in-flight modules to gain
nothing: the extraction this section asks for has *happened*, under a different
name and with a wider remit than "palette and width". So `style` stays as
`orc/theme`, `Common` takes the rest, and a tool requires whichever it needs.
See [Theme.md](../Theme.md).

The last two are here for [Dock](../Dock/Plan.md), which needs both byte-for-byte
identically to Anno, and for Macmuffin's own store, which performs the commit
sequence in §4. They are extracted **now**, in this one sitting, rather than at
Dock's own milestone 0: Anno is then retrofitted once instead of twice, while the
extraction is open and its "nothing changed" criterion is already being verified.

`commit` is only the six-step file replacement — not Anno's splice planning,
which stays in Anno because it is about annotation structure. That split is what
keeps the extraction honest: the thing three tools do identically is small and
finished.

`fault` becomes the union of the three tools' vocabularies: Anno's eight
sentinels, Mailman's `Auth`, and Macmuffin's `Denied` and `Scope`. A tool
referencing a fault it never returns costs nothing; three vocabularies that
drift cost a debugging session each time an exit code means two things. The
sentinel → exit code mapping moves here with them, as one table, and
`Claude/Docs/ExitCodes.md` is written from it — so the three tools share not
just the vocabulary but the numbering, and a fourth cannot reuse `8` for
something new without seeing what it already means.

`render` stays per-tool. The three tools draw different objects, and a shared
table primitive designed from three call sites would be an abstraction invented
before it was needed — which is the same mistake as copying, made in the other
direction. If a fourth tool wants a table, that is when to look again.

**The retrofit is behaviour-preserving, and that is its acceptance criterion.**
Anno is finished, at 97.1% coverage with committed fuzz corpora; Mailman is in
progress. Neither may change behaviour. Both test suites stay green under
`-race -count=2`, and every golden constant stays byte-identical — if a golden
moves, the extraction was wrong, not the golden.

Wiring is a `go.work` at `Orc/` listing every module, plus a `replace` directive
in each tool's `go.mod` pointing at `../Common` (and `../Theme` where it is
used), so a `go build` from inside a single module still works without the
workspace. That is verified rather than assumed: each module builds under
`GOWORK=off`.

### 10.2 Macmuffin

Module at `Orc/Macmuffin/go.mod`, module path `orc/macmuffin` — matching
`orc/anno` and `orc/mailman`.

```
Macmuffin/
  go.mod
  cmd/muff/main.go         thin: build App from os streams, dispatch, exit
  cmd/muff-hook/main.go    the PreToolUse scope guard (§8)
  internal/task/           Name, Task, Subtask, Status, Score — values + codec
  internal/scope/          path normalisation, matching (§8.2)
  internal/repo/           worktree resolution, no subprocess (§9)
  internal/policy/         the permission table as one function (§3)
  internal/store/          paths, locking, atomic write, journal append and replay
  internal/view/           store → frozen pool and task views
  internal/notify/         Mailman integration and the outbox (§7)
  internal/render/         board and card; pure, two-pass
  internal/cli/            one method per command, each (args) → error
  internal/hook/           event decoding and the block decision (§8)
  internal/fixture/        the golden corpus (§11)
```

Only `store`, `repo`, `notify`, and `cli` touch the filesystem; only `notify`
starts a process; only the common `clock` touches time. Everything in `task`,
`scope`, `policy`, `view`, and `render` is a pure function of its input. That
boundary is what makes the test suite cheap, and it is the boundary both existing
plans draw.

---

### 10.3 Amendment — membership implies visibility (milestone 7)

`Task.Visible` said a draft is its author's business alone. Once `invite` existed
that produced an invitation to a task the invitee could not see: the command
exited 0, the collaborator was recorded, the notice went out, and `muff pool`
showed them nothing. Visibility now follows membership — a draft is visible to
its author *plus* anyone they deliberately added. The alternative was refusing to
invite anyone to an unpooled draft, which is a worse answer to "I want help with
this before I pool it".

The owner still cannot `leave`, and that refusal is exit 8 (denied) rather than 6
(conflict): the policy table settles it before an event is ever built. It now
names the way out — complete it or delete it.

### 10.4 Amendment — a read-only door on the store (milestone 8)

Rule 3 of §8.4 says the hook never writes, but `store.Open` creates the layout
and writes a version marker on open — so a hook using it would conjure a task
store into whatever directory an agent happened to be in. `store.Read` opens an
existing store without touching it and sets a `readOnly` flag that every write
path refuses. The rule is now enforced by the store rather than by the hook
remembering not to, and the test fingerprints the whole store before and after
rather than reading the code.

### 10.5 Amendment — worktree bindings are keyed canonically (milestone 8)

`bindingPath` documented itself as hashing "the resolved absolute path" and
hashed the caller's spelling instead. `repo.Find` resolves symlinks, so a binding
made through a symlinked path was stored under a key the hook could never look
up — on macOS, where `/tmp` and `/var` are symlinks, that is every binding made
through either. Both the key and the record's own `path` are now canonical: the
path is resolved, and when it does not exist the nearest existing ancestor is
resolved and the rest appended, so a lookup stays stable after the directory is
deleted, which is exactly when `Unbind` needs it.

The bug was invisible from the CLI, which always binds a root `repo` had already
resolved. It took the hook — a second, independent caller — to expose it.

---

### 10.6 Amendment — Anno's exit codes were a copy (milestone 9)

Anno had its own `Code` classifier and its own code constants, written before
`orc/common/fault` held the table. The moment Anno could return a scope fault it
mapped to 70, "this is a bug" — an out-of-scope write reported as a defect in
Anno. Both now delegate to the shared table: one number, one meaning, across
every tool, which is the property hooks branch on.

The mechanism itself is `Anno/internal/guard`: `anno write` asks
`muff check-scope <path>` before it reads the file, and relays the answer.
Everything except a definite exit 9 is a yes — Macmuffin missing, broken, slow,
or unauthenticated must not stop somebody editing their own files, since Anno
worked before Macmuffin existed. Its deadline is a parameter rather than a
constant, because a check that times out fails open *silently*, and a test
pinning the refusal path must not be racing a timer to observe it.

### 10.7 Amendment — the title bar needed a gap (milestone 9)

`verify`'s title is long enough to fill its share of the bar, which showed that
a truncated title was drawn flush against the note: `…store✓ no problems found`.
One column of the title's space now belongs to the gap. `Table` also grew a
`NotePaint`, because the note had always been muted — right for a count, wrong
for a verdict.

---

### 10.8 Amendment — colour reaches the prose, not only the tables

§6 painted the board and the card and left everything else plain, which made a
tool whose tables were coloured and whose sentences were not look half-finished.
Every screen is now painted from the same roles cq uses — command names, flags,
placeholders, environment variables, task and agent names, paths, status words —
so the two tools cannot disagree about what a command name looks like.

Three consequences worth recording:

- **The help is data now.** It was one long constant, which cannot be painted
  for what each part *is* without matching it with a regular expression
  afterwards. `internal/cli/help.go` renders it from a table, and the plain
  rendering was checked byte-for-byte against the constant it replaced before
  the constant was deleted.
- **The palettes are resolved in `defaults()`, per stream.** They used to be
  resolved in `begin()`, after authentication, so the one thing muff printed
  plainly was a failure on the way to the store. stdout and stderr are asked
  separately, because `muff pool > board.txt` still has a terminal to be
  diagnosed on.
- **Every pad measures the plain text.** Escape sequences occupy no columns, so
  padding a painted string indents a coloured line differently from an
  uncoloured one. The whole-screen strip test is what catches that.

`--no-color` and `--color` are global flags, taken off the line before dispatch.
The environment already turns colour off three ways, but an environment variable
is awkward for a caller assembling one command — which is what Orc will be doing.
`ORC_AGENT` still wins over `--color`: turning colour off for every tool at once
must not be defeatable per command, and that is a test.

---

### 10.9 Amendment — `assign`, and the shape of a cross-tool permission

Orc landed with `orc check-control <agent>`, which exits 0 if the caller
controls the agent and 8 if not — the contract §2.3 was waiting for. Its
`authz.Controls` names `muff assign` as the consumer, so the two halves were
designed for each other rather than negotiated afterwards.

The split: **who may direct whom** is Orc's question, because it owns the fleet
and a second copy of the tree here would be a second thing to keep right.
**Whether the task may be given away** is Macmuffin's, and it is exactly what
`claim` asks — so `Assign` takes claim's row in the permission table, and the
event refuses an owned task by naming the holder, just as claim does.

`internal/control` is the bridge, and it is deliberately the mirror image of
Anno's `internal/guard`: that one **fails open**, this one **fails closed**. A
scope check is a bystander in somebody's editing session and must not stop them
working when Macmuffin is broken. A control check stands between an agent and
work it may not be allowed to direct, and a restriction that evaporates when its
authority is unreachable is not a restriction. Orc missing is exit 10 with a
redirect to `claim`, not a silent yes.

Two smaller decisions:

- **Orc's wording is relayed, not restated.** The first draft wrapped it, and
  the result said the refusal twice: "you may not assign work to carol: bob may
  not direct carol: carol is not below bob in the tree". `control.Refused` now
  carries Orc's sentence and nothing else. A missing agent gets its own type so
  the message says *agent* — "nothing matches" beside a command taking an agent
  and a task leaves the reader guessing which was missing.
- **Reassigning somebody else's task is not supported**, even by their boss.
  Taking work off an agent mid-flight is a different act from handing out work
  nobody holds, and the spec's condition is only about the target.

The store's own encode/decode round-trip check caught the missing journal case
for the new op before a single event was written — the invariant paid for itself.

---

### 10.10 Amendment — identity is verified, where there is an authority to ask

§2.2 recorded that Macmuffin *resolved* a credential without verifying it, since
user records live in Mailman's store and that coupling was never designed. Orc
closes it without the coupling: it mints one key per identity, provisions
Mailman with the same key, and authenticates on every command, so
`orc introspect --only identity` prints who a credential really belongs to and
exits 7 when it proves nothing. `internal/control` asks, in `begin()`, before any
argument is examined.

The design turns on one distinction: **"you are not who you say" and "nobody
could be asked" are opposite answers and must not share a code path.** A definite
no refuses with exit 7. No `orc` on the machine leaves the claim standing, and
the session records that nobody checked — `muff` predates Orc and works beside
it, and refusing every command on a fleetless machine would make it unusable
(the mock stores and the standalone case both live there). `control.Unverifiable`
is the second answer, and it is deliberately not a fault the CLI returns.

`verify` reports an unchecked identity as a **note** rather than a problem. It
does not touch the exit code: a store on a machine with no fleet is healthy, and
a check nobody can keep green is a check people learn to ignore. But it is said,
because every permission in `policy` rests on that claim and a health report
that omitted the load-bearing assumption would be worth less than nothing.

Two things measured rather than assumed:

- **Cost.** `orc introspect` is ~3.5ms, so it runs on every authenticated
  command and there is no cache to invalidate. Caching a verification result
  would have been state, and the hook must never write.
- **`$ORC_IDENTITY` and `$ORC_SESSION` do not exist.** Orc's reference documents
  them as set in populated sessions, and comparing `$ORC_USER` against
  `$ORC_IDENTITY` would have been a cheaper check with no exec at all — but
  nothing in Orc's code sets either, so building on them would have been
  building on a doc promise.

The limit is stated in the reference rather than implied away: an agent that
controls its own `PATH` can hide `orc` as easily as it can lie about its name.
This catches a mistaken identity, not a determined one. Closing that needs an
authority Macmuffin cannot be denied — Orc starting the session — which is Orc's
to provide, not Macmuffin's to fake.

---

## 11. Validation and testing

### 11.1 Validation discipline

The house rule, applied without exception, as in both existing plans:

- **No meaningful zero values.** Every domain type has unexported fields and a
  `New…(…) (T, error)` constructor that validates. A zero `Task` cannot be
  produced outside `task`.
- **A private `validate() error` on every type with invariants**, run at
  construction *and* again on any value that arrives by decoding — a constructor
  proves nothing about bytes another process wrote.
- **Entry and exit guards.** `fault.Check(cond, where, format, args…)` is the
  assertion primitive, and it *returns* an `Internal` error. Assertions report;
  they do not abort.
- **Decoders are strict.** `json.Decoder` with `DisallowUnknownFields`, every
  field range-checked, every timestamp bounded, every name re-normalised. An
  unknown field means a newer Macmuffin wrote this store, and guessing is worse
  than saying so.
- **Bounds on everything from outside.** Scope entry count and length, subtask
  count, journal line length, name length, outbox size. Each a named constant
  with its reason in the doc comment.
- **No `panic`**; `cli` and `hook` recover at the top and convert any panic into
  `Internal` + exit 70 — and for the hook, exit 0, since rule 1 of §8.4 outranks
  reporting.
- **Write errors are errors.** Stdout goes through one `say` helper that returns
  its error. The only discards are explicit `_, _ =` with a comment saying why
  nothing can be done. No bare `_ = err` anywhere.
- **Every `os`/`io`/`exec` error wrapped** with `%w`, the operation, and the path.

Errors are typed, carry position, and unwrap to sentinels, so §5's exit codes are
derived mechanically in exactly one place — and that place is now shared by all
three tools (§10.1). Multi-problem inputs report **all** problems via
`errors.Join`.

### 11.2 Testing

Conventions follow both plans: `package foo_test` for behaviour, `export_test.go`
where an internal guard is unreachable through the public API, table tests as the
default shape, `internal/fixture` as the single source of golden data.

**Table tests** — name normalisation, score validation, the §3 permission table
in full (every actor × action × task state, which is small enough to enumerate
and important enough to), scope matching, exit-code mapping, and every command's
argument handling, including `assign`'s deliberate refusal.

**Golden tests** — `fixture` holds a small store (five tasks: a draft, an unowned
pooled one, a claimed one with eight subtasks, a scopeless stub, a completed one)
and the exact rendered output of `pool`, `pool --all`, and `info` on each. The
two `pool` goldens differ by exactly the completed row, which is the whole of
§6's retention rule stated as a diff. A change to the board layout breaks exactly
one constant, as Anno's `fixture.ExampleGo` does.

**Fuzz targets** — `task.ParseName` (output always re-parses to itself),
`scope.Match` (no panic, no path escaping the root ever matches), journal-line
decoding, and `hook.Run` (no input whatsoever produces an exit code other than
`0` or `2`, which is Anno's `FuzzRun` property and the reason its hook is
trusted).

**Property tests**
- Replaying a journal is deterministic; any *prefix* replays without error, which
  states crash-consistency as a test rather than a hope.
- A claimed task has exactly one owner under any interleaving of events.
- `scope.Match` is closed under symlink resolution: no path resolving outside the
  worktree root ever matches, for any scope.
- `pool` never shows another agent's draft, over generated stores.

**Fault injection** — the filesystem is an `ops` struct with `export_test.go`
setters, exactly as `edit.commit` does it in Anno. Every call gets a test that
fails it and asserts: the store is unchanged, the error is the right sentinel, no
temp file survives. Same for a full disk, a read-only store, a lock held by
someone else, and a `rename` that fails after a successful `fsync`.

**Concurrency** — the tests Macmuffin exists to pass:
- 64 goroutines claiming one task: exactly one succeeds, 63 get `ErrConflict`
  naming the winner, and the journal parses.
- 8 real subprocesses (`go test` re-executing the test binary) doing the same,
  which is the only honest test of `flock`.
- `create` racing `create` on one name: one task, one `ErrConflict`.
- `delete` racing `info`: `info` either succeeds or returns `ErrNotFound`, never
  a partial read.
- Concurrent subtask completion: every event lands, counts are exact.

**Notification tests** — `notify` against a recorder: the body and recipients of
every notice pinned as goldens, an exec failure leaving an outbox entry, the next
command draining it, and the give-up threshold reported by `verify`.

**End-to-end CLI tests** — drive `cli.Main` with `bytes.Buffer` streams and a
`t.TempDir()` store, asserting stdout, stderr, and exit code together. Every
command in `Reference.md` is a test case that must reproduce.

**Hook tests** — `TestNothingUnexpectedEverBlocks` enumerating every rule-1 input
from §8.4, a subprocess test that runs the real `muff-hook` binary, and a
deadline test with a deliberately stalled store.

**Hygiene** — `go test -race ./...` across the workspace, `-count=2` to catch
state leaking between runs, fuzz corpora committed. No test reads the real
`$HOME`; `store.Root` is resolved from an injected environment.

---

## 12. Milestones

| # | Deliverable | Done when |
|---|---|---|
| 0 | `Orc/Common` extracted; Anno and Mailman retrofitted; `ExitCodes.md` — **done** | Both suites green under `-race -count=2`; every golden byte-identical; fuzz corpora still pass; `go.work` and `replace` directives both build; both tools map errors through the shared table and their exit codes are unchanged; `edit.Commit`'s injected-failure tests still drive every step through `common/commit`. |
| 1 | `task` + store-less basics — **done** | Name normalisation fuzzes clean (300k+ execs); score and status validation table-tested; no test touches `$HOME`. |
| 2 | `store` — **done** | Journal prefix-replay property holds and is fuzzed (1.7M execs); `Apply` is the only conditional-write path; fault injection covers every fs call; the 64-goroutine and 8-process claim races are green under `-race`. |
| 3 | `policy` + `create`, `push`, `claim` — **done** | Permission table fully enumerated (every actor × action × task state); claim races green through both the store and the CLI under `-race`. |
| 4 | `render`, `view` + `pool`, `info` — **done** | Board, `--all` board, and card goldens byte-exact; coloured output strips to the plain rendering byte-for-byte, for every task in the corpus. |
| 5 | `scope`, `repo` + `scope`, `worktree`, `check-scope` — **done** | Escape-resolution property fuzzed clean (6.5M execs); every worktree binding refusal tested; `check-scope` exit codes pinned, including escape as 11 rather than 9. |
| 6 | Subtasks + `status`, `complete`, `delete` — **done** | Every `delete` refusal tested, the forced-`complete` journal entry pinned, and the deletion log recovers like a journal (any prefix reads; interior corruption is refused). |
| 7 | `notify` + `invite`, `kick`, `leave` — **done** | Outbox survives an exec failure and drains on the next command; notice bodies pinned. |
| 8 | `hook` + `muff-hook` — **done** | `FuzzRun` clean; `TestNothingUnexpectedEverBlocks` complete; deadline test green. |
| 9 | Anno calls `check-scope`; `verify` — **done** | Anno refuses an out-of-scope `anno write` end-to-end; store-corruption cases each reported. |
| 10 | Orc integration — **done** | `identity` swapped for Orc's real contract; `assign` built on Orc's control contract (§2.3); exercised with two live agents. |

Milestone 0 is a prerequisite, not part of Macmuffin, and is the one place this
plan can damage working software — hence its acceptance criterion is "nothing
changed". Milestones 1–3 are one coherent sitting and are where the correctness
lives; 4 makes it usable; 8 is where it starts enforcing anything.

### Where it stands

Every milestone is done. `muff` and `muff-hook` build and work end to end: a task
can be created, scoped, pushed, claimed, assigned, worked, collaborated on,
completed, and deleted; the hook refuses an out-of-scope edit before it happens;
`anno write` asks `muff check-scope` and relays the refusal; `assign` asks
`orc check-control`; identity is verified against `orc introspect` where an Orc
is installed; `verify` reports damage without repairing it.

The whole workspace — Anno, Common, Macmuffin, Mailman, Theme — is clean under
`gofmt`, `go vet`, and `go test -race -count=2`.

What is left is not on this plan:

- **The `PATH` limit on identity** (§10.10). Closing it needs Orc to be the thing
  that starts the session, which is Orc's to provide.
- **Reassigning a task somebody else owns** is deliberately not supported
  (§10.9). If it is ever wanted, it is a new event and a new rule, not a tweak.

---|---|
| — | Identity is **verified** now: `orc introspect --only identity` is the authority, and §10.10 records what that does and does not cover. |
| — | `assign` is **built**: Orc now exists, and `orc check-control` is the contract §2.3 was waiting for. Exercised against a real two-agent fleet. |
| — | Milestone 0's remainder (`source` and `commit` into `Orc/Common`) has since landed; Anno reads both from there, and Dock is no longer blocked. |

Nothing else in this plan is outstanding. Where the implementation departed from
what was written here, §10.3–§10.7 record it and say why.

---

## 13. What is deliberately not built

- **`assign`** (§2.3) — waiting on Orc's agent-control contract. The only
  documented command not delivered.
- **Subtask grouping into steps.** The vision asks for subtasks "arranged in
  groups as steps"; the reference gives no syntax for it, and the flat list is
  what ships. This is the plan's one open divergence from Vision.md, deferred
  rather than dropped: `sub.add` events carry no group field, so adding one later
  is an additive journal change with no migration.
- **Scope overlap reporting.** Two tasks may declare the same file and Macmuffin
  will not mention it (§8.2).
- **No task dependencies or blocking edges.** A general DAG is a project planner,
  and the vision asks for minimal and highly focused.
- **No due dates, estimates, or burndown.** Priority and difficulty are the two
  scores the reference names, and two scores that get used beat six that do not.
- **No daemon, no sockets, no ports.** The filesystem is the medium, as in
  Mailman.
- **No search index.** The pool is small; a full scan is fast and cannot disagree
  with the source of truth.
- **No rename or re-scoring.** A renamed task breaks every worktree binding and
  every reference in already-sent mail.
- **No history rewriting.** The journal is append-only, including mistakes.

Each is cheap to add later and expensive to remove once agents depend on it.

---

## 14. Decisions

Settled with the author:

1. **Draft, then push** (§1) — `create` drafts privately; `push` publishes.
2. **No subtask grouping** (§1, §13) — flat list; the vision's "steps" are
   deferred.
3. **`assign` not built** (§2.3) — rather than guess Orc's control contract or
   default open.
4. **`muff check-scope`, called by Anno** (§8.3) — enforcement happens where it
   is decidable, at the cost of a change to Anno.
5. **The hook fails open** (§8.4) — a broken store must not freeze every agent's
   editing.
6. **`Orc/Common` extracted now** (§10.1) — before Macmuffin, with a
   behaviour-preserving retrofit of Anno and Mailman. It carries `source` and
   `commit` for Dock as well, so Anno is retrofitted once rather than twice.
   Amended in flight: `style` is `orc/theme`, already extracted and already
   depended on by four modules, so `Common` does not take it (§10.1).
7. **`verify` kept in both Macmuffin and Mailman** (§5).
8. **An owner cannot `leave`, and only an owner sets `scope`** (§3).
9. **`invite` and `kick` notify** (§7), through a journaled outbox with retry.
10. **`complete` refuses on unfinished subtasks; `--force` journals the skip**
    (§5).
11. **Scope overlap is not reported** (§8.2).
12. **Module paths are `orc/common` and `orc/macmuffin`** (§10), matching
    `orc/anno` and `orc/mailman`. No VCS host is baked into an import path.
13. **One exit-code table, in `common/fault`, documented in
    `Claude/Docs/ExitCodes.md`** (§5, §10.1) — shared numbering, not three
    agreeing copies of it.
14. **Subtask names are unique per-task** (§2.1). Two tasks may each hold a
    `write-tests`; a subtask is always addressed as `<task> --sub <name>`, which
    is what every documented command already does.
15. **`pool` shows active tasks; `--all` adds completed ones** (§5, §6). Nothing
    is deleted to keep the board readable.

Nothing is open. The plan is startable at milestone 0, and the assumptions it
still rests on are external rather than undecided: Orc's credential contract
(§2.2) and Orc's agent-control contract (§2.3), each isolated to one package so
that landing them is a rewrite of one file, not a redesign.
