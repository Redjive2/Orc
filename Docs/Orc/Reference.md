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
| `refresh <identity>`                        | Create a new Code session to replace the old one for the identity          |
| `move <identity> <boss>`                    | Move the identity to be under the boss; lower authority/perms as needed    |
| `employ <identity> [--model <m>] [--effort <e>]` | Add the identity to the work list; populate it as needed automatically |
| `fire <identity>`                           | Remove the identity from the work list; do not repopulate it               |
| `introspect [--only <field name>]`          | Shows information on the active agent in this leaf session. Can show one only one field with no formatting for remote authorization and other purposes. |
| `check-control <agent>`†                    | Exit `0` if the caller controls the agent, `8` if not                      |
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
| `--worktree`   | `new identity`            | Make the workspace a git worktree of the main repo          |
| `--watch <dur>`| `tend`                    | Keep reconciling on an interval, as a backstop              |
| `--only <f>`   | `introspect`              | Print one field, raw, with no formatting                    |
| `--json`       | `status`, `introspect`, `list`, `budget` | Print the stable JSON shape instead of the screen |
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

## §1.3 Authority, permissions, and roles

See `Auth_Perm_Role.md`. In short: authority is a number on a role, the user is
100 and everyone else is 1–99, a permission has a minimum authority, and an
identity holds exactly one role.

Nothing effective is stored. An identity's authority is the lower of its role's
and its boss's, and its permissions are its role's plus its grants, intersected
with its boss's — so `move` changes what a whole subtree may do without editing
anything but one line.

`status` shows both numbers whenever they differ, and says which one capped the
other.

## §1.4 Load

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

## §1.5 Sessions

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

## §1.6 Platforms

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

## §1.7 Control

`check-control <agent>` exits `0` if the caller is above the agent in the tree
and `8` if not. It is what `muff assign` calls, so Macmuffin holds no opinion
about authority and Orc holds no opinion about tasks.

Acting on your own subagents — `move`, `fire`, `employ`, `poke`, `refresh`,
`attach` — needs no permission, only ancestry. Adding load to the work list needs
`spawn`.

## §1.8 Exit codes

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

## §1.9 Colour

Catppuccin, Macchiato by default, shared with every Orc tool.
`ORC_THEME=macchiato|mocha|frappe|latte|none`; `NO_COLOR` disables it, and
`ORC_AGENT` forces plain output for agents — which Orc sets in every session it
populates.

Colour is a layer and never information: every colour is redundant with a glyph
or a word, so a pipe through `grep` loses nothing.

The project is stored at `Orc/Orc/go.mod`.
