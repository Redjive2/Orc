# Instruct — managed prompts

A plan for keeping an agent's standing instructions in Orc, and editing them from
cq. Four kinds, as asked: **system**, **role**, **identity**, and **wake**.

This says what to build, in what order, and — where a choice was not obvious — why
that one. **Steps 1 to 4 of §11 are built**; see §13–§15 for what they came to and
what they changed.

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

---

## §13 Step 1, as built

The store and the composition. Everything here is reachable from Go and from
nothing else yet: there is no verb, no delivery, and no tab, which is §11's order
and is deliberate — the layering rules are what every later step assumes, so they
are what got tests first.

**`internal/instruct`** is the composition, and it is a leaf package that reads no
files and knows no paths. `Compose` is additive and puts each layer under a heading
naming where it came from; `WakeFor` overrides, most specific winning, with
`continue` as the built-in bottom. Both rules are tested against each other rather
than only in isolation: a test asserts the losing wake messages are *not* appended
anywhere, because the failure worth catching is one rule quietly acquiring the
other's shape.

**`internal/store/prompts.go`** is where they live — beside the thing they describe,
as §3 says. `orc remove role engineer` already takes `roles/engineer/` with it, and a
test kills a role's directory and checks its prompt went too.

**One thing came out differently.** §3 and §7 talk about a prompt's *target* as a
kind and a name, and the first cut of the API took `(kind, name, wake)`. That does
not survive contact with the store: a role is a `model.Name` and an identity is a
`user.Name`, and one parameter for both would have accepted
`PromptPath(Role, emberTheIdentity)` and filed a role's prompt under an agent's
directory. It is a `store.Target` struct with separate fields, built by
`FleetPrompt`, `RolePrompt`, and `IdentityPrompt`, so a caller cannot express the
mistake.

**Two decisions the plan left open, taken here:**

- **Writing empty text clears the layer** rather than writing an empty file. "No
  layer" and "a layer that says nothing" compose identically, and two ways to spell
  one state is a state somebody eventually disagrees about.
- **Bounds are checked on read as well as on write.** These are plain files an
  operator is expected to edit by hand, so a prompt that would be refused on write
  must not be delivered because it arrived another way. A hand-edited 20 KiB
  `system.md` is refused when it is read, not truncated into a session.

**Next is §11 step 2, delivery**, and it opens with the check §5 asks for: does the
installed `claude` accept `--append-system-prompt`? Until that is answered nothing
composed here reaches an agent, and the answer decides whether the fallback in §5.1
is needed.

---

## §14 Step 2, as built — delivery

### The check §5 asked for, answered

`claude 2.1.220` **accepts `--append-system-prompt <text>`**, and it is in
`--help`. The fallback in §5.1 — a generated `ORC.md` and an import line the agent
can delete — is not needed and should not be built.

Two things found while checking, both worth writing down:

- **`--append-system-prompt-file` is also parsed**, though it appears in `--help`
  only inside another flag's prose, not as an option of its own. It is real: an
  unknown flag errors (`error: unknown option '--definitely-not-a-real-flag'`) and
  this one does not. It is *not* used here, for two reasons — it cannot be verified
  as honoured without a live credential, and a flag absent from `--help` is one that
  can vanish in a release without anybody calling it a break. It is the upgrade path
  if either of the concerns below ever bites.
- **`ARG_MAX` is 1 MiB on this machine**, against a composed bound of 48 KiB. §5's
  worry about argument length is real but not close, and the bound holds it well
  clear. The other argument for the file form is that a command line is visible in
  `ps` to every user on the machine — which for *instructions* is a much weaker
  concern than it is for the keys and message bodies this tree already keeps out of
  argv, but it is the reason to prefer the file form the day it is documented.

### Where the composition happens

In `session.New`, once, and held on the supervisor for the session's life.

Not in `Args()`, which cannot fail and is called again on every restart. Not in
`Prepare`, which would mean threading the text back out to the process that builds
the command line. `New` has the store, returns an error, and runs exactly once per
session — which is also the *semantics* wanted, and for the reason `Prepare` already
gives about the permission snapshot: **a restart continues the same conversation, so
it must continue under the instructions that conversation has been following.** An
operator who edits a prompt while an agent is running has changed the next session,
not this one. `orc refresh` is how they mean otherwise, and a test pins it.

### A broken prompt does not stop a session

An unreadable or oversized layer leaves the prompt empty and the session starts
anyway: an agent that cannot think is worse than an agent missing a layer somebody
added, and one bad file must not make a fleet unstartable.

The first draft swallowed that error with a comment claiming it was logged. It is
logged now — `start` writes an `instruct` line saying the instructions could not be
composed and why, at every start rather than once, because a session that keeps
restarting is a session somebody is reading the log of.

### Verified end to end

A real spawn against the fixture, which prints its arguments:

```
fake-claude --session-id f8203bc7… --model sonnet --effort medium --name ember \
  --append-system-prompt '# the fleet

ask before you guess. never force-push.

# the engineer role

you write the parser, and only the parser.

# ember

you are covering for atlas this week.'
```

Three files, three layers, in order, each under a heading naming where it came from.
**At this point the whole feature works for anybody at a terminal** — a fleet can be
instructed by writing three files — which is what §11 said step 2 would buy. §11 step
3 is `orc instruct`, which makes it reachable without knowing the layout.

---

## §15 Steps 3 and 4, as built — the CLI and the journal

`orc instruct` exists, with `show`, and every layer records who last changed it.

### §9's journal, filed differently

§9 said to journal prompt changes in the entity's existing journal. They are in a
separate append-only file beside the prompts instead — `prompts/journal.jsonl` for
the fleet as §9 wanted, and `prompts.jsonl` in the role's or identity's own
directory.

The reason is what those journals are. `roles/<r>/journal.jsonl` and its identity
equivalent are typed event streams that a **fold replays to reconstruct state**. A
prompt change reconstructs nothing — the file is the state — so an event there would
be one every fold had to carry and ignore, and one more shape in a vocabulary whose
totality is tested. The change record still goes away with the thing it belongs to,
which is the property §3 and §9 were both protecting.

One line per change: who, when, the digest, and the size. The digest rather than the
text, as §9 says, and for the reason it gives.

### Who may, and the one thing that surprised

§8's table, implemented as written: the fleet's layer is the operator's alone, a
role's or an agent's needs the `instruct` toolkit permission (floor 70, `tool(...)`
clause so no broad permission confers it), and an agent's needs ancestry too.

The addition: **nobody instructs themselves.** §8 does not mention it, and the
ancestry rule alone would have allowed it — an operator is above everybody, so `orc
instruct identity boss` would have been fine. An agent writing its own standing
instructions is an agent deciding what it is for, which is the one thing the layer
exists to decide from outside. It is refused with that sentence.

### `orc wake` now reads what is set

This was the gap that would have made the feature a lie: `orc instruct wake` wrote a
file, and `orc wake` still sent its own hard-coded `continue`. The cycle now walks
the override chain per identity — the agent's, else its role's, else the fleet's,
else `continue` — and `--message` still beats all of it, because an operator who
typed a message meant that one. `--dry-run` says which layer the message came from.

### What is left

Step 5 and 6: cq's `instruct` tab, reading then writing. Everything below them is
done, and a fleet can be instructed from a terminal today.

One thing found on the way, and not mine to fix:
`session.TestPokeIsBracketedWhenMultiline` has a **test-order dependency**. It passes
when the package runs whole and fails 8 times out of 8 under
`-run TestPokeIsBracketed` alone, because it asserts on pty-echoed output whose
escape rendering depends on terminal state an earlier test leaves behind. It is
occasionally flaky in the full run too. The file is another stream'''s.
## §16 Steps 5 and 6, as built — cq

Done. A fleet can now be instructed from a browser, and the round trip is the same
one every other verb in cq takes: the browser queues, `cq sync` applies on the agent
machine, and the server never reaches back.

### Reading

`orc instruct --json` came first, and shares `instructRows()` with the overview so
the table a person reads and the list a program reads cannot disagree about which
layers exist. The text travels with each row. cq's tab is an editor, not a listing,
and an editor that fetched each layer separately would be one that could open a
prompt somebody changed since the snapshot — the thing every other screen in cq is
careful about.

`source.Orc.Fleet` runs it as a second call and hangs the result on
`protocol.Fleet.Prompts`. A machine whose Orc predates the command mirrors a fleet
with no prompts rather than failing to mirror at all.

### Writing

Two ops, `orc.instruct.set` and `orc.instruct.clear`, carrying `Prompt` (the kind),
`PromptName` (the role's or the agent's), `Wake`, and the library's existing `Text`
rather than a second operand meaning the same thing. Both are idempotent: the same
body twice lands in the same place.

Three things worth keeping:

- **The bounds are checked before queueing.** `checkInstructArgs` enforces §6's
  16 KiB and 2 KiB at the browser. An oversized prompt refused only at apply time
  would be refused on a machine nobody is watching, hours later.
- **The prose never enters argv.** The apply side writes the text to a 0600 temp
  file and passes the path, so a standing instruction does not appear in `ps` on a
  machine several agents share.
- **The layer is in the path, not the body.** `PUT /api/v1/instruct/roles/{name}`,
  `.../identities/{name}`, `/instruct/system`, and `/instruct/wake/...` for the
  other mechanism, with `DELETE` on each to clear. A client that passes a
  discriminator in a body is one that can pass a discriminator disagreeing with the
  route it posted to.

### The tab

`admin → instruct`, after `identities`, `roles` and `perms` — the four questions in
the order each answers the next one's "why", with this one last because it is the
only one that persuades rather than permits.

Layers and wake messages are drawn as two groups, each headed by its rule
("additive", "overriding"), because they are edited in the same place and mean
opposite things; somebody who writes a wake message expecting it to add to the
fleet's has made a mistake nothing else would tell them about. Every layer gets a
row whether or not it exists — a row that appeared only once something was set would
be a row you could not use to set the first one.

**edit** opens `editor.js`, and an emptied editor is a clear rather than an empty
file, matching the store. The note under each editor names the running sessions that
will keep their old instructions until refreshed, rather than saying "some sessions",
which is not something an operator can act on.

**show composed** assembles the three layers from the mirror rather than fetching
them: the server cannot reach the agent machine, so a round trip would return the
same data with a delay on it. The sheet says so, and names `orc instruct show` as
the authority. It is offered on agents only — a composed prompt is what one agent is
told, and a role is one layer of several agents' worth.

`dialog.show` was added for it: a sheet with a document and a way out. Reusing
`confirm` would have put a "do it" button on something that does nothing, which is
how somebody learns not to trust the buttons.
