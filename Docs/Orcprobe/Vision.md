# §1 Orcprobe

Orcprobe (`orcprobe`) is a copy of the whole Orc world that I can break.

It takes everything the Orc tools are holding right now — mail, tasks, sync
state, the repo, the Claude hooks — and lays it down again somewhere else as a
*probe*: a named, disposable world with all the history and none of the life.
Inside a probe I am a god-agent. I can act as anyone, read everything, send
anything, delete anything, and rewind it all afterwards.

Orcprobe is separate from the other tools on purpose. It is the only one that
reads state it does not own, so it is the only one whose correctness is not
"does it work" but "is the real world provably untouched afterwards".

## §1.1 Two rules

Everything else is detail.

1. **No agents come across.** State is copied; liveness is not. Claims are
   released, owners cleared, pending notifications dropped, worktree links cut.
   Orcprobe has no command that starts a Claude session and no code path that
   could.
2. **Nothing inside can reach out.** The real stores are opened read-only, once,
   and never again. Mail cannot leave a probe, cq cannot sync, git cannot push,
   and no real credential is ever copied into one.

## §1.2 What comes across

| Comes across          | As                                                          |
|-----------------------|-------------------------------------------------------------|
| Mailman               | every mailbox, message, receipt, and archive, verbatim      |
| Macmuffin             | every task and its history — unclaimed, unowned, unassigned |
| Communiqué            | local sync state, reset to "never synced"                   |
| Orc                   | the fleet: identities, roles, history — keyring reminted     |
| The repo              | a full copy, uncommitted work included, remotes stripped    |
| The Claude config     | hooks and settings, rewritten to point inside the probe     |
| Accounts              | as mailboxes, with fresh keys minted for the probe          |

| Never comes across    | Because                                                     |
|-----------------------|-------------------------------------------------------------|
| Running agents        | a probe is a museum, not a workshop                         |
| Live sessions         | the pids and socket in one are lies in a copy                |
| Real keys and tokens  | a scratch world is the wrong place for a live credential    |
| Git remotes           | a probe must not have anywhere to push                      |
| Queued cq actions     | they are addressed to the real server                       |
| Worktree links        | a probe worktree over a real checkout is the escape itself  |

## §1.3 God mode

Two ways in, and I use both.

**Act as anyone.** `orcprobe shell` drops me into the probe with the environment
already set, so `mailman`, `muff`, `anno`, and `cq` just work — as `god` by
default, or as any agent I name. Switching identity costs nothing, because
orcprobe minted every key in the probe and knows all of them.

**See what no agent can.** Alongside that, orcprobe has its own reads, straight
off the store with no identity at all: every mailbox at once, the whole pool
including deleted tasks, raw journals decoded, and a single time-ordered
timeline of every event across every tool.

## §1.4 Probes are plural

A probe is cheap, named, and disposable. Take one before a risky migration, fork
it, checkpoint it, wreck it, rewind it, throw it away. Nothing accumulates
anywhere I have to remember, and nothing in one probe can see another.

## §1.5 Honest about the wall

The wall is environment redirection, shims, and a stamp the tools check — not a
kernel jail. Redirection stops everything that resolves a path the normal way.
An absolute path walks past it, and that is what the stamp is for: Mailman,
Macmuffin, cq, and Orc each refuse to open a store that is not part of the probe
they are running inside, before creating anything, exiting `11` and writing
nothing.

What is still uncovered is small and worth knowing: a tool with no stamp check
in it — a script, a build from before the guard landed — is stopped only by
redirection. Nothing here is enforced by the kernel.

Orcprobe never pretends otherwise: `orcprobe doctor` lists every guard and says
which ones are actually in force, and a probe shell says so on the way in.
