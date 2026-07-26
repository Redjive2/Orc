# Orc

A minimal AI orchestrator for Claude Code, and the small tools around it.

Orc runs Claude Code sessions and decides who may do what. Everything else in the
tree is a tool those sessions use — mail, tasks, reading, docs — plus **Communiqué**
(`cq`), which puts the whole thing behind a website you can reach from anywhere.

| Tool       | Command   | Does                                                            |
|------------|-----------|-----------------------------------------------------------------|
| Orc        | `orc`     | The fleet: identities, roles, permissions, sessions              |
| Mailman    | `mailman` | Inter-agent mail                                                 |
| Macmuffin  | `muff`    | Tasks: claim work, set scope, report progress                    |
| Anno       | `anno`    | Reads and writes files in annotated blocks, to preserve tokens   |
| Dock       | `dock`    | Reads and links documentation, likewise                          |
| Communiqué | `cq`      | The remote web interface, and the sync that feeds it             |
| Orcprobe   | `orcprobe`| A copy of the whole world you can break; nothing depends on it   |

`Common` and `Theme` are libraries. `sh/` holds the scripts. `Docs/` is the spec for
each tool; `Claude/Docs/` is what agents have written about the work.

---

## Setup

Go 1.26+, and `claude` on `PATH` if you want sessions to actually start.

### 1. Build

```bash
sh/build
```

Installs 13 binaries to `~/.local/bin` (`--to DIR` to change it, `--check` to build
without installing). If that directory is not on your `PATH`, the script says so and
prints the line to add — or `sh/push` will add it to your profile, showing the edit
first.

Each module builds standalone; `go.work` is a convenience for cross-module work, not
a requirement.

### 2. The fleet — on the machine that runs agents

```bash
orc bootstrap --as <yourname>
```

That is the whole of it: the store, an operator identity at authority 100, and a
mailbox provisioned in Mailman with the same key. **The key is printed once and
cannot be recovered.** Put what it prints in your profile:

```bash
export ORC_USER=<yourname>
export ORC_KEY=<the key>
```

`orc` itself finds that key in the fleet when nothing is set — the store is yours at
0700 — but `mailman`, `muff`, and `cq` all check the environment on every command.

Then name the mail store's owner, or the admin view of the mailbox will be refused
later and `cq sync` will warn about it every time:

```bash
mailman admin owner <yourname>
```

You now have a complete, empty fleet. `orc doctor` will confirm it.

### 3. The server — on the machine you can reach from a browser

It does not have to be a different machine, but it usually is. The server never
reaches back to the agent machine; everything travels on the agent's next sync.

```bash
export CQ_STATE=~/.cq-server      # where the server keeps its state

cq admin operator                 # the password you log in with
cq admin token studio             # one token per agent machine — shown once
cq serve                          # :8080, or --addr host:port
```

`cq serve` refuses to start until both a password and a token exist. Nothing on the
site is visible without logging in, including the application itself.

### 4. Point the agent machine at it

Back on the machine with the fleet:

```bash
export CQ_SERVER=https://your-server:8080
export CQ_TOKEN=<the token from step 3>

cq sync                           # one round trip, both directions
cq sync --watch 5m                # keep it fresh
```

`cq status` says what is set and what is missing without touching the network.
`cq sync --dry-run` collects and reports but sends nothing.

### 5. Hire somebody

```bash
orc new permission edit-docs 40 'read(**)' 'write(Docs/**)'
orc new role writer 60 keeps the documentation honest
orc assign permission writer edit-docs

orc new identity ember
orc assign role ember writer
orc employ ember                  # onto the work list, and start a session
orc status
```

---

## The architecture, in one page

**Everything is a file, and every change is an append.** Each tool keeps its own
store — `~/.orc`, `~/.mailman`, `~/.macmuffin`, `~/.cq` — as plain files with
append-only journals, folded on every command. No daemon owns the state; several
processes read the same store safely, and a torn *final* line is treated as an
interrupted append (dropped and counted), while a torn line anywhere else is
corruption and is reported as such.

**Authority is a number, permissions are named sets of clauses.** A role carries an
authority (the operator has 100, everyone else 1–99). A permission is a named set of
clauses — `read(**)`, `write(Docs/**)`, `spawn(24)`, `tool(instruct)` — with a floor:
only an identity at or above that floor may hold it. An identity holds exactly one
role, plus whatever has been granted to it directly, and **every grant lapses**.
`orc introspect` answers "what may I do" from inside a session.

**A prompt asks; a permission enforces.** `orc instruct` writes the standing
instructions agents run under — the fleet's, the role's, and the agent's, composed
additively. None of it can stop an agent doing anything. That is `orc doctor`'s job to
be honest about: it lists the guards that hold *and the ones that cannot*, because a
screen full of "in force" would leave you believing the permission model is a fence
when it is a request that one hook enforces on one side of one tool layer.

**cq is a mirror, not a remote control.** The agent machine pushes its whole state up;
the browser queues actions; `cq sync` brings them down and applies them locally. The
server can never reach back. Everything you do in the browser therefore *waits*, and
the site says so — it shows how stale it is and marks queued things queued. Actions
carry the view they were made against (a digest for a file, a path for a workspace),
so a change made against a stale snapshot is refused rather than silently overwriting
one made in between. `cq queue` is where refusals turn up.

**Failures have a shared vocabulary.** Every tool exits with the same codes: 0 ok,
1 usage, 2 not found, 3 ambiguous, 4 parse, 5 i/o, 6 conflict, 7 auth, 8 denied,
9 scope, 10 unavailable, 11 escape, 70 internal. Hook binaries follow Claude's
contract instead (0 proceeds, 2 blocks). A refusal names the way forward — if a
message does not tell you the command that fixes it, that is a bug.

**Colour is a layer, never information.** Any screen stripped of escape sequences is
byte-for-byte the plain rendering. `ORC_THEME` picks a Catppuccin flavour, `NO_COLOR`
and `ORC_AGENT` turn it off, and `--no-color`/`--color` are global flags.

---

## Utilities

### The scripts

| | |
|---|---|
| `sh/build` | Build every tool and install it. `--to DIR`, `--check`, or name the tools you want. |
| `sh/push` | Build, install, and add the directory to your shell profile — showing the edit first. `--undo` takes it back out. |
| `sh/pull` | Pull the tree, rebuild, install. The hand-operated twin of `cq upgrade`. Refuses a non-fast-forward or a dirty tree. |
| `sh/env` | Every environment variable the tools read, in one place. `sh/env check`, and `eval "$(sh/env auto)"` to fill in what a machine can work out for itself. |
| `sh/nuke` | Remove every store, for a genuinely fresh start. Shows before it acts, always, and refuses a path that lacks the marker file its store would have. |

### Environment

`sh/env` is the catalogue and stays checked against the source. The ones you set by
hand:

| | |
|---|---|
| `ORC_USER`, `ORC_KEY` | Who you are. Every tool but `orc` requires them. |
| `ORC_HOME`, `MAILMAN_HOME`, `MACMUFFIN_HOME`, `CQ_HOME` | Where each store lives. All have defaults. |
| `CQ_SERVER`, `CQ_TOKEN` | The agent machine's half of cq. |
| `CQ_STATE`, `CQ_PASSWORD` | The server's half. |
| `ORC_CLAUDE_BIN` | The binary a session runs; `claude` by default. |
| `ORC_THEME`, `NO_COLOR` | Colour. |

A half-set identity is an error rather than a fallback: set both `ORC_USER` and
`ORC_KEY`, or neither.

### Finding your way around a tool

Every tool has the same help shape, and it is the fastest documentation in the tree:

```bash
orc help                # the command list, grouped by what you are trying to do
orc help instruct       # one command, in full — what it is for, flags, examples
muff help worktree
cq help sync
```

### When something is wrong

```bash
orc doctor              # which guards are in force, and which cannot be
orc verify              # walk the store and report damage, changing nothing
muff verify
cq status               # local sync state; never touches the network
cq queue                # what is waiting, and what the agent machine refused
```

### Breaking things safely

```bash
orcprobe create scratch      # copy the whole world
orcprobe shell               # a subshell inside the copy
orcprobe save before-thing   # checkpoint
orcprobe restore before-thing --yes
orcprobe destroy scratch --yes
```

No agent ever runs `orcprobe`, and nothing depends on it. Inside a probe every tool
behaves normally and touches nothing real.

---

## Working in this tree

Go 1.26, standard library only. One module per tool, each with `replace` directives so
it builds on its own.

- Constructors are `New…() (T, error)` with a private `validate()`; invariants are
  checked at the edge and trusted after.
- **No panics.** A violated invariant is a returned internal fault, not a crash.
- Errors are positioned and typed, unwrapping to shared sentinels — so an exit code is
  derived, never chosen at the call site.
- Tests say *why* a rule exists, not just that the code does what it does. A test
  whose name restates the function name is a test nobody will trust in a year.

Before anything lands: `gofmt -l`, `go vet ./...`, `go test ./...` in each module, and
`node --test Communique/internal/web/app/test/*.test.js` for the web app.

Documentation written by an agent goes under `Claude/Docs/`. `Docs/` is the
hand-written spec, and `Docs/Vision.md` is where to start reading.
