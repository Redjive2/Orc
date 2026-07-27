# §1 Macmuffin — CLI

Macmuffin exposes the same CLI structure as every other Orc sub-app:

```
muff <command> <args...>
```

| Command                                 | Does                                                      |
|-----------------------------------------|-----------------------------------------------------------|
| `create <task> <priority> <difficulty>` | Create a new task                                         |
| `create <task> --sub <subtask>`         | Create a new subtask of `<task>`                          |
| `pool`                                  | See the high-level status of all tasks                    |
| `info <task>`                           | See full status, scope, and info on this task             |
| `status <task> <status>`                | Say how the work is going                                 |
| `assign <agent> <task>`                 | Assign `<task>` to `<agent>`, given you control `<agent>` |
| `claim <task>`                          | Take `<task>` as your own                                 |
| `invite <agent> <task>`                 | Add `<agent>` as a collaborator                           |
| `leave <task>`                          | No longer be a collaborator on `<task>`                   |
| `kick <agent> <task>`                   | Remove `<agent>` as a collaborator on `<task>`            |
| `complete <task> [--sub <subtask>]`     | Mark the task, or one subtask, completed                  |
| `delete <task> [--sub <subtask>]`       | Remove the task, or one subtask                           |
| `scope <task> <paths...>`               | Limit editing to `<paths...>`                             |
| `push <task>`                           | Push `<task>` to the task pool                            |
| `worktree <task> <worktree>`            | Match this task to a git worktree in the main git repo    |
| `describe <task> [--set <f>\|--edit\|--clear]` | What the work is, in markdown             |
| `rebind [--dry-run] <old> <new>`        | Follow every binding at or under `<old>` to `<new>`       |
| `check-scope <paths...>`†               | Exit `0` if every path is in scope, `9` if any is not     |
| `verify`†                               | Walk the store and report damage, changing nothing        |
| `help`                                  | The command list, scores, status values, and environment  |

† Not part of this spec. `check-scope` is the contract Anno calls before it
writes (see **Scope**); `verify` is how a store several unsupervised agents
write to can be checked without reading the source. Both are additive: nothing
else depends on them.

A new task cannot be edited or completed — only claimed or deleted — until a
scope is added.

`pool` reports, per task: `n/m` subtasks complete, owner, collaborators, status.

## §1.1 Flags

| Flag       | On                  | Does                                                     |
|------------|---------------------|----------------------------------------------------------|
| `--all`    | `pool`              | Include completed tasks; without it the board is active work only |
| `--sub`    | `create`            | Make a subtask of the named task                          |
| `--yes`    | `delete`            | Required when stdin is not a terminal, which for an agent is always |
| `--force`  | `complete`          | Complete a task with unfinished subtasks anyway           |
| `--json`   | `pool`, `info`      | Print the stable JSON shape instead of the board          |
| `--no-color` | any               | Never emit colour                                         |
| `--color`  | any                 | Emit colour even when stdout is not a terminal            |

The two colour flags are global: they may appear before or after the command,
and no command sees them as arguments.

## §1.2 Identity

Every command but `help` authenticates first, so an agent with no identity is
told that rather than told its arguments are malformed.

| Variable    | Is                        |
|-------------|---------------------------|
| `$ORC_USER` | the agent to act as       |
| `$ORC_KEY`  | the key that proves it    |

Orc normally provides both. There is no credential file and no `--user` flag:
one way in, so there is one thing to get right.

**The claim is checked, where there is anything to check it against.** Macmuffin
keeps no user records — Orc mints one key per identity and provisions Mailman
with the same one — so it asks: `orc introspect --only identity` prints the
identity a credential really belongs to, and exits `7` when the key proves
nothing. A definite no refuses the command with exit `7`, before any argument is
examined.

Where no `orc` is installed the claim stands and nothing is checked. `muff`
predates Orc and works beside it: a machine with no fleet is not a machine with
a liar on it, and refusing every command there would make the tool unusable.
`muff verify` says when nobody confirmed you, because every permission below
rests on that claim.

The limit is worth stating plainly: an agent that controls its own `PATH` can
hide `orc` as easily as it can lie about its name, so this catches a *mistaken*
identity — a stale key, a typo, a credential copied from another agent — and not
a determined one. Stopping that needs an authority Macmuffin cannot be denied,
which today means Orc being the thing that starts the session.

**The fleet's operator stands in for the owner a task does not have.** `scope`,
`complete`, `invite`, `describe` and `delete` are the owner's, so on a task in the
pool they refuse and say "claim it first" — the right answer to an agent, because
taking the work is how it acquires the say over it, and the wrong one to whoever
runs the fleet. Retiring a stale task, fixing a bad scope, or handing one on are
things the operator does without wanting the work.

So an identity Orc names as the operator is treated as the owner of any task
**nobody owns**, and of nothing else. Two limits, and both are the point:

- A task with an owner stays that owner's. This is not a master key; an operator
  who wants the work claims it in the open, like anybody.
- A draft stays private (§1.3). An unowned draft somebody else made is still
  reported as not found.

`muff` says so when it happens — "nobody owns parser; acting as the operator" —
because a change made with nobody on the task is otherwise a change the next
reader cannot account for.

The question goes to `orc introspect --only operator` and **fails closed**, which
is the opposite of the identity check above and deliberate: verification only ever
refuses, so a missing Orc leaves a claim standing, and this only ever widens, so a
missing Orc must leave things exactly as narrow as they were. On a machine with no
fleet nobody is the operator. It is asked only when the answer would change
something — after the table has refused — so ordinary work runs no `orc` for it.

## §1.3 Privacy

A task is a **private draft** until it is pushed. A draft is visible to its
author and to anyone the author deliberately added to it, and to nobody else —
a task you cannot see is reported as not found rather than as forbidden, since
saying "you may not" would confirm it exists. The fleet's operator is not an
exception: §1.2.

## §1.4 Scores

Both are set at creation and run 1 to 5.

| Score        | 1    | 5    |
|--------------|------|------|
| `priority`   | low  | high |
| `difficulty` | easy | hard |

## §1.5 Status

| Value | Means                 |
|-------|-----------------------|
| `1`   | not working           |
| `2`   | slow / problematic    |
| `3`   | nominal               |
| `4`   | done / basically done |

## §1.6 Scope

`scope` enforces editing (even via Anno) only on files in scope, via a Claude
hook. Enforcement has two halves, because one of them cannot cover the other:

- **`muff-hook`** runs on `PreToolUse` and refuses an `Edit`, `Write`,
  `NotebookEdit`, or `MultiEdit` outside the scope of the task in force. It
  blocks *before* the write, never after. See `Claude/Docs/Macmuffin/Hooks.md`
  for the `settings.json` wiring.
- **`muff check-scope`** is what Anno calls before `anno write` touches a file.
  A shell command's writes are undecidable from a hook, but on Anno's side the
  question is decidable — Anno knows exactly which file it is about to change.

Which task is in force is worked out, not declared per call: `$MUFF_TASK` if
set, else the worktree binding for the session's directory, else **no task, and
nothing is enforced**. An agent that never opted in is never blocked, and
neither is a task with no scope.

Entries are matched as an exact file, a directory prefix, or a glob. A path
resolving outside the worktree is an escape (exit `11`) rather than an ordinary
scope refusal (exit `9`): the first is a containment failure, the second is
routine.

Writes through `Bash` other than `anno write` are out of reach. That is stated
rather than implied to be covered.

## §1.7 Descriptions

A task's description is what the work actually *is*. Everything else a task
carries is a fact with a shape — a score, an owner, a set of paths, a list of
steps — and none of them says what to do.

It is markdown, in `description.md` inside the task's own directory, so it can be
edited in an editor and read in a browser, and so deleting the task takes it with
it. At most 32 KiB, refused rather than truncated: half a specification looks
like a whole one. Control characters are refused too, since it is printed to a
terminal by `describe` and named by `info`.

```
muff describe <task>              print it, and nothing else, so it redirects
muff describe <task> --set <file> replace it, or `-` for standard input
muff describe <task> --edit       $EDITOR on the real file
muff describe <task> --clear      remove it
```

Writing is the owner's, and the author's while the task is a draft — the same
rule as `scope`, because both say what the task is rather than how it is going.
Reading is anyone who can see the task.

The task's journal records `describe` and `describe.clear`: **that** it changed
and who changed it, never the text. A record folded on every command must not
carry 32 KiB of prose. So `info` says who last described a task, and `pool` says
which tasks have one at all.

## §1.8 Notifications

`invite` and `kick` tell the agent by Mailman, addressed to them and to the
caller. The membership change is the fact and the mail is only the
announcement, so a Mailman that is missing or broken **delays a notification
rather than losing one**: the notice is queued, retried by whichever agent next
touches the store, and never fails a change that already happened. A notice that
has given up after repeated attempts is reported by `verify` rather than retried
forever.

## §1.9 Assignment

`assign` notifies the agent via Mailman automatically, with a CC for you.

**Not built.** It refuses with exit `1` and says why: assigning work *to* an
agent needs Orc's agent-control contract, which does not exist yet, and
inventing one now would mean rewriting it when the real one lands. Until then
`claim` (take it yourself) and `invite` (add a collaborator) cover the ground
that does not need it.

## §1.10 Exit codes

Shared with every Orc tool, so a script or hook branches on them uniformly:

`0` ok · `1` usage · `2` not found · `3` ambiguous · `4` parse · `5` i/o ·
`6` conflict · `7` auth · `8` denied · `9` out of scope · `11` escape ·
`70` internal.

`10` (unavailable) is in the shared table but Macmuffin never returns it: a
Mailman it cannot reach delays a notice rather than failing a command.

`muff-hook` does not use these: a Claude hook's contract is `0` proceeds, `2`
blocks. See `Claude/Docs/ExitCodes.md`.

## §1.11 Colour

Catppuccin, Macchiato by default, shared with every Orc tool — the board, the
card, the help, confirmations, and diagnostics are all painted from the same
roles `cq` uses, so the two cannot disagree about what a command name looks
like.

`ORC_THEME=macchiato|mocha|frappe|latte|none`; `NO_COLOR` disables it, and
`ORC_AGENT` forces plain output for agents. `--no-color` and `--color` are the
same controls for a caller assembling a single command, which is what Orc will
be doing. `ORC_AGENT` wins over `--color`: turning colour off for every tool at
once must not be defeatable per command.

Colour is a layer and never information — every colour is redundant with a
glyph or a word, and a test asserts that every screen, stripped of its escape
sequences, is byte-for-byte the plain rendering. A pipe through `grep` loses
nothing.

## §1.12 JSON

`pool --json` and `info --json` print the board as JSON, for another program to
read. It is the contract Communiqué mirrors through, so it is a stable shape,
not a rendering. Colour is off under `--json`.

The two differ in what they carry, deliberately. `pool` is a board: it reports
`described`, `described_by` and `described_at`, and no prose. `info` carries the
`description` itself, along with the subtasks. A listing of forty tasks should
not be forty specifications, and a reader that needs one asks for the task it is
about.
