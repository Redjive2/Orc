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

| # | Delivers | Done when |
|---|---|---|
| 1 | `--worktree` honestly resolved: implemented, or struck from the help and the Reference. | The help does not advertise a flag that errors. |
| 2 | Orc: stored workspace, `OpWorkspace`, `orc workspace` show/set, refusals, journal, `verify` on an unfinished migration. | An empty workspace field still resolves to the derived path; every refusal in §2.3 is a test; a killed relocate leaves both paths and a journal that names the authoritative one. |
| 3 | Orc: the things that move with it — settings recompiled, session-cwd disagreement reported by `doctor`, affected `muff` bindings enumerated and rebound. | A relocate under a bound worktree leaves the hook enforcing, or names every binding it could not rebind. |
| 4 | cq: `orc.workspace` op with `Workspace`/`From`, and the API route. | A stale `from` is refused and the queue says so; the op's idempotency case is written down with its reasoning. |
| 5 | cq: the CLI command, both sides. | Agent side applies; server side enqueues; both set refuses. |
| 6 | cq: the web UI row, the form, the queued and failed states, the cwd-disagreement warning. | An operator can move a workspace from the browser and see it land, and sees the refusal verbatim when it does not. |

Milestone 1 is a five-minute decision that should not wait behind the rest of it, and
milestones 4–6 are only worth starting once 2 and 3 exist — cq proxying a verb that
does not work would make the queue a place actions go to fail.
