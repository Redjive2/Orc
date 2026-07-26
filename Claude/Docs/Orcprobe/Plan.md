# Orcprobe — Implementation Plan (Go)

Derived from [Vision.md](../../../Docs/Orcprobe/Vision.md) and
[Reference.md](../../../Docs/Orcprobe/Reference.md), and written to the
conventions [Anno](../Anno/Plan.md), [Mailman](../Mailman/Plan.md), and
[Macmuffin](../Macmuffin/Plan.md) already establish for this tree.

Both of those were written from this brief, verbatim:

> A tool, `orcprobe`, separate from all the others. It just:
> - copies the current state of all the Orc tools
> - creates a new test environment
> - lets me access everything as if I was a god-agent
> - does **not**, under any circumstances, bring over active agents **or** let me
>   affect the real environment

Guiding constraints, in priority order:

1. **Inert** — nothing orcprobe does may change the real world. Real stores are
   opened read-only, once, and never again. The binary contains no code path
   that spawns a Claude session, sends mail outward, or opens a socket.
2. **Honest** — every barrier states what it actually enforces. A guard that
   can be defeated says so in `doctor` rather than implying a jail it is not.
3. **Robust** — same discipline as the rest of the tree: no panics, no partial
   writes, every error positioned and classified.
4. **Omniscient inside** — within a probe there are no permissions. Reads are
   unmediated, any identity is available instantly, nothing is hidden.

Orcprobe differs from every other tool in the tree in one structural way, and it
drives the whole design: **it is the only tool that reads state it does not
own.** Anno owns files, Mailman owns mail, Macmuffin owns tasks. Orcprobe owns
nothing and touches everything — so its correctness argument is not "does it
work" but "is the real world provably unchanged afterwards" (§9).

---

## 1. Semantics recovered from the brief

**"Copies the current state" means a snapshot, not a mount.** A probe is a
byte-copy taken at a moment, living in its own root, never a symlink or a
reference back. Two probes taken a minute apart are independent worlds.

**"A new test environment" means a named, disposable world.** Probes are
plural. Each has a name, a creation record, a manifest of what came across, and
a `destroy` that removes it whole. Nothing accumulates in a place the user has
to remember to clean.

**"As if I was a god-agent" means identity is free, not that identity is
absent.** The Orc tools are identity-mediated: `mailman` acts as `$ORC_USER`,
`muff` refuses owner-only actions. A probe keeps that machinery intact — it is
half the thing worth testing — and hands the user every key at once. So there
are two access modes: *act-as*, which runs the real tools under a chosen
identity, and *omniscient views*, which read the store directly with no identity
at all and show what no real agent could see (§7).

**"Does not bring over active agents" is about liveness, not about data.**
Confirmed with the user: state is copied, then **neutered** — claims released,
owners cleared, pending notifications dropped, worktree links cut, credentials
reminted. The probe world has a full history and nobody working in it. A
`--live-state` flag keeps ownership intact for debugging a real situation; it
never changes the fact that no agent is ever launched.

**"Or let me affect the real environment" is a property of the probe process
tree, not of orcprobe alone.** Once `orcprobe shell` hands control to
`mailman`, orcprobe is no longer in the loop. So the wall cannot live only in
orcprobe: it is env redirection plus shims plus a stamp the other tools check
(§4). §4.6 says plainly what that does and does not stop.

**A probe is a world, not a directory.** Mailman, Macmuffin, cq, the repo, and
the Claude hook configuration are one system — a task scope enforced by a hook
against a repo that no longer exists is not a test environment. Confirmed with
the user: state dirs **and** a repo copy **and** the relevant `.claude/`
configuration all come across.

---

## 2. What exists to copy

Resolved from each tool's plan. Every root has an env override, which is the
lever the whole design turns on.

| Tool      | Root resolution                                            | Shape                        |
|-----------|------------------------------------------------------------|------------------------------|
| Mailman   | `$MAILMAN_HOME` → `$XDG_DATA_HOME/mailman` → `~/.mailman`   | journals, write-once `.msg`  |
| Macmuffin | `$MACMUFFIN_HOME` → `$XDG_DATA_HOME/macmuffin` → `~/.macmuffin` | per-task journals, outbox |
| Communiqué| `$CQ_HOME` → `$XDG_STATE_HOME/cq` → `~/.cq`                 | applied journal, cursor      |
| Anno      | none — operates on files                                    | needs the repo               |
| Dock      | none — operates on files                                    | needs the repo               |
| Orc       | `$ORC_HOME` → `$XDG_DATA_HOME/orc` → `~/.orc`               | identities, and the one **plaintext** keyring |

Two properties of these stores make the copy tractable, and both are inherited
rather than assumed:

- **Append-only journals, write-once messages.** A copy taken while agents write
  can only ever catch a partial *final* line, and both tools already define that
  case: a truncated final line is an interrupted append and is dropped with a
  note. So a live copy is not a torn state — it is an *earlier* state.
- **Per-entity locks, held only for appends.** Nothing needs a global quiesce,
  so orcprobe never takes a lock on a real store by default and never blocks a
  working agent.

`orcprobe create --quiesce` takes each advisory lock for the duration of that
entity's copy, for a user who wants an exact cut and accepts briefly stalling
live appends. Off by default: blocking real agents to build a toy is the wrong
trade.

---

## 3. Storage

Root is `$ORCPROBE_HOME`, else `$XDG_DATA_HOME/orcprobe`, else `~/.orcprobe`.
Creation refuses if that path resolves inside any real tool root, or inside the
source repo — a probe inside the thing it copies is a loop and a foot-gun.

```
<root>/
  version                      format version; an unknown one is a hard, clear error
  current                      name of the default probe for bare commands
  probes/<name>/
    probe.json                 immutable creation record (§3.1)
    STAMP                      the sandbox marker; contains the probe id
    manifest.jsonl             append-only: copied / neutered / refused, one line each
    identities.json            probe-minted keys, 0600 — never real ones (§5.3)
    env                        the exact environment the shell exports, readable and diffable
    bin/                       shims for every Orc binary (§4.2)
    state/mailman/             copy of the real Mailman root, stamped
    state/macmuffin/           copy of the real Macmuffin root, stamped
    state/cq/                  copy of the real cq root, stamped
    state/orc/                 copy of the fleet, keyring reminted, session claims cut
    repo/                      copy of the working repo, remotes stripped (§5.4)
    claude/                    copied hooks and settings, rewritten to the probe (§5.5)
    checkpoints/<label>/       saved whole-probe states (§8)
    log/session.jsonl          every command run inside the probe, with exit code
```

Same journal discipline as the rest of the tree, for the same reason and with
the same failure rule: a truncated final line is dropped with a note, an
unparseable line anywhere else is corruption and a hard error.

### 3.1 The creation record

`probe.json` is written once, at the end of creation, through the commit
sequence Anno establishes: temp file in the same directory, write, `fsync`,
`chmod`, `rename`, `fsync` the directory, temp removed on every failure path. A
`rename` onto an existing name is the uniqueness check, enforced by the
filesystem rather than by a read-then-write another process can interleave.

It carries: probe id (`<unix-micros-hex>-<8 random hex>`, as Mailman's message
ids), name, created, orcprobe version, and for each source: absolute path, byte
count, file count, and a content digest. The digest is what `orcprobe doctor`
compares against the real root later, to answer "has the world moved since this
probe was taken" without re-copying anything.

The record is written **last**. A probe without `probe.json` is an interrupted
creation, is refused by every other command, and is what `create` cleans up on
its own next run.

---

## 4. The wall

Confirmed with the user: **env redirection plus guards**, not a container and
not a seatbelt profile. Five layers, weakest to strongest, and then an honest
statement of what the stack does not stop.

### 4.1 Environment

`orcprobe` composes an environment and writes it to `probes/<name>/env` so it
can be read, diffed, and pasted. Nothing is implicit.

| Variable                          | Set to                                | Why                                           |
|-----------------------------------|---------------------------------------|-----------------------------------------------|
| `ORCPROBE_ACTIVE`                 | the probe id                          | the tripwire every guard keys off (§4.3)      |
| `MAILMAN_HOME`                    | `state/mailman`                       | primary redirection                           |
| `MACMUFFIN_HOME`                  | `state/macmuffin`                     | primary redirection                           |
| `CQ_HOME`                         | `state/cq`                            | primary redirection                           |
| `XDG_DATA_HOME`, `XDG_STATE_HOME` | `state/xdg`                           | backstop for any tool without a `*_HOME` yet  |
| `HOME`                            | left alone by default; `--fake-home` points it at `state/home` | see §4.6 |
| `ORC_USER`, `ORC_KEY`             | the chosen probe identity             | act-as (§7.1)                                 |
| `CQ_NO_NUDGE`                     | `1`                                   | kills the outbound sync spawn at the source   |
| `CLAUDE_CONFIG_DIR`               | `claude/`                             | hooks resolve to probe copies (§5.5)          |
| `GIT_CONFIG_GLOBAL`               | `repo/.probe-gitconfig`               | no real git identity, no real credential helper |
| `PATH`                            | `bin/` prepended                      | shims win over the real binaries (§4.2)       |

### 4.2 Shims

`bin/` holds one small wrapper per Orc binary (`mailman`, `muff`, `cq`, `anno`,
`dock`, `orc`, and `git`). Each wrapper is the same `orcprobe-shim` binary,
hard-linked under each name, which:

1. refuses immediately if `ORCPROBE_ACTIVE` is unset or does not match the
   `STAMP` beside it — so a shim copied out of a probe is inert;
2. re-asserts the **isolation** variables from `env`, so a subshell that
   clobbered `MAILMAN_HOME` is corrected rather than obeyed. Identity is
   deliberately *not* re-asserted: `orcprobe as bob` sets `ORC_USER` on purpose,
   and a shim that corrected that would break the god-agent's main verb. The
   rule, stated once: **the shim protects isolation, never identity**;
3. refuses a denied invocation outright — `cq serve` on a non-loopback `--addr`,
   `cq sync` in any form, `git push`/`fetch`/`pull`, a network `git clone`, and
   `orc` at all — with an `ErrEscape` fault naming what it blocked and why;
4. runs the real binary and appends the invocation and its exit status to
   `log/session.jsonl`, then exits with that status.

The wrapper stays resident for the length of the call. An `exec` would be
cheaper and was the first design, but it cannot record how the command turned
out — and a session log without exit statuses answers "what ran" while leaving
"what worked" to guesswork. One extra process per command is the right price.

`orc` is refused wholesale rather than by subcommand. It does not exist yet, so
there is no list of spawn verbs to enumerate; refusing all of it errs in the
direction rule 1 demands, and narrowing it is a one-line change once orc lands.

### 4.3 The stamp

Every store root inside a probe contains `.orcprobe-stamp`, holding the probe
id. Real roots must never contain one, and `doctor` checks that too.

The guard that makes this worth anything is **in the other tools**, not in
orcprobe — and it is now built. It lives in `orc/common/sandbox`, so the tool
that writes a stamp and the tools that check one share a single definition;
two definitions of a security boundary is one too many. On startup, when
`$ORCPROBE_ACTIVE` is set, a tool refuses to open a store root that lacks a
matching stamp. That is the check that catches the one
real escape route — an absolute path, a hardcoded `~/.mailman`, a
`MAILMAN_HOME` restored by a shell profile.

Three properties make it worth having rather than merely present:

1. **It runs before anything is created.** Opening a store creates its layout,
   so a guard that ran after would already have written to the real world.
2. **It fails closed.** A stamp that cannot be read for any reason other than
   absence is a refusal. A guard that assumes the best when it cannot check is
   one the operator only believes in.
3. **It costs nothing outside a probe.** With `$ORCPROBE_ACTIVE` unset it is one
   map lookup and a `nil`. Every ordinary run of every tool goes through it, and
   the first rule of adding a check to somebody else's hot path is that it must
   not change what that path does.

The refusal is an escape (exit **11**), not a permission problem — see §10 for
where it landed in each tool.

### 4.4 Network

Mailman and Macmuffin have no network surface. cq does, and it is the one path
by which a probe could reach the real server:

- `CQ_NO_NUDGE=1` stops the nudge spawn inside mailman and muff themselves.
- The shim refuses `cq sync` entirely, and refuses `cq serve` unless `--addr`
  binds loopback.
- `state/cq/cursor.json` is reset and the outbound queue dropped at neuter
  time (§5.2), so even a sync that somehow ran would carry nothing.
- No sync token or operator password is ever copied (§5.3), so a sync that
  somehow ran, carrying something, would fail to authenticate.

Four independent stops, each sufficient alone. This is the one place the design
is deliberately redundant, because it is the one place a mistake is visible to
someone other than the user.

### 4.5 Git

`repo/` is a plain recursive copy with `.git` included, then: every remote
removed, `GIT_CONFIG_GLOBAL` repointed at a probe-local config with no
credential helper and no user identity, `push` refused by the shim. The copy is
detached from origin in three independent ways for the same reason as §4.4.

Worktrees registered in the real repo are **not** copied — a probe worktree
pointing at a real checkout is precisely the escape this tool exists to
prevent. `git worktree` metadata is pruned during the copy and the removals are
recorded in the manifest.

### 4.6 What this does not stop

Stated plainly here, printed by `orcprobe doctor`, and printed once on
`orcprobe shell` entry:

- **An absolute path defeats env redirection**, and is caught by the §4.3 stamp
  guard — *in the tools that have it*. Mailman, Macmuffin, and cq do. Anno and
  Dock hold no state of their own, so there is nothing for them to guard. **Orc
  does not exist yet, and will need the same three lines when it does**; until
  then a probe's protection against it is that the shims refuse `orc` outright.
  Any other binary that writes to an Orc store — a script, a hand-rolled tool, a
  build of Mailman from before this landed — is unguarded.
- **`HOME` is real by default.** Redirecting it breaks the shell, git, and
  Claude in ways that make the probe unlike the thing it models, so it is opt-in
  via `--fake-home`. With real `HOME`, a tool that ignores its `*_HOME` variable
  finds the real store — and the stamp guard catches that too, which is what
  makes leaving `HOME` real a defensible default rather than a hole.
- **The shims are a `PATH` convention.** A full path to the real binary bypasses
  them, and then only the stamp guard is in the way. That is now a real
  backstop rather than a promise, but it is one layer, not two.
- **Nothing here is a kernel boundary.** A `sandbox-exec` profile denying writes
  outside the probe root is the obvious upgrade and is deliberately not built
  (§12) — it belongs on top of this, once the layers below it are proven.

The tool's guarantee is therefore precise, and worth stating as such: *orcprobe
itself never writes outside its own root, and never opens a real root except
read-only during a snapshot.* Everything above is defence for what runs
**inside** a probe.

---

## 5. Snapshot and neuter

Two phases, both recorded line by line in `manifest.jsonl`. Copy is mechanical;
neuter is the part with judgement in it, so every decision it makes is a
manifest line the user can read back.

### 5.1 Copy

Per source root: walk, copy, preserve modes, never follow a symlink out of the
tree (a symlink leaving the root is dropped and recorded, not resolved). Real
roots are opened `O_RDONLY`; the copy is verified by digest as it lands.

**`clonefile` is not used, and will not be.** The plan called for it: on APFS a
clone would snapshot a large mail store in milliseconds and cost no disk until
it diverged. Reaching it needs `clonefile(2)`, which Go's standard library does
not expose — and the two ways to get there both break a rule this tree holds
deliberately:

- `golang.org/x/sys/unix` has it, and every tool here is stdlib-only;
- shelling out to `cp -c` would put an `exec` in the one package that touches
  real paths, and orcprobe's no-spawn guarantee (§9) is worth more than a fast
  copy.

A raw `syscall.Syscall` on darwin goes through a deprecated libc shim that is
not documented to carry `clonefile`, and a copy that silently half-works is the
worst possible outcome for the one function that reads real state. So the copy
is a plain streaming copy, and checkpoints cost what a copy costs. If a probe of
a very large store ever becomes slow enough to matter, the honest fix is to
revisit the stdlib-only rule for this one call, not to smuggle it in.

Immediately after copying, orcprobe runs each tool's own integrity command
inside the probe — `mailman verify`, `muff verify` — and prints their reports.
A snapshot taken from a live store may legitimately have a dropped final journal
line; the user should see that, and see that nothing worse happened.

### 5.2 Neuter

Default on; `--live-state` skips it. Every change is an **append**, never a
rewrite, and — this is the part that matters — every append is written in the
**owning tool's own vocabulary**, not a probe-specific one:

> A claim is released by appending an ordinary `release`, exactly as the agent
> holding it would have. A collaborator is removed by appending their `leave`.

A probe-only event kind (`probe.neuter`, as an earlier draft of this plan had
it) would force every tool that replays a journal to learn what a probe is, and
would break the replay of any that had not. An ordinary release is something
that could have happened in the real world, so a neutered probe is not a special
state anything has to know about — it is a state where everyone went home. What
records *why* it happened is the probe's manifest, which is orcprobe's own file.

| Real thing                        | In the probe                                                        |
|-----------------------------------|---------------------------------------------------------------------|
| Macmuffin task owner              | released; task returns to the pool with its status and scope intact |
| Macmuffin collaborators           | removed, each by an appended `leave`                                |
| Macmuffin `worktrees/*.json`      | **dropped** — see below                                             |
| Macmuffin `outbox/*.json`         | dropped — these are undelivered notifications aimed at real agents  |
| cq `cursor.json`, `pending`       | removed; the probe has never synced and never will                  |
| cq `applied.jsonl`                | kept — that is history, not liveness                                |
| cq anything that looks like a credential | removed whatever it is called (§4.4's fourth stop)           |
| Mailman read state, archive, receipts | preserved verbatim — this is data, not liveness                 |
| Mailman user accounts             | preserved as mailboxes, keys reminted (§5.3)                        |
| Git remotes, worktrees, credentials | removed (§4.5)                                                    |
| Claude hooks naming a binary outside the probe | disabled and recorded (§5.5)                           |

**Worktree bindings are dropped rather than repointed.** The plan first said
"rewritten if the path lands inside `repo/`". They cannot be: a binding is stored
at `worktrees/<hash>.json`, keyed by a hash of the resolved path, and orcprobe
does not know Macmuffin's hash function. A rewritten path under an unchanged key
is a binding the hook can never find — worse than no binding, because it looks
present. Dropping is honest, and rebinding inside a probe is one `muff worktree`
away.

The scrub is tolerant of ops it does not recognise and intolerant of lines it
cannot parse. Macmuffin is still being built, and refusing to make a probe
because a task carried an op orcprobe had not heard of would be this tool
getting in the way of the tool it exists to test. A line that is not JSON at all
is corruption — refused, unless it is the *final* line, which is an interrupted
append and is dropped exactly as every journal reader in this tree drops it.

### 5.3 Credentials

**No real credential is ever copied.** Mailman's `user.json` holds algorithm,
salt, and key digest — not the key — so the probe rewrites each with a fresh
salt and the digest of a freshly minted probe key, and stores the plaintext
probe keys in `identities.json` at `0600`. Consequences, all wanted:

- the user can act as anybody instantly, because orcprobe knows every key;
- a probe leaked or committed by accident discloses no real credential;
- a probe key is worthless against the real store, because the real store's
  digests are unchanged and unknown here.

cq's operator password hash and sync tokens are not copied at all. `cq serve`
inside a probe needs `cq admin operator` run locally first, which is correct: a
probe's web surface should never accept the real password.

### 5.4 The repo

`--repo <path>` names it, defaulting to the git root containing the working
directory. Copied whole (including `.git`, including uncommitted work — the
current state is the point), then detached per §4.5. `--no-repo` skips it for a
mail-only probe.

### 5.5 Claude configuration

Project `.claude/settings.json` and `~/.claude/settings.json` are copied into
`claude/`, and `CLAUDE_CONFIG_DIR` points there. So Anno's guard hook and
Macmuffin's scope hook behave inside a probe as they do outside — against probe
state, over the probe's repo copy.

Hooks are the one piece of copied configuration that *executes*, so the scrub
applies one rule to them:

> A hook whose command is a **bare name** — `anno-hook`, `mailman` — is kept. It
> resolves through the probe's PATH, where the shims come first, so it runs
> against probe state like everything else. A hook whose command is an
> **absolute path outside the probe** is disabled and recorded: it names a
> binary the probe cannot vouch for, bypasses the shims entirely, and would run
> on every matching tool call.

Rewriting such a command to point at the probe's `bin/` was the first design and
is wrong: orcprobe would be guessing that some binary in the probe does the same
job as the one the operator configured. Disabling is the honest failure, and the
manifest names what was disabled so it can be put back deliberately.

A settings file orcprobe cannot parse is **left exactly as it is** and reported.
Rewriting a file on a guess about its shape is how a working hook configuration
becomes a broken one.

---

## 6. Commands

```
orcprobe <command> <args...>
```

| Command                             | Does                                                           |
|-------------------------------------|-----------------------------------------------------------------|
| `create <name> [--from <probe>]`    | Snapshot the real world (or fork a probe) into a new one       |
| `list`                              | Every probe: age, size, source drift, checkpoint count         |
| `use <name>`                        | Set the default probe for bare commands                        |
| `shell [--as <user>]`               | Subshell inside the probe, environment applied                 |
| `as <user> -- <cmd...>`             | One command as one identity                                    |
| `world`                             | The whole probe on one screen (§7.2)                           |
| `mail [query]`                      | Every mailbox at once, cross-user (§7.2)                       |
| `tasks`                             | The full pool, including tombstoned tasks                      |
| `journal <thing>`                   | A raw append-only journal, decoded, one event per line         |
| `timeline [--since <t>]`            | Every tool's events merged into one time-ordered table         |
| `save <label>` / `restore <label>`  | Checkpoint and rewind within a probe (§8)                      |
| `diff <a> <b>`                      | What differs between two probes, or a probe and its source     |
| `doctor`                            | Verify every guard and say which are absent (§4.6)             |
| `destroy <name>`                    | Remove a probe whole                                           |
| `manifest`                          | What was copied, neutered, and refused                         |

Flags shared by every command, as elsewhere in the tree: `--probe <name>`,
`--no-color`, `--width <n>`, `--yes`.

`destroy` is the only irreversible command. It prints what it will remove,
refuses any path outside `$ORCPROBE_HOME`, and needs `--yes` when stdin is not
a terminal — which, for an agent, is always. Same rule as `mailman prune`.

**There is no `run`, no `spawn`, no `agent`.** Not omitted for scope: orcprobe
must not contain a path that starts a Claude session, and the absence is
enforced by a test asserting no `os/exec` call in the tree targets anything but
a shim or a shell (§9).

---

## 7. God mode

Confirmed with the user: act-as **plus** omniscient views.

### 7.1 Act-as

`orcprobe shell --as alice` starts the user's `$SHELL` with the §4.1 environment
and `ORC_USER`/`ORC_KEY` set to alice's probe identity. The prompt is marked so
a probe shell is never mistaken for a real one:

```
probe:scratch(alice)$ mailman inbox
```

`orcprobe as bob -- muff claim refactor` is the one-off form. Switching identity
costs nothing and needs no password, because §5.3 minted every key.

A synthetic `god` identity exists and is the default: a real mailbox, recipient
of nothing, holder of every capability the tools grant to any single user. It is
what `shell` uses with no `--as`. The name is plain rather than `@god` because
Mailman's names are lowercase letters, digits, and `. _ -` — a record orcprobe
wrote under any other name is one Mailman would refuse to read.

### 7.2 Omniscient views

These bypass identity entirely and read the store directly — the things no real
agent could ever see:

- **`world`** — one screen, box-drawn: probe name and age, drift from source,
  every mailbox with unread counts, the task pool by status, cq queue depth and
  staleness, guard status from `doctor`. The screen a god-agent opens first.
- **`mail`** — every message in every mailbox in one table, sender and recipient
  columns, Mailman's own query language over the whole store rather than over
  one inbox. Reuses Mailman's query grammar so there is one language, not two.
- **`tasks`** — the pool with owners, collaborators, scope, and tombstones,
  including what `muff pool` hides.
- **`journal <thing>`** — the raw append-only truth for a user, task, or convo,
  decoded into a table. The debugging primitive the other tools deliberately do
  not expose.
- **`timeline`** — mail, task events, and cq actions merged by timestamp into
  one table. Cross-tool causality — a task claimed, the mail it sent, the reply
  — is otherwise reconstructable only by hand.

All read-only, all straight off the copied store, none of them requiring the
tools to be installed at all.

---

## 8. Checkpoints

A probe holds labelled checkpoints under `checkpoints/<label>/`, made with the
same copy as §5.1 — plain, not cloned; see §5.1 for why.

`save before-migration`, break everything, `restore before-migration`. `restore`
requires `--yes` non-interactively and records the rewind in the manifest, so a
probe's history stays legible after time travel.

Three decisions worth stating, because each is a place the obvious behaviour is
the wrong one:

**A checkpoint captures contents, never identity.** `state/`, `repo/`, and
`claude/` are saved and restored; `probe.json`, the stamp, `identities.json`,
`env`, `bin/`, and the manifest are not. A rewind that could change who you are
inside a probe — or revoke the keys you are holding — is one nobody can reason
about, and restoring a stamp or a record would either be a no-op or a way to
make a probe claim to be a different probe.

**The manifest is append-only *through* a rewind.** A restore is itself an event
in the probe's history, not a way to erase one. A probe that could be rolled
back without leaving a trace would be one whose manifest describes a history it
no longer has.

**A label is never overwritten.** `save before-migration` typed twice, an hour
apart, refuses the second rather than silently discarding the first hour — the
one operation in this tool that would destroy state without saying so.

Restoring swaps directories rather than writing over them: the new copy is built
beside the live one, the old is renamed aside, the new is renamed in, and only
then is the old removed. A restore killed halfway leaves either the old state or
the new, never a directory half of each.

`create --from <probe>` — forking a probe rather than rewinding one — is not
built. It is a snapshot of a snapshot with a different name on it, and nothing
so far has wanted it.

---

## 9. Validation and testing

Same shape as the rest of the tree — golden-file CLI tests, a `fixture` package
that builds synthetic source worlds, table-driven unit tests — plus three
classes specific to this tool:

1. **The inertness test.** Build a synthetic real world, digest every file in
   it, run the *entire* orcprobe test suite against it, digest again, assert
   byte-identical. Any test that mutates a source root fails the whole suite.
   This is the tool's central correctness claim, so it is a test, not a review
   item.
2. **The no-spawn test.** Static assertion over the package tree that no
   `os/exec` target resolves outside `bin/` or the user's shell, and that
   nothing links a Claude session path.
3. **Escape tests.** For each §4.6 hole: attempt the escape from inside a probe
   and assert the stamp guard refuses it (once §10 lands) or that `doctor`
   reports it as unguarded (until then).

Plus: crash-mid-copy leaves no probe that any command will accept; copy from a
concurrently-written store yields a valid earlier state; neuter is idempotent;
`--live-state` and default differ in exactly the §5.2 table and nowhere else.

---

## 10. Cross-tool work

Orcprobe needs four small changes elsewhere. Each is additive and independently
useful; none is a blocker for a first usable version.

| Tool                      | Change                                                                    | State |
|---------------------------|---------------------------------------------------------------------------|-------|
| Common                    | `sandbox`: the guard, the stamp, and the tripwire variable, defined once   | **built** |
| Mailman                   | Guard in `store.Open`, before the layout is created                        | **built** |
| Macmuffin                 | Guard in `store.Open` **and** `store.Read` — the hook's path               | **built** |
| cq                        | Guard in `agent.New`, plus an escape code in its own fault vocabulary      | **built** |
| **Macmuffin**             | **Define the `release` journal op** — an owner drops a task back to the pool. Blocking: without it no probe can be fully inert (§5.2) | open |
| Orc                       | The same guard, when Orc gains state of its own                            | open |
| Mailman, Macmuffin        | Honour `$CQ_NO_NUDGE` — already in the cq plan, confirm it landed          | open |
| Macmuffin                 | Scope hook resolves worktrees relative to the store root, not absolutely   | open |

Two details of how it landed are worth recording, because both were wrong first:

**Macmuffin's `Read` is guarded as well as its `Open`.** `Read` creates nothing,
so guarding it looks unnecessary — but it is the constructor the scope hook uses,
and answering "is this edit in scope?" from the *real* pool while inside a probe
would let real state govern what happens in a sandbox. Quieter than a write, and
still a containment failure.

**cq needed an exit code before it needed a guard.** Its fault vocabulary is
still its own rather than Common's, and had no escape concept, so the refusal
classified as internal and exited 70 — reading as "cq has a bug" rather than
"containment failed". It now carries `ErrEscape`/`ExitEscape = 11` and
recognises Common's sentinel alongside its own. Mailman had the same gap in its
local exit table and now has the same case; Macmuffin already routed through
`fault.Code` and needed nothing.

**`release` is the one thing orcprobe needs Macmuffin to have that Macmuffin
does not.** It is now built and its journal refuses an op it does not know, so
orcprobe cannot invent one: an owned task in a probe keeps its owner, and the
probe reports itself as `partial` rather than `neutered` (§5.2). This is the one
place a probe falls short of "nobody is working here".

The change is small and worth having on its own terms. `leave` covers a
collaborator walking away and nothing covers an owner doing the same, which
leaves "I claimed this and shouldn't have" with no move but `delete`. Two
plausible shapes:

1. a new `release` op, which orcprobe would then append — one line changes here;
2. or letting `leave` release an owner when the task has no other holder, which
   keeps the vocabulary at twelve ops but weakens "a task is never orphaned by
   accident".

The first is cleaner and is what the code in `internal/neuter/macmuffin.go` is
written to switch to.

---

## 11. Package layout

Module `orc/orcprobe` at `Orc/Orcprobe/go.mod`. Stdlib only, as everywhere else.

```
cmd/orcprobe/         entry point
cmd/orcprobe-shim/    the hard-linked wrapper (§4.2)
internal/
  cli/        command parsing, exit codes, help
  fault/      the shared vocabulary, plus ErrEscape
  probe/      the probe root: create, open, list, destroy, doctor
  snapshot/   walking and copying source roots, clonefile
  neuter/     the §5.2 rules, one file per tool
  env/        composing and writing the environment
  shim/       the wrapper's refusal rules
  source/     locating real roots, per tool — the only package that knows them
  view/       world, mail, tasks, journal, timeline
  journal/    decoding the other tools' journals, read-only
  render/     tables and box drawings
  style/      colour, matching the tree
  clock/      injectable time
  fixture/    synthetic worlds for tests
```

`source/` is the only package that knows a real path exists, and it exposes
read-only handles. Everything else physically cannot reach outside a probe —
the isolation is a package boundary before it is a runtime check.

Orc itself is not built yet; `source/` carries a stub for it so adding Orc's
state is one file, not a refactor.

---

## 12. What is deliberately not built

- **A `sandbox-exec` jail.** The right next layer, and explicitly deferred: it
  belongs on top of a proven env layer, and it needs the §10 guards first to be
  worth its complexity.
- **Containers or VMs.** Off-pattern for this tree and far heavier than the
  problem.
- **Any agent spawning.** Structural, per §6.
- **Two-way sync back to real state.** A probe is a dead end by design; the way
  work leaves it is a patch the user applies themselves.
- **A daemon or watcher.** Every command is one process that exits.
- **A web UI.** cq is the web surface; a probe can run it locally if wanted.

---

## 13. Milestones

| # | Delivers                                                              | State |
|---|-----------------------------------------------------------------------|-------|
| 1 | `create`, `list`, `use`, `destroy`, `shell`, `as`, `manifest` — env layer and shims | **built** |
| 2 | Neuter, `--live-state`, hook handling in the copied Claude config      | **built** |
| 3 | Omniscient views: `world`, `mail`, `tasks`, `journal`, `timeline`     | **built** |
| 4 | Checkpoints, `diff`, `doctor` with full guard reporting                | **built** |
| 5 | The §10 stamp guards in Mailman, Macmuffin, and cq                    | **built** |

Milestone 1 is usable on its own: a copied world, a shell inside it, and no
agents. Everything after makes it safer and more legible.

### Milestone 1, as built

Four things came out differently from the plan above, each for a reason worth
keeping:

1. **Credential reminting moved from milestone 2 into 1.** `shell` and `as` are
   milestone 1's whole point, and both are useless without a key — a copied
   store's digests belong to keys orcprobe does not have. So §5.3 ships now and
   milestone 2 keeps only the liveness scrub.
2. **The shim stays resident** for the length of a call, to record exit status
   (§4.2).
3. **`clonefile` is deferred to milestone 4**, where checkpoints make it worth a
   raw syscall (§5.1).
4. **Every probe reports its own unkept promises.** A milestone-1 probe copies
   state verbatim — claims, owners, outboxes and worktree links all come across
   — so `create` prints that in red, and the manifest records it as a `defer`
   entry. A probe can therefore say, on its own, which of the tool's guarantees
   were true on the day it was made.

### Milestone 2, as built

The scrub is `internal/neuter`, one pass over three tools, and it changed three
things the plan had said differently — each written up where it belongs: the
appended events use **Macmuffin's own vocabulary** rather than a `probe.neuter`
kind (§5.2), worktree bindings are **dropped rather than repointed** because
their filename is a hash orcprobe cannot recompute (§5.2), and hooks pointing
outside a probe are **disabled rather than rewritten**, because rewriting one
means guessing that some binary in the probe does the same job (§5.5).

Two findings from building it:

- **The scrub must not introduce blank lines.** Appending opened the journal
  write-only, so the check for a trailing newline could not read the file's last
  byte and took its "add one to be safe" path every time — leaving a blank line
  between every appended event. Orcprobe's own replay skips blank lines, so
  nothing in its tests failed; Macmuffin's replay is another tool's code, and a
  journal only this tool can read is not a journal. There is now a test that
  reads the file back and refuses a blank line anywhere in it.
- **`--live-state` has to be loud.** A probe that kept its liveness is dangerous
  in exactly the way a neutered one is not, so it is marked `verbatim` in `list`,
  it prints a red line at creation, and its manifest says so. The failure to
  avoid is not "the flag did the wrong thing" but "a month later, nobody can tell
  which kind of probe this is".

### Milestone 3, as built — and the correction it forced

The views are two packages: `internal/read` decodes the other tools' stores
without their code, and `internal/view` turns that into tables. Nothing in
either runs another tool, which is what makes them the right thing to reach for
when a tool is what you are debugging — a probe whose binaries are not even
installed still reads.

`mail` takes Mailman's query grammar, ported rather than approximated: `&`, `|`,
`!`, `()`, and `= != ~` over the documented fields, with an unknown field always
an error. What differs is scope, and it is stated in the help, the reference,
and the package comment: Mailman evaluates against one mailbox, so `unread` and
`id` mean "unread by you" and "your puid"; orcprobe evaluates against the store,
where a message is unread by some people and read by others, so they mean
"unread by anybody" and "any recipient's puid".

**Macmuffin now exists, and it invalidated milestone 2's central decision.**
§5.2 said the scrub writes in the owning tool's own vocabulary, and appended a
`release` on that basis. Reading the real code:

- `Macmuffin/internal/store/journal.go` **hard-errors on an op it does not
  know**, and `release` is not one of the twelve it defines;
- `Macmuffin/internal/task/event.go` refuses an owner's `leave` outright — *"the
  owner cannot leave; a task is never orphaned by accident"*.

Both refusals are right for the real world, and together they leave orcprobe
with **nothing valid to append**. The `release` it was writing would not have
released anything; it would have made the whole task unreadable inside the
probe. So:

- collaborators still leave, which is ordinary Macmuffin and works;
- an owned task **keeps its owner**, and the probe says so — a `defer` entry in
  the manifest, a warning line naming the task at creation, and a third liveness
  state, `partial`, wherever a probe reports what it is;
- a test now walks any journal the scrub touched and fails on an op Macmuffin
  does not define, so the mistake cannot come back quietly.

This is the §10 Macmuffin row promoted from "worth having anyway" to **blocking
for a fully inert probe**. Until `release` exists, a probe of a world with
claimed tasks is scrubbed everywhere except ownership.

### Milestone 4, as built

Checkpoints, `diff`, and `doctor` — with `clonefile` dropped for the reasons in
§5.1, which is a change to the plan rather than a deferral.

**`doctor` is the command this milestone was worth doing for**, and it is more
useful than when it was planned, because milestone 5 gave it something real to
measure. Its checks come in two kinds, and the distinction is the whole design:

- *Structural* checks read the probe — stamps, redirection, shims, remotes, the
  permissions on `identities.json`. Facts about this probe, and cheap.
- *Behavioural* checks run the real tools and watch what they do. Whether the
  stamp guard is in force is a fact about **the binaries on this machine**: a
  build from before §10 landed will silently not have it. So doctor measures it
  — a scratch directory nothing has stamped, the tripwire set, the tool pointed
  at it — and reports what happened rather than what the plan says.

It has three outcomes, not two. **`not checked` is not reassurance**: a tool
that is not installed cannot be measured, and saying so is different from saying
the guard is absent *and* different from saying it is fine. The summary line
says which of the three the report as a whole amounts to, because the one thing
an operator must not do is skim a doctor report and take silence for safety.
`--strict` exits 11 when anything is absent or unmeasured.

Verified against a deliberately guardless build — a stub `mailman` that exits 0
whatever it is pointed at — which doctor reports as `absent` with "this build
does not refuse an unstamped store". That is the case the command exists for.

**A defect the smoke test found, which no unit test had:** a probe of a machine
where a tool has never run got an *unstamped* state directory. Minting creates
the god mailbox whether or not a mail store came across, so the directory
appeared anyway — and an unstamped store is one every tool now refuses, which
would have read as a bug in Mailman rather than a hole in the probe. Every
tool's state directory is now created and stamped whether or not there was
anything to copy: an empty stamped store is exactly what the tool would have
made itself. `create` says so in the manifest, and a test asserts the stamp.

### Milestone 5, as built — the hole is closed

The guard is `orc/common/sandbox`, called from `store.Open` in Mailman, `Open`
and `Read` in Macmuffin, and `agent.New` in cq. §4.3 has the three properties
that make it worth having; §10 has where each call sits and the two things that
were wrong first.

Verified end to end rather than only in tests: from inside a probe shell, with
`MAILMAN_HOME` forced back at the real store by absolute path, `mailman inbox`
refuses and exits 11, and the real store is byte-identical afterwards. The same
holds with `MAILMAN_HOME` unset entirely, where resolution falls through to
`~/.mailman`. Outside a probe, both commands behave exactly as before.

**Orcprobe's own exit code for an escape was wrong, and this is what found it.**
It used 9, chosen before `Claude/Docs/ExitCodes.md` existed. In the shared table
9 is *out of scope* and 11 is *escape* — so a hook seeing 9 from orcprobe would
have read a containment failure as an ordinary scope refusal, which are the two
statuses a probe most needs to keep apart. Now 11 everywhere, with a test
pinning the number rather than the constant.

One thing the tests found that the plan had not thought through: the refusal in
§3 that keeps a probe store from being created inside real state compares
expanded paths, and the probe root usually **does not exist yet** when it is
checked. On macOS a temporary or user directory is reached through a symlink
(`/var` → `/private/var`), so comparing an expanded root against an unexpanded
path found no overlap and the refusal did not fire. Resolution now walks up to
the nearest ancestor that exists, expands that, and re-appends the remainder.
The guard is only as good as the path comparison underneath it.

### Orc as a fourth source, as built

Stream D of `Claude/Docs/Orc/Finish.md`: Orc's own store is now the fourth thing
a probe copies, and it is the one that changes what "no real credential" costs.

**Every other store keeps a digest. Orc keeps the key.** Mailman, Macmuffin, and
cq hold salted digests, so copying them leaks nothing a probe key can open —
§5.3's reminting is about making the copy *usable*, not about containing a
secret. Orc issues credentials, so `identities/<name>/key` is the credential, in
the clear. A probe of a fleet that carried those across would be a scratch
directory — made to be broken, thrown away, perhaps pasted into a message —
holding the keys to the real fleet. So minting now rewrites Orc's keyring as
well as Mailman's records, and a test greps the whole probe for the real key.

**One key per agent, not per store.** An agent usually exists in both — Orc
issued the credential, Mailman holds a mailbox under the same name — so the two
are reminted together from one key. Two keys for one name would make a probe
where `orc introspect` proves an identity that `mailman inbox` then refuses.

**The session claim goes; the session log stays.** `session.json` names a
supervisor pid, a child pid, and a socket, and in a probe all three are lies:
the pids belong to whatever the real machine is running now, and the socket
cannot be connected to. Orc's own comment says the file's existence is a claim
rather than a fact, which is exactly why a probe must not make it. What the
session *did* — `log.jsonl` — is history, and history is what a probe is for.

**The `orc` shim narrowed from "refuse everything" to an allow-list.** The
refusal was wholesale because, as this plan said, "orc does not exist yet, so
there is no list of spawn verbs to enumerate". Now there is — but the list runs
the other way. Reading verbs (`status`, `introspect`, `check-control`, `verify`,
`doctor`, `env`, `help`) are allowed; everything else is refused, including a
verb this build has never heard of. A deny-list would let whatever Orc grows
next week into a probe unexamined, and the one thing that must never happen here
is a probe bringing an agent to life.

Two things the building forced:

- **The tool table was indexed positionally** — `Tools()[0]`, `[1]`, `[2]` — in
  eight places. Adding a fourth entry would not have failed to compile; it would
  have silently pointed the cq reader at Orc's store. There is now `source.Of(kind)`
  and no positional access anywhere.
- **Adding a source is not enough to make its tool work.** The first end-to-end
  run had `orc status` refused *by Orc's own stamp guard*: the probe copied and
  stamped `state/orc`, but nothing set `ORC_HOME`, so Orc resolved through the
  XDG backstop to an unstamped directory and — correctly — refused. A source
  needs a row in `source/`, a redirect in `env/`, and a check in `doctor/`, and
  the guard turned a silent misdirection into a loud one. That is the guard
  working on its author.

Verified against a real fleet rather than only in tests: `orc bootstrap`, an
`orc new identity`, then a probe of it. Inside the probe `orc status` draws the
fleet and `orc employ` exits 11; the real keys appear nowhere in the probe; a
probe key is refused by the real fleet; and the fleet is byte-identical
afterwards.

---

## 14. Decisions

### Confirmed

1. Active agents are **neutered on import** — claims released, owners cleared,
   outboxes dropped, worktrees cut — with `--live-state` as the opt-out. No
   agent is ever spawned either way.
2. Isolation is **env redirection plus guards**, not a jail; §4.6 states the
   limits and `doctor` prints them.
3. God mode is **act-as plus omniscient views**.
4. Snapshot scope is **state dirs, a repo copy, and the Claude configuration**.

### Still assumed

- The command is `orcprobe`, with no short alias, kept verbose because every
  invocation is a deliberate act.
- Real `HOME` by default, `--fake-home` opt-in (§4.6).
- Probes live in `~/.orcprobe`, not beside the repo, so `destroy` can never
  reach project files.
- Mailman's query grammar is reused verbatim for `orcprobe mail` rather than a
  second language being invented.
- `--quiesce` is off by default: a slightly earlier snapshot beats stalling a
  live agent.
