# §1 Communiqué — CLI

Communiqué exposes the same CLI structure as every other Orc sub-app:

```
cq <command> <args...>
```

| Command          | Does                                              |
|------------------|---------------------------------------------------|
| `serve`          | Serve the website and the cq API on port 8080     |
| `sync`           | Run one sync against the server, both directions  |
| `status`         | Print local sync state; never touches the network |
| `admin operator` | Set or change the login password                  |
| `admin token`    | Mint a sync token; printed once, stored hashed    |
| `queue`          | What is waiting, and what did not work            |
| `workspace`      | Move where an agent works — either side           |
| `upgrade`        | Rebuild and restart every tool, everywhere        |

## §1.1 Flags

| Command  | Flags                                                                                               |
|----------|-----------------------------------------------------------------------------------------------------|
| `serve`  | `--addr` `--tls-cert` `--tls-key` `--state` `--no-admin` `--admin-metadata-only`                    |
| `sync`   | `--server` `--machine` `--user` `--home` `--watch` `--nudge` `--dry-run` `--admin` `--admin-bodies` |
| `status` | `--home`                                                                                            |
| `queue`  | `--state` `--json`                                                                                  |
| `workspace` | `--adopt` `--from` `--state` `--machine`                                                         |

`cq serve` refuses to start until a password and a token are set. Nothing on the
site is visible without logging in.

Repeated login failures from one source are slowed, exponentially and per source,
resetting the moment one succeeds. The *first* failure imposes no wait: a mistyped
password followed straight away by the right one has to work, because that is what
a phone does. The second consecutive failure is where the delay starts.

Every refusal on the login path answers in the shape of the request. A browser
submitting the form gets the login page back with the reason on it — including
when it was refused for rate limiting or origin before the password was even
looked at — and a program calling the API gets JSON and a status it can branch on:
401 for a wrong password, 429 with `Retry-After` for a wait.

`cq sync --nudge` is what Mailman and Macmuffin call after every action that
changed something. It coalesces, never blocks its caller, and never fails it —
an agent parsing `mailman send` never learns cq exists. `--watch` is the backstop
that collects my replies while the agents are idle.

## §1.2 Agent machine

| Variable     | What it is                                        |
|--------------|---------------------------------------------------|
| `CQ_SERVER`  | Where to sync. Also what makes nudging happen     |
| `CQ_TOKEN`   | The sync token, from `cq admin token`             |
| `CQ_USER`    | The mailbox to mirror. Optional — see below       |
| `CQ_KEY`     | That mailbox's orc key                            |
| `CQ_MACHINE` | What to call this machine                         |
| `CQ_LIBRARY` | A repository to mirror for reading, if any        |
| `ORC`        | The orc executable, if not on the path as `orc`   |

The admin panel needs one more thing: the mirrored account must own the Mailman
store (`mailman admin owner <me>`). Without it the panel is left out and the
sync says so — the mailbox still mirrors, since the panel is an extra.

### Whose mailbox

A mirror is one account's, and cq works out which without being told. Three
rungs, tried in order:

| Rung | Resolves to                                          | When                           |
|------|------------------------------------------------------|--------------------------------|
| 1    | `--user`, else `$CQ_USER`                            | anything explicit wins         |
| 2    | `$ORC_USER`, if orc agrees it is the operator        | the operator's own shell       |
| 3    | orc's keyring, via `orc owner env`                   | no credential presented at all |

Rungs 2 and 3 both end at the operator, and nothing below rung 1 can resolve to
anyone else. That is what keeps an agent's `mailman send` — which fires a nudge
carrying *that agent's* credential — from publishing the agent's mailbox as the
machine's. cq refuses that outright, with exit `6`, and says what to set.

`CQ_USER` and `CQ_KEY` are therefore needed in two cases only: to mirror somebody
who is not the operator, and to let an agent's mail nudge a sync — with the key,
cq authenticates as the mirrored account regardless of who triggered it.

`cq status` prints the account it resolved and which rung answered, so "why is it
mirroring that" has a visible answer.

## §1.3 API

The API lives under `/api/v1` and mirrors Mailman's verbs:

| Route                                  | Mirrors                                  |
|----------------------------------------|------------------------------------------|
| `GET inbox`, `GET sent`, `GET archive` | `inbox --all`, `inbox --sent`, `archive` |
| `GET messages/<puid>`                  | `open`                                   |
| `GET convos/<cuid>`                    | `convo`                                  |
| `POST messages`                        | `send` — the compose page                |
| `POST messages/<puid>/reply`           | `reply`                                  |
| `POST messages/<puid>/read`            | `read`                                   |
| `POST messages/<puid>/archive`         | `archive <query>`                        |
| `POST messages/<puid>/prune`           | `prune <query> --yes`                    |
| `POST convos/<cuid>/cc`                | `cc` — from a message in the thread      |
| `GET messages/<puid>/check`            | `check`                                  |
| `GET tasks`, `GET tasks/<name>`        | `muff pool`, `muff info`                 |
| `POST tasks`                           | `muff create <task> <priority> <difficulty>` |
| `POST tasks/<name>/push`               | `muff push`                              |
| `POST tasks/<name>/claim`              | `muff claim`                             |
| `POST tasks/<name>/assign`             | `muff assign <agent> <task>`             |
| `POST tasks/<name>/invite`             | `muff invite`                            |
| `POST tasks/<name>/kick`               | `muff kick`                              |
| `POST tasks/<name>/leave`              | `muff leave`                             |
| `POST tasks/<name>/scope`              | `muff scope <task> <paths...>`           |
| `POST tasks/<name>/worktree`           | `muff worktree`                          |
| `POST tasks/<name>/status`             | `muff status <task> <1..4>`              |
| `POST tasks/<name>/subtasks`           | `muff create <task> --sub <name>`        |
| `POST tasks/<name>/complete`           | `muff complete [--sub] [--force]`        |
| `DELETE tasks/<name>`                  | `muff delete [--sub] --yes`              |
| `GET fleet`                            | `orc status --json` — every machine's    |
| `POST fleet/identities`                | `orc new identity`                       |
| `POST fleet/roles`                     | `orc new role <name> <authority> <what>` |
| `POST fleet/permissions`               | `orc new permission <name> <floor> …`    |
| `POST fleet/identities/<n>/role`       | `orc assign role`                        |
| `POST fleet/identities/<n>/move`       | `orc move`                               |
| `POST fleet/identities/<n>/employ`     | `orc employ [--model] [--effort]`        |
| `POST fleet/identities/<n>/fire`       | `orc fire --yes`                         |
| `POST fleet/identities/<n>/poke`       | `orc poke [message]`                     |
| `POST fleet/identities/<n>/refresh`    | `orc refresh`                            |
| `POST fleet/identities/<n>/workspace`  | `orc workspace <path> [--adopt]`†        |
| `POST fleet/identities/<n>/grant`      | `orc grant permission [--until]`         |
| `POST fleet/identities/<n>/revoke`     | `orc revoke permission`                  |
| `DELETE fleet/identities/<n>`          | `orc remove identity --yes`              |
| `POST fleet/roles/<n>/authority`       | `orc assign authority`                   |
| `POST fleet/roles/<n>/permissions`     | `orc assign permission`                  |
| `POST fleet/roles/<n>/budget`          | `orc budget <role> <load>`               |
| `DELETE fleet/roles/<n>`               | `orc remove role --yes`                  |
| `POST fleet/pace`                      | `orc pace <cycle> [<who>] [flags]`       |
| `POST fleet/tariff`                    | `orc tariff <setting> <n> --yes`         |
| `GET/POST sync/pace`                   | how often each machine syncs — the server's own |
| `GET activity?since=<dur>`             | the series: what each agent cost and touched, folded to a `period` it names |
| `PATCH fleet/permissions/<n>`          | `orc edit permission <name> --floor …`   |
| `DELETE fleet/permissions/<n>`         | `orc remove permission [--from] --yes`   |
| `PUT tasks/<n>/description`            | `muff describe <task> --set <file>`      |
| `DELETE tasks/<n>/description`         | `muff describe <task> --clear`           |
| `POST fleet/tend`                      | `orc tend`                               |
| `POST fleet/toolkit`                   | `orc bootstrap --as <operator>`†         |
| `PUT instruct/system`                  | `orc instruct system --set`              |
| `PUT instruct/roles/<n>`               | `orc instruct role <n> --set`            |
| `PUT instruct/identities/<n>`          | `orc instruct identity <n> --set`        |
| `PUT instruct/wake[/roles/<n>\|/identities/<n>]` | `orc instruct wake … --set`   |
| `DELETE` on any of the five above      | `orc instruct … --clear`                 |
| `POST upgrade`                         | pull, rebuild, restart — here and queued out |
| `GET upgrade`                          | how this server's own last build went        |
| `GET admin/state`                      | `admin mail` — the whole store           |
| `GET library`                          | the repository's structure, no text      |
| `GET library/file?path=`               | one file, with its text                  |
| `GET events`                           | live updates                             |
| `POST queue/<id>/retry`                | try a refused action again               |
| `DELETE queue/<id>`                    | forget one                               |

Every `POST` queues rather than sends, and answers `202` with its place in the
queue. It leaves on the next sync.

† `workspace` requires a `from`: the directory the browser was showing when
somebody clicked. A snapshot is minutes old by the time it is acted on, and a
workspace is the one fleet value whose old location still exists on disk
afterwards — so the agent machine compares `from` against where the identity
works *now* and refuses a move made against a stale view, rather than silently
overturning one somebody made in between. It is the same protection the library's
writes get from a digest.

† `toolkit` installs the permissions every fleet is made with, on a fleet that
does not have all of them — `orc bootstrap` is safe to run again and creates only
what is absent, so a fleet that redefined one of them keeps its own. The fleet
snapshot carries the toolkit as the agent machine's orc defines it, with whether
each one is present, which is what lets the browser show a permission that is
*missing*: a screen drawn from what exists can never do that.

## Driving Macmuffin

Every `muff` verb that changes something has a route, so the pool can be run from
the browser and not only read there. The four that have none are `pool` and `info`,
which arrive in every snapshot already, and `check-scope` and `verify`, which answer
about the agent machine's own filesystem.

One route per verb rather than one that runs a command line. A pass-through would
make the queue a list of shell commands nobody can report on, would put argument
checking on the far side of a sync, and would make every future Macmuffin flag
reachable from a browser without anyone deciding it should be.

The operands are checked before anything is queued — priority and difficulty 1 to 5,
status 1 to 4, names that Macmuffin would accept, scope paths that stay inside the
checkout. A value the pool would refuse never becomes an action that fails hours
later on a machine nobody is watching.

A task's **description** is the one operand that is prose. It travels in the
snapshot rather than being fetched when somebody opens a task, because the server
cannot reach the agent machine — a description not in the mirror is one the
browser cannot show at all. The board says which tasks have one; the text arrives
with the same per-task `muff info` call that carries the steps, and only for the
tasks that have either. Going the other way it is written to a temporary file at
0600 and passed by path, never in argv: `ps` is public on a machine several agents
share.

Whether a task *has* a description travels separately from the text, and the two
must not be confused. A task the agent could not read the description of arrives
described-but-empty, and the panel says exactly that — offering to write a first
one over the top of prose nobody has seen is how a specification disappears.

**cq holds no opinion about who may do what.** Every command runs as the mirrored
account, and Macmuffin's own rules apply exactly as they do at a terminal: setting
the status of a task you do not own is refused, and the refusal comes back word for
word in `cq queue`, with a retry offered. That is the whole authorisation story —
there is no second copy of it here to drift.

`muff pool` is a board and does not carry every step of every task, while `muff info`
does. So the agent asks twice — once for the board, once per task that has steps —
which is what lets the browser complete or delete a named step rather than only see
`2/5`. A task with no steps costs no second command.

Retrying is bounded by the same rule the mail verbs follow: `scope`, `worktree`,
`status`, `assign`, and `invite` set a value and may be repeated; `create`, `push`,
`claim`, `complete`, and the rest are transitions whose second application refuses,
so an action whose outcome is unknown is never retried blindly.

## Deleting mail

Mail is deleted from the site by ticking rows and pressing delete, or from an open
message. It is `mailman prune`, and it is permanent.

Mailman prunes the archive and refuses a query that touches anything still live,
so deleting from the inbox is **two** queued operations — an archive, then a
prune, in that order. The browser queues both rather than one operation doing
both: one operation is one Mailman command, and a verb that archived on the way
past would be a rule about mail that Mailman does not have. Deleting from the
archive is a prune alone.

The confirmation is in the browser, because a confirmation is a conversation with
whoever is looking at the mail and that is the only place they are. The queued
action carries `--yes`; by the time it reaches the agent machine it is a decision
already taken, and a prompt there would only be a way for it to hang.

A prune is **not** idempotent and is never retried after a doubt. Deleting twice
lands in the same place but the command does not: the second run matches nothing
and Mailman refuses it as not found, which is a confusing failure over mail that
has already gone.

Every mailbox can be ticked, one row at a time or the whole list, and the bar over
it applies **mark read**, **archive** and **delete** to what is ticked. One queued
action per message rather than one over a list: the queue's unit is an operation
the agent can report on, and twelve rows queued as one entry that half-applied
would leave nobody able to say which half. The selection lives in the page's state,
not in its checkboxes, so the redraw on every sync does not quietly clear it while
the button still offers to delete twelve things.

## When something does not work

An action the agent refused can be tried again or forgotten, from the queue page
or from `cq queue` on the server:

```
cq queue                    -> everything, and what became of it
cq queue retry 2c6f875a     -> try it again (ids may be abbreviated)
cq queue drop   2c6f875a    -> forget it
```

A retry is a **new** action and cq says so. It has to be: the agent remembers
every action it has applied and skips the ones it recognises, so reusing the id
would produce something that looks like a retry and is quietly ignored.

An action interrupted mid-apply is **in doubt**, not failed — it may or may not
have happened. Marking mail read twice is harmless, so those can be retried;
a send cannot, because it may already be in somebody's inbox. cq refuses that one
and says to check my sent mail instead.

An unfinished action is in one of two states, and the site tells them apart
everywhere it mentions one — beside the message it was written from as well as on
the queue page. **waiting** means no sync has collected it yet, which a sync from
the agent machine fixes; **with the agent** means a sync took it and has not
reported back, which nothing on this side will change. It is never called *sent*:
in a mailbox that word means a person received it, and this means a machine picked
it up.

The project is stored at `Orc/Communique/go.mod`.

## Reading the repository

`CQ_LIBRARY` points at a checkout on the **agent** machine — the one that runs
`cq sync`, not the one serving the site. `cq status` there says whether it is
set, which is the first thing to check when the tabs are empty.

### Moving it

**project › location** shows the mirrored checkout above that machine's agents,
and offers to move it. The change queues like everything else, and the machine
applies it on its next sync — so a mirror syncing every five minutes takes up to
five minutes to hear about it, and one syncing hourly takes an hour.

The choice is recorded in `settings.json` in the agent's home, beside the journal
and the cursor, and is read again **between rounds**. That is what makes it work
at all: a watcher's environment is fixed when it launches, so exporting
`CQ_LIBRARY` in a shell afterwards reaches nothing that is already running.

Three answers, in order: a `--library` typed on the command line, the choice
recorded from the website, then `$CQ_LIBRARY`. A typed flag wins for that run and
also stops the re-reading, so one run means one directory throughout.

The machine decides whether to accept the path, because it is the only end that
can see the filesystem. It refuses one that is not there, one that is a file, and
one that is — contains, or sits inside — either the fleet's home or cq's own. The
last of those is the one that matters: everything under the library root is
mirrored to the site and writable from it, so a root that swallowed `$ORC_HOME`
would put every agent's key on a web page.

`CQ_LIBRARY` points at a checkout on the agent machine. Its documents and source
are mirrored with the mailbox, and the site gains two tabs: **docs**, the files
Dock found `§` sections in, and **code**, everything else.

Both fold. A repository arrives as its top-level directories with their totals,
and nothing below that is drawn until I ask for it. A file's structure comes from
Dock and Anno — `§` sections for documents, section/symbol/part for annotated
code — so unfolding a file gives its parts rather than a wall of lines.

The structure and the text travel separately. The tree is tens of kilobytes for a
repository of thousands, so the whole thing can be listed and folded before
anything is read; opening a file is what fetches it, once.

Either lens missing from the agent's `PATH` is carried into the snapshot as a
note rather than shown as emptiness. I am at a browser on another machine, so
without it the tabs would tell me nothing in the tree has sections or
annotations — a statement about the fleet made from a fact about one machine.

## Upgrading

An upgrade pulls with `--ff-only`, and refuses before it starts when the checkout
cannot be pulled at all: a branch with no upstream, a branch the remote does not
have, or a detached head. Each refusal names the branch and the one command that
resolves it, because git's own advice is written for somebody at a terminal and
this one arrives in a server log hours later. It never sets an upstream or
switches branches itself — that is a change to a checkout on a machine nobody is
logged into, which is the same thing `--ff-only` exists to refuse.

`cq upgrade --yes`, the **rebuild everything** button in the admin panel, and
`POST /api/v1/upgrade` are the same request. It pulls the tree, rebuilds every Orc
tool, and restarts — on the machine serving the site *and* on every agent machine.

Two halves, because the two are reachable in opposite directions:

- **The server** upgrades itself: a local `git pull --ff-only`, `sh/build --to`,
  and a restart. It needs a supervisor, because a process cannot exec its own
  replacement and still be there to report on it.
- **Each agent machine** gets a queued `system.upgrade` action and does the work on
  its next sync. The server cannot reach them; that is the whole architecture.

The order is deliberate: queue the agents, answer the caller, *then* restart. A
server that restarted first would come back to a queue it had not written yet.

### The supervisor

`cq serve` runs a child `cq serve` and starts a new one when the child asks, by
exiting `75` — `EX_TEMPFAIL`, "try again". `--supervise=false` opts out, and the
endpoint then says it cannot restart itself rather than exiting into nothing.

After a restart the supervisor `exec`s its own path, so it becomes the new binary
too: same pid, so whatever watches *it* — systemd, a terminal — sees nothing. A
restart that fails immediately backs off, so a bad build is a log somebody can read
rather than a busy loop.

The build happens **before** the restart. The other order looks equivalent and is
not: restarting first brings the old binary back up, and restarting after a failed
build brings nothing up at all. A failed build leaves the server exactly as it was.

### Nothing is lost mid-flight

- The queue and the snapshots are on disk and fsynced before each reply, so what
  comes back reads exactly the state that went down.
- In-flight requests are drained for up to 15s before the process goes.
- Sessions survive: they are in the credential store, not in memory.
- An agent that synced during the gap fails and retries on its next round — which
  is what it already does for any unreachable server.
- Replacing a binary on unix leaves running processes on their old inode, so an
  `orc-session` supervisor and its agent carry on and pick the new build up when
  they next exec. `orc tend` reconciles whatever did not.

### By hand

`sh/pull` is the same three steps on the machine you are sitting at: pull the tree,
rebuild every tool, install them. It is what to reach for when the fleet's own
upgrade is the thing that is broken, and `sh/pull --check` says what would come
down without touching anything.

### Who may

`cq upgrade` needs Orc's builtin `upgrade` permission — floor 90, executive agents
only. cq asks with `orc check-permission upgrade` and repeats what it is told; it
holds no copy of the model and does not know what a floor is.

The check is on the **client**, and that is not an oversight. The server has no Orc
fleet: it runs on another machine, authenticates with a password and a token, and
has never heard of an identity. Teaching it the model would be exactly the second
copy of authority this tree exists to avoid. The server's own gate is the one that
was already there — a session or a sync token — and Orc's answer is the narrower
one on top, for the case an operator actually worries about: an agent with a shell
on a machine that has the token.

`$CQ_SOURCE` is the checkout to pull; `$CQ_BIN` is where binaries are installed,
defaulting to the directory the running one is in. A machine with neither refuses
and says so, which is right for one that installs binaries rather than building.

`$CQ_BIN` names two things and always has: `Common/nudge` reads it as *the cq command
to run*, and this reads it as *the directory to install into*. Both spellings are
accepted here — a path that is an existing file is taken as the binary and the tools
go beside it — because both are already written into shell profiles and neither is
wrong. A path that exists and is neither a directory nor a file is refused.

The build itself is a promise. The response to the button is sent first and the work
runs after it, because the work ends with the process going away — so
`GET /api/v1/upgrade` reports how the serving machine's own build actually went, and
the browser polls it while `tooling › rebuild` is open. A build that fails leaves the
server running on the old binary, which is the right outcome and used to be a silent
one: the reason reached a log and the page went on saying it was building.

## Driving Orc

The admin panel opens on the fleet: who exists, what each may actually do, what is
running, and what it costs. Orc was the one tool with no remote face — mail has a
mailbox and tasks have a board, and who-may-what had a terminal on the agent
machine and nothing else.

Every `orc` verb that changes something has a route. What is deliberately absent
is as considered as what is there:

- `bootstrap` makes the fleet; there is nothing to mirror before it runs.
- `attach` hands over a terminal, which a queue running minutes later cannot.
- `env` and `owner env` print a credential. Nothing in cq carries one.
- `owner rename` and `owner reset` act on the operator — the account the mirror
  authenticates *as*. A sheet in a browser, over state minutes old, is the wrong
  place to rename or destroy that. Both stay at the terminal.

**Everything shown is Orc's own derivation.** Authority in the panel is effective —
already the lower of a role's and a boss's — and the clause list is already
intersected down the chain. The browser recomputes none of it: a second derivation
would be a second opinion about who may do what, and the wrong one would be the one
on screen. Where the two numbers differ both are shown, with Orc's own `‡`.

**cq holds no opinion about who may do what.** Every command runs as the mirrored
account and Orc's rules apply exactly as they do at a terminal — an agent cannot
raise its own budget from a browser any more than from a shell — and the refusal
comes back word for word in `cq queue`.

The one thing worked out in the browser is a role's budget, and only as a form
default: a budget is a `spawn(n)` clause on a permission rather than a field on a
role, so the sheet reads it off the clauses already on screen. Nothing branches on
it. What an identity may actually employ is what Orc derived.

Retrying follows the same rule as everywhere else. `assign`, `move`, `budget`,
`fire`, `revoke`, and `tend` set a state to what was asked for and may be repeated;
`tend` most of all, since reconciling twice reconciles. The creates and removes,
`grant`, `employ`, `poke`, and `refresh` may not — `refresh` least of all, since a
second one discards the conversation the first one started.

## Editing from the site

Code is shown in a block — monospaced, highlighted, and scrolling inside itself
rather than wrapping, because a wrapped line of code is a line whose indentation
lies. Markdown is rendered rather than highlighted.

Everything the site asks for it asks for **in the site**: a sheet in cq's own
frame and typeface, over an inert page, with escape to leave. No `prompt`, no
`confirm`, no `alert`. Those arrive in the system font at the top of the window,
cannot say what a value is *for*, and ask one question at a time — so making a
task took three of them in a row. A sheet asks for a whole thing at once, and
says what is wrong with a value beside the box it was typed into rather than in
a second popup on top of the first.

A sheet complains **as it is typed**, and its submit will not fire while
anything is wrong. Waiting for a field to be left made sense against a rule a
good value passes through bad ones to satisfy; it makes none against these,
where every prefix of a good name is a good name — so the only way to see a
complaint while typing one is to type a character that will never be allowed,
which is the moment to say so. An untouched empty box is still left alone. The
submit carries `aria-disabled` rather than `disabled`, so it stays focusable and
pressing it marks every problem and moves the cursor to the first, instead of
being a wall with no explanation.

Names may be written **with capitals and spaces**. The rule underneath is not
negotiable — these become directory names, socket paths, and words in a command
line on the agent's machine — but whether somebody has to type them that way is.
A capital was always lower-cased downstream, so refusing it refused something
that would have worked; a space is how a person writes "fix the parser", and `-`
is how a machine spells it. Both are folded on the way out and the sheet says
what the name will be, because the board shows the tidied name afterwards and
learning it there reads as cq having renamed the thing. Every other character is
still refused, and named with its position: turning `%` into something would be
guessing at what was meant.

A refusal of what the caller sent is reported as theirs. Operands are checked in
the handler that read them rather than deep inside the queue, because the store's
failures are deliberately reduced to "internal error" — a path on the server's
disk is no business of a browser — and a task named `%parser` was going through
that same reduction. The person who typed it got a 500 and one word.

An opened file offers **edit** and **delete**; a section or annotation offers to
edit just itself, which splices back into the whole file. A directory offers
**new file** and **new folder**, and **delete folder** only when it is empty —
cq removes empty directories only, so offering it otherwise would be offering a
button that refuses a sync later. The root of the checkout is a directory like
any other and offers the first two; it is never offered for deletion.

The editor opens over the page rather than inside it. That is not decoration:
the view is redrawn on every sync, and a textarea inside it would be replaced
mid-sentence. While it is open the page behind is inert, so tab cannot walk off
into the delete button of the file being edited.

| Route                        | Queues                          |
|------------------------------|---------------------------------|
| `POST library/write`         | replace a file's contents       |
| `POST library/create`        | make a file that does not exist |
| `POST library/delete`        | remove a file                   |
| `POST library/mkdir`         | make a directory                |
| `POST library/rmdir`         | remove an empty directory       |

`rmdir` takes a directory and nothing else. It carries no digest — there is no
text to have changed — so pointing it at a file would be a way to delete one
without the precondition `delete` requires, and it refuses instead.

Every one queues and leaves on the next sync, like everything else I do from the
site. Three things make that safe to do from a mirror that is minutes old:

- **Every edit says what it was made against.** The action carries the SHA-256 of
  the text I was looking at, and the agent refuses if the file no longer matches.
  Without it, an edit made from my phone would silently discard whatever an agent
  changed in between.
- **The same digest makes it exactly-once.** After a write lands the file no
  longer matches, so a repeat refuses rather than overwriting — which matters
  most for the one action that cannot be undone.
- **Nothing leaves the checkout.** Paths are resolved *after* following symlinks,
  because a link is exactly how a path that looks contained stops being
  contained.

A refused edit says so in the queue, with what to do: open it again and redo the
change on what is there now.

## The tree

The **tree** tab is the survey of the same checkout: how many files and lines,
where the weight sits by directory and by kind, and the largest files. It is
shape and size only — never contents, which is what **docs** and **code** are
for. The median sits beside the mean because they disagree in the case worth
noticing: a tree of small files with a few enormous ones has a mean nothing
resembles.

It used to open the admin panel. Admin is about the **mail store** — who has an
account, what has been sent, what the queue is doing — and this is about the
**checkout**; they shared a tab because both are "the state of things", which is
a category rather than a subject. It was also the wrong tab in the other
direction: the survey needs no admin view, so living there hid it from every
machine that had not run `mailman admin owner`, and an operator with no admin
panel had no way to see the shape of their own repository.

## On a phone

The site lays out for the screen it is on, and nothing scrolls sideways at any
width. Below 40rem — a phone, or a narrow window on a desk — the layout changes
shape rather than shrinking:

- a message becomes two lines, subject first, then who and when;
- the board keeps a task's name and status on one line and folds its owner,
  scores and progress onto the next;
- the navigation wraps, and every control is at least 44px so a thumb can hit it;
- text fields are 16px, because below that a phone zooms the page when a field
  takes focus and leaves me scrolled sideways.

Nothing is hidden to make it fit. A truncated fact is a wrong fact, and that does
not stop being true because the screen got smaller — the scores keep their
labels when the header row goes, and a long path scrolls inside its own box
rather than moving the page.

## On Windows

cq builds and runs on Windows 11, on both amd64 and arm64. Four things there are
not what they are on unix, and each is handled rather than assumed away:

- **Lines end with a carriage return.** The wire refuses control characters, and
  one refused file fails the whole snapshot — so a rule that counted CR as
  binary would not skip a file on Windows, it would refuse the entire mirror.
  CR is text. The browser then normalises it away, because a textarea has no
  carriage returns, and puts it back when the edit leaves: a one-line change
  arrives as a one-line change rather than as a whole-file rewrite.
- **Paths are slash-separated, on both ends.** A backslash means nothing to a
  server on Linux and means a separator to an agent on Windows, so a path
  holding one means two different things at the two ends of the wire. It is
  refused at the door, and so is a drive letter.
- **The sync lock is held by the kernel.** Windows has no flock, but a handle
  opened with no sharing is exclusive and is closed when the process dies —
  which is the property that matters, because a lock left behind by a crash
  would stop the mirror updating until somebody found a file and deleted it.
- **The console has to be asked.** A console there decodes output with the
  machine's OEM code page and does not interpret escape sequences until told to,
  so without asking, every box rule arrives as mojibake and every colour as
  literal noise. cq asks on startup. Windows Terminal is taken at its word about
  24-bit colour, which it supports and advertises no other way.

Two smaller ones. Replacing a file is retried briefly, because a virus scanner
or the search indexer holding it open for a few milliseconds is not a reason to
lose somebody's edit. And `cq serve` will raise a firewall prompt the first time
it binds a port that is not on the loopback.
