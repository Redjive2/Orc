# §1 Orcprobe — CLI

Orcprobe exposes the same CLI structure as every other Orc sub-app:

```
orcprobe <command> <args...>
```

| Command                            | Does                                                            |
|------------------------------------|-----------------------------------------------------------------|
| `create <name>`                    | Snapshot the real world into a new probe                        |
| `list`                             | Every probe: age, size, drift from source, checkpoints          |
| `use <name>`                       | Set the default probe for every other command                   |
| `shell [--as <user>]`              | Open a subshell inside the probe                                |
| `as <user> -- <cmd...>`            | Run one command inside the probe, as one identity               |
| `world`                            | The whole probe on one screen                                   |
| `mail [query]`                     | Every mailbox at once, cross-user                               |
| `tasks`                            | The full pool, including deleted tasks                          |
| `journal <user\|task\|convo>`      | One append-only journal, decoded, one event per line            |
| `timeline [--since <time>]`        | Every tool's events merged into one time-ordered table          |
| `save <label>`                     | Checkpoint the probe's contents as they stand                   |
| `restore <label> --yes`            | Rewind to a checkpoint, discarding everything since             |
| `diff <a> <b>`                     | What differs between two probes, or a probe and its source      |
| `manifest`                         | What was copied, what was neutered, what was refused            |
| `doctor`                           | Check every guard and report which are in force                 |
| `destroy <name>`                   | Remove a probe whole                                            |

Every command but `create`, `list`, `use`, and `destroy` acts on the default
probe, or on `--probe <name>`.

`destroy` is the only irreversible command. It refuses any path outside the
orcprobe root, prints what it will remove, and needs `--yes` when standard input
is not a terminal — which, for an agent, is always. `restore` overwrites the
probe's working state and takes `--yes` on the same terms.

A checkpoint captures `state/`, `repo/`, and `claude/` — what a run inside the
probe changes. It does **not** capture the probe's identity: the record, the
stamp, `identities.json`, and the environment survive every rewind, so the keys
you are holding never change under you. A label is never overwritten, and both
the save and the rewind are recorded in the manifest — a rewind is an event in
the probe's history, not a way to erase one.

## §1.1 Flags

| Command    | Flags                                                              |
|------------|--------------------------------------------------------------------|
| `create`   | `--repo <path>` `--no-repo` `--live-state` `--fake-home`            |
| `shell`    | `--as <user>` `--fake-home`                                        |
| `mail`     | Mailman's query language, over the whole store — see below         |
| `timeline` | `--since <time>` `--tool <name>`                                   |
| `diff`     | `--source` to compare a probe against the world it came from       |
| `doctor`   | `--strict` to exit `11` when any guard is absent or unmeasured      |

Shared by everything: `--probe <name>`, `--no-color`, `--width <n>`, `--yes`.

`--live-state` keeps task ownership and claims intact, for reproducing a real
situation rather than a clean one. It changes nothing about rule 2: no agent is
ever started, and nothing ever leaves.

Two flags in earlier drafts of this reference are **not built**: `--quiesce`,
which would take each store's lock for the moment of the copy, and `--from`,
which would fork one probe from another. A live copy is already an *earlier*
state rather than a torn one, and forking a probe is a snapshot of a snapshot;
neither has been wanted yet.

## §1.2 Queries

`orcprobe mail` speaks Mailman's query language exactly — `&`, `|`, `!`, `()`,
and `=` `!=` `~` — so there is one language for selecting mail, not two:

```
orcprobe mail 'from="boss" & unread=true'
orcprobe mail '!(to="alice") & subject~"work"'
```

What differs is the scope, and it is the whole point of the view. Mailman
evaluates against one mailbox; orcprobe evaluates against the store, where a
message is unread by some people and read by others. So two fields mean
something wider here, and the table shows which:

| Field    | In mailman        | In orcprobe                  |
|----------|-------------------|------------------------------|
| `unread` | unread by you     | unread by **anybody**        |
| `id`     | your puid         | **any recipient's** puid     |

An unknown field is an error naming every valid one, never a term that quietly
matches nothing.

## §1.3 Identity

Inside a probe, identity is free. Orcprobe mints a fresh key for every account
as it copies it — real keys are never copied — so it knows all of them and can
hand me any one instantly.

| Variable    | Holds                             |
|-------------|-----------------------------------|
| `$ORC_USER` | the identity `shell` and `as` set |
| `$ORC_KEY`  | that identity's probe key         |

The default is `god`: a real mailbox, a recipient of nothing, holding every
capability the tools grant to a single user. The name is plain because Mailman's
names are — lowercase letters, digits, and `. _ -` only — so a mailbox cannot be
called `@god` however much it would suit.

Probe keys are worthless against the real store, and real keys are worthless
inside a probe.

## §1.4 The environment

`shell` and `as` apply one environment, written to the probe as a readable file
so it can be diffed and pasted:

| Variable                          | Points at                          |
|-----------------------------------|------------------------------------|
| `$ORCPROBE_ACTIVE`                | the probe id — the tripwire        |
| `$MAILMAN_HOME`                   | the probe's Mailman store          |
| `$MACMUFFIN_HOME`                 | the probe's Macmuffin store        |
| `$CQ_HOME`                        | the probe's cq state               |
| `$ORC_HOME`                       | the probe's copy of the fleet      |
| `$XDG_DATA_HOME`, `$XDG_STATE_HOME` | the probe, as a backstop         |
| `$CLAUDE_CONFIG_DIR`              | the probe's copied hooks           |
| `$GIT_CONFIG_GLOBAL`              | a probe git config, no credentials |
| `$ORC_NO_NUDGE`                   | `1` — mailman and muff never spawn `cq sync` |
| `$PATH`                           | the probe's shims, first           |

## §1.5 Guards

Four layers stand between a probe and the real world. `doctor` names each one
and says whether it is in force — and for the stamp guard it *measures* rather
than assumes, by running each tool against an unstamped directory and watching
what it does. A build from before the guard landed is reported `absent`.

`doctor` has three answers, not two. `in force` and `absent` are findings;
`not checked` means a tool is not installed on this machine and its guard could
not be measured — which is neither reassurance nor a failure, and is never
silently rounded to either.

| Guard          | Stops                                                              |
|----------------|--------------------------------------------------------------------|
| Redirection    | anything that resolves a store the normal way                      |
| Shims          | `cq sync`, `cq serve` off loopback, `git push`, agent spawning      |
| Stamp          | a store root reached by absolute path, or by any other route        |
| Detachment     | reaching the server at all: no remotes, no tokens, no password      |

The stamp guard lives in Mailman, Macmuffin, cq, and Orc, not here. With
`$ORCPROBE_ACTIVE` set, each refuses to open a store root that is not stamped as
part of that probe — before creating anything, and failing closed if it cannot
read the stamp. The refusal exits `11`, and nothing is written:

```
$ mailman inbox      # inside a probe, MAILMAN_HOME forced back at the real store
mailman: refusing to open /Users/me/.mailman: this process is inside probe
  657…-68eb, and that store is not part of it.
  Nothing was written.
```

What it does not cover: Anno and Dock hold no state, so there is nothing to
guard; Orc will need the same when it has state of its own; and any other binary
that writes to an Orc store — a script, a build from before this landed — is
unguarded. Outside a probe the guard does nothing at all.

`orcprobe` itself never writes outside its own root, and never opens a real root
except read-only while taking a snapshot.

## §1.6 Colour

`ORC_THEME=macchiato|mocha|frappe|latte|none`; `NO_COLOR` disables it, and
`ORC_AGENT` forces plain output for agents. `--no-color` and `--color` are the
same controls for a caller assembling a single command, which is what Orc will
be doing. `ORC_AGENT` wins over `--color`: turning colour off for every tool at
once must not be defeatable per command.

A misspelled `ORC_THEME` is a usage error rather than a silent fallback — a
setting that quietly does nothing is one the operator concludes is broken.

Each stream is asked about separately. `orcprobe shell > log` writes its banner
to a terminal while stdout is a file, and `2> log` is the reverse; deciding both
from stdout would either drop the colour where a person is reading it or write
escape sequences into a file where nobody is.

Colour is a layer and never information — every colour is redundant with a glyph
or a word, and a test asserts that every screen, stripped of its escape
sequences, is byte-for-byte the plain rendering. A pipe through `grep` loses
nothing.

## §1.7 Storage

Root is `$ORCPROBE_HOME`, else `$XDG_DATA_HOME/orcprobe`, else `~/.orcprobe`. A
probe is refused if it would land inside a real store or inside the repo it
copies.

```
<root>/probes/<name>/
  probe.json       what this probe is, and what it was taken from
  manifest.jsonl   copied, neutered, refused — one line each
  env              the environment shell and as apply
  bin/             the shims
  state/           mailman, macmuffin, cq
  repo/            the working copy, remotes stripped
  claude/          hooks and settings, rewritten
  checkpoints/     saved states
  log/             every command run inside this probe
```

The project is stored at `Orc/Orcprobe/go.mod`.
