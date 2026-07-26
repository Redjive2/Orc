# Instruct — managed prompts

A plan for keeping an agent's standing instructions in Orc, and editing them from
cq. Four kinds, as asked: **system**, **role**, **identity**, and **wake**.

Nothing here is built yet. This says what to build, in what order, and — where a
choice was not obvious — why that one.

## §1 The constraint everything else bends around

`provision/claude.go` says this, and means it:

> An operator editing this file afterwards is expected; Orc never rewrites it,
> because a tool that overwrites an agent's own instructions on every restart
> would make the identity's memory Orc's rather than the agent's.

That rule is right and this feature must not repeal it. An agent's `CLAUDE.md` and
its `memory/` are **the agent's**. A managed prompt that clobbered them on every
`employ` would take an identity's continuity away, which is the one thing the whole
persistent-identity design exists to provide.

So managed prompts do not touch `CLAUDE.md`. They travel by a separate channel that
Orc owns end to end, and the two never write the same bytes.

| Channel | Owner | Written | Survives an edit by the agent |
|---|---|---|---|
| `claude/CLAUDE.md`, `claude/memory/` | the agent | once, at provisioning | yes — it is theirs |
| the composed system prompt | Orc | at every spawn | yes — the agent cannot reach it |

## §2 Four kinds, two mechanisms

The four are not four of the same thing, and treating them alike would be the first
mistake.

**system, role, identity** are *prompt layers*. They shape the agent for a whole
session and are fixed once it starts.

**wake** is a *message*. It is text sent into a session that is already running —
what `orc wake` and `orc poke` type at an agent that has gone quiet. It has no
business in a system prompt and changes nothing about the session.

| Kind | Is | Delivered | Takes effect |
|---|---|---|---|
| system | fleet-wide standing instructions | composed into the system prompt | next session |
| role | what this job is | composed into the system prompt | next session |
| identity | what this agent in particular is for | composed into the system prompt | next session |
| wake | what to say to a silent agent | the message `poke` sends | next wake, immediately |

## §3 Where they live

Markdown files, beside the thing they describe:

```
<root>/prompts/system.md            the fleet
<root>/prompts/wake.md              the fleet's wake message
<root>/roles/<role>/prompt.md       this job
<root>/roles/<role>/wake.md
<root>/identities/<name>/prompt.md  this agent
<root>/identities/<name>/wake.md
```

Beside the entity rather than in one central `prompts/` tree, because the store's
existing rule is one directory per thing: `orc remove role engineer` already takes
`roles/engineer/` with it, and a prompt filed elsewhere would be an orphan nobody
notices for months. The two fleet-level files have nowhere else to go.

They are plain files, not journalled records. A prompt is prose an operator edits;
it has no fields to validate and no invariant to hold. What *is* journalled is the
fact that it changed — see §7.

## §4 Composition

### Prompt layers are additive

```
system.md  +  roles/<role>/prompt.md  +  identities/<name>/prompt.md
```

Concatenated in that order, each under a heading naming where it came from, and the
whole passed to the session.

**Additive, not overriding.** An identity prompt adds to its role's; it cannot
replace it. This is the asymmetry worth defending: the fleet prompt is where an
operator writes the things that must hold for every agent — how to use the tools,
when to ask rather than guess, what never to do — and a design where a role prompt
could shadow that would mean the fleet-wide instruction is only a default. It is
not a default. It is the floor.

An operator who wants one agent to ignore the fleet prompt does not need a feature
for it; they need to edit the fleet prompt.

### Wake messages override

```
identities/<name>/wake.md  else  roles/<role>/wake.md  else  prompts/wake.md  else  "continue"
```

Most specific wins, and the others are not sent. The opposite of the rule above, and
for a plain reason: a system prompt is a document and documents concatenate, while a
wake message is *one thing you say*. Three of them stapled together is not a message,
it is a mess arriving in the middle of somebody's work.

`"continue"` stays as the built-in bottom, so a fleet that sets nothing behaves
exactly as it does today.

## §5 Delivery

The composed prompt reaches the session as **`--append-system-prompt`**, added to
the argument list `session.Supervisor.Args` already builds.

Two things to check before building, in this order:

1. **That the installed `claude` accepts it.** If it does not, the fallback is a
   generated `claude/ORC.md` plus an `@ORC.md` import at the top of the seeded
   `CLAUDE.md`. That works today and is worse in one specific way: the agent can
   delete the import line, so a *mandatory* instruction becomes advisory. Prefer
   the flag; take the fallback only if forced, and say so in the docs if we do.
2. **Argument length.** A composed prompt is three files of prose on a command
   line, and `ARG_MAX` is real. Bound each layer (§6) so the total cannot approach
   it, and have `orc doctor` report the composed size per identity.

The fixture claude in `internal/fixture/claude` prints its arguments, so the
delivery is testable without the real binary.

## §6 Bounds and validation

A prompt is text that enters every session's context, on every restart, for ever.
That is a cost, and an unbounded one is a fleet that gets slower and more expensive
in a way nobody can see.

| Rule | Value | Why |
|---|---|---|
| one layer | 16 KiB | long enough for real instructions, short enough to read |
| composed total | 48 KiB | three layers at full size, and a bound on the command line |
| wake message | 2 KiB | it is a sentence, not a brief |
| encoding | UTF-8, no control characters but newline and tab | it goes on a command line and into a pty |

Over the limit is a refusal that names the layer and its size — not a truncation.
Silently cutting an instruction in half is how an agent ends up following the first
paragraph of a rule.

`orc instruct` prints the composed size beside each layer, so the cost is visible
where the editing happens.

## §7 The CLI

A new verb family, named for the tab so the two are obviously the same feature.

```
orc instruct                          every layer, its size, and who it reaches
orc instruct system                   print the fleet prompt
orc instruct role <name>              print a role's
orc instruct identity <name>          print an identity's
orc instruct wake [--role r | --identity i]

orc instruct <target> --edit          open $EDITOR on it
orc instruct <target> --set <file>    replace it from a file, or `-` for stdin
orc instruct <target> --clear         remove that layer

orc instruct show <identity>          the composed prompt, exactly as the agent gets it
orc instruct show <identity> --diff   what would change if it restarted now
```

`orc instruct show` is not a convenience. Layered configuration is only debuggable
if the composition can be seen, and "why is this agent behaving like that" is the
question this feature will generate most.

**When it takes effect.** Prompt layers apply at the next session, and the command
says so — the same words `orc model` already uses. `--now` refreshes the affected
sessions immediately, which is `orc refresh` and costs the conversation. The command
names how many sessions that is before doing it.

Wake messages take effect at the next wake, with nothing to restart.

## §8 Who may

Editing a prompt is not editing a file; it is deciding how an agent thinks. It sits
with policy, not with work.

| Target | Rule |
|---|---|
| system | the **operator** alone — like `owner rename`, and for the same reason: it reaches every agent in the fleet |
| role | the `instruct` permission |
| identity | the `instruct` permission, **and** ancestry — you may instruct what you control |
| wake, at any level | the same rule as the prompt at that level |

One new toolkit permission, added to `store/toolkit.go`:

| Permission | Floor | Clauses | Is |
|---|---|---|---|
| `instruct` | 70 | `tool(instruct)` | write the standing instructions agents run under |

`tool(...)` rather than a path glob, for the reason `upgrade` uses it: containment
is by clause, so a marker permission needs a clause nothing broader can cover. A
role with `write(**)` must not acquire the ability to rewrite the fleet's
instructions by being broad.

Floor 70 rather than 85: instructing an agent you already control is closer to
directing it than to handing out authority. The system prompt, which is the part
that reaches everybody, is fenced off separately by being the operator's alone.

**Prompts are not permissions.** A prompt that says "do not edit Communique" is a
request; `write(Anno/**)` is a rule, enforced by the hook on every tool call. The
docs must say this plainly in the same breath as introducing the feature, because
the failure mode — somebody using a prompt where they needed a permission — is
silent, and looks like it is working right up until it does not.

## §9 What changed, and when

Prompt changes are journalled on the entity that owns them: roles and identities
already have journals, and the fleet prompt gets `prompts/journal.jsonl`.

One line per change, holding who, when, and the digest of the new text — not the
text. The file is the text; a journal that carried every revision would be a second
copy of the prose diverging from the first. The digest is enough to answer "did this
change between Tuesday and now", which is the question.

`orc instruct` shows the last change per layer, so the answer to "why is it
behaving differently" starts on the screen you are already looking at.

## §10 cq: the **instruct** tab

The same shape as the fleet panel, which is the same shape as everything else in cq:
the server holds a mirror, the browser queues actions, the agent applies them on its
next sync.

**Reading.** The snapshot's `Fleet` grows a `Prompts` block — the fleet prompt, each
role's, each identity's, and each wake message, with sizes and last-changed. One
more thing `orc instruct --json` already knows how to print.

**Writing.** New operations, alongside the fleet verbs:

| Route | Becomes |
|---|---|
| `PUT /api/v1/instruct/system` | `orc instruct system --set -` |
| `PUT /api/v1/instruct/roles/{name}` | `orc instruct role <name> --set -` |
| `PUT /api/v1/instruct/identities/{name}` | `orc instruct identity <name> --set -` |
| `PUT /api/v1/instruct/wake/...` | the same, for wake |
| `DELETE` on any of them | `--clear` |
| `GET /api/v1/instruct/show/{identity}` | the composed prompt |

Operations `orc.instruct.set` and `orc.instruct.clear`, carrying the target kind, the
name, and the text. Idempotent — setting a prompt to what it already says lands in
the same place — so a retry after an unknown outcome is safe.

**The screen.** A tab beside `tree` and `admin`:

- the fleet prompt at the top, then roles, then identities, each with its size;
- **edit** opens `editor.js` — the existing inline editor, which is already markdown,
  already survives the redraw that happens on every sync, and is already the thing
  used for editing library files. This is the feature it was built for;
- **show** on an identity renders the composed prompt with each layer's heading, so
  the browser can answer the same question the CLI does;
- a warning where it belongs: editing a prompt changes nothing until the session
  restarts, and the panel says which identities are affected and offers to refresh
  them.

Text arriving over the wire and being written to a store is a place to be careful:
the size bounds of §6 are enforced in the protocol, not only in Orc, so an oversized
prompt is refused before it is queued rather than after a sync on a machine nobody
is watching.

## §11 Order to build it

Each step is useful on its own and leaves the tree green.

1. **Store and composition.** The files, the read/write API, the composition
   functions, the bounds. Tests for the layering rules — additive prompts,
   overriding wake — because those are the decisions everything else assumes.
2. **Delivery.** `--append-system-prompt` in `Supervisor.Args`, verified against
   the real `claude` and the fixture. At this point a fleet can be instructed by
   editing files, which is already the whole feature for anybody at a terminal.
3. **The CLI.** `orc instruct`, including `show`. The `instruct` permission in the
   toolkit, and the operator-only rule for the system prompt.
4. **The journal**, and `orc instruct`'s last-changed column.
5. **cq: reading.** The `Prompts` block in the snapshot and the `instruct` tab as a
   read-only screen. Worth shipping alone: seeing every prompt in the fleet from a
   phone is most of the value.
6. **cq: writing.** The operations, the routes, the editor.

## §12 Deliberately not in this

- **Per-task prompts.** A task's instructions belong in the task, and Macmuffin has
  them. A fifth layer that only applies while a task is claimed would make the
  composed prompt change under a running session, which §5 says it cannot.
- **Templating.** No `{{ identity }}` substitution. The moment prompts have a
  language they have a syntax error, and an operator debugging a template at 2am is
  worse served than one who typed the name twice.
- **Versions and rollback.** The journal records that a change happened; the text is
  the file. A fleet that wants history should keep its store in git, which works
  today and needs nothing from Orc.
- **Prompts for the operator's own identity.** The operator is a person at a
  terminal, not a session Orc starts. Composing a system prompt for them would be
  writing instructions nobody reads.
