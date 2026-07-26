# Orc — Implementation Plan (Go)

Derived from [Vision.md](../../../Docs/Orc/Vision.md),
[Reference.md](../../../Docs/Orc/Reference.md), and
[Auth_Perm_Role.md](../../../Docs/Orc/Auth_Perm_Role.md), and written to the
conventions [Anno](../Anno/Plan.md), [Mailman](../Mailman/Plan.md),
[Macmuffin](../Macmuffin/Plan.md), [Communiqué](../Communique/Plan.md), and
[Orcprobe](../Orcprobe/Plan.md) already establish for this tree.

Orc is the last tool and the only one every other tool has been waiting on.
Five places in the tree already name it as the thing that does not exist yet:

| Where | What it is waiting for |
|---|---|
| `Common/identity` | "Orc's remote auth… this file is the one that should need rewriting" |
| `Mailman admin` | "This command writes exactly the records Orc will write, so it can be deleted once Orc does" |
| `Macmuffin assign` | "assign is waiting on orc's agent-control contract, which does not exist yet" |
| `Orcprobe source/` | "Orc has none yet; when it does, it is one entry here" |
| `Orcprobe doctor` | "Orc will need the same three lines when it has state of its own" |

So this plan is scored on two things at once: does Orc work, and does it close
those five holes with the contract each of them assumed.

Guiding constraints, in priority order:

1. **One authority.** An identity, its key, its authority, and its permissions
   have exactly one home. Every tool asks Orc; nothing derives an answer twice.
2. **Derived, never stored.** Effective authority and permissions are computed
   from the tree on every read. Moving a boss re-caps its whole subtree in the
   same instant, because there was never a cached copy to update.
3. **Liveness is not state.** A Claude session can die at any moment and that
   must cost nothing but a restart. The identity is the durable thing; the
   session is a process it happens to be wearing.
4. **Honest about the wall.** Orc's boundary is Claude's tool layer, not the
   operating system's. §7.5 says exactly what that does not stop, and `orc
   doctor` prints it.
5. **Robust.** Same discipline as the rest of the tree: no panics, no partial
   writes, every error positioned and classified, every screen legible without
   colour.

---

## 1. Semantics recovered from the docs

**An identity is an account plus a home plus a place in a hierarchy.** Vision
says a given identity "keeps personal memories + authorization info + a
workspace + identifying information all in one place", and that this is what
lets `mailman` and `muff` have no account machinery of their own. So an identity
is not a row in a table — it is a directory that a Claude session is pointed
at, with a credential, a config dir, and a workspace inside it.

**Populating is a verb about processes, not about data.** "[De]Populate — to
[un]fill an identity with a Claude Code instance." An identity with no session
is a perfectly ordinary identity: it has mail, tasks, memories, and a place in
the tree. It is simply not thinking right now.

**The worklist is a set with a budget.** "It holds all currently employed
identities, and automatically populates them with their requested models and
efforts." Two things follow: employment is durable and survives a crash (it is
state, in the store), and *something* has to notice a dead session and refill
it (§6.4).

**Authority is a number on a role; permission is a named set of commands with a
floor.** From Auth_Perm_Role.md: the user is 100, everyone else 1–99, each
permission has a minimum authority, and only those at or above it *may* hold it
— "but they may not, for whatever reason". So holding a permission is not
implied by clearing its floor; the floor is a *ceiling on what can be granted*.

**The tree caps everything.** "A subagent can only have as high of a permission
as their boss." Combined with `move … lower authority/perms as needed`, this is
a live constraint rather than a check at assignment time — which is exactly why
§2.4 derives rather than stores.

**Some things need no permission at all.** "All agents are able to move, fire,
employ, and otherwise act on their subagents without need for permissions. They
*do* need permissions to add more agent load to the worklist." Two distinct
rules: *who* you may act on is answered by the tree, and *how much thinking* may
be running at once is answered by a permission. `employ` is therefore permitted
by ancestry and *budgeted* by `spawn`.

**`introspect` is two commands wearing one name.** Reference: it shows the
active agent in this leaf session, and "can show only one field with no
formatting for remote authorization and other purposes". So the default is a
human card, and `--only <field>` is a machine contract other tools call. It is
the read half of what `muff assign` is waiting for; §7.4 adds the check half.

**`attach` is Orc's own screen, not a passthrough.** Confirmed with the user:
`orc attach` gives a cleaner interface over the session, and `--direct` is the
escape hatch into the real Claude TUI. That single decision is what makes §6.2
the largest section in this plan, because a clean view needs a *structured* feed
of what a session is doing, and a PTY carrying a TUI does not provide one.

---

## 2. The model

Four nouns. Everything in `orc new` and `orc assign` is one of them.

### 2.1 Permission

A permission is a name, a minimum authority, and a set of command patterns.

```
permission { name, min_authority, patterns[] }
```

Patterns match a tool-level action, not a shell string. The three builtins from
Auth_Perm_Role.md are the pattern **kinds**, not pre-created permission records —
which is how it came out in the building, and it is the right reading: the
document names `read(path list)`, `write(path list)`, and `spawn(agent load)` as
shapes a permission is written in, not as three permissions somebody has to work
around. A fourth kind exists because a permission "can include any number of
specific commands or command patterns":

| Kind | Shape | Governs |
|---|---|---|
| `read` | `read(<glob>)` | reading files — §7.2 |
| `write` | `write(<glob>)` | editing files — §7.2 |
| `spawn` | `spawn(<load>)` | how much thinking this actor may add to the worklist — §6.4 |
| `orc` | `orc(<verb>)` | which of Orc's own verbs a role may run — §7.1 |

So a fresh fleet has no permissions at all, and the operator can still run
everything, because the operator's authority is not derived from a permission set
(§2.4). Policy is created rather than inherited: `orc new permission edit-anno 40
read(Anno/**) write(Anno/internal/**)`.

Permissions compose: a role holds several, and the effective set is their union
before the tree caps it.

### 2.2 Role

A role is a name, an authority level, a description, and a set of permissions.

```
role { name, authority, description, permissions[] }
```

`assign permission <role> <permission>` refuses when the permission's floor is
above the role's authority — that is what the floor is for. Lowering a role's
authority below a permission it already holds is the same refusal from the other
direction: it names the permissions in the way and requires them removed first.
An implicit drop would be a silent de-authorisation, and the whole point of a
floor is that nothing crosses it quietly.

### 2.3 Identity

```
identity { name, role, boss, created, id }
```

**Exactly one role.** Confirmed with the user: `assign role` replaces. Authority
lives on the role, so one role means an identity's authority is a number and not
a maximum over a set — and `remove role` never has to answer "which of these was
granting that?".

`boss` is another identity's name, or the operator for a top-level agent. Every
identity's boss chain terminates at the operator; a chain that does not is
corruption, and `orc doctor` reports it.

### 2.4 Derivation — the rule the whole tool turns on

Nothing about effective authority or permission is stored. Both are computed on
every read, bottom-up along the boss chain:

```
authority(operator) = 100
authority(i)        = min(authority(role(i)), authority(boss(i)))

perms(operator)     = every permission
perms(i)            = (perms(role(i)) ∪ grants(i)) ∩ perms(boss(i))
                      filtered to those whose floor ≤ authority(i)
```

Three consequences, all wanted:

- **`move` needs no fixing-up.** Reference says move "lower[s] authority/perms
  as needed"; with derivation there is nothing to lower. The move is one journal
  append and the subtree's effective rights change with it.
- **A demoted boss demotes its whole subtree**, instantly and without a walk.
- **A role's authority is a request, not a fact.** An authority-80 role under an
  authority-40 boss yields 40. `status` shows both numbers and why they differ,
  because an agent told it has authority 40 while its role says 80 will
  otherwise file a bug.

Path-carrying permissions intersect by path: `write(Common/**)` under a boss
with `write(Common/user/**)` yields `write(Common/user/**)`. Two globs where
neither contains the other intersect to the narrower literal set where that is
computable, and to nothing where it is not — a permission Orc cannot prove is
inside the boss's is not granted. Failing closed on an unprovable intersection
is the only direction that keeps rule 1 of §2.4 true.

### 2.5 Grants

`grant permission <identity> <permission>` is a temporary, direct grant that
skips the role. It is capped by the boss chain exactly as a role's permissions
are — a grant is a shortcut through the *role*, never through the *tree*.

**Every grant expires, and any grant can be ended early.** Confirmed with the
user, all three mechanisms together:

| | Lapses when |
|---|---|
| default | the identity is depopulated or refreshed — a session-scoped grant |
| `--until <dur>` | a wall-clock deadline, which survives a refresh |
| `orc revoke permission <identity> <permission>` | immediately, whichever of the two it was |

The default is session-scoped because "temporarily" for an agent most naturally
means "for this stretch of work", and because a refresh is a clean slate in
permissions as well as in context. A grant to an *unpopulated* identity with no
`--until` takes a one-hour default rather than silently meaning "forever" —
there is no way to write a grant that never lapses, which is what keeps the word
temporary true. `revoke` exists so that noticing a mistake does not mean waiting
out a clock, and it is the only remove-shaped verb in Orc that needs no `--yes`:
taking a permission away is never the dangerous direction.

`status` and `introspect` show every live grant and when it lapses; `verify`
reports grants whose expiry has passed but whose journal entry was never
compacted, which is bookkeeping rather than damage.

---

## 3. Storage

Root is `$ORC_HOME`, else `$XDG_DATA_HOME/orc`, else `~/.orc`. Directory mode
`0700`, as everywhere else in the tree, and here it is load-bearing rather than
conventional (§4.2).

```
<root>/
  version                     store format; an unknown one is a hard, clear error
  operator                    the operator identity's name — written once at bootstrap
  permissions/<name>.json     the whole permission; immutable, so no journal (§13)
  roles/<name>/role.json      creation record: name, created, and the initial values
  roles/<name>/journal.jsonl  authority, description, permission set
  roles/<name>/lock
  identities/<name>/
    identity.json             creation record: name, id, created, boss at creation
    journal.jsonl             role, boss, grants, employ/fire, populate/depopulate
    user.json                 salt + digest, via orc/common/user  (§4)
    key                       the plaintext key, 0600            (§4.2)
    lock
    claude/                   $CLAUDE_CONFIG_DIR for this identity (§5)
      settings.json           compiled from effective permissions (§7.2)
      CLAUDE.md               the identity's own standing instructions
      memory/                 its personal memories
      projects/…              Claude's own session transcripts land here (§6.2)
    workspace/                the session's cwd — a directory or a git worktree
    session/
      session.json            current session: uuid, pid, model, effort, started
      authz.json              effective permissions at populate — the hook's fallback (§7.2)
      session.sock            the supervisor's socket (§6.1)
      scrollback              fixed-size ring of raw PTY output (§6.2)
      events.jsonl            structured session events, from orc-hook (§6.2)
      log.jsonl               supervisor's own log: spawns, exits, restarts
  worklist.jsonl              employ/fire, and every populate decision tend made
```

Same discipline and the same failure rule as every other store in the tree:
mutable state is an append-only journal replayed on every command, creation
records are written once through the commit sequence (temp file in the same
directory → write → `fsync` → `chmod` → `rename` → `fsync` the directory, temp
removed on every failure path), a `rename` onto an existing name *is* the
uniqueness check, a truncated final journal line is an interrupted append and is
dropped with a note, and an unparseable line anywhere else is corruption and a
hard error.

Locking is per-entity, as Macmuffin's is: one lock per identity, and the store
lock only for the writes that must be globally conditional — creating a name,
and the load check in `employ` (§6.4), which is a decision against a total.

**The sandbox guard goes in `store.Open`, before the layout is created.** This is
the third of Orcprobe's open rows and it closes with three lines
(`sandbox.Guard(env, root)`), for the reasons its §4.3 gives: it must run before
anything is created, it must fail closed, and it must cost one map lookup
outside a probe. `store.Read` — the read-only door the hook uses — is guarded
too, for the reason Macmuffin's plan records: answering "may this agent write
that file?" from the *real* store while inside a probe is a containment failure,
quieter than a write and no less real.

---

## 4. Identities, keys, and the other tools' accounts

Confirmed with the user: **Orc mints, tools verify.**

### 4.1 Provisioning

`orc new identity <name>` does, in order, with the identity's own lock held:

1. Validate the name through `orc/common/user.Parse` — one normalisation, shared
   with every tool, so a name Orc accepts is a name Mailman can file mail under.
2. Mint a key with `user.NewKey` — 32 random bytes, base64, exactly the
   properties `user.Record` verifies against.
3. Write `identities/<name>/identity.json`, `user.json` (salt + HMAC digest, key
   never stored in it), and `key` at `0600`.
4. Provision the mailbox: `mailman admin user add <name> --key -`, key on stdin
   (§9, one small Mailman change).
5. Create `claude/` (settings compiled from the new identity's effective
   permissions, an empty `CLAUDE.md`, `memory/`) and `workspace/`.
6. Append `created` to the journal.

Step 4 is the only one that touches another tool's store, and it does so through
that tool's own command — Orcprobe's plan concluded that writing another tool's
records directly is the wrong shape, and it is no more right when Orc does it.
A Mailman that refuses (name taken, store broken) fails the whole creation and
the partial identity is removed: an identity without a mailbox cannot do the one
thing every agent in this tree does.

### 4.2 The keyring, and why it is plaintext

Orc holds the only plaintext copy of every key, because Orc is the only thing
that must hand a key out *later* — on populate, on refresh, on every restart —
and a digest cannot be turned back into a key. Orcprobe already made this
decision for its probes and it is the same decision here.

The consequences, stated rather than discovered:

- `~/.orc` at `0700` is what protects every credential in the fleet. Not the
  digests, not the tools — the directory mode.
- **Every session runs as the same unix user, so a session that can run a shell
  can read every key in the fleet.** This is the tool's largest honest hole and
  it is §7.5, not a footnote.
- Orcprobe must remint Orc's keyring when it copies it, and must rewrite
  `user.json` to match — otherwise a leaked probe discloses the real fleet's
  credentials, which is precisely what its §5.3 exists to prevent (§9).

### 4.3 What the other tools do with it

| Tool | Today | After Orc |
|---|---|---|
| Mailman | own `users/<name>/user.json`, verified on every command | unchanged; Orc provisions it with a key Orc chose |
| Macmuffin | resolves `$ORC_USER`, **verifies nothing** | verifies through `orc/common/account` (§9) |
| Communiqué | `CQ_USER`/`CQ_KEY`, an ordinary mailbox | unchanged; `orc env --operator` prints the block |
| Anno, Dock | no accounts | unchanged |

Macmuffin's hole is real and current: any process that sets `ORC_USER=atlas`
claims a task as atlas. Closing it is a milestone-5 deliverable and needs one
new package in `Common` and one call in `muff`'s `begin`.

---

## 5. What a session is given

A populated identity is a `claude` process with an environment Orc composed.
Nothing is implicit, and the whole set is written to
`identities/<name>/session/session.json` so it can be read back, diffed, and
pasted.

| Variable | Set to | Why |
|---|---|---|
| `ORC_USER`, `ORC_KEY` | the identity and its key | the credential contract `Common/identity` already defines |
| `ORC_IDENTITY` | the identity name | what `introspect` reads when there is no session socket |
| `ORC_SESSION` | the session uuid | ties a leaf shell to one session |
| `ORC_HOME` | the store root | so a session's `orc` and Orc agree |
| `CLAUDE_CONFIG_DIR` | `identities/<name>/claude` | per-identity settings, memories, and transcripts |
| `ORC_AGENT` | `1` | forces plain output from every Orc tool, as their docs specify |
| `MUFF_TASK` | unset | which task is in force is the session's business, not Orc's |
| `CQ_*` | inherited | a machine that mirrors keeps mirroring |
| `HOME` | left alone | redirecting it breaks git, the shell, and Claude's own auth |

And the command:

```
claude --session-id <uuid>            Orc mints it, so refresh and recovery are deterministic
       --model <model> --effort <e>   the identity's requested load (§6.4)
       --settings <compiled path>     the compiled permissions (§7.2)
       --permission-mode bypassPermissions
                                      confirmed: nothing prompts, ever (§7.2)
       -n <identity>                  so a --direct attach is labelled
       [--resume <uuid>]              recovery only, never refresh (§6.3)
```

Orc mints the uuid rather than reading one back. That one choice removes an
entire class of problem: there is no window in which a session exists and Orc
does not know its name, and `refresh`, recovery, and transcript discovery are
all deterministic rather than best-effort. `$ORC_CLAUDE_BIN` overrides the
binary, which is also what makes every liveness test in §11 possible without an
API key.

---

## 6. Liveness

Confirmed with the user: Orc owns a PTY, `attach` is Orc's own interface, and
`--direct` hands over the real terminal.

### 6.1 The supervisor

One `orc-session` process per populated identity — a second binary, as
`anno-hook`, `muff-hook`, and `orcprobe-shim` are. It:

1. allocates a PTY (`/dev/ptmx` plus `TIOCPTYGRANT`/`TIOCPTYUNLK`/`TIOCPTYGNAME`
   on darwin, `TIOCSPTLCK`/`TIOCGPTN` on linux — all present in `syscall`, so
   the tree's stdlib-only rule holds and no dependency is added);
2. starts `claude` on the slave side with `Setsid` and `Setctty`, in the
   identity's workspace, with the §5 environment;
3. copies output into `scrollback` — a fixed-size ring, so a session that runs
   for a week does not fill a disk — and serves it to attachers;
4. listens on `session.sock` (unix, `0600`) for attach, poke, resize, status,
   and stop;
5. writes `session.json` **after** the child is up and removes it on exit, so
   the file's existence means "there is a process", and appends every spawn,
   exit, and restart to `log.jsonl`;
6. on unexpected child exit, restarts with `--resume <same uuid>` under a
   backoff, up to a bounded number of attempts, then gives up loudly and leaves
   the identity employed-but-unpopulated for `tend` to report.

The supervisor stays resident, and that is the point: it is the thing that
outlives an `orc` command so that the *session* can. Every other binary in this
tree exits; this one is the exception, and it is one process per session rather
than a fleet-wide daemon, so nothing has to be running for Orc to be usable.

### 6.2 `attach` — the clean view

A PTY carrying a TUI is a stream of screen redraws, not a stream of facts, so
the clean view is not built by parsing it. It is built from two structured feeds
Orc already controls:

- **`orc-hook`**, installed in the identity's own `claude/settings.json`, fires
  on `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Notification`, `Stop`,
  `SubagentStop`, `SessionStart`, and `SessionEnd`, and appends one line per
  event to `session/events.jsonl`. Turn boundaries, tool names, file paths,
  and "it is waiting for input" all come from here — the same hook mechanism
  Anno, Dock, and Macmuffin already use, doing its third job.
- **The session transcript.** The first hook event of a session carries
  `transcript_path`, so Orc learns where Claude's own JSONL lives without
  knowing anything about how the path is derived. Tailing it gives assistant
  text and tool results.

The view is therefore a rendering of Orc's own event journal, with the
transcript as the source of prose:

```
┌─ ember ─ engineer(60) ─ sonnet/medium ─ load 4 ────────────── turn 3 ─┐
│                                                                       │
│  14:22:09  ●  read    Common/user/user.go                    1.2k tok │
│  14:22:11  ●  read    Common/identity/identity.go            0.9k tok │
│  14:22:14  ▲  edit    Common/account/account.go       ✗ denied: write │
│  14:22:14     "Common/account is outside write(Macmuffin/**)"         │
│  14:22:31  ●  bash    go test ./...                              ok   │
│  14:23:02  ◆  waiting for input                                       │
│                                                                       │
├─ compose ─────────────────────────────────────────────────────────────┤
│ › the account verifier goes in Common, not in muff_                   │
└─ ^S send · ^\ d detach · ^] direct · ^R refresh ───── mail 2 · task 1 ─┘
```

**Typing composes; `^S` sends.** Confirmed with the user: `Enter` inserts a
newline and nothing reaches the session until `^S`. The reason is the case this
view exists for — watching four agents at once, one of them mid-turn — where a
stray keystroke landing in a working session is a real cost and one extra key is
not. The composed buffer is held in the attacher, so detaching with text unsent
warns rather than discarding, and the pane is labelled `compose` so the mode is
never in doubt.

Sending is the same path `poke` uses (§6.3), so there is one way text reaches a
session and one thing to test.

The footer carries what an operator watching a fleet actually needs: unread mail
and the task in force, both read from the other tools' own stores.

Two properties of this design are worth stating because they are the reason to
prefer it:

- **It is legible when the session is not.** A wedged TUI, a session mid-compact,
  a terminal too narrow — the event feed still says what happened last.
- **It never touches the child.** The clean view is a reader. Nothing about it
  can hang the session it is describing.

And one honest limit: **the transcript's shape is Claude's, not Orc's.** It is a
compatibility surface. So the view degrades rather than fails — an unparseable
or missing transcript costs the prose, not the pane, and `attach` says so on the
way in and names `--direct`.

### 6.3 `--direct`, `poke`, `refresh`

**`attach --direct`** is the real thing: replay the scrollback ring, put the
local terminal in raw mode (`TIOCGETA`/`TIOCSETA`), proxy bytes both ways,
forward `SIGWINCH` as `TIOCSWINSZ` to the PTY, and restore the terminal on every
exit path including a signal. The detach key is `^\ d` — deliberately not `^C`,
`^D`, or `^]`, each of which a Claude session wants for itself. Multiple
attachers are allowed; all see the same stream, and the newest to send input
wins, because two humans typing into one agent is a coordination problem Orc
should report rather than arbitrate.

**`poke <identity> [message]`** writes a message and a newline into the PTY
without attaching — the "nudge the identity to continue working" of the
Reference. The default message is `continue`. Multi-line messages are sent inside
bracketed paste so a TUI does not treat the first newline as submit, which is
also what makes the clean view's composed buffer (§6.2) deliverable in one piece.
A poke to a session mid-turn queues in Claude's own input box, which is the
correct behaviour and is what the command prints.

**`refresh <identity>`** replaces the session: stop the child gracefully, mint a
*new* uuid, start again. Fresh context, same identity — the memories, mailbox,
tasks, and workspace are all in the identity, which is precisely what Vision
means by several Code sessions filling the role of one persistent agent. The
distinction from recovery is exact and worth pinning in a test:

| | uuid | context | why |
|---|---|---|---|
| crash recovery | same, `--resume` | continues | nobody asked for a new agent; the process died |
| `refresh` | new | fresh | somebody asked for a clean start |

Session-scoped grants (§2.5) lapse on refresh. That is what makes "temporarily"
mean something.

### 6.4 The worklist, load, and `tend`

`employ` adds an identity to the worklist; `fire` removes it. Both are state,
both survive a crash, and neither is the same as populated:

| | employed | populated |
|---|---|---|
| `employ` | yes | as soon as tend runs |
| session crashed | yes | no — tend refills it |
| `fire` | no | until the current turn ends |
| `remove identity` | refused while employed | refused while populated |

**Load** makes `spawn(<n>)` mean something. Auth_Perm_Role.md calls it "a
function of model, effort, and # of models active", and confirmed with the user,
the count is a **third input rather than just a summation** — a fleet is charged
for being a fleet:

```
weight(model)  haiku 1 · sonnet 2 · opus 3
weight(effort) low 1 · medium 2 · high 3 · xhigh 4 · max 6

session      = weight(model) × weight(effort)          1 … 18
total(S)     = ⌈ Σ session(s) × (9 + |S|) / 10 ⌉       integer, ceiling
```

`S` is the set the budget is measured over: everything the actor employs,
transitively. The same set feeds both the sum and the count, so a deep subtree is
not charged twice for its depth.

```
1 × opus/max        sum 18  × 1.0  =  18
4 × sonnet/medium   sum 16  × 1.3  =  20.8  →  21
8 × haiku/low       sum  8  × 1.7  =  13.6  →  14
```

Three properties this shape has and a plain sum does not: the tenth agent costs
more than the first, so a budget discourages sprawl without anyone writing a
rule about sprawl; swapping two haikus for one sonnet is *cheaper* at equal
weight, which is the trade a budget should encourage; and the marginal cost of
employing is visible before the fact, so `employ` can print what a decision
costs rather than only whether it was allowed.

All of it is integer arithmetic — `(sum × (9 + n) + 9) / 10` in Go, ceiling by
construction — so a budget never depends on floating point and never rounds in
the fleet's favour. The check happens inside `orc employ`, under the store lock,
against the live total: a budget checked without a lock is a budget two
concurrent `employ`s can both clear.

One consequence worth stating because it will look like a bug the first time it
happens: **employing one agent can push an actor over budget without that agent
being expensive**, because `|S|` grew. `employ` says so in those terms —
`load 21 → 26 of 24: the count multiplier rose from 1.3 to 1.4` — rather than
reporting a bare refusal.

**`tend`** reconciles the worklist with reality: employed and unpopulated →
populate; populated and not employed → depopulate; supervisor gone but session
alive → adopt or reap; over budget → report, never kill. It is called
opportunistically at the start of **every** `orc` command, which is Macmuffin's
`drain` idiom and it works for the same reason: a fleet that anybody is watching
is a fleet somebody is running commands against. `orc tend --watch <dur>` is the
backstop for a fleet nobody is watching, exactly as `cq sync --watch` is.

No daemon, no timer, no launchd unit. If nothing ever runs an `orc` command
again, nothing gets repopulated — and that is the correct failure, because the
alternative is a background process that keeps spending money after everyone has
gone home.

---

## 7. Enforcement — where each rule is actually checked

Four places, and they are not equally strong. Saying which is which is the
point of this section.

### 7.1 Orc's own commands — exact, fail closed

Every `orc` verb resolves the caller through `Common/identity`, verifies the key
against `user.json`, derives effective authority and permissions (§2.4), and
refuses with exit `8` (denied) when the caller falls short. This is in-process,
against live state, with the relevant lock held, and it is where the rules that
matter most are enforced:

| Verb | Permitted when |
|---|---|
| `new`, `remove`, `assign` (role, authority, permission) | caller's authority ≥ the authority being handed out, and ≥ every permission's floor |
| `employ`, `fire`, `move`, `poke`, `refresh`, `attach` | target is in the caller's subtree — ancestry, no permission needed |
| `employ` (the load it adds) | caller holds `spawn(n)` with room left in the budget |
| `grant` | caller holds the permission itself, and target is in its subtree |
| `status`, `introspect` | self always; others in the caller's subtree |

`spawn` is enforced **here** rather than in a hook, and that is deliberate: a
spawn is rare, deliberate, and already going through Orc, so it can be checked
exactly and refused closed without a bystander's dilemma.

### 7.2 Compiled settings and the snapshot — written at populate

Confirmed with the user: **sessions run in `bypassPermissions`.** Nothing
prompts, ever, because a worklist that stalls waiting for a human is not an
unattended worklist — and the whole point of `tend` is that nobody has to be
watching.

That decision moves weight onto everything else in this section, so it is worth
being exact about what is left. At populate, Orc writes two files:

| File | Holds | Read by |
|---|---|---|
| `claude/settings.json` | `permissions.deny`/`allow` compiled from effective `read`/`write`, a deny on `$ORC_HOME` (§7.5), a deny on the `Agent` tool (below), and `permissionMode: bypassPermissions` | Claude |
| `session/authz.json` | the same effective permissions, as Orc's own record, written once and never locked | `orc-hook`, when the live store cannot be read (§7.3) |

**Whether Claude honours a `deny` rule under `bypassPermissions` is not
something this plan assumes.** The flag's own help says it bypasses all
permission checks, and the difference between "all checks" and "all checks except
deny" is the difference between the settings file being a boundary and being
documentation. **The design does not depend on the outcome:** `PreToolUse` hooks
run regardless of permission mode, so §7.3 is the boundary either way, and a
honoured deny list is a cheap extra layer rather than the mechanism.

**Attempted 2026-07-25: inconclusive, for want of a credential.** The check is
`Claude/Mock/deny-probe.sh` — a deny rule, a canary file, a session in
`bypassPermissions`, and a `PreToolUse` hook that logs — and it exits `2` saying
so when it cannot authenticate, which is what happened here (`401 OAuth access
token has expired`). It is one command for whoever has a live token, and it also
establishes the other thing this layer assumes: that hooks fire under
`bypassPermissions` at all.

Until somebody runs it, milestone 3 is built to the **pessimistic** reading:
`settings.json` is treated as documentation, `orc-hook` is the whole boundary, and
every denial that matters — the `Agent` tool, the keyring — is enforced by the hook
rather than by a rule. If the probe later says deny holds, nothing has to change;
one layer turns out to be real that was not being counted on.

`authz.json` exists because of that. It is what makes "the permissions this
identity had at populate" a thing the hook can still read when the store is
unreadable — one small file, no lock, no journal replay — rather than a claim
that rested on Claude enforcing settings.

**The `Agent` tool is denied for every identity.** Confirmed with the user: all
parallelism goes through `orc employ`, so the worklist is the complete picture of
what is running and the load budget is exact rather than approximate. The cost is
real and worth naming: an Orc session cannot use even the read-only Explore
agent, and an agent that wants fan-out has to ask for identities. Since a denied
tool may not be enforceable under `bypassPermissions`, the hook matches the
`Agent` tool too (§7.3) — this is the one denial that must hold whatever the
empirical check finds, because it is the one the accounting depends on.

### 7.3 `orc-hook` — live, and the boundary

`orc-hook` runs on `PreToolUse` for `Read`, `Edit`, `Write`, `NotebookEdit`,
`MultiEdit`, `Bash`, and `Agent`, derives the identity's *current* permissions,
and blocks with exit 2 and a message naming both ways forward — the rule
Macmuffin's hook doc sets out, and the reason a refusal does not just get worked
around.

With `bypassPermissions` (§7.2) this hook is the boundary, not a second opinion.
So it cannot simply inherit `muff-hook`'s fail-open rule, and the resolution is
a three-step fallback rather than a single answer:

| The hook can read | It decides from | On a violation |
|---|---|---|
| the live store | current permissions, grants included | blocks |
| only `session/authz.json` | the permissions at populate | blocks, and says the decision is stale |
| neither | nothing | **reads pass, writes and `Agent` block** |

The last row is the one that differs from every other hook in this tree, and the
difference is the honest consequence of `bypassPermissions`: there is no other
brake, so failing open on a write would mean an unsupervised agent editing
anything the moment Orc's store hiccups. A stalled write is recoverable and says
exactly what to do — `orc doctor`, then `orc poke` — while an unbounded one is
not. Reads still pass, because a blocked read produces a confused agent and
discloses nothing new: it already has whatever the last successful read gave it.

Everything else `muff-hook` guarantees carries over verbatim, because this hook
fires on every tool call in a live session too: it never writes, it is bounded by
a 2-second deadline, unparseable input and unknown events exit 0, and `FuzzRun`
asserts no input produces an exit code other than 0 or 2. `authz.json` being a
single unlocked file is what lets the middle row stay inside that deadline.

The `spawn` budget is never in this path at all — it is checked in-process by
`orc employ` (§7.1), which is why the one rule with money attached does not
depend on any of the above.

The hook is also the event feed the clean view reads (§6.2). One binary, two
jobs, one payload parse.

### 7.4 `check-control` — the contract `muff assign` is waiting for

```
orc check-control <agent>     exit 0 if the caller controls agent, 8 if not, 2 if unknown
orc introspect --only <field> raw value, no formatting, no colour
```

`muff assign <agent> <task>` calls `check-control` and relays the answer, exactly
as `anno write` calls `muff check-scope`. Macmuffin holds no opinion about
authority, Orc holds no opinion about tasks, and the two meet at an exit code.
Both commands are marked additive (†) in §8 for the same reason `check-scope`
is: they are contracts between tools, not part of the user-facing spec.

Anything other than a definite exit `8` is **not** a yes here. This is the one
place the tree's fail-open convention is inverted, and the difference is the same
one Macmuffin's hook doc draws: a hook is a bystander in someone else's session,
and an authority check is asked a question it can always answer.

### 7.5 What this does not stop

Printed by `orc doctor`, and printed on the way into `attach`:

- **A session that can run a shell can read the whole keyring.** Every session
  runs as one unix user, and `~/.orc/identities/*/key` is readable by that user.
  `cat` defeats the entire permission model, and with `bypassPermissions` (§7.2)
  the only thing in the way is `orc-hook`: it blocks a `Read` of `$ORC_HOME` and
  the `Bash` command shapes that obviously reach for it, records the attempt as
  an escape rather than an ordinary refusal, and `orc doctor` alarms on it. But a
  shell can obfuscate a path, and none of this is a kernel boundary. The real fix
  is one unix user per identity, or a keyring the session cannot read at all —
  both deliberate future work (§12). Until then, this is the wall, and the
  compiled deny list is a request rather than a fence.
- **Subagents are denied rather than counted.** Confirmed with the user: the
  `Agent` tool is off for every identity (§7.2), so the worklist really is the
  whole picture of what is thinking. What this does not stop is a session
  reaching parallelism another way — a `Bash` call to `claude -p`, which is a
  shell command Orc cannot decide, and which arrives with no identity, no budget,
  and no place in the tree. `orc doctor` looks for stray `claude` processes whose
  parent is an Orc session and reports them; it does not kill them, because
  killing something an agent is mid-way through is worse than naming it.
- **`Bash` writes are undecidable**, exactly as Macmuffin's doc says of its own
  hook. `anno write` is covered because Anno asks; `sed -i` is not.
- **A permission is only as narrow as its patterns.** `write(**)` is a
  permission Orc will happily enforce.

---

## 8. Commands

```
orc <command> <args...>
```

Reference.md's set, in full, plus the additive ones marked †.

| Command | Does |
|---|---|
| `bootstrap [--as <name>]` † | Create the store and the operator identity; print the shell block (§8.1) |
| `new identity <name>` | Create an identity: account, key, mailbox, config, workspace (§4.1) |
| `new role <name> <authority> <description>` | Create a role |
| `new permission <name> <min authority> [patterns…]` | Create a permission |
| `assign role <identity> <role>` | Replace the identity's role |
| `assign authority <role> <authority>` | Set a role's authority |
| `assign permission <role> <permission>` | Add a permission to a role |
| `remove identity <name>` | Delete an identity — refused while employed or populated |
| `remove role <name>` | Delete a role — refused while any identity holds it |
| `remove permission <name>` | Delete a permission — refused while any role holds it |
| `grant permission <identity> <permission>` | Temporary direct grant; `--until <dur>` (§2.5) |
| `revoke permission <identity> <permission>` † | End a grant early (§2.5) |
| `status [<identity>]` | One identity's card, or the whole fleet (§8.2) |
| `attach <identity>` | Orc's live view; `--direct` for the real TUI (§6.2) |
| `poke <identity> [message]` | Type into the session without attaching |
| `refresh <identity>` | New session, same identity |
| `move <identity> <boss>` | Re-parent; effective rights follow (§2.4) |
| `employ <identity>` | Add to the worklist; tend populates it |
| `fire <identity>` | Remove from the worklist; do not repopulate |
| `introspect [--only <field>]` | This leaf session's agent (§7.4) |
| `check-control <agent>` † | Exit 0 / 8 — the contract `muff assign` calls |
| `env <identity>` † | Print the export block for a manual shell — discloses a key |
| `tend [--watch <dur>]` † | Reconcile the worklist; called by every command anyway |
| `doctor` † | Every invariant and every guard, and which are in force |
| `verify` † | Walk the store and report damage, changing nothing |
| `help` | The command list, the model, the load table, the environment |

Everything marked † is absent from Reference.md as written. Confirmed with the
user: **the reference is updated now, before implementation**, so the spec is the
thing to build against rather than the thing to reconcile afterwards. The
additions, and why each one is not scope creep:

1. **`bootstrap`** — the store, the operator identity, and the mailbox cq mirrors
   have to come from somewhere, and a silent bootstrap is hard to reason about a
   month later. Named `bootstrap` rather than `init` at the user's request.
2. **`revoke permission`** — the decision in §2.5 asks for it: a grant you can
   only wait out is a grant you cannot correct.
3. **`status` with no argument** — Reference has `status <identity>` only, and a
   fleet with no fleet-wide screen is a fleet you read one agent at a time.
4. **`new permission` taking patterns** — Auth_Perm_Role.md says a permission
   "can include any number of specific commands or command patterns"; Reference's
   signature has nowhere to put them.
5. **`poke` taking a message** — "nudge the identity to continue working" has a
   sensible default and an obvious argument.
6. **`tend`, `doctor`, `verify`** — every other tool in the tree has `verify`;
   `doctor` is where §7.5 gets printed; `tend` is called by every command anyway
   and having it as a verb is what makes that testable.
7. **`check-control` and `env`** — tool-to-tool contracts, marked as such the way
   Macmuffin's reference marks `check-scope`.

`Docs/Orc/Reference.md` is a document you own, so the update is a separate pass
whose diff stands on its own rather than a change folded into other work.

Flags shared by everything, as elsewhere: `--json` on the read commands,
`--no-color`, `--color`, `--width <n>`, `--yes` (required by `remove` and
`fire` when stdin is not a terminal, which for an agent is always).

Exit codes are the shared table: `8` for a permission or authority refusal, `6`
for "identity is populated" and other conditional-write conflicts, `10` when a
session socket cannot be reached, `11` when a path escapes a root or a session
reaches for the keyring, `2` for an unknown name — including an identity outside
the caller's subtree, which is reported as not found rather than forbidden,
following Macmuffin's privacy rule: saying "you may not" would confirm it exists.

### 8.1 `bootstrap`

Once per machine. It creates the store, mints the operator identity — named after
`$USER` unless `--as <name>` says otherwise — gives it authority 100 and every
permission, provisions its mailbox, and prints the block for a shell profile:

```
export ORC_USER=redjive2
export ORC_KEY=…
```

It does **not** wire Communiqué. Setting cq's operator password and minting a
sync token stay `cq admin operator` and `cq admin token`, because a bootstrap that
half-succeeds across two tools has to explain which half — and `orc env
--operator` prints the `CQ_USER`/`CQ_KEY` pair cq needs anyway.

Running it twice is not an error and not a re-creation: it reports what already
exists and exits 0, so it is safe in a setup script.

### 8.2 Screens

The user likes tables, alignment, and box drawing, and every Orc tool paints
from the same Catppuccin roles, so colour is a layer and never information.

`orc status` — the fleet as the tree it actually is:

```
|--------------------:---------|-----------:--------|-------:----|----------------------|
[orc]  4 employed · 4 populated · load 27/40 · macchiato
|  redjive2             100     operator            —      —     you, unpopulated
|  |  atlas              80     architect     opus/high     9     ● turn 14 · 2m ago
|  |  |  ember           60     engineer     sonnet/med     4     ● turn 3  · 11s ago
|  |  |  quill           60     engineer     sonnet/med     4     ○ idle    · 6m ago
|  |  |  scribe        40/60‡   reviewer     haiku/low      1     ✗ exit 1  · 20m ago
|--:--:-----------------:-------|-----------:--------|-------:----|----------------------|
‡ role asks 60; boss caps it at 40
```

`orc status ember` — the card, with derivation shown rather than asserted:

```
┌─ ember ───────────────────────────────────────────────────────────────┐
│  role        engineer                                    authority 60 │
│  boss        atlas → redjive2                                         │
│  mailbox     ember          2 unread                                  │
│  task        fix-the-parser  status 3 · scope Anno/internal/**        │
├─ permissions ─────────────────────────────────────────────────────────┤
│  read        Anno/** Common/**                    from role           │
│  write       Anno/internal/**                     from role, capped   │
│  spawn       —                                    floor 70 > 60       │
│  write       Docs/Anno/**                         granted, 41m left   │
├─ session ─────────────────────────────────────────────────────────────┤
│  uuid        0c4d…9f1e        sonnet/medium · load 4                  │
│  started     14:02:11         3 restarts, last 14:19:40               │
│  workspace   ~/.orc/identities/ember/workspace  (worktree: anno-fix)  │
└───────────────────────────────────────────────────────────────────────┘
```

---

## 9. Cross-tool work

Five changes elsewhere. Each is small, each is additive, and each closes a hole
another tool's own docs already name.

| Tool | Change | Why | State |
|---|---|---|---|
| Mailman | `admin user add <name> --key -` reads a key from stdin | so Orc chooses the key once and both records agree (§4.1) | **built** |
| ~~Common~~ | ~~new `account` package~~ — **superseded**, see below | | withdrawn |
| Macmuffin | verify through `orc introspect --only identity`; unblock `assign` via `orc check-control` | closes an unverified-identity hole, and the refusal its docs promise to remove | **built, both halves** |
| Orcprobe | Orc's state in `source/`; **remint Orc's keyring**; narrow the `orc` shim refusal | its own §5.3 rule applied to the one store that holds plaintext keys | **built** |
| Orcprobe | copy `identities/*/claude` and workspaces, cut session sockets and pids | a probe must not carry a live session's socket | **built** |

**The `common/account` row is withdrawn, and what replaced it is better.** The plan
had Macmuffin verifying a credential by reading Orc's records through a shared
package in `Common`. Macmuffin instead *asks Orc*: `orc introspect --only
identity` prints who the credential really belongs to and exits 7 when it proves
nothing, and `muff` refuses on that exit. The difference matters for the one thing
this store is unusual for — Macmuffin never opens Orc's store, so the keyring at
0700 stays reachable only by the process that owns it, and there is no second
reader of a credential file to keep right. It also keeps rule 1 exact: the
authority is asked, not copied.

Two limits come with it, and Macmuffin's own `control/identity.go` states both:
an agent that controls its own `PATH` can hide `orc` as easily as it can lie about
its name, so this catches a *mistaken* identity — a stale key, a typo, a
credential copied from another agent — rather than a determined one; and `muff` on
a machine with no fleet still works, because a task list that refused every
command for want of an authority nobody was contesting would be useless. Closing
the first needs Orc to be the thing that starts the session, which is milestone 2.

Verified end to end rather than only in tests: with all three real binaries and a
real Mailman store, `muff assign ember fix-the-parser` transfers ownership and
delivers the notice; `ember` assigning upward exits **8** with Orc's own message
relayed; an unknown agent exits **2**; a valid name with another identity's key
exits **7** from `muff pool`, not just from `assign`; and with `orc` off the
`PATH`, `assign` exits **10** rather than proceeding.

Two more deserve more than a row.

**Mailman's `admin user add` should not be deleted when Orc lands**, whatever its
help text says. It is how an empty store is bootstrapped and how Mailman is
tested without Orc installed. What changes is that it grows `--key -` and its
help stops describing itself as a placeholder. Deleting the only way to make a
mailbox without the whole orchestrator would make Mailman's own test suite depend
on Orc.

**Orcprobe's shim currently refuses `orc` wholesale**, and its plan says so
explicitly: "It does not exist yet, so there is no list of spawn verbs to
enumerate." Now there is, and the narrowing is a table:

| In a probe | Verbs |
|---|---|
| Allowed | `status`, `introspect`, `check-control`, `verify`, `doctor`, `help` — museum reading |
| Refused | `employ`, `attach`, `poke`, `refresh`, `tend`, and anything that populates |

Everything Orc mutates is state, and state in a probe is already redirected and
stamped; what must never happen is a probe bringing an agent to life, which is
exactly the verb list above. And Orc now needs the stamp guard for the same
reason Mailman does (§3) — the last of Orcprobe's four open rows.

---

## 10. Package layout

Module `orc/orc` at `Orc/Orc/go.mod`, added to `go.work`, with `replace`
directives for `orc/common` and `orc/theme` so it still builds on its own.
Stdlib only, as everywhere else.

```
cmd/orc/              entry point
cmd/orc-session/      the PTY supervisor (§6.1)
cmd/orc-hook/         PreToolUse enforcement + the event feed (§6.2, §7.3)
internal/
  cli/        command parsing, exit codes, help
  store/      the root: open, journals, locks, atomic commit, sandbox guard
  model/      identity, role, permission, grant — the types and their records
  authz/      derivation (§2.4), the only package that answers "may they?"
  worklist/   employment, load accounting, tend (§6.4)
  session/    supervisor, PTY, socket protocol, scrollback ring
  pty/        the raw ioctl layer, one file per GOOS
  view/       the clean attach view (§6.2)
  events/     reading and writing session events; tailing Claude's transcript
  provision/  keys, mailbox creation, compiled settings (§4.1, §7.2)
  hook/       the hook's payload parsing and refusal rules
  render/     tables, cards, the tree
  style/      colour, matching the tree
  fixture/    synthetic fleets, and a fake `claude` for §11
```

`authz` is the load-bearing boundary: it is pure, it takes a snapshot of the
model and answers questions about it, and it touches no filesystem, no process,
and no clock beyond one injected `now`. Every permission decision in every other
package is one call into it, so there is one place where "may they?" is
answered and one place to test exhaustively.

`pty` is the only package with a build tag, and `session` is the only one that
starts a process.

---

## 11. Validation and testing

Golden-file CLI tests, a `fixture` package that builds synthetic fleets,
table-driven unit tests — the tree's usual shape — plus six classes specific to
this tool.

1. **Derivation, by property.** Generate random trees with random roles, grants,
   and permission floors, then assert the §2.4 invariants hold for every node:
   effective authority never exceeds the boss's, effective permissions are a
   subset of the boss's, no permission is held below its floor, and a `move`
   changes exactly the moved subtree. This is where the model is proven, not in
   the CLI tests.
2. **Liveness without Claude.** `$ORC_CLAUDE_BIN` points at a fake in `fixture`
   — a small Go program that reads its argv, honours `--session-id`, emits hook
   events, echoes stdin, and can be told to hang, crash, or exit non-zero. Every
   populate, attach, poke, refresh, crash-recovery, and reap path is testable
   with no API key, no network, and no cost. **Nothing in the test suite ever
   starts a real `claude`,** asserted statically the way Orcprobe asserts it has
   no spawn path at all.
3. **PTY behaviour.** Attach proxies bytes both ways; `SIGWINCH` reaches the
   child as a real `TIOCSWINSZ`; detach leaves the child alive; the local
   terminal is restored on every exit path including a signal; two attachers see
   one stream; a `kill -9` of an attacher does not disturb the session.
4. **Key hygiene, as a test rather than a review item.** Every rendered screen,
   every `--json` shape, every log line, every event line, and every error
   message is scanned for the fixture fleet's key material. A key reaches a
   session's environment and nothing else. The `env` command is the single
   deliberate exception and is tested for exactly that.
5. **Crash safety at every phase.** Kill the process during `new identity`,
   during `employ`, during populate, and after the mailbox exists but before the
   journal append — in each case assert no half-made identity is accepted by any
   later command, and that `verify` names what it found. Same for the supervisor:
   `kill -9` it and assert `tend` adopts or reaps, never both.
6. **The sandbox guard**, mirroring Mailman's `sandbox_test.go`: inside a probe,
   an unstamped root is refused before anything is created, exit `11`, nothing
   written; outside a probe the guard changes nothing.
7. **The budget, as arithmetic.** The §6.4 formula is table-driven over the whole
   grid of models, efforts, and fleet sizes, asserting integer ceiling behaviour
   at every boundary — and asserting the case that will look like a bug:
   employing a load-1 haiku pushes an actor over budget because `|S|` grew. Grant
   expiry is the same shape: session-scoped grants lapse on refresh and on
   depopulate, `--until` grants survive both, `revoke` ends either, and no code
   path produces a grant with no expiry at all.

And the hook, which fires in someone's live session, carries `muff-hook`'s rules
over with its own `FuzzRun` — no input whatsoever produces an exit code other
than 0 or 2, it never writes, and the 2-second deadline is tested against a store
stalled for real. Two tests are Orc's own, because `bypassPermissions` makes this
hook a boundary rather than a bystander: the **fallback ladder** is exercised at
each rung (live store → `authz.json` only → neither, where reads pass and writes
and `Agent` block), and the **`Agent` denial** is asserted through the hook rather
than only through the settings file, since the accounting in §6.4 is only as good
as that one refusal.

---

## 12. What is deliberately not built

- **One unix user per identity.** The right fix for §7.5 and explicitly
  deferred: it needs `sudo` at install time, breaks a single-user laptop
  workflow, and belongs on top of a proven single-user version.
- **A fleet daemon.** `tend` opportunistically plus `--watch` covers it, and a
  resident process that spends money unattended is the wrong default.
- **Model routing or scheduling intelligence.** Orc runs what an identity asked
  for. Deciding that a task deserves opus is the operator's judgement, or a
  future tool's, not this one's.
- **A web UI.** cq is the window, and it already mirrors Mailman and Macmuffin;
  Orc's fleet state is the obvious next thing for it to mirror, in *its* plan.
- **Multi-machine fleets.** cq has a machine concept because it syncs; Orc is
  local, and a distributed worklist is a different tool.
- **Per-identity API keys or billing.** Load is a budget in units of thinking,
  not money. Money is `--max-budget-usd`, and it belongs to whoever spawns.
- **Rewriting Mailman's account store.** Two authorities is the mistake this
  plan avoids; one authority reached by migration (§4.3) rather than by
  replacement is how it gets avoided without breaking a working tool.

---

## 13. Milestones

| # | Delivers | State |
|---|---|---|
| 0 | The Reference.md pass (§8), as its own diff. | **built** |
| 1 | Store, model, derivation, `bootstrap`, `new`/`assign`/`remove`/`grant`/`revoke`/`move`/`status`/`introspect`, key provisioning with Mailman's `--key -`, the sandbox guard. **No liveness.** | **built** |
| 2 | `orc-session`: PTY, populate/depopulate, `employ`/`fire`, `poke`, `refresh`, crash recovery, load accounting, `tend`. `attach --direct` only. | **built** |
| 3 | `orc-hook`: compiled settings, `authz.json`, the three-step fallback, the `Agent` denial, the event feed. `attach`'s clean view on top of it. | enforcement **built** (stream A); the clean view is stream B |
| 4 | Fleet legibility: the tree screen, `doctor` with §7.5 printed, `verify`, `--json` everywhere, `env`. | **mostly built in 1** — only `doctor` is left |
| 5 | Cross-tool: `common/account`, Macmuffin verification, `check-control` and `muff assign` unblocked, Orcprobe's Orc support and shim narrowing. | `check-control` **built**; the rest open |

**Milestone 3 opens with the empirical check** from §7.2 — does a `deny` rule
still refuse under `bypassPermissions`? — because it decides whether the compiled
settings are a fence or a request, and that is worth an hour before writing the
hook rather than a surprise after.

### Milestone 4, stream E (the gaps: tests and docs), as built

Four things came out differently, and one of them was a bug the tests found
rather than a decision the writing forced.

**A table could draw wider than the terminal it was given.** `measure` only took
width back from `Grow` columns, so a table whose long content sat in a column
that does not grow — a role name on a narrow terminal — drew a rule past the edge
of the screen and wrapped into nonsense. Reproduced from both directions: 315
columns when given 48. `measure` now takes a second pass over the remaining
left-aligned columns, and right-aligned ones are still never touched, because the
rule that an authority level with a digit missing is a different authority level
was the whole reason the first pass existed. `internal/render/table.go` is not
listed in Finish.md's contention map, so no stream owned it; the fix is recorded
here rather than merged silently.

**`render.Align` has no valid zero value.** A `Column` that omits it fails at
draw time with an internal fault. Every real caller sets it, so this is a trap
rather than a live bug, and it is pinned by a test rather than fixed — making
`Left` the default is a change to the type's contract and belongs to whoever owns
the file next.

**The settings test asserts the rule, not the absence.** The first draft checked
that `settings.json` does not exist, which was true when it was written and would
have broken the moment stream A wired the compiler in. The durable invariant is
the one `claude.go` actually states — it is never an empty placeholder, because a
settings file that permits everything claims an enforcement that is not there —
so that is what the test asserts, and it holds on both sides of that landing.

**Two provisioning failures are only reachable through the `Run` seam.** A failed
Claude config and a failed rollback both happen after the identity exists, so
there is no way to provoke either from outside the call. The injected runner
fires at exactly that moment, so the fake uses it to seal a directory mid-flight.
That is what the seam is for, and it is worth saying because the alternative —
adding a second seam for the tests — would have been a worse answer.

The `attach --direct` proxy loop is tested end to end: a real pty for the
operator, a real unix socket for the session, keystrokes in one end and output at
the other, and the detach sequence actually detaching. It covers the two
goroutines, raw mode, the resize travelling on its own connection, and a session
that goes away being a detach rather than a failure.

---

### Milestone 4, stream C (doctor, verify, tend --watch), as built

Three things came out differently, and two of them are about what a guard screen
is allowed to count as a failure.

**Where you are is not a guard.** The first draft listed the orcprobe stamp
beside the others, so a healthy real fleet reported "1 guard not in force" and
exited 6 forever — the screen was calling its own normal state damage. It is on
the header line now: still the first thing said, still painted to catch the eye
on the real fleet, and no longer arithmetic.

**"Not checked" is unmeasured, not broken.** A fleet at rest has no populated
identity, so the compiled-settings guard has nothing to check. Counting that as a
failure would have meant `doctor` never exits zero on an idle fleet, which is
most fleets most of the time. Only `absent` and `partial` set the exit code —
which is Orcprobe's rule for the same screen, and it is right for the same
reason. The §7.5 holes are exempt too: they are the wall's shape, not its damage,
and they print on a healthy fleet precisely so nobody concludes the model is a
fence.

**`verify` gained a question liveness cannot answer.** "Is something there" is
the wrong question for a pid read out of a file written days ago, because the
operating system reuses them — a stale `session.json` can point at an unrelated
process that answers yes to every check in the tree. So `verify` asks `ps` what
the pid actually *is*, and reports a supervisor that is not an `orc-session`. A
pid that cannot be inspected returns "not known" rather than a guess: not knowing
is not evidence, and reporting a healthy session as damage would send an operator
to fire it.

Smaller notes:

- `tend --watch` refuses anything under `MinWatch` (5s) and gives up after five
  consecutive failed passes. A pass that fails once is a session dying mid-read,
  which is what a backstop is *for*; five in a row is not a race, and a loop that
  cannot make progress should stop rather than fill a terminal with one line.
- The loop is quiet by design — it prints only when it acted or when a pass
  failed. A supervisor that logs "nothing to do" every thirty seconds is one an
  operator stops reading.
- `doctor` wraps its detail column rather than truncating it, because the detail
  is the half that says what to do. With no `Width` (a pipe or a test) it stays
  on one line: wrapping output that is about to be grepped only makes it harder.
- `help.go` is not in stream C's file list, but it listed `doctor` and
  `tend --watch` under "not built yet" — a claim this stream falsified. Those two
  lines moved; `attach`'s stayed, since it is stream B's to move.

### Milestone 3, stream B (the clean view), as built

`attach` without `--direct` is Orc's own view now. The shape held: the screen is a
fold over `session/events.jsonl` with the transcript as prose, and it was built and
tested entirely against a hand-written fixture — no session, no pty, no hook — which
is what the settled schema in Finish.md was for.

**Where it landed.** `internal/view` is the model and is pure: `Fold` turns decoded
events into rows, `Composer` is the keystroke state machine, `ReadProse` reads
Claude's transcript, `Facts` gathers the footer. `internal/render/pane.go` draws it,
also pure. `internal/cli/attach.go` holds the only impure part — the terminal, the
clock, and the one socket write a send is — and is deliberately thin.

**What came out differently:**

- **No token counts.** §6.2's mock-up has a `1.2k tok` column; the event schema
  carries no token count, and the schema is settled and shared with stream A. The
  column is not drawn rather than invented. If it is wanted, it is a field on the
  event and a change both streams agree to.
- **Prose is a band under the feed, not interleaved.** The transcript's entries carry
  no timestamp this reader trusts, so threading them into a timestamped feed would
  have meant inventing an order for them.
- **`PostToolUse` is not a row.** It says a tool finished, which the next row implies.
  Drawing both would double the length of every screen to add nothing.
- **The header's authority is the effective one**, not the asked one: a role that
  wants 80 under a boss with 60 has 60, and a header should say what the agent can
  actually do.
- **`view.Unverifiable`-shaped degradation throughout.** A missing feed is "no events
  yet", an unreadable one keeps the pane and says so, a missing transcript costs the
  prose and names `--direct`, and a footer that cannot reach `mailman` or `muff`
  shows nothing rather than zero — "0 unread" and "I could not ask" are different
  facts and a status line that conflates them starts lying.

**What the building forced.** The first pane computed each column by hand — width
minus the timestamp minus the gap minus the glyph — and every one of those
subtractions was a chance to be one out, which at 48 columns it was. It is a builder
that measures as it goes now, and `fit()` guarantees the width. The rectangularity
test at four widths and three heights is what caught it, and a second one catches the
same class of bug with colour on, where an escape sequence occupies no columns.

One test bug worth recording because it looked like a code bug for ten minutes: the
first `runeWidth` in `pane_test.go` was hand-rolled and counted box-drawing characters
as double-width, so every correctly drawn line measured too long. It defers to
`theme.Width` now — a test that measures differently from the code does not check the
code, it checks the test.

**Contention.** Two streams were live in files this one had to compile against:
stream C's `doctor.go` (a duplicate `plural2`, since resolved) and stream E's
`proxy_test.go` and `render_test.go` (mid-write). Nothing of theirs was edited; this
stream's own packages were verified independently while they finished. `tend --watch
0s` currently panics with a divide-by-zero — that is stream C's, in `liveness.go`,
and is left for them.

### Milestone 3, as built — stream A (enforcement)

`orc-hook` is the boundary, the compiled settings are the cheap layer in front of it,
and `authz.json` is the rung between them. Six things came out differently, and two of
them were the same bug in two places.

**The empirical check could not be run**, and §7.2 above records it: no live credential,
so `Claude/Mock/deny-probe.sh` exits 2 saying so. Everything here is therefore built to
the pessimistic reading — the settings are documentation, the hook is the whole
boundary, and every denial that matters is enforced by the hook rather than by a rule.
That is why the `Agent` denial and the store denial both appear twice.

**Denying the store root is wrong, and it broke every session — twice.** An identity's
workspace lives *inside* the store, at `identities/<name>/workspace`, so "deny
`$ORC_HOME/**`" denies an agent its own files. The first version of the hook refused
every legitimate write with a message about the keyring; the compiled settings then did
the same thing independently. Both now work from the same rule, spelled once as
`hook.protected` and once as `provision.Protected`: credentials, journals, policy, and
each session's own snapshot are off limits; workspaces, `CLAUDE.md`, and `memory/` are
the agents' own. The snapshot is in that list deliberately — an agent that could rewrite
it could rewrite what the hook's second rung believes.

**The deadline has to bound the whole check, not the permission read.** The first version
checked a timer inside `permissions`, which a stalled store never reaches: `store.Read`
is itself a read, and it blocks. The check now runs in a goroutine under a `select`, and
a timeout drops to the third rung. The test stalls the store for real — a FIFO with no
writer, Macmuffin's trick — because a test that faked the timer would have passed against
the broken version.

**"The hook never writes" was too broad, and is now precise.** It writes: one line per
firing, to its own session's event feed. What it must never touch is *fleet state*, and
the test fingerprints identities, roles, and permissions before and after while
expecting the feed to grow. The feed is appended through `event.Append`, which takes a
path rather than a store, so the read-only door the hook opens stays a door with no
exceptions in it.

**Reads and writes fail in opposite directions, and the asymmetry is deliberate.** A
write must match a `write` clause, and an identity with none may write nothing. Reads
narrow only when there is something to narrow them with: an identity holding no `read`
clause is unrestricted, because a rule that must be bootstrapped before anything can be
read is a deadlock rather than a rule — the same reading `orc(<verb>)` gating already
uses.

**The feed's turn numbering stays off the hot path.** Only `UserPromptSubmit` reads the
feed back, to count turns; a tool call appends and reads nothing. The view assigns turns
to the calls between prompts, which it is scanning for anyway.

Two notes for the other streams. `internal/store/enforcement.go`'s stubs became
`WriteAuthz`/`ReadAuthz` over a typed `AuthzSnapshot` rather than `[]byte`, and
`AppendEvent` was dropped from the store for the reason above. And **file ownership is
not enough inside a shared test package**: `internal/provision/settings_test.go` and
stream E's `provision_test.go` collided on `epoch` and `mustUser`, so this stream's
helpers are prefixed. Finish.md's contention map now says so.

### Milestone 2, as built

`orc-session` is the supervisor, `internal/pty` is the terminal layer, and
`employ`/`fire`/`tend`/`poke`/`refresh`/`attach --direct` all work against real
sessions. Seven things came out differently from the plan, and two of them were
bugs a test found rather than decisions.

**The pty is two build-tagged files and no dependency.** `syscall` exposes what is
needed on both platforms: `/dev/ptmx` plus `TIOCPTYGRANT`/`TIOCPTYUNLK`/
`TIOCPTYGNAME` on darwin, `TIOCSPTLCK`/`TIOCGPTN` on linux, and
`TIOCGETA`/`TIOCSETA` (darwin) or `TCGETS`/`TCSETS` (linux) for raw mode. A test
runs `tty` inside the pty and checks it reports the pty, which is the real
assertion that `Setsid`+`Setctty` made the child a terminal program rather than a
process holding terminal-shaped descriptors.

**A unix socket path has a hard limit, and the store's natural path can exceed
it.** `sun_path` is 104 bytes on darwin; `bind` past it fails `EINVAL`, which says
nothing about length. So `store.SocketPath` puts the socket beside the session
state when it fits and falls back to `/tmp/orc-<uid>/<hash>.sock` when it does not,
**and session.json records which** — so no client guesses. The hash is of the store
root and the identity, which keeps a fleet and an Orcprobe copy of it from ever
sharing a socket. Found by the first live `employ`, which is exactly the sort of
thing that never shows up in a temp-dir-free unit test.

**Employment is journalled; population is not.** The plan said this; building it
made the consequence concrete. `session.json` is written whole and removed on exit,
and **its existence is a claim rather than a fact** — a `SIGKILL`ed supervisor
leaves one behind. So every read checks the supervisor pid with signal 0, and a
stale file reads as "not populated", which is what lets `tend` restart the thing it
exists to restart.

**One supervisor per identity is enforced with `flock`, not by convention.** A
second one takes the session lock, fails, and exits. `flock` because the holder is
a long-lived process that gets killed: the lock goes when its descriptors close, so
there is no stale lock to reap by guessing whether its owner is alive.

**`Afford` is a hypothetical, not an addition.** The count multiplier means the
marginal cost of a session is not its own load, so the budget check totals the set
*with* the new session in it. A caller that added a number to a total would refuse
the wrong employments. This is also why the refusal names the multiplier: verified
live, a fourth `sonnet/medium` session under `spawn(24)` is refused at `load 21 → 28
of 24: the count multiplier rose from 1.2 to 1.3`, while a `haiku/low` fourth fits
at exactly 24 — the boundary the design intends.

**Two bugs the tests caught.** `orc employ` reconciled against the fleet snapshot
it had derived *before* appending its own employ event, so it decided the identity
it had just employed was not employed and never populated it; `reconcile` now reads
the store. And every display path passed an empty session id to `LiveGrants` while
the derivation passed the real one, so a session-scoped grant showed as lapsed the
moment it was made — `authz.Fleet.Session` now exists so there is one answer.

**What is not covered by an automated test**, stated rather than implied: the
`attach --direct` proxy loop itself. Its pieces are — the pty's raw mode and resize,
the attach stream over the socket, and `scanDetach`'s held-prefix state machine —
but the loop that wires a real terminal to a real session is exercised by hand. A
test for it needs a pty on both ends and a fake operator, which is milestone 4's
work if the clean view makes it worth building.

### Milestone 1, as built

Module `orc/orc` at `Orc/Orc/go.mod`, stdlib only, in the workspace. Nine things
came out differently from the plan above, each for a reason worth keeping.

**Permissions have no journal, because nothing mutates one.** §3 reserved a
journal for every entity. No command in Reference.md changes a permission after
creation — it is created, assigned, and removed — so a journal would be a file
that is always empty and a fold that can never run. A permission is therefore one
immutable record, and widening one is creating another under a new name, which
shows up in every card that lists it. What this leaves missing is a way to narrow
a *role*, so `orc remove permission <name> --from <role>` is that: it reads as the
sentence it is, rather than adding a verb.

**Roles and identities are directories, not pairs of flat files.** §3 wrote
`roles/<name>.json` beside `roles/<name>.jsonl`. A directory gives the per-entity
lock somewhere to live, and matches Macmuffin, whose locking discipline this store
copies wholesale.

**Three doors on the store, not one.** `Create` (bootstrap only), `Open` (refuses
a fleet that is not there), and `Read` (creates nothing, refuses every write —
the door `orc-hook` and `orc introspect` will use). Every other store in this tree
creates its layout on open, deliberately, so that an agent's first command works.
Orc must not: a store is not the whole of a fleet — there is also an operator, a
key, and a mailbox — so a store conjured by `orc status` would be a fleet with no
operator, which is a state no command can do anything with. A mistyped `ORC_HOME`
now says `orc bootstrap` instead of silently becoming a second fleet.

**`orc(<verb>)` clauses narrow rather than enable.** The rule is not in
Auth_Perm_Role.md, and it had to be decided to build `mayRunVerb`: an identity
with no orc-kind clause is governed by the structural rules alone, and one with
any is additionally held to them. The alternative reading — every verb needs an
explicit `orc()` clause — makes a freshly bootstrapped fleet unable to create the
permission that would let it create anything. A rule that requires bootstrapping
itself is not a rule.

**`orc new identity` needs no authority at all.** A new identity has no role, so
it holds nothing, and an unemployed identity costs nothing to have. What costs
something is employing it, and that is exactly what `spawn` is for. This is
Auth_Perm_Role.md read literally, and it is worth stating because "anyone may
hire" looks permissive until you notice the hire can do nothing.

**A shared role is an escalation path, and the authority check alone does not
close it.** `assign authority` and `assign permission` also refuse when the role
is held by anybody outside the caller's subtree. Without that, a mid-level agent
could promote a peer by editing the job they happen to share — the caller's own
level bounds what it can hand out, but not *who* receives it.

**Two rendering bugs, both found by tests rather than by eye.** Padding must be
computed on the *plain* text and applied after the paint: `%-44s` on a painted
string lays the help out one way with colour and another way without, and the test
that strips the escapes and compares byte for byte is what caught it. And a
duration must choose its unit *after* rounding up — 59m59s was printing as "60m"
while an exact hour printed "1h", which is one column showing the same duration
two ways.

**The dedupe rule needed provenance in it.** Running a grant of something a role
already gave showed every clause twice, once "from role" and once "granted". Two
clauses of the same permission now absorb each other regardless of source,
preferring the role's — but a *wider* granted clause is never absorbed by a
narrower role clause, because that is a permission about to lapse and the expiry
is the whole reason to show it. `orc grant` also says when a grant is redundant.

**`Sessions()` exists and returns nothing.** It is what a session-scoped grant
asks to know whether it has lapsed. With nothing populated, every identity maps to
the empty string — and that is the correct answer rather than a placeholder: a
grant tied to a session that does not exist has already lapsed. So `orc grant`
without `--until` falls back to a one-hour clock and says so, which is the honest
reading of "temporarily" in a build that cannot yet populate anything.

**What milestone 1 does not have**, stated so a fleet with no sessions does not
read as a broken tool: `employ`, `fire`, `attach`, `poke`, `refresh`, `tend`, and
`doctor` all refuse with exit 1 and name what they are waiting on, following the
precedent `muff assign` set. `orc verify` is the half of `doctor` that exists.

The tests are §11's classes 1, 4, 5, and 6: the derivation is proven by property
over 200 random trees (authority never exceeds the boss's, every clause is
provably inside the boss's, no clause sits below its floor, every chain ends at
the operator), key hygiene is asserted over every command on both streams,
journal recovery is exercised with a real truncated append, and the sandbox guard
is tested at all three doors. Classes 2 and 3 — the fake `claude`, the PTY — belong
to milestone 2 with the code they test.

Milestone 1 is useful on its own and is the one every other tool is waiting for:
it makes accounts with keys the whole tree already knows how to verify, and it
answers "who may what" exactly. Milestone 2 is where Orc becomes an
orchestrator. Milestone 3 is where its permissions stop being advice.

The ordering has one deliberate inversion: `attach --direct` ships a milestone
before the clean view, even though the clean view is the default. The raw proxy
is what proves the PTY layer works, and building Orc's own interface on an
unproven PTY would mean debugging two new things through each other.

---

## 14. Decisions

### Confirmed with the user

1. **Orc owns the PTY**, and `attach` is Orc's own cleaner interface over the
   session, with `--direct` for a full attach to the real Claude TUI (§6.2, §6.3).
2. **Orc mints keys and the tools verify them.** Orc holds the only plaintext
   copy; Mailman gains `--key -`; a new `common/account` closes Macmuffin's hole
   (§4, §9).
3. **Exactly one role per identity**; `assign role` replaces (§2.3).
4. **Load is superlinear in the count** — `⌈ Σ session × (9 + |S|) / 10 ⌉` over
   the actor's transitive employed set, in integers (§6.4).
5. **Grants get all three mechanisms**: session-scoped by default, `--until` for
   wall-clock, and a new `revoke` verb for ending one early. No grant is
   unbounded (§2.5).
6. **A workspace is a plain directory**, with `--worktree` opt-in and bound
   through `muff worktree` (§5).
7. **`orc bootstrap [--as <name>]`** creates the store and the operator identity.
   It does not wire cq (§8.1).
8. **Sessions run in `bypassPermissions`.** Nothing prompts; `orc-hook` is the
   boundary, with `authz.json` as its fallback and a fail-closed floor for writes
   (§7.2, §7.3).
9. **The `Agent` tool is denied for every identity**, so the worklist is the
   whole picture of what is thinking (§7.2, §7.5).
10. **Reference.md is updated before implementation**, as its own diff, and
    `init` is named `bootstrap` (§8, milestone 0).

### Still assumed

Small enough to decide in the building, and each named so it is a decision rather
than an accident:

- **The detach key is `^\ d`**, and `^S` sends from the compose pane — `^C`,
  `^D`, and `^]` all belong to the session. Milestone 2.
- **An identity outside the caller's subtree is "not found", not "denied"**,
  following Macmuffin's privacy rule. Milestone 1.
- **Orc's root is `~/.orc`**, beside the other tools' roots rather than inside
  the repo, so nothing Orc removes can reach project files.
- **`orc env` exists and discloses a key** to a caller that controls the
  identity. It is the one command that prints key material, and §11's hygiene
  test is written around that being the only one.
- **The hook's write-blocking floor** (§7.3, third row) stalls an unattended
  agent when Orc's own store is unreadable. That is the intended trade under
  `bypassPermissions`, and `orc doctor` is what makes it diagnosable — but it is
  the one rule in this plan whose cost lands on an agent that did nothing wrong.
