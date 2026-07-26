# What the fleet is spending — a plan

Filling in `manage › tokens`: a status page for what the fleet costs over time, and
the controls that change it.

The short version: **nothing measures this yet, and the measurement has to be built
in Orc rather than in cq.** Claude writes what every turn cost into its own
transcript; Orc knows where each transcript is and reads only its tail, for prose.
No total is kept anywhere, and cq keeps no history at all — a snapshot is replaced
wholesale on every sync, so a rate has nothing to be a rate *of*. §2 and §3 are
therefore most of the work, and they are Orc's. §4–§6 are the wire and the screen,
and they are small once the numbers exist.

---

## 1. What is actually there today

**The tab exists and says it is empty.** `screens.js` draws one card explaining that
Claude records the numbers, Orc knows where, and nothing adds them up. That card is
the thing this plan replaces, and its honesty is the standard to keep: an empty tab
that explains itself is worth more than one that draws a zero, because a zero is a
measurement and there is no measurement.

**The transcript is read for prose, tail only.** `internal/view/transcript.go` seeks
to the last 256 KB and pulls out who said what. It is the one decoder in the tree
that *ignores* unknown fields rather than refusing them, because the file is Claude's
and an unknown field means Claude shipped a release. Everything built on it degrades
and nothing built on it fails. Both rules carry over here unchanged.

**What a transcript actually holds.** Verified against a real one on this machine —
one 12-hour session, 8.5 MB, 1391 assistant turns:

| Counter | This session |
|-----------------------------|-----------------|
| `input_tokens`              | 2,582           |
| `output_tokens`             | 1,452,573       |
| `cache_creation_input_tokens` | 14,160,174    |
| `cache_read_input_tokens`   | 616,670,401     |

That table is the argument for the whole design. The four numbers differ by five
orders of magnitude, so a tab showing "tokens" as one figure would be showing cache
reads and nothing else. Each turn also carries `model`, a per-line `effort`, a
`timestamp`, `service_tier`, `cache_creation` split by TTL (1 h and 5 m),
`server_tool_use` counts for web search and fetch, and `isSidechain` — which is how a
Task-tool subagent's spend is told from its parent's.

**Orc's own records already cover time.** Three of them, all append-only, all on the
agent machine:

- `identities/<name>/journal.jsonl` — `OpEmploy`, `OpModel`, `OpFire`, each with `at`,
  `model`, and `effort`. What was running, on what, when.
- `identities/<name>/session/events.jsonl` — one line per hook firing: `at`,
  `session`, the hook name, `tool`, `path`, `turn`, and a `verdict` for blocked calls.
  The first event of a session carries the transcript path, so nothing has to guess
  where the file is. `RemoveSession` deletes the state file and the socket and leaves
  this, so the feed survives a `fire`.
- `identities/<name>/session/session.json` — `model`, `effort`, `started`,
  `restarts`, `workspace`.

**Load exists and is a prediction, not a measurement.** `internal/model/load.go`:

    session(model, effort) = weight(model) × weight(effort)        1 … 18
    total(S)               = ⌈ Σ session(s) × (9 + |S|) / 10 ⌉

with weights 1/2/3 for haiku/sonnet/opus and 1/2/3/4/6 for the effort levels, all
integer arithmetic so two machines cannot round differently. `spawn(<n>)` budgets it.
Nothing has ever checked those weights against a token count.

**cq has no history.** `internal/store/store.go`, first paragraph: a snapshot is
replaced wholesale on every sync. The queue is per-action files. There is no series
anywhere, and `POST fleet/roles/<n>/budget` is the only spend-shaped control.

**Plan limits are not knowable from here.** Nothing under `~/.claude` records a rate
limit or a window: no usage file, no limit file, nothing in `settings.json` or
`telemetry/`. Those are facts about the account, held by Anthropic.

---

## 2. Measurement, in Orc

### 2.1 What is counted, and against whom

Six counters per turn, kept apart because they are different things: `input`,
`output`, `cache_create_1h`, `cache_create_5m`, `cache_read`, and `web_calls`. A
seventh derived figure, **new tokens** = input + output + cache-create, is the one
worth putting in a headline: it is what the turn actually caused to be produced, and
it is not swamped by cache reads.

Attribution is by **session id**, and this is load-bearing rather than tidy.
`~/.claude/projects/<slug>/` holds every session in that directory, including the
operator's own — this plan was written in one. Orc mints the session id it passes to
`--resume`, and the event feed records it, so a turn counts only when its `sessionId`
is one Orc started. Anything else is somebody's own work and is not the fleet's spend.

A sidechain turn belongs to the identity whose session spawned it. That is a real
divergence from the load model worth naming out loud: Claude's internal subagents cost
tokens and are invisible to `spawn(n)`, which budgets *employed identities*. The tab
will show the gap; §3 is what to do about it.

### 2.2 Buckets, not events

The rollup is **totals per hour bucket**, UTC, per identity, split by model and
effort:

    identities/<name>/spend.jsonl     one line per (bucket, model, effort), totals
    identities/<name>/spend.cursor    per session: transcript path, size, offset

Beside the identity's journal rather than inside `session/`, because a rollup outlives
any one session and `orc refresh` mints a new id under the same identity.

Three properties, and each one is why the shape is this and not a stream of events:

- **A bucket total only grows**, so it is idempotent to send, to receive, and to
  write twice. Deltas would have to be de-duplicated by identity, and a lost
  acknowledgement would either double-count or lose an hour.
- **The cursor makes reading incremental.** 8.5 MB and 1391 turns for one session is
  cheap once and not cheap every thirty seconds. Read from the recorded offset,
  advance it, and a sync costs the new turns only.
- **A shrinking file is a rotation, not a mystery.** If the transcript is smaller
  than the cursor, the reader restarts from zero and writes a `reset` marker into the
  rollup. Double counting an hour is a thing the operator can see, and silently
  losing one is not.

Buckets are kept for 90 days and pruned by rewriting the journal at the tail — the
same discipline as everywhere else in the tree.

### 2.3 Where the reader runs

`orc spend` is the verb: a read that also advances the rollup.

    orc spend                      the fleet's last 24 hours, per identity
    orc spend <identity>           one card, per model and effort
    orc spend --since <dur>        a window
    orc spend --json               the buckets, for cq

`orc tend` runs the rollup as a side effect, which is what makes measurement
continuous without a daemon: `tend` already runs implicitly under almost every other
command, and `tend --watch` is a cycle somebody may already be running. A fleet nobody
touches for a day loses no data — the transcripts are still on disk and the next read
catches up from the cursor.

Not gated. Reading what a fleet you are in has spent is not a privilege, and the
rollup it writes is a measurement rather than a policy. This matches `status` and
`list`, and `orc(spend)` will not appear in the clause vocabulary.

### 2.4 What it will not measure

- **Money.** Under a subscription plan a dollar figure is a fabrication, and API
  prices are account facts this tree does not hold. If it is ever wanted, it is a
  declared price table and a line saying so.
- **Plan percentages.** §5.1.
- **Anything before the first read.** A fleet that has been running for a month gets
  the whole of every transcript still on disk at first read, and nothing older.

---

## 3. The tariff, in Orc

The weights are the fleet's judgement about what thinking is worth, and today they
are `const` in Go. They become a stored record, journaled and amendable, exactly as a
permission is.

    tariff/tariff.json      creation record: the defaults this build shipped
    tariff/tariff.jsonl     each amendment: who, when, what changed
    tariff/lock

Every value stays an integer, because the whole load model is integer arithmetic and
a tariff that introduced a float would introduce a machine that rounds differently:

| Setting | Today | What it does |
|--------------------|--------------|--------------------------------------------|
| `haiku`, `sonnet`, `opus` | 1, 2, 3 | the model half of a session's load |
| `low` … `max`      | 1, 2, 3, 4, 6 | the effort half |
| `crowd-base`       | 9            | the count multiplier's numerator offset |
| `crowd-scale`      | 10           | its denominator |

    orc tariff                            what it is now, and what it makes things cost
    orc tariff <setting> <n> [--yes]      change one
    orc tariff --calibrate                what measurement suggests instead

**A tariff change is felt immediately by everything.** Every budget is derived, so
raising `opus` re-prices every running opus session at once, and an actor that was
inside its budget can be over it without anybody touching that actor. `edit
permission` set the precedent for this: say who is affected, then do it. `orc tariff`
prints the identities that would go over budget and requires `--yes` when the list is
not empty. It does not refuse — a fleet that has drifted over its own budget is
information, and a tariff that could not be tightened while agents were running would
only ever be loosened.

Gated on a new orc verb, `tariff`, at the policy floor: it is a change to what
everything costs, which is the same kind of act as handing out authority. The
vocabulary test in `internal/cli/vocabulary_test.go` will enforce that the word and
the check agree.

**Calibration is a proposal, never an application.** From the measured buckets, mean
*new tokens per session-hour* for each (model, effort) actually observed, normalised
so the cheapest observed combination is 1 and rounded to integers. It reports:

    measured over 6d, 41 session-hours
    model    weight  measured  suggests      effort   weight  measured  suggests
    haiku         1       —          —        low           1      —         —
    sonnet        2     1.0×        1         medium        2    1.0×       1
    opus          3     3.4×        3         high          3    1.9×       2
                                              xhigh         4      —         —
                                              max           6      —         —

Combinations with no observations say so rather than proposing a number from nothing,
and the calibration weighs **new tokens** — cache reads are reported beside it and
excluded from the weighting, because a cache read is a different economic event and
including it would make the tariff a measure of context size.

---

## 4. The wire, in cq

### 4.1 Protocol

`protocol.Fleet` gains a `spend` field: per identity, a list of buckets, plus the
tariff. Following the vocabulary precedent from the clause work — the fleet carries
what the browser would otherwise keep a stale copy of.

```go
type FleetSpend struct {
    Identity string        `json:"identity"`
    Buckets  []SpendBucket `json:"buckets,omitempty"`
}

type SpendBucket struct {
    At     string `json:"at"`                // the hour, in the tree's format
    Model  string `json:"model,omitempty"`
    Effort string `json:"effort,omitempty"`
    New    int64  `json:"new"`               // input + output + cache-create
    In     int64  `json:"in"`
    Out    int64  `json:"out"`
    Cached int64  `json:"cached"`            // cache reads
    Turns  int    `json:"turns"`
    Tools  int    `json:"tools"`
    Blocks int    `json:"blocks,omitempty"`
    Reset  bool   `json:"reset,omitempty"`   // a rotation was detected in this hour
}
```

A sync carries the last **7 days** — 168 hours × the (model, effort) pairs an identity
actually used, which for a fleet of ten agents is a few thousand small objects and
tens of kilobytes. `MaxListItems` bounds it as everywhere else, and the *server* keeps
the longer series.

### 4.2 Merging, on the server

The one new rule in cq's store: `spend` is **not** replaced wholesale with the rest of
the snapshot. It is merged by `(machine, identity, bucket, model, effort)`, last write
wins per key. Because a bucket total only grows, that is idempotent, order-independent,
and self-backfilling: a machine that could not sync for six hours delivers all six
buckets on the next sync and the series has no hole.

Kept in `spend/<machine>/<yyyy-mm>.jsonl`, append-only, one line per bucket write, read
by folding. Retention on the server is a year, which is around 9,000 lines per
identity — small enough to fold on request and to leave alone.

### 4.3 Verbs

| Op | Command |
|-----------------------------|--------------------------------------|
| `orc.tariff` (new)          | `orc tariff <setting> <n> --yes`     |
| `orc.budget` (exists)       | `orc budget <role> <load>`           |
| `orc.model` (exists)        | `orc model <identity> <model>`       |
| `orc.broadcast` (new)       | §6.3                                 |

`orc.tariff` is **not** idempotent-by-class: two queued changes to the same setting are
two real changes, and the queue must not coalesce them. `orc.budget` already is.

---

## 5. The screen

Five bands, top to bottom, in the order somebody asks the questions.

### 5.1 What replaces plan percentages

Asked for, and deliberately not built: a percentage needs a denominator, Anthropic
holds the real limits, and nothing local exposes them. A bar filling toward a number
cq invented would be the most authoritative-looking thing on the page and the least
true.

What is shown instead is a percentage of something the fleet **does** own — load
against budget — and rates against the fleet's own recent history: *this hour versus
the last 24, this day versus the last 7.* If Claude Code ever writes a real limit
locally, it becomes one more row here and the shape does not change.

### 5.2 Now

One line of live figures, from the newest complete bucket and the session list: new
tokens per minute, sessions running, load and budget with the headroom as a
percentage, and how stale the numbers are. Staleness is never omitted — the whole
panel is a mirror of another machine, and every other screen in cq says when it last
heard from it.

### 5.3 Rates over time

A window selector — hour, day, week — and one chart per window drawn the way the rest
of cq draws things: on the character grid, in block glyphs, exactly as `anno index`
and `orc budget` do. Two series, never summed into one: new tokens, and cache reads.
Turns and tool calls underneath at the same scale, because a flat token line over a
rising turn line is a fleet that has started spinning.

### 5.4 Who and what

One table, sortable, one row per identity: new tokens, cache reads, turns, tool calls,
files touched, blocked calls, tokens per turn, load, budget. Then the same totals split
by model and by effort, which is the table that answers "what would help" — a fleet
spending 80% of its tokens on `opus/max` has one obvious lever and a fleet spread
evenly has none.

The tariff sits beside it: what the tariff says each combination costs, what
measurement says, and the ratio. That comparison is the reason §3 exists.

### 5.5 Productivity, defined narrowly

The honest list of what this tree can count: turns, tool calls, files touched, blocked
calls, tasks completed with their difficulty (`muff`'s journals carry `at` and `by`),
and mail sent (already mirrored, with timestamps). One ratio is worth showing —
**new tokens per completed task** — and it is shown per fleet and per window, not per
agent.

Not per agent, on purpose. Tokens per agent is a number that looks like a ranking and
is not one: a hard task costs more than an easy one, and a fleet where the cheapest
agent is the most rewarded is a fleet that will do the easy work. The tab will say
this where the numbers are, not only here.

---

## 6. The controls

Every one goes through the existing queue, gets a confirmation sheet that says what
will happen, and reports who it affects — the pattern the permission editor already
follows.

### 6.1 Budgets

`orc budget <role> <load>`, which exists end to end. What is new is the context: the
form opens showing the role's current budget, what its holders are spending now, and
what the change would mean for each of them. A budget set without seeing that is a
number somebody guessed.

### 6.2 The tariff

The calibration table from §3 with an "apply what measurement suggests" button, and a
per-setting form for doing it by hand. The confirmation names every identity that goes
over budget, because it re-prices a running fleet. Authority-gated, so an agent
looking at this screen sees the figures and not the buttons.

### 6.3 Broadcasting

One action, two effects: `mailman send` to every identity in the subtree, and `orc
poke` into every running session. Mail is the record and the poke is what reaches an
agent that is mid-turn.

Three limits, because a broadcast is the one control here that writes into everybody's
context at once:

- **Rate-ceilinged.** One broadcast per fifteen minutes per operator, refused with the
  time remaining. An agent whose context is a third notices from the operator is an
  agent that has been made worse at its job.
- **The subtree, not the fleet.** It reaches who the sender controls, which is the
  same rule every other fleet verb follows.
- **Not a standing instruction.** A broadcast is a message, once. Changing what an
  agent is *always* told is `Claude/Docs/Orc/Instruct.md`, and the two must not blur:
  one is a poke, the other is policy.

---

## 7. Milestones

Each one is shippable and each one leaves the tab more honest than it was.

| | What | Where |
|---|--------------------------------------------------|------------------------|
| **0** | The turn reader: usage out of a transcript, by session id, with the cursor and the rotation marker. Tests over a fixture transcript, including a truncated tail and an unknown field. | `Orc/internal/spend` |
| **1** | The rollup: buckets, the journal, pruning. `orc spend` and `orc spend --json`. `tend` advances it. | `Orc/internal/spend`, `internal/cli` |
| **2** | The wire: `FleetSpend` on the snapshot, merge-by-bucket in cq's store, retention. No screen yet — the data lands and `GET /api/v1/admin/state` shows it. | `Communique/internal/{protocol,source,store}` |
| **3** | The screen: §5.2 to §5.4, read-only. The tab stops explaining itself and starts answering. | `Communique/internal/web/app/spend.js` |
| **4** | The tariff: the stored record, `orc tariff`, `--calibrate`, the new verb and its floor. | `Orc/internal/{model,store,cli}` |
| **5** | The controls: budget-with-context, the tariff form, the queue op. | cq, both halves |
| **6** | Productivity: task completions and mail volume in the series, the one ratio, the caveat beside it. | Orc reader, cq screen |
| **7** | Broadcasting, with its ceiling. | cq, both halves |

Milestones 0–3 are the plan's spine: after 3 the tab is a status page built entirely
from measurement, with no control that could mislead anybody, and 4–7 add the levers.

---

## 8. Decisions, and why

**Measured tokens, with the tariff beside them.** Not measurement alone, because a
budget nobody can check against reality stays uncheckable, and calibration is the most
useful single screen this tab can have. Not the load model alone, because it contains
no token count and never will.

**Orc rolls up; cq mirrors buckets.** The source data is on the agent machine, the
recording discipline for append-only journals is already there, and a series sampled
by the server would have a hole wherever a sync was missed and a resolution set by the
sync interval rather than by the data.

**Rates, not plan percentages.** §5.1.

**A stored tariff, editable from cq.** The weights are a judgement about money, which
is a thing an operator changes, not a thing a build decides — and once they are stored
they can be journaled, gated, and compared against measurement.

**No dollars, no per-agent ranking, no per-identity quota.** The first is a
fabrication under a subscription, the second is an incentive to do easy work, and the
third is a second budget model competing with `spawn(n)`.
