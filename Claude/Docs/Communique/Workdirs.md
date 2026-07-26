# Migrating working directories — a plan

Moving an agent's working directory, driven from all three of cq's surfaces: the web
UI, the CLI, and the API.

The short version: **cq cannot proxy a verb Orc does not have, and Orc does not have
this one.** An identity's workspace is a path Orc derives, not a value it stores, so
before cq can offer to move one there has to be something to move. That is §2, and it
is most of the work. §3–§5 are the three surfaces, and they are small once §2 exists.

---

## 1. What is actually there today

**A workspace is a derived path.** `store.WorkspaceDir(name)` returns
`$ORC_HOME/identities/<name>/workspace`, computed from the layout every time it is
asked. Nothing stores it, nothing can change it, and every screen that shows it —
`orc status`, `orc introspect`, `orc env`'s block, cq's mirrored `Workspace` field —
is showing that computation rather than a fact.

**`--worktree` is documented and does not exist.** `orc help new` and
`Docs/Orc/Reference.md` §1.1 both advertise `orc new identity <name> [--worktree]`,
described as "make the workspace a git worktree of the main repo". The string appears
in `internal/cli/help.go` and nowhere else in the tree; the command refuses it:

```
$ orc new identity ember --worktree
orc: new identity takes one name, got 2 arguments
```

That is worth fixing on its own — a help screen is the one place a reader trusts
without checking — and it matters here because it is the closest thing to a
working-directory *choice* Orc has ever claimed to offer. This plan replaces it with
something real rather than implementing the flag as written: `--worktree` picks a
shape at creation, and the question being asked is how to change one afterwards.

**cq already mirrors the value.** `protocol.Identity` carries `Workspace`, so the
browser can show where an agent works today without any change. It has no way to
change it, which is the gap.

---

## 2. The Orc layer: a workspace you can move

### 2.1 What "migrate" means — three operations, not one

These get conflated, and they have different risks:

| | What it is | Files move? |
|---|---|---|
| **Relocate** | Same content, new path — a bigger disk, a faster volume, a tidier tree. | Yes |
| **Adopt** | Point the identity at a directory that already exists: a checkout somebody made, a git worktree, a repo the agent should work in. | No |
| **Replace** | Throw the contents away and start again. | No — it is `rm` and a fresh clone |

**Relocate and adopt are this plan. Replace is not** — it is a library operation
(cq already writes the mirrored checkout) or a shell command, and giving it a fleet
verb would make "migrate" a word that sometimes destroys work.

### 2.2 The model change

The workspace becomes a stored property with a derived default, exactly as model and
effort are stored with a default:

- `model.Identity` gains a `workspace` field. Empty means "the derived path", so
  every existing identity and every existing journal keeps working with no migration
  of the store itself.
- A new identity event, `OpWorkspace`, carrying the new path. It follows `OpModel`'s
  precedent: its own op rather than a reuse, because "work somewhere else" and
  "start" are different intents.
- `store.WorkspaceDir(name)` keeps its signature and answers from the identity when
  it has one. Every screen that already shows a workspace then shows the right thing
  with no change — including cq's mirror.

### 2.3 The verb

```
orc workspace <identity>                        show where it works
orc workspace <identity> <path> [--adopt] [--now]
```

Shaped on `orc model`, which is the closest thing in the tree and was built this way
for the same reasons:

- **Auth is `controls`.** Directing where somebody else's agent works is the boss's
  call. An agent may read its own, and `orc introspect --only workspace` already
  answers that for a session.
- **Refusals before anything moves**, and each names the way forward:
  - a path that is not absolute;
  - a path inside `$ORC_HOME` other than the identity's own — an agent whose
    workspace contains the fleet's keyring is one `permissions.deny` was written to
    prevent;
  - a path inside another identity's workspace, which would make two agents' scopes
    overlap invisibly;
  - **relocate** onto a path that exists and is not empty (that is `--adopt`);
  - **adopt** of a path that does not exist (that is a relocate, or a typo).
- **The sandbox guard applies.** `orc doctor` reports whether it is in force, and a
  workspace outside the sandbox stamp is refused there rather than discovered later.

### 2.4 What has to move with it

This is the part that makes it a migration rather than a field edit. Each of these is
a test.

1. **The live session's cwd is fixed at launch.** A running agent keeps working in
   the old directory until its session is replaced — the same physics `orc model`
   ran into. Same answer: say so by default, `--now` to refresh, and name the cost.
   Unlike a model change, this one is *dangerous* to leave deferred: the agent is
   writing to a directory Orc no longer considers its workspace. The default message
   should say that plainly, and `orc doctor` should report the disagreement.
2. **The compiled Claude settings name paths.** `permissions.allow`/`deny` are
   generated from effective clauses at populate time, and a workspace change makes
   them stale. Recompiling is part of the migration, not a follow-up.
3. **Macmuffin worktree bindings.** `muff worktree <task> <path>` binds a task to a
   directory, keyed by the canonical resolved path, and the hook looks up the
   session's cwd to decide which task is in force. Moving the directory orphans every
   binding under it: the hook stops enforcing, silently, which is the worst way for
   an enforcement mechanism to fail. The migration must **enumerate the affected
   bindings and rebind them**, and where it cannot, say which tasks lost their
   binding and what `muff worktree` command restores each. This is a cross-tool step
   and belongs behind `muff`'s CLI, not inside Orc reaching into another store.
4. **Task scopes are relative.** `muff scope` entries are relative to the worktree
   root, so a relocate that keeps the tree intact keeps them valid. An *adopt* of a
   different tree does not, and the plan should say so rather than pretend: adopting
   is the operation that can leave a task pointing at paths that no longer exist,
   and `muff verify` is what reports it.

### 2.5 Crash safety

A relocate copies bytes; something will be interrupted eventually. The tree's
discipline applies: **record the intent before doing the work.**

1. Journal `OpWorkspace` with the new path and a `moving` marker.
2. Copy to the new path (copy, not rename — a rename across filesystems is a copy
   anyway, and a copy leaves the old one intact if it dies).
3. `fsync`, verify, then journal the migration complete.
4. Remove the old directory only after that, and only for a relocate.

A crash between 1 and 3 leaves both directories and a journal that says which one is
authoritative. `orc verify` reports it as an unfinished migration and names both
paths; nothing guesses.

The alternative — move first, record after — makes a crash indistinguishable from a
workspace that was never moved, with an agent's work in a directory nobody is
looking at.

---

## 3. cq's protocol

One new operation, following the fleet ops already there:

```go
OpOrcWorkspace Op = "orc.workspace"   // orc workspace <identity> <path> [--adopt]
```

**Two new operands**, and neither reuses an existing field:

- `Workspace string` — the new path. *Not* `Path`: that field means "relative to the
  mirrored checkout and may not climb out of it", and a fleet path is absolute and
  outside it. Two meanings on one field would make the queue's own report of what an
  action is about depend on which op sits beside it, which the fleet fields were
  split up to avoid in the first place.
- `From string` — **the workspace the operator was looking at when they asked.**

`From` is the interesting one, and it is the library ops' `Base` under another name.
A cq snapshot is minutes old by the time somebody acts on it. Without `From`, an
operator who moves ember's workspace in the browser — while an agent on the machine
has already moved it — silently overwrites a decision they never saw. With it, the
agent compares and refuses, and the queue says why. It is the same argument that
makes a mirror safe to edit files from, applied to the one fleet value whose old
location still exists on disk afterwards.

**Idempotency:** setting a pointer twice lands in the same place, so the *adopt* form
is idempotent. The *relocate* form is not — the second application finds the source
gone — but it is self-guarding through `From`, exactly as `OpWrite` is through
`Base`. That distinction belongs in `Op.Idempotent()`'s switch with the reasoning
written down, as the others are.

---

## 4. The three surfaces

### 4.1 API

```
POST /api/v1/fleet/identities/{name}/workspace
     {"workspace": "/Users/…/trees/ember", "from": "/Users/…/.orc/identities/ember/workspace", "adopt": true}
```

One route, beside the fourteen `/fleet/identities/{name}/…` verbs that already
enqueue. It validates the operands, enqueues, and returns the action id — the same
shape as `employ`, `poke`, and `refresh`, so nothing new has to be learned to use it.

`GET /api/v1/fleet` already carries `Workspace` per identity, so the client can fill
`from` without another call. That is not incidental: **the API should refuse a
request with no `from`**, because a client that cannot say what it was looking at is
a client that cannot be protected from acting on a stale view.

### 4.2 CLI

cq's CLI has no per-verb queueing commands today — the browser queues, and `cq queue`
is how the server side inspects. A workspace migration is the first fleet change
worth having on the command line, because it is the one somebody does *while sitting
at the machine*, having just moved a directory.

```
cq workspace <identity> <path> [--adopt] [--from <path>]
```

It behaves differently on each side, and says which side it is on — cq's help already
labels every command `server` or `agent`:

- **agent side** (`$CQ_SERVER` set): runs `orc workspace` directly. There is no
  queue to wait for; the fleet is right here.
- **server side** (`$CQ_STATE` set): enqueues, exactly as the API does, and prints
  the action id and that it waits for the next sync.

If both are set, it refuses and asks which was meant rather than guessing — a command
that silently picks one of two machines to change is one nobody can script.

### 4.3 Web UI

The fleet screen's identity card gains a **workspace row**, which today shows a path
and nothing else:

- the path, with a **move** affordance beside it;
- a form taking the new path, a relocate/adopt choice, and a plain statement of what
  each does to the files;
- the queued badge every other action already gets, and the same staleness note the
  site shows everywhere — this one has to name the path the snapshot carried, since
  that is what `from` will be;
- when the action fails, the queue's error verbatim. A refusal here is nearly always
  "somebody already moved it", and paraphrasing that would lose the only fact that
  matters.

The card should also show, when Orc reports a **disagreement**, that the running
session's cwd is not the workspace — the §2.4.1 case. That is the state an operator
most needs to see and the one they will never think to look for.

---

## 5. What it costs to get wrong

Worth stating plainly, because the failure modes are quiet:

- A workspace moved while a session runs leaves an agent writing where nobody is
  looking. Mitigated by the refresh prompt, the doctor check, and the UI row.
- A relocate that orphans Macmuffin bindings turns the scope hook off without saying
  so. Mitigated by enumerating and rebinding, and by refusing to call the migration
  complete while bindings are unaccounted for.
- An adopt pointed at a tree that does not match a task's scope leaves the scope
  enforcing paths that do not exist. `muff verify` reports it; the migration should
  say to run it.

---

## 6. Milestones

| # | Delivers | State |
|---|---|---|
| 1 | `--worktree` honestly resolved. | **done** — struck from `orc help new` and the Reference. `orc workspace --adopt` is the real form of what it promised, so implementing it would have been two ways to do one thing. |
| 2 | Orc: stored workspace, `OpWorkspace`, `orc workspace` show/adopt, refusals, journal. | **done** |
| 3 | Orc: relocation (moving the files, crash-safely) and the things that move with it. | **done** — settings recompiled, session-cwd disagreement reported by `orc doctor`, affected `muff` bindings enumerated and rebound by `muff rebind`. |
| 4 | cq: `orc.workspace` op with `Workspace`/`From`, and the API route. | **done** |
| 5 | cq: the CLI command, both sides. | **done** — `cq workspace`, agent side and server side, refusing to guess between them. |
| 6 | cq: the web screen. | **done** — `project → location`. |

Milestones 4–6 are only worth starting once 2 and 3 exist — cq proxying a verb that
does not work would make the queue a place actions go to fail.

---

## 7. As built, so far

**Milestone 1.** `--worktree` is struck from `orc help new` and from
`Docs/Orc/Reference.md` rather than implemented. What it promised — a workspace that
is a git worktree — is what `orc workspace <identity> <path> --adopt` does, from a
verb that can also be used after creation. Implementing the flag as written would
have been a second way to do one thing, and the worse of the two.

**Milestone 2, except relocation.** `model.Identity` carries a `workspace`, empty
meaning the derived path, so every journal written before this stays valid.
`OpWorkspace` is its own event beside `OpModel`, for the same reason: where an agent
works and whether it is working are different questions.

The load-bearing decision was **where the stored value is honoured**. `WorkspaceDir`
is asked in eight places, including the supervisor's `cmd.Dir` and the enforcement
hook's path resolution — and `internal/hook/**` belongs to another stream. Threading
the value through every caller would have meant editing their files and would have
left eight chances for one of them to miss the exception, which is an agent working
in one directory while its permissions are checked against another. Instead
`WorkspaceDir` keeps its signature and consults the identity itself, falling back to
the derived path when the identity will not load — which is the behaviour the hook
needs anyway, since it fails open by design.

**Relocation is refused rather than faked.** `orc workspace ember /new/path` with no
directory there says the move is not built, and shows the two commands that do it by
hand. Making an empty directory and calling it a move would leave the agent's work in
the old one with nothing pointing at it.

**Milestone 3, except two things.**

*Relocation is built*, and the ordering in §2.5 changed. The plan said journal the
intent first, on the tree's "record before you act" rule. That rule is for operations
that **destroy**, and this one copies: copying first means a crash leaves the old
directory untouched and the identity still pointing at it — a stray directory rather
than an agent pointed at half a tree. Journalling first would have produced exactly
the failure the rule exists to prevent, inverted. So: copy, verify, journal, and
**never remove the old directory**. Orc does not delete an agent's work as a side
effect of a settings change; the command says where the original is and leaves it.

Symlinks are recreated rather than followed — a link to something large would be
copied twice, and one pointing at its own tree would not terminate — and a target
inside the source is refused as the loop it is.

*The drift warning is built.* `store.SessionState` gained a `Workspace`, written by
the supervisor from `cmd.Dir`, so "the running session is working somewhere else" is
a fact rather than an inference. `orc workspace <identity>` reports it unasked, names
where the session actually is, and says `orc refresh` moves it. A session recorded
before the field existed says nothing: *cannot say* is not a disagreement.

*The compiled settings needed no work at all*, which was worth checking rather than
assuming. `session/prepare.go` compiles them from `store.WorkspaceDir` at every
populate, so making that function honour the stored value made the settings follow a
move for free. Verified live: after a move, `permissions.allow` reads
`Read(<new>/**)` and `Write(<new>/Anno/**)`.

**Left, and why:**

- **The `doctor` check** for the same drift. `internal/cli/doctor.go` belongs to
  stream C, and `orc workspace` already reports it, so this waits rather than
  reaching into their file.
- **The Macmuffin rebinding** (§2.4.3). Still the sharpest edge here: a relocate
  under a bound worktree orphans the binding and the scope hook stops enforcing,
  silently. It needs a verb on `muff`'s side to rebind by path, which is a change to
  another tool rather than another line here.

## 8. cq, as built

**The op and the route.** `orc.workspace` carries `Workspace`, `From`, and `Adopt`.
Both paths must be absolute and both are required — validated in the protocol, so a
malformed action never reaches a queue. A relative path would mean a different
directory depending on where the sync happened to run, and *the machine that applies
a queued action is not the machine that wrote it*, which is the one thing a queue
cannot let an operand depend on.

`From` is checked on the agent side, in `source/orc.go`, by reading
`orc workspace <identity>` before moving anything. Orc has no opinion about what a
browser was looking at; this is the moment the two can be compared. A move against a
stale view is refused with what changed, rather than silently overturning a decision
made on the machine while the action sat in the queue.

That guard makes this the only fleet verb that runs **two** commands, which the
shared mapping test asserted against. The test now pins the command that *changes*
something — the last one — rather than the count, since reading before writing is
the design rather than an accident.

**The screen is `project → location`**, not `manage → fleet`. Manage is about what an
agent *is*; `project/code` and `project/docs` show what is in the repository; this
shows which copy of it each agent has its hands on, which is the same question asked
from the other side. It is its own file, `location.js`, so a tab with a form on it
does not grow inside the file holding every other tab.

It leads with **how many agents share a directory**. Two in one tree may be
deliberate; two by accident is how a scope stops meaning anything, and it should not
have to be worked out by reading down a column.

The form offers two operations rather than a checkbox — *work in what is already
there* and *move its files to the new directory* — because that is what they are. A
tickbox would have made the more destructive of the two the unlabelled default.

**Three totality guards caught this on the way in**, each asking the right question:
every op needs valid arguments (protocol), every fleet verb needs a route or the
browser cannot reach it (server), and every op must be classified idempotent or not
with a reason (store). The third is the interesting one: a workspace move is two
operations behind one verb, and it is classified by the half that is not idempotent
and guarded the way the library's writes are.

**Left in cq:** the CLI (milestone 5), and `.path` is unstyled — `app.css` was being
edited by another agent at the time, and the site is monospace throughout, so the row
reads correctly without a rule.


---

## 9. Milestones 3 and 5, as built

Everything in §6 is now done.

### `orc doctor` reports the drift

`orc workspace <identity>` has always said when a running session is working
somewhere its identity no longer names. Nobody runs that for every agent, so the
question now has an answer on the screen people open when something is *already*
suspected: a `workspace drift` guard that compares each live session's recorded cwd
against its identity's workspace and names every disagreement, with
`orc refresh <identity>` as the fix.

It reads as **partial** rather than **absent** when it finds something, because the
guard exists and is working — it is what it found that is wrong. A session recorded
before the workspace field existed says nothing at all: "cannot say" is not a
disagreement, and reporting it as one would make `doctor` cry wolf on every fleet
that upgraded.

### `muff rebind`, and Orc calling it

§2.4.3's cross-tool step, and the one with the worst failure mode in the whole plan:
a binding is keyed by a worktree's resolved path, the hook looks the session's
directory up in it, and a moved directory leaves the lookup finding nothing. The hook
then concludes no task is in force and enforces nothing — silently, looking exactly
like an agent that never opted in.

```
muff rebind [--dry-run] <old> <new>
```

Every binding at or under `<old>` is rebound to the matching path under `<new>`.
Four decisions worth keeping:

- **`filepath.Rel`, not a string prefix.** `/a/bc` is not under `/a/b`, and a prefix
  comparison that thought otherwise would point another tree's binding at a directory
  that does not exist.
- **Bind before Unbind.** A crash between them leaves a task bound to both
  directories — a duplicate the next rebind resolves. The other order leaves it bound
  to neither, which is the silence this command exists to prevent.
- **What could not follow is named, with the command that restores it**, and the exit
  is 6. A task whose binding did not survive has no scope enforcement anywhere; a
  migration script needs to know that, and an operator needs the `muff worktree` line
  rather than a list of failures to reconstruct paths from.
- **Same authority as binding.** Rebinding *is* that operation, aimed at where the
  directory went. An agent who may not bind a task may not quietly move where its
  scope is enforced either.

Orc runs it: `orc workspace` shells out to `muff rebind` after the identity is
written, and relays what comes back verbatim. It shells out rather than editing
Macmuffin's store, because one tool rewriting another's records is how two tools come
to disagree about a file's format — Orc knows a directory moved, and only Macmuffin
knows what a binding is. A muff that is not installed is not an error: most fleets do
not run one, and a missing binary means there are no bindings to strand. A muff that
*refuses* does not fail the move — the files are copied and the identity is written,
so failing there would report a move that happened as one that did not.

### `cq workspace`

```
cq workspace [--adopt] [--from <path>] [--state <dir>] [--machine <id>] <identity> <path>
```

The agent side runs `orc workspace` and prints orc's own words — what was copied,
what was left behind, which bindings followed. There is nothing cq could usefully add
to that, and summarising it would drop the parts a person needs.

The server side queues the same action the browser does. Two things it decides rather
than guesses:

- **Which side it is on.** With both `$CQ_SERVER` and `$CQ_STATE` set it refuses and
  asks, because the two differ in *when* the change takes effect — the part somebody
  would not notice going wrong. An explicit `--state` is the operator answering, and
  wins outright.
- **What `from` is.** The protocol requires it, and unstated it is where the mirror
  says the identity works — exactly what the browser sends, and what the operator was
  looking at when they typed the command. An identity the mirror does not know is
  refused with `--from` named, rather than queued for the agent machine to reject
  hours later on a machine nobody is watching.

`--adopt` and `--from` come before the identity, as the flag package requires.
