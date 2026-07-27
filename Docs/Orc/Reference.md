# §1 Orc — CLI

Orc exposes the following commands:

| Command                                     | Does                                                                       |
|---------------------------------------------|----------------------------------------------------------------------------|
| `bootstrap [--as <name>]`†                  | Create the store and the operator identity; print the block for a shell profile |
| `new identity <name>`                       | Create an identity with the given name                                     |
| `new role <name> <authority> <description>` | Create a new role with the given permissions, authority, and description   |
| `new permission <name> <min authority> [patterns...]` | Create a new permission with the given name, required authority level, and command patterns |
| `assign role <identity> <role>`             | Assign the role to the identity                                            |
| `assign authority <role> <authority>`       | Assign the authority level to the role                                     |
| `assign permission <role> <permission>`     | Assign the permission to the role                                          |
| `edit permission <name> [--floor <n>] [patterns...]` | Change a permission's floor, its clauses, or both; every holder feels it at once |
| `remove identity <name>`                    | Delete the given identity (impossible if populated)                        |
| `remove role <name>`                        | Delete the given role (impossible if in use)                               |
| `remove permission <name>`                  | Delete the given permission (impossible if in use)                         |
| `grant permission <identity> <permission>`  | Temporarily grant the permission to the identity                           |
| `revoke permission <identity> <permission>`†| End a grant early                                                          |
| `status [<identity>]`                       | See the current status and info on the given identity, or on the whole fleet |
| `list identities\|roles\|permissions\|grants`†| The flat rosters: one line per thing, filtered to your own branch          |
| `budget`†                                   | What each identity may keep employed, and what it is spending              |
| `budget <role> <load>`†                     | Set the load a role may keep on the work list                              |
| `attach <identity>`                         | Attach to the Claude Code session within Orc                               |
| `poke <identity> [message]`                 | Nudge the identity to continue working                                     |
| `wake [<identity>…] [--every <dur>]`†      | Poke whatever has gone quiet; `--every` runs it as a cycle                  |
| `refresh <identity>`                        | Create a new Code session to replace the old one for the identity          |
| `move <identity> <boss>`                    | Move the identity to be under the boss; lower authority/perms as needed    |
| `model <identity> [<model>] [--effort <e>]`† | Show, or change, what an identity thinks with; `--now` replaces the running session |
| `workspace <identity> [<path>] [--adopt]`†  | Show, or change, where an identity works; `--now` replaces the running session |
| `instruct [<target>] [--set <f>|--edit|--clear]`† | The standing instructions agents run under: `system`, `role <n>`, `identity <n>`, `wake` |
| `instruct show <identity> [--diff]`†        | The composed prompt, exactly as the agent gets it                          |
| `employ <identity> [--model <m>] [--effort <e>]` | Add the identity to the work list; populate it as needed automatically |
| `fire <identity>`                           | Remove the identity from the work list; do not repopulate it               |
| `introspect [--only <field name>]`          | Shows information on the active agent in this leaf session. Can show one only one field with no formatting for remote authorization and other purposes. |
| `check-control <agent>`†                    | Exit `0` if the caller controls the agent, `8` if not                      |
| `check-permission <name>`†                  | Exit `0` if the caller holds it, `8` if not, `2` if no such permission     |
| `env <identity>`†                           | Print the export block for a manual shell; discloses a key                 |
| `tend [--watch <dur>]`†                     | Reconcile the work list: populate what is employed, depopulate what is not |
| `doctor`†                                   | Check every invariant and guard, and report which are in force             |
| `verify`†                                   | Walk the store and report damage, changing nothing                         |
| `owner`†                                    | Who the operator is, and how orc knows it is you                           |
| `owner env`†                                | The operator's export block, found without being told who they are         |
| `owner rename <name> --yes`†                | Rename the operator; it keeps its key, memories, workspace, and children   |
| `owner reset --yes [--as <name>]`†          | Destroy the fleet and bootstrap a fresh one                                |
| `help`                                      | The command list, the model, the load table, and the environment           |

Three screens, in every Orc tool. `<tool> help` is all of it. `<tool>` on its own
is the verbs and the error that nothing was named — a usage error, exit `1`, on
stderr. Any other usage error is **just the error**: an unknown verb comes with a
guess when one is unambiguous, and everything else already says what was wrong.

The full screen used to follow every usage error, which made the answer to a typo
something to find rather than something to read.

† Not part of the original spec. `check-control` is the contract Macmuffin calls
before `muff assign` (see **Control**); `bootstrap`, `revoke`, `env`, `tend`,
`doctor`, `verify`, `list`, and `budget` are additive — nothing else depends on
them, and every other command runs `tend` on its own anyway.

`list` takes the singular too (`orc list role`), and `perms`. Every roster is
filtered the way `status` is: what is not below you is not yours to read, so a role
nobody in your branch holds is a role you are not shown.

Terms:
| Term           | Means                                                   |
|----------------|---------------------------------------------------------|
| Identity       | A persistent, single agent                              |
| Role           | A job, like Engineer or Reviewer                        |
| Permission     | A named list of allowed commands; composable            |
| [De]Populate   | To [un]fill an identity with a Claude Code instance     |
| Work List      | The list of identities currently visible to others of equal or lower authority; marked if off the list, even if visible |
| Leaf Session   | A shell session for a given agent (or the user).        |

## §1.1 Flags

| Flag           | On                        | Does                                                        |
|----------------|---------------------------|-------------------------------------------------------------|
| `--as <name>`  | `bootstrap`, `owner reset` | Name the operator identity; defaults to the unix user     |
| `--until <dur>`| `grant`                   | Give the grant a wall-clock expiry instead of a session one |
| `--model <m>`  | `employ`                  | Which model the session runs; defaults to `sonnet`          |
| `--effort <e>` | `employ`                  | How hard it thinks; defaults to `medium`                    |
| `--direct`     | `attach`                  | Hand the terminal to the real Claude session, not Orc's view |
| `--watch <dur>`| `tend`                    | Keep reconciling on an interval, as a backstop              |
| `--every <dur>` | `wake`                    | Run the wake cycle on an interval instead of once            |
| `--after <dur>` | `wake`                    | How long a waiting session may stay waiting (default 10m)    |
| `--message <t>` | `wake`                    | What to say instead of `continue`                            |
| `--dry-run`     | `wake`                    | Report what would be woken, and poke nothing                 |
| `--effort <e>`  | `model`, `employ`         | The effort half of the load: low, medium, high, xhigh, max  |
| `--now`         | `model`, `workspace`      | Replace the running session so the change takes effect now  |
| `--adopt`       | `workspace`               | Work in a directory that already exists, rather than making one |
| `--only <f>`   | `introspect`              | Print one field, raw, with no formatting                    |
| `--json`       | `status`, `introspect`, `list`, `budget`, `instruct` | Print the stable JSON shape instead of the screen |
| `--yes`        | `remove`, `fire`, `owner rename`, `owner reset` | Required when stdin is not a terminal, which for an agent is always |
| `--no-color`   | any                       | Never emit colour                                           |
| `--color`      | any                       | Emit colour even when stdout is not a terminal              |
| `--width <n>`  | any                       | Render to a fixed width                                     |

`introspect --only` takes: `identity`, `role`, `authority`, `asked`,
`permissions`, `grants`, `boss`, `chain`, `subordinates`, `workspace`, `mailbox`,
`operator`, `employed`, `session`, `model`, `effort`, `load`. An unknown field is
an error naming every valid one, never an empty line.

`authority` is what the identity may actually use; `asked` is what its role
claims, which is the higher of the two when a boss caps a subordinate. `chain` is
the line of bosses up to the operator.

## §1.2 Identity

Every command but `help` and `bootstrap` authenticates first — but the operator does
not have to carry a credential to do it.

**With neither `$ORC_USER` nor `$ORC_KEY` set, orc reads the operator's own credential
out of the fleet.** The keyring is plaintext at `0600` inside a `0700` directory, so a
process that can read the directory can already read every key in it; making the owner
export one adds friction rather than security. The fallback is deliberately narrow:

- it applies only when **both** variables are absent. A half-set environment stays an
  error, so a typo in one of them never silently promotes anybody to operator;
- it yields the **operator** and nobody else — an agent always presents its own;
- it requires the store to be **private to this unix user**. A group-readable store
  refuses it and says why, because that is the condition the whole argument rests on.

`orc owner` says which of the two happened, so "why does orc believe I am the operator"
has a visible answer. `mailman` and `muff` have no keyring to read and still need the
pair exported; `orc owner env` prints it.

`cq` is the exception, because it needs to know *whose* mailbox a machine mirrors
rather than who is running it. It asks orc — `introspect --only operator`, and
`owner env` when nothing is presented — so a freshly bootstrapped machine syncs with
nothing set. It only ever agrees with orc about who the operator is, so an agent's
mail nudging a sync cannot cause the agent's own mailbox to be published; see
`Docs/Communique/Reference.md` §1.2.

| Variable        | Is                                                    |
|-----------------|-------------------------------------------------------|
| `$ORC_USER`     | the identity to act as                                |
| `$ORC_KEY`      | the key that proves it                                |
| `$ORC_HOME`     | the store root; else `$XDG_DATA_HOME/orc`, else `~/.orc` |
| `$ORC_IDENTITY` | set in a populated session: whose session this is     |
| `$ORC_SESSION`  | set in a populated session: the session's id          |

Orc sets the first two for every session it populates. That is the same
credential contract every other Orc tool reads, so an agent Orc started needs no
further setup to use `mailman`, `muff`, `anno`, or `dock`.

### Starting without a person

Every identity gets its own `CLAUDE_CONFIG_DIR`, which used to mean every new
agent's first session opened on Claude's first-run wizard — theme picker, then
the trust prompt — and hiring somebody meant attaching to click through it. Orc
now seeds `.claude.json` when it prepares a session: `hasCompletedOnboarding`,
and `hasTrustDialogAccepted` for the workspace that session will start in. It
**merges** rather than overwrites, since Claude keeps the agent's own history in
that file.

The third screen is the `bypassPermissions` acceptance warning. The compiled
settings carry `skipDangerousModePermissionPrompt`, which is the answer the
operator already gave in their own settings — Orc's file replaces those for an
agent, so without it their answer was lost rather than inherited.

`ORC_PERMISSION_MODE` still chooses the mode, but **do not set `dontAsk`**: it
auto-denies any tool no allow rule covers, and Orc's allow list names no `Bash`,
so agents would be refused every command they ran.

### Coming back from a stop

The supervisor restarts a session five times with a backoff, then gives up and
removes its state, leaving the identity employed with nothing running for `orc
tend` to pick up. Before it goes it records the ending: the session id, why it
went, how many restarts it spent, and whether it stopped **mid-turn** — while
working, rather than while waiting for somebody.

`orc tend` resumes that session rather than starting a new one, so an agent
stopped by something outside itself — a usage limit reached mid-turn, a network
that came and went — comes back to the work it was part-way through instead of to
a blank conversation. Where the session had stopped mid-call it is also told to
carry on, since the turn it was inside will never finish on its own.

`orc refresh` and `orc fire` forget the ending: both are somebody saying the
conversation is over. `orc status <identity>` shows what became of the last
session when there is nothing running.

### The toolkit

Every fleet is made with these. A fresh fleet used to have no permissions at all,
so the first thing anybody did was invent a vocabulary — and invent it differently
each time, so two fleets could not be discussed in the same words.

`orc bootstrap` installs them and is safe to run again: it creates only what is
absent and never rewrites a permission, so a fleet that redefined one keeps its
own. That is how a fleet made before one of these existed gets it — and because
an absence is otherwise invisible, `orc list permissions` marks which rows are the
toolkit's and names any that are missing, and `orc status --json` carries the whole
toolkit with a `have` flag so cq's browser can show a permission the fleet has not
got.

| Permission   | Floor | Clauses                          | Is                                          |
|--------------|-------|----------------------------------|---------------------------------------------|
| `read-all`   | 1     | `read(**)`                       | read every file in the workspace            |
| `read-docs`  | 1     | `read(Docs/**)`                  | read the specifications and nothing else    |
| `write-docs` | 20    | `read/write(Docs/**)`            | edit the specifications                     |
| `write-all`  | 70    | `read(**)` `write(**)`           | edit anything in the workspace              |
| `orc-read`   | 1     | `orc(introspect)`                | confine to reading — see below              |
| `orc-agents` | 60    | `orc(new)` `orc(move)` `orc(employ)` `orc(fire)` `orc(attach)` `orc(poke)` `orc(refresh)` | hire agents and direct them |
| `orc-policy` | 85    | `orc(assign)` `orc(grant)` `orc(revoke)` `orc(remove)` | hand out roles, permissions, authority |
| `shell-read` | 10    | `shell(ls find grep …)`          | run the commands that look without changing |
| `shell-build`| 40    | `shell(go make npm …)`           | run the toolchain: compile, format, test    |
| `shell-all`  | 75    | `shell(**)`                      | run any command at all                      |
| `upgrade`    | 90    | `tool(upgrade)`                  | rebuild and restart every tool, every machine |

They are **ordinary permissions**. Assignable, grantable, listed by
`orc list permissions`, refused below the floor, and removable if a fleet wants its
own vocabulary. Nothing in the derivation knows they exist; the only thing that
makes them builtin is that `bootstrap` creates them.

`bootstrap` is safe to run twice, and that is what tops these up on a fleet made
before one of them existed. An existing permission is never rewritten, so a fleet
that has redefined one keeps its own.

Budgets are not here: `orc budget <role> <load>` manages a `spawn-<n>` permission
per load, so there is no fixed set to provide.

**The floor is the policy.** Reading is 1 because an agent that cannot read cannot
work. Writing everything is 70 because it is most of a machine. Policy is 85
because handing out authority is how authority leaks. `upgrade` is 90 because it
replaces every binary on every machine in the fleet.

**The `orc(…)` ones narrow rather than enable.** An identity with no orc-kind
clause is governed by the structural rules alone; one with any is additionally held
to them. Only the verbs that *change* something consult that gate — `status`,
`list`, `introspect`, `verify`, `doctor`, `tend`, and `budget` never do — so every
clause above names a verb that is actually checked, and `orc help` prints the list.
`orc-read` is the odd one and
worth understanding before handing it out: its clause allows nothing anybody
lacked, and its effect is the narrowing. Holding it bars every orc verb that
changes anything.

**`upgrade` is a marker, and that is why its clause is `tool(…)`.** Containment is
by clause rather than by name — an identity that may write everything may hand on a
permission to write one directory — which is right for paths and wrong for a
capability meaning "may run this privileged action". With a path clause, anybody
holding `write-all` at floor 70 would reach a permission whose floor is 90. No path
glob covers `tool(upgrade)`.

A wide enough clause of the *same* kind still does — `tool(**)` covers every
capability, as `read(**)` covers every file — so the floor is checked as well as the
clause: an identity below a permission's floor does not hold it, however its clauses
are spelled. The floor is the one part of a permission that is not a pattern, which
makes it the one thing a pattern cannot argue its way past.

`check-permission` is how another tool asks. It answers with an exit code, the way
`check-control` does, so the tool that needs the answer never holds a copy of the
model — see `Docs/Communique/Reference.md` for how `cq upgrade` uses it.

## §1.3 Authority, permissions, and roles

See `Auth_Perm_Role.md`. In short: authority is a number on a role, the user is
100 and everyone else is 1–99, a permission has a minimum authority, and an
identity holds exactly one role.

A clause is `kind(argument)`. The kinds are `read` and `write` over path globs
where `**` crosses directories, `spawn` over a load budget, `orc` over Orc's own
verbs, `shell` over the commands an agent may run at a prompt, and `tool` over a
named capability in another Orc tool. `Auth_Perm_Role.md` names the first three;
`orc`, `shell` and `tool` are this build's reading of "any number of specific
commands or command patterns".

The argument is a list, and it may take things back out:

| Written                            | Means                                          |
|------------------------------------|------------------------------------------------|
| `read(Anno/**)`                     | one thing                                      |
| `read(Anno/** Dock/**)`             | several, space separated                       |
| `write(** except Docs/**)`          | everything but these                           |
| `orc(new assign)`                   | two verbs                                      |
| `orc(** except remove)`             | every verb but one                             |
| `shell(ls cat echo)`                | three commands                                 |
| `shell(** except rm curl)`          | every command but these                        |
| `tool(**)`                          | every named capability                         |
| `spawn(24)`                         | a budget, which is none of the above           |

Every kind but `spawn` takes both halves, and every term of every kind is a glob:
`orc(re*)` is a pattern over verbs exactly as `read(Anno/*)` is one over paths. A
budget is a number, so `spawn(24 48)` is refused rather than resolved.

### `shell` is the one that refuses by default

Every other kind narrows something an agent could otherwise do freely. `shell` is
the reverse: with no `shell` clause an identity may run only these, and nothing
else at all.

```
basename  dirname  echo  false  mailman (not mailman admin)  printf  pwd  true
```

A command earns a place there one of two ways.

Most of them **cannot do anything**: `echo` and `printf` write to a stream the
agent already owns, `pwd`, `basename` and `dirname` are string arithmetic on a
path it already knows, and `true` and `false` are control flow. None takes a
path, so none can be turned into a file read by choosing a clever argument. `ls`
is deliberately not among them — it discloses what is on a disk the agent may not
be allowed to read — and neither are `cat`, `head` or `tail`, which read files
and would be a second path around the `read(…)` clause that is supposed to decide
that.

`mailman` is there for the other reason: it **decides for itself, against the
same identity**. Every command it takes is authenticated against the caller's own
key, and shows that caller its own mailbox and no other, so a `shell(mailman)`
clause would not narrow anything that mailman has not already decided. It also
has to be free, because mail is how an agent is told what to do and how it says
it is done: a fleet where reading that took a grant is a fleet where a new
identity is deaf until somebody notices.

The exception is `mailman admin`, which is the one part that does *not*
authenticate — it has to be able to bootstrap a store that has no identities in
it yet — and which can name the owner who reads the store whole. It needs a
clause like anything else, and `shell(mailman)` covers it. In an orc fleet you
should not need it: `orc new identity` provisions the mailbox for you.

Because the default set does not depend on the store, it survives losing it. An
agent whose permissions cannot be read at all may still run these commands — and
that is deliberate, because such an agent is exactly the one that needs to report
what happened. Everything else still stops.

A clause names commands as they are typed. Matching is on the base name of what a
line actually runs, so `shell(rm)` covers `/bin/rm`, and `cd x && rm y` is a `rm`.
Every command in a line is checked, not just the first.

**A line that hides what it runs needs `shell(**)`.** Substitutions — `$(…)`,
backticks, `${…}` — and interpreters that take a program as data — `sh -c`,
`eval`, `xargs`, `python -c` — are refused by any narrower clause, because the
name in front of them says nothing about what would happen. That is a shape
match on an undecidable thing, and it is eager on purpose: a false positive costs
a rephrase, and a false negative costs the gate.

**It gates which commands run, not what they touch.** `shell(rm)` lets `rm` run;
the `write(…)` clauses still decide which files it may be pointed at, as far as
the hook can tell — which for an arbitrary command is not far. The two are
different questions and both are asked.

Three toolkit permissions cover the usual cases: `shell-read` at floor 10 for the
commands that look without touching, `shell-build` at 40 for the toolchain, and
`shell-all` at 75 for a shell with nothing taken out. A fleet that wants a
different set writes one.

An exception always wins over a term, whatever order they are written in. Terms are
sorted and de-duplicated on the way in, so two people who typed the same set in
different orders wrote the same permission and `edit permission` given what is
already there changes nothing.

Containment stays conservative, and exceptions are the same rule pointed the other
way: a clause is provably wider than another only if every one of its own
exceptions is already beyond that other's reach. `read(** except .git/**)` covers
`read(Anno/**)`, because `.git` and `Anno` diverge at their first segment and no
path is in both. `read(** except Anno/internal/**)` does not.

Quote a clause with a space in it. `orc` puts a clause the shell split back
together where it can, but an unclosed one is an error rather than a guess.

Nothing effective is stored. An identity's authority is the lower of its role's
and its boss's, and its permissions are its role's plus its grants, intersected
with its boss's — so `move` changes what a whole subtree may do without editing
anything but one line.

`status` shows both numbers whenever they differ, and says which one capped the
other.

## §1.4 Keeping a fleet moving

An agent finishes a turn and stops. Nothing is wrong with it — Claude has said its
piece and is waiting for the next thing somebody says — but in a fleet nobody is
watching, that is where work stops.

`orc wake` is what speaks. It reads each session's event feed, finds the ones that
have been **waiting** longer than `--after`, and pokes them through the same path
`poke` uses. `--every` makes it a cycle, alongside `tend --watch`: two backstops,
one that keeps sessions *running* and one that keeps them *moving*.

Two rules keep it from becoming noise:

- **Only what is waiting is woken.** A session mid-turn is silent for good reasons —
  a long build, a slow read — and a poke would queue a nudge into the middle of work
  it is already doing. The feed's last event decides, not the clock alone.
- **Each silence is woken once.** An agent that does not move after a poke is stuck
  rather than idle; the next pass says so instead of filling its context with nudges,
  and `orc doctor` is where a stuck session belongs.

A session that has never said anything at all is judged from when it started: up for
an hour with no tool call is as stopped as one that finished and waited, and the more
worrying of the two.

The cycle's memory lives in the running process, not the store — a wake is a fact
about this cycle's last pass rather than about the fleet, so a restarted cycle looks
at a quiet fleet with fresh eyes.

## §1.5 Changing what an agent runs on

`model` and `effort` are set when an identity is employed and can be changed after
with `orc model`. They are the two halves of load — a session costs its model weight
times its effort weight — so changing either is spending, and it goes through the
same budget arithmetic `employ` does. A boss who cannot afford the new load is
refused, and shown which half of the arithmetic refused it.

Changing them on an identity that is **not** employed costs nothing and does not
employ it: it is what the next `orc employ` will start it on.

A model is fixed when a Code session starts, so a **running** session keeps the one
it was launched with. `orc model` says so rather than acting: replacing the session
costs its context, which is not a decision a settings change should make on the
operator's behalf. `--now` is how to ask for it, and `orc refresh` does the same
thing by hand.

## §1.6 Load

`spawn(<n>)` is a budget in units of thinking. A session's load is its model
weight times its effort weight, and a fleet is charged for being a fleet:

| Weight  | Values                                                   |
|---------|----------------------------------------------------------|
| model   | `haiku` 1 · `sonnet` 2 · `opus` 3                        |
| effort  | `low` 1 · `medium` 2 · `high` 3 · `xhigh` 4 · `max` 6    |

```
total = ⌈ Σ session_load × (9 + count) / 10 ⌉
```

The sum and the count both run over everything the actor employs, transitively.
So `sonnet/medium` is 4, `opus/max` is 18, and four `sonnet/medium` agents cost
21 rather than 16. `employ` prints what a decision costs before it refuses one,
including the case where the count multiplier — not the new agent — is what went
over.

A budget is a `spawn(n)` clause on a permission a role holds, and an identity's is
the largest such clause its effective permissions carry — so the boss chain caps it
like everything else.

`orc budget <role> <load>` is the shorthand for setting one. It manages a
permission per load, named after it: `orc budget engineer 24` puts `engineer` on
`spawn-24`, whose only clause is `spawn(24)`. Permissions are immutable, so
*changing* a budget swaps which one the role holds rather than editing anything —
the old permission comes off first, so an interrupted change lands on "no budget"
and refuses work rather than on the old higher number. A `spawn-<n>` nothing holds
any more is harmless, and shows up in `orc list permissions` as held by nothing.

It refuses rather than guesses when a role gets a spawn clause some other way: the
derivation takes the largest, so adding a second would not decide the answer.
Setting a budget is the operator's alone — it is authority over machine time, and
an agent that could raise its own would have no budget at all.

## §1.7 Sessions

A populated identity is a `claude` process Orc owns, in a pty, whose session id
Orc minted.

| Command   | Does                                                                       |
|-----------|----------------------------------------------------------------------------|
| `attach`  | Orc's own live view of the session: what it read, wrote, and ran, and whether it is waiting. Typing composes; `^S` sends; `^\ d` detaches; `^]` switches to `--direct` |
| `poke`    | Send a message into the session without attaching. Default message: `continue` |
| `refresh` | Stop the session and start a new one — new session id, fresh context, same identity, same memories |

A session that dies on its own is restarted with the same session id, so the
conversation continues; `refresh` is the only thing that starts a new one. An
identity's memories, mailbox, tasks, and workspace live in the identity, which is
what lets several Code sessions fill the role of one persistent agent.

Sessions run with permission prompting off. What actually limits them is the
identity's permissions, enforced by a Claude hook on every tool call, and the
`Agent` tool is off for every identity — all parallelism goes through `employ`,
so the work list is the whole picture of what is running.

## §1.8 Platforms

Orc runs on macOS, Linux, and Windows 11 — every part of it on the first two,
and everything but session supervision on the third.

What Windows has is the fleet: the store, identities, roles, permissions,
authority, the work list, `orc status --json`, and every verb that changes any of
them. That is the whole of what Communiqué drives, so a fleet can be read and
managed from a phone with the agent machine running Windows.

What it does not have is `orc employ`. A session is a `claude` process in a pty,
and Windows gives a child its pseudoconsole only at creation, through a
process-thread attribute the standard library's `os/exec` cannot set. Reaching it
means Orc calling `CreateProcessW` itself and rebuilding process startup around
it. Until then `orc employ` refuses on Windows, and says so in those terms rather
than blaming the platform — run the session on a unix machine and reach it from
anywhere with `orc attach`.

Two smaller differences follow from the same place. A console there has no
window-change signal, so a terminal resized while attached keeps the size it was
attached at until the operator detaches and returns; and stopping a detached
process is abrupt, because Windows has no polite equivalent for one that shares
no console.

## §1.9 Control

`check-control <agent>` exits `0` if the caller is above the agent in the tree
and `8` if not. It is what `muff assign` calls, so Macmuffin holds no opinion
about authority and Orc holds no opinion about tasks.

Acting on your own subagents — `move`, `fire`, `employ`, `poke`, `refresh`,
`attach` — needs no permission, only ancestry. Adding load to the work list needs
`spawn`.

## §1.10 Exit codes

Shared with every Orc tool, so a script or hook branches on them uniformly:

`0` ok · `1` usage · `2` not found · `3` ambiguous · `4` parse · `5` i/o ·
`6` conflict · `7` auth · `8` denied · `9` out of scope · `10` unavailable ·
`11` escape · `70` internal.

`8` is a permission or authority refusal. `6` is a conditional write that lost —
removing a populated identity, or two `employ`s racing for the same budget. `10`
is a session socket that cannot be reached. `11` is a path resolving outside the
root it was measured against, or a session reaching for the keyring. An identity
outside the caller's subtree is `2`, not `8`: saying "you may not" would confirm
it exists.

## §1.11 Colour

Catppuccin, Macchiato by default, shared with every Orc tool.
`ORC_THEME=macchiato|mocha|frappe|latte|none`; `NO_COLOR` disables it, and
`ORC_AGENT` forces plain output for agents — which Orc sets in every session it
populates.

Colour is a layer and never information: every colour is redundant with a glyph
or a word, so a pipe through `grep` loses nothing.

The project is stored at `Orc/Orc/go.mod`.
