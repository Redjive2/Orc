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
| `activity [<identity>] [--since <dur>]`†     | What each agent is doing, and what it has cost and touched                 |
| `pace [wake\|tend] [<who>] [--after\|--every\|--watch]`† | How often the fleet is woken and tended, per agent, role, or fleet |
| `tariff [<setting> <n>] [--calibrate] [--clear]`† | What thinking costs: the model and effort weights, and the crowd multiplier |
| `list identities\|roles\|permissions\|grants`†| The flat rosters: one line per thing; identities is the fleet, the rest your branch |
| `budget`†                                   | What each identity may keep employed, and what it is spending              |
| `budget <role> <load>`†                     | Set the load a role may keep on the work list                              |
| `attach <identity>`                         | Join a session: the composed pane. Type, `^S` sends, `^Q` leaves, `^]` hands over the terminal |
| `poke <identity> [message]`                 | Nudge the identity to continue working                                     |
| `prose <path…>`                             | Measure writing against the house rule; exit 6 when it disagrees           |
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

`list` takes the singular too (`orc list role`), and `perms`.

**`identities` is the whole fleet. The other three are your branch.**

That split replaces one rule for all four — filtered the way `status` is, what is
not below you is not yours to read — and it was overturned on purpose. The reason
the roster exists is a name in a task or a mailbox that nobody recognises, and such
a name is almost never *below* the reader: an agent looks up and sideways far more
often than down. So the scope that made the answer safe also made it useless. An
agent with nobody under it saw one row, itself.

It discloses a name, a role, a boss and a date. Mail and tasks already carry those
names to everybody who works with them, and none of the columns is a capability.

What an identity may **do** is still the branch rule: a role nobody in your branch
holds is a role you are not shown, a permission likewise, and `grants` is the
caller's branch. `orc status` is unchanged — it draws the tree you command, which
is a question about authority rather than about who is there.

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
| `--direct`     | `attach`                  | The raw terminal instead of the pane; `Ctrl-\` then `d` leaves |
| `--view`       | `attach`                  | Accepted, and means the default — the composed pane          |
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

### What an agent may run without a clause

`shell` is deny by default, so the default set is the whole of what an
unprivileged agent can do at a prompt. It is the Orc toolkit plus a handful of
commands that cannot do anything:

```
anno  basename  dirname  dock  echo  false  mailman  muff  orc  printf  pwd  true
```

Each tool earns its place one of two ways. `orc`, `muff` and `mailman`
**authenticate every command against the caller's own key** and then apply their
own rules — `orc` refuses a verb the caller may not run, `muff` refuses a task it
does not own — so a `shell(orc)` clause would narrow nothing they have not
already decided. Without them a new agent cannot run `orc introspect`, which is
the command that tells it what it may do.

`anno` and `dock` earn it differently: they **name the file they are about**, so
the hook checks it against the identity's read and write clauses exactly as it
would a Read or an Edit. That is what keeps them off the same objection that
keeps `cat` out — a reader no clause governed would be a second path to what
`read(...)` decides.

The parts that do not check their caller still need a clause: `mailman admin`,
which bootstraps a store and can hand over the whole fleet's mail; `orc
bootstrap`, which runs before there is an identity; and `orc env`, which prints
a key.

### What an agent keeps

Two things belong to the identity rather than to the fleet, and neither needs a
clause:

| Path                    | Is                                              |
|-------------------------|-------------------------------------------------|
| `<claude>/CLAUDE.md`    | its standing instructions; Orc writes this once at creation and never again |
| `<claude>/memory/`      | anything it wants to outlive a session          |

Both sit in the identity's Claude configuration, which is **beside** its workspace
rather than inside it. That matters, because every `read` and `write` clause is
workspace-relative: no permission that could be written or granted would reach
them, so they are decided before the clauses are consulted at all rather than by
them.

Everything else in that directory still stops, `settings.json` most of all — it
carries the hook's own wiring, and an agent that could edit it could switch off
the thing refusing everything else. So does another identity's memory, and so does
a lookalike like `memory-notes.md`.

One limit: the directory is found by asking the store where it is, so when the
store cannot be opened at all — the third rung — memory goes with it and a write
there is refused like any other. A store that cannot be opened is also one whose
memory directory is not there to write to.

### Authenticating an unattended session

A session with no credential does not fail — it opens a **login prompt**, and a
login prompt on a pty nobody is attached to is an agent that sits there for ever,
employed and running and doing nothing.

Sessions inherit the real `HOME`, so a subscription login in the keychain
reaches them. For a fleet that should not depend on one, `claude setup-token`
mints a long-lived token for exactly this case, and `$CLAUDE_CODE_OAUTH_TOKEN`
now reaches every session — along with `$ANTHROPIC_AUTH_TOKEN`, the Bedrock,
Vertex and Foundry switches, and their region settings.

`orc doctor` reports which credential a session would use, in Claude's own
precedence order, and says what to run when it cannot tell.

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
| `shell-interpret` | 70 | `shell(sh bash python3 …)`      | run interpreters — a shell by a longer route |
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

**An agent's workspace is its own.** `read` and `write` clauses do not narrow the
inside of it. That used to be the rule, and it was the wrong boundary in the way
that costs most: an agent is given a directory to work in, told to work, and then
refused the ordinary acts of working in it — a scratch file, a build output, a new
package, a `go.mod`. Every one of them needed a clause somebody had to have
thought of in advance, and the failure arrived as a refusal mid-task rather than
as anything an operator could see coming. An identity with no path clause at all
could not write a single file anywhere.

What partitions agents is the thing that actually partitions them: **give them
different workspaces**, with `orc workspace <identity> <path>`. Two agents in two
directories cannot reach each other's work whatever either of them holds. Two
agents in one directory could always have reached each other's work in a dozen
ways a path glob does not see — a shell, a symlink, a build that writes where it
likes — so the clause was buying tidiness rather than the isolation it resembled.

**A path clause is measured from the project.** Outside its workspace, an agent
may reach what a clause names and nothing else — and the clause is resolved
against the repository the workspace sits in, found by walking up to the nearest
`.git`. So `read(Docs/**)` is the repository's `Docs`, which is the same directory
whichever agent reads it and whatever its own workspace is called. One permission
means one thing across a fleet, which a workspace-relative clause never managed.

A workspace in no repository is its own project. That is the narrow direction on
purpose: rooting a clause at a parent nobody chose would widen what a permission
reaches by accident.

The two refusals outside a workspace are different, and say so. A path **inside**
the project that no clause covers names the clause that would cover it, because
that is a permission away. A path **outside** the project says no clause reaches
there, because asking for one would not help.

Everything else is unchanged and still refused: the fleet's own store, the shell
(shut by default), and subagents. None of them consults a path clause.

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
where it may be pointed is still checked, as far as the hook can tell — which for
an arbitrary command is not far. The boundary there is the workspace, not a path
clause: see *An agent's workspace is its own* below.

**`anno` and `dock` are read by a table, not by a shape.** Every form of both
tools that names a path is listed, with which operand carries it and whether it
reads or writes; a verb not in the table names no path and falls through with
every other unrecognised command. It used to be a shape — "`dock <path>`, and
`dock read <path>`" — which took the second word as a path whenever it was not
the word `read`. `dock index Docs/Vision.md` therefore checked a file called
`index`, found it inside the workspace, and never looked at the document: an
agent holding read on nothing could map a tree with `index`, `overview` and
`check`, and `dock write` was an unguarded write. A section address (`§`, `#`,
`@`, `^`) is cut before the path is resolved.

**An interpreter runs when a clause names it.** `python3 -c …` and `sh -c …` take
a program as data, so the name says nothing about what will *happen* — but it says
exactly what will *run*, and that is the question a clause answers. So `shell(sh)`
permits `sh -c`. What it grants is everything that interpreter can do, which for a
shell or for python is a shell; the toolkit prices it at 70, beside `shell-all`
rather than beside the compilers.

That is a change. Interpreters used to be refused as unreadable along with
substitutions, which made `shell(python3)` a clause nobody could satisfy — and
`shell-build` named python, python3, sh and bash, every one of which was refused.
Those four have moved out of `shell-build` and into `shell-interpret`, so a
permission no longer names commands it cannot grant.

Four toolkit permissions cover the usual cases: `shell-read` at floor 10 for the
commands that look without touching, `shell-build` at 40 for the toolchain,
`shell-interpret` at 70 for the interpreters, and
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

### The backstops do not stop

Nothing restarts these cycles. There is no service, no supervisor above them, and
`orc doctor` reports a missing cycle to whoever runs `orc doctor`. So both loops
used to count failures and return an error at five, which turned a bad half hour —
a full disk, a sleeping machine, an upgrade replacing binaries — into a fleet with
no backstop at all. Five passes is also thin evidence: at a one-minute cycle it is
five minutes.

A pass may now fail as often as it likes and the loop keeps its interval. Only how
often the failure is **said** changes: the first three in full, then one in ten,
which leaves a trail without scrolling a terminal. Recovery is always said. A
panic in a pass is caught, reported as the defect it is, and the cycle carries on
— a nil map somewhere below must not take a whole fleet down with it.

### Every start is told to begin — including a resumed one

A Claude session with no user turn does nothing. It holds the fleet's, the role's,
and the identity's instructions in its system prompt and has no occasion to act on
any of them.

Which message a new session gets used to depend on **who asked** for it: `employ`
said "opening" and a backstop said nothing. So a session `tend` rebuilt was never
spoken to, and it sat at its prompt until the wake cycle noticed it a whole
interval later. `orc refresh` left one that looked started and never moved.

Every start speaks, and what differs is only the wording.

The resumed path used to speak only where the last turn had been *interrupted*,
on the reasoning that a session which finished its turn is waiting and waking it
is the wake cycle's job. That reasoning missed what a resume is. `--resume`
restores the conversation and leaves the agent at its prompt: it has history and
no occasion to act on it, exactly like a new session. So an agent that came back
from a clean stop sat there until the quiet threshold elapsed.

This is the case a fleet lives in. `recordEnding` writes an Ended record whenever
a supervisor exits, and only a refresh or a fire forgets it — so after the first
`orc employ`, nearly every start is a resume: a crash, a reboot, a machine that
slept, an upgrade. The opening message went out once, on the first employ, and
after that the only prompts that ever arrived were the wake cycle's.

A start that follows an interrupted turn still says so, because the two are
different situations and the wording is how somebody reading a log tells them
apart.

### Delivery is confirmed, not assumed

Writing into a pty is not delivery. A write to the master succeeds whether or not
anything on the other end is listening, so every path that speaks to an agent —
`poke`, `wake`, the opening message a start sends, and `^S` from an attached
session — was writing and hoping. Measured against the real Claude binary, a
message written while it is starting is dropped: sometimes the whole thing, and
sometimes only the return that submits it, which leaves the text sitting in the
input box unsent. The binary has finished painting its interface by about a
quarter of a second and is *still* losing input a second later, so there is no
moment to wait for and no output that says "ready".

So Orc asks. Claude's `UserPromptSubmit` hook — already installed, already
recorded in the event feed — fires once per submitted prompt and nothing else
appends one. A poke notes the count, types, and waits for it to move.

When it does not, two things are tried, and the order is the safety of it:

1. **A bare return.** If the text is loaded and unsent this submits it. It carries
   no content, so it cannot duplicate anything, and it fixes the more common
   failure outright.
2. **The message again.** Only reached when a bare return changed nothing, which
   means the box was empty and the text never landed.

The other order would deliver the message twice every time the first attempt was
merely unsent, and an agent acting twice on one instruction is a worse outcome
than one that missed it. Past both, the poke is **refused** with the reason, so a
caller reports a fleet that cannot be spoken to rather than logging a success.

A session that has **not reported yet** is waited for, up to five seconds, before
any of that is decided. This is why `orc poke` worked while the opening message
did not, and the two differ only in when they run. A person pokes a session that
has been up for a while, so its feed has events, so delivery was confirmed and
the ladder rescued whatever the terminal dropped. The opening message goes the
moment `orc employ` finishes waiting — and what it waits for is the supervisor's
*state file*, written before Claude has done anything — so the feed was empty,
confirmation switched itself off, and the message went into a terminal that drops
input for its first second with nothing left to notice.

The first event is also the readiness signal there was no other way to get: the
hook fired, so Claude is far enough along to have run one. The wait happens once
per start. A fleet whose hooks are not installed reports nothing ever, and paying
it on every message would put half a minute on a wake cycle's first pass over
seven agents.

Two cases are not retried. A session **mid-turn** has its prompt queued by Claude
and submitted when the turn ends, so the hook may be minutes away and the message
is exactly where it belongs. And a session that has **never written an event** —
a fleet whose hooks are not installed — cannot report, so absence of a submission
means nothing there; those are written to once and believed, as before.

### Usage limits

An agent that hits its usage limit does not fail, does not stop, and says nothing
Orc can see from the outside. The child process is alive, the socket answers, and
`orc status` shows a filled circle — and the agent will never do anything again
until somebody speaks to it.

It is invisible to the two rules above, and that is the point worth understanding.
The limit lands wherever the turn happened to be, which is almost always straight
after a tool call, so the feed's last event is a `PostToolUse` — and a feed ending
mid-tool is exactly what *working* looks like. The one backstop built to notice a
fleet that has quietly stopped skipped these on every pass. In one real fleet, seven
agents stopped at 03:10 and were still stopped twelve hours later, nine of those
after the limit had already lifted.

So the fact is read where it exists: Claude's own transcript, which carries one line
flagged as an API error saying what happened and when it resets. `orc status` shows
`✗ limit · 06:10` instead of a healthy circle, and `orc wake` treats it as its own
state:

- **Before the reset**, nothing is poked. A poke spends the agent's next turn on a
  second refusal, and — worse — records a wake, which is how the cycle decides it has
  already tried. The pass says how long is left instead. These are counted apart from
  every other outcome: an agent at a limit is a fleet working normally against a
  clock, and reading it beside "still silent" would send somebody looking for a fault
  that is not there.
- **After the reset**, it is woken — whatever the wake mark says. The mark records
  that a silence has been nudged once already, which is the right rule for an agent
  that will not move and the wrong one here: the reason it did not move was the
  limit, and the limit is over.
- **When the message does not name a reset time**, it falls back to the ordinary
  cadence, measured from when the limit was hit. A poke to a still-limited session
  costs one refusal line; never poking costs the fleet.

`orc doctor` has two things to say about all this, and they are deliberately not
the same kind of thing.

**`wake cycle`** is a guard, and it counts toward the exit code. Every other guard
answers "is the wall holding"; this one answers "is anybody watching". A fleet with
no cycle does not recover from anything — an agent that finishes a turn waits, an
agent that hits a limit waits, and nobody ever speaks to either. The answer comes
from the watcher registry, so it is about *this* fleet and the process it names is
known to be alive; looking for `orc wake --every` in `ps` would count a cycle
watching somebody else's fleet as cover for this one.

**The `sessions` section** is not a guard and never sets the exit code. A session at
a usage limit is a fleet working normally against a clock, and failing a cron every
time an agent hits one is an alarm nobody reads. What each line says depends on what
can be done about it: still limited says how long is left; lifted with nothing
watching says to run a cycle; lifted with a cycle running and half an hour gone says
the cycle is not doing its job and to check it is the current build — which is the
one case that looks fine on every other screen.

A limit is only current if it is the *last* thing in the transcript. Anything the
agent or the operator said afterwards means the session moved on, and a limit that
has been moved on from is history — otherwise an agent that recovered would be
reported as stopped for the rest of its life.

The cycle's memory lives in the running process, not the store — a wake is a fact
about this cycle's last pass rather than about the fleet, so a restarted cycle looks
at a quiet fleet with fresh eyes.

An agent with **no session at all** is reported rather than passed over: employed,
costing budget on the worklist, and running nothing is a louder kind of stopped than
silence, and a cycle that said "all working" over it would be answering the wrong
question. `orc tend` is what starts it, and `orc wake --tend` makes the cycle do it
— for the machine where a cron entry running `wake` is the only thing there is.

### The first thing a session is told

A Claude session that nobody has spoken to does nothing. `employ` and `refresh` pass
the composed standing instructions as `--append-system-prompt`, and a session with no
user turn has no occasion to act on any of them — it sits at its prompt until the
wake cycle calls that silence, which is however long `--after` is.

So both verbs speak to the session they start, and what they say is the **wake
message**: the identity's, else its role's, else the fleet's, else `continue`. The
same override chain deliberately — a fleet that has written what to tell an idle
agent has already written this, and a second setting to keep in step with the first
would be a second thing to get wrong.

A message that could not be delivered is reported and does not fail the command. The
session is up, which is what was asked for, and an agent nobody has spoken to has
said nothing — which is exactly what the wake cycle looks for.

`tend` is unchanged: it resumes a conversation that was already going, and speaks
only to a session whose predecessor stopped part-way through a turn. There is
nothing to begin.

### What it has done

`orc activity` reports the window rather than the moment: turns, tokens, and the
files and lines an agent read and wrote. It reads before it reports, so the figures
are current rather than as stale as the last thing that ran `tend`.

**Two sources, two guarantees.** Files are counted from Orc's own event feed, which
has a line per tool call whatever Claude's file format does next. Lines come from
Claude's transcript, which Orc reads the way `internal/view` reads it: unknown
fields ignored, everything degrades, nothing fails. So a file count is always right
and a line count is missing where the transcript could not be read.

**Tokens are four numbers, not one.** On a real session, 3,614 input against
892,563,160 cache reads. `new` — input + output + cache writes — is what the turns
caused to be produced; `cached` is what was read back. A single column called
`tokens` would only ever be showing the second.

Reading is incremental. A cursor records where the last pass stopped, so a session
that is megabytes costs one read rather than one per command, and a transcript that
has shrunk is a rotation: the reader starts again and says so, because an hour
counted twice is visible and an hour lost is not. Totals live in
`identities/<name>/activity.jsonl`, one line per read, folded by the **minute** they
fall in; each line is a delta, so a bucket's total only ever grows.

The minute is the floor on every question asked of the measurement, and nothing above
it can recover detail the reading never took — "is it working right now" and "did that
change help" are questions about the last few minutes, and an hourly reading answers
both with one bar. The cost is bounded at both ends: nothing is written for a minute in
which an agent did nothing, and everything older than twelve hours is folded to the
hour by whoever stores it. Folding is the same addition that merges two readings, so a
chart drawn every five minutes and one drawn every hour are two views of one number.

`tend` advances the rollup on every pass, which is what makes the measurement
continuous without a daemon.

### What thinking costs

A session costs `model × effort`, and a set of *n* costs
⌈sum × (crowd-base + n) / crowd-scale⌉ — so the tenth agent costs more than the
first and a fleet is charged for being a fleet. Those weights are a judgement about
money: opus costing three haikus is a claim no code can settle, and two fleets can
disagree without either being wrong.

They were constants. `orc tariff` stores them, journaled like a permission, so "what
did it used to be, and who changed it" has an answer. A fleet that has never set one
has no record at all and pays the built-in prices; the absence is the answer, and
there is no migration.

Every budget is derived, so a change is felt by everything at once: raising `opus`
re-prices every running opus session, and an actor inside its budget can be over it
without anybody touching that actor. `orc tariff` names who that would be and asks
for `--yes` when the list is not empty. It refuses nothing — a fleet over its own
budget is information, and a tariff that could only be loosened while agents ran
would be one nobody could tighten.

Everything that computes a load is handed the tariff rather than reading one. That
is wider than a global would have been and is the reason for it: a load computed
against whichever price list happened to be loaded is a load nobody can reproduce,
and two processes disagreeing about what a session costs is how a budget stops
meaning anything.

`--calibrate` proposes weights from what the fleet actually spent over the last
week, counting **new tokens** only — a tariff that counted cache reads would be
pricing context rather than work. It proposes and never applies: the numbers are one
fleet over one window, and deciding from them is the judgement this exists to leave
to a person. A combination with no observations proposes nothing rather than a
number from none.

### How often, and where that is kept

`orc wake --after`, `orc wake --every` and `orc tend --watch` are flags, read once
when a process starts — so nothing but whoever started that process could change
them, and a browser could not offer them at all. `orc pace` stores them instead.

The layering is the wake message's: **the identity's, else its role's, else the
fleet's, else the built-in.** `orc pace` with no arguments shows what every agent
will actually do and marks where each value came from.

Every cycle re-reads at the top of a pass, so a change lands on the next one with
nothing to restart and no signal to send. Two rules about flags, and they differ on
purpose:

- **`--after` typed on the line wins for that run.** Somebody debugging with
  `--after 1m` is deciding about the run in front of them, and a stored value
  overriding it would be the tool arguing with the operator.
- **A stored interval wins over the flag a loop was started with.** A cycle running
  since Tuesday, in a shell nobody has open, is exactly what a stored setting has to
  be able to reach; there is no other way to tell it. The loop says when its pace
  changes rather than changing it in silence.

`--off` is a state and not a zero: an agent nobody is waking must look different
from one being woken and not answering. `--on` turns back on what a layer above
turned off — which is why the switch is a word rather than a boolean, since "not
set" and "set to on" are different things. Anything but a plain `yes` leaves a cycle
running: a fleet that quietly stopped because a file said `off ` with a trailing
space is a fleet nobody is watching.

Sync is not here. `cq sync --watch` belongs to the mirror between two machines, and
its setting lives in cq.

### Reaching a session that is not ready

`poke`, `wake`, and the nudge `tend` sends all end at a session's socket, and that
socket is legitimately missing for short stretches: a session that has just started
has its state file before it has a listener, and a supervisor restarting a crashed
child has no pty to type into until the child is back. Both are a fleet working
correctly, and one attempt against them is a coin toss.

So a delivery is retried — six attempts over about nine seconds, which outlasts a
supervisor's first restart backoff — and only for refusals that mean *not yet*. A
message that cannot be typed, an identity nobody controls, and a session that has
been stopped are answered once. The distinction travels over the socket: a
supervisor marks a refusal it expects to outgrow, so a client can tell the two apart
rather than guessing from the text.

Where the fleet says an agent should be running and it is not, `poke` and `wake`
start it and then deliver, instead of refusing with advice to run `tend` first. An
identity that is **not employed** is still refused: starting a session nobody
employed spends budget on a decision the caller did not make.

A start that keeps failing is paced. The first retry is immediate — most failures
are a moment of a busy machine — and after that the wait widens to a cap of fifteen
minutes, so an agent that cannot start does not fork a doomed supervisor every time
somebody types `orc status`. It never stops trying, because a fleet that gave up
needs a person to notice, and `orc employ` clears the pacing: an explicit ask always
gets an attempt.

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


## §9 How to write

Every agent is given one instruction nobody sets and nobody can turn off: write in
Simplified Technical English (ASD-STE100), at 90% or better, and never use six
spellings — `honest`, `honestly`, `caveat`, `genuine`, `genuinely`,
`load-bearing`.

It is not a layer. A fleet where half the agents write one way produces documents
that read as though several people wrote them, which is the thing the rule stops.
`instruct.House` holds the text and `instruct.Compose` puts it above the fleet's
own layer, so a fleet that has set no instructions still has this one.

`orc prose <path…>` measures a file, a directory, or standard input against it.
Every agent may run it: it reads and prints and changes nothing, and the agent
whose writing is judged is the one that should see the judgement first. A
directory is walked for `.md`, `.txt`, and `.markdown` and nothing else — an agent
that had to rewrite every comment in a package to land a change would stop running
it. Exit 6 means the writing and the rule disagree.

**What the score measures.** Sentence length, passive voice, stacked subordinate
clauses, and paragraph length. These need no dictionary and no parser, and they carry most of
what STE is for. The score does **not** cover the approved vocabulary, which is the
other half of the standard, nor noun clusters, which need to know which words are
nouns. A score is a measure of the checkable rules and not a certificate of
conformance.

**The paragraph rule is the counterweight.** Six sentences, as STE100 gives for
descriptive writing. Every other rule here makes sentences shorter, and a writer
who follows only those produces a wall of short sentences with nothing to break
it up. That reads worse than the long sentences the other rules prevent.

A paragraph ends at a blank line, at any markdown structure, and at a list item.
So a list of twelve points is twelve paragraphs and passes. The rule asks for the
break, not for fewer words. Each sentence past the sixth counts against the score
on its own, so a paragraph of thirty costs more than a paragraph of seven.

Inline code spans are quoted rather than written, so they are not measured. A
document that explains the rule has to show what breaks it, and scoring those
examples would make the clearest way to state a rule the way that fails it. The
rule's own text passes its own check, and a test holds it to that.

The two halves are enforced differently. A banned word is exact: one occurrence
fails the text, whatever the score. The style rules are a proportion, because prose
has sentences that need the length, and a rule that failed every one of those is a
rule people write around. At nine in ten there is room for one such sentence in a
paragraph of ten, and no room for a second.
