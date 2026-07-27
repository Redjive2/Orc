# What the fleet is doing — a plan

Filling in `manage › activity`, which was `manage › tokens` and is renamed because
spend turned out to be the smallest of the four things the tab is for:

1. **What each agent is doing right now** — its session, its state, its turn, and
   what it has been saying.
2. **What it has done** — files and lines read and written, turns, tool calls, and
   what it cost.
3. **The controls that change how the fleet is kept moving** — wake, tend, and sync,
   set from the browser, globally and per agent.
4. All of it **with respect to time**.

The short version, and it is the same shape for all four: **the facts are on the agent
machine and cq keeps no history, so most of this is Orc's.** §2–§5 are Orc's; §6–§7
are the wire and the screen, and they are small once the facts exist.

One thing is genuinely new and worth naming up front: wake, tend, and sync have no
settings today. Their intervals and thresholds are **flags on running processes** —
`orc wake --every`, `orc tend --watch`, `cq sync --watch` — and a browser cannot set a
flag on a process that is already running. §5 is what makes them state.

---

## 1. What is actually there today

**The tab exists and says it is empty.** `screens.js` draws one card explaining that
Claude records what a turn costs, Orc knows where, and nothing adds it up. That card
is what this replaces, and its honesty is the standard to keep: an empty tab that
explains itself beats one that draws a zero, because a zero is a measurement and there
is no measurement.

**The activity model already exists, for `orc attach`.** `internal/view` reads the
event feed into a `view.Session`, and it is very nearly what this tab wants:

| Field | Is |
|----------------------|--------------------------------------------------------|
| `Turn`               | which turn the session is on |
| `Waiting`            | whether it is waiting for input, derived from the last event |
| `Rows`               | every event: `At`, `Turn`, `Kind`, `Tool`, `Detail`, `Verdict`, `Reason` |
| `Transcript`         | where Claude's own JSONL is |
| `Dropped`, `Skipped` | what the reader could not keep, said out loud |

`Kind` is `Prompt`, `Action`, `Waiting`, `Lifecycle`, or `Unknown` — the view's
reading of Claude's hook names, kept tolerant so an event Claude adds becomes an
unknown row rather than a parse failure. **None of it reaches cq.** The browser gets
`employed`, `model`, `effort`, `load`, and a session id.

**Orc's own records already cover time.** All append-only, all on the agent machine:

- `identities/<name>/session/events.jsonl` — one line per hook firing: `at`,
  `session`, the hook name, `tool`, `path`, `turn`, and a `verdict` for blocked calls.
  The first event of a session carries the transcript path. It survives a `fire`, so
  it is a history and not only a live feed.
- `identities/<name>/journal.jsonl` — `OpEmploy`, `OpModel`, `OpFire`, with `at`.
- `identities/<name>/session/session.json` — `model`, `effort`, `started`, `restarts`,
  `workspace`, and what the session was instructed with.

**What a transcript holds.** Verified against a real one — one 12-hour session,
8.5 MB, 1391 assistant turns:

| Counter | This session |
|-------------------------------|--------------|
| `input_tokens`                | 2,582        |
| `output_tokens`               | 1,452,573    |
| `cache_creation_input_tokens` | 14,160,174   |
| `cache_read_input_tokens`     | 616,670,401  |

Five orders of magnitude between them, which is why a tab showing "tokens" as one
figure would be showing cache reads and nothing else.

**And it holds the file work**, in `toolUseResult` — the same session:

|         | Calls | Lines |
|---------|-------|-------|
| reads   | 29    | 2,549 |
| edits   | 146   | +3,028 / −1,116, from `structuredPatch` |
| creates | 60    | 12,801 |

That is where "file and line counts" comes from, and it means **one reader** serves
both §2's halves: a single pass over a transcript yields usage and tool results.

**cq has no history.** `store.go`, first paragraph: a snapshot is replaced wholesale
on every sync. A rate has nothing to be a rate *of*.

**The cycles have no settings.** `orc wake --after --every --message`, `orc tend
--watch`, `cq sync --watch --ttl`: every one is a flag, read once when a process
starts. Nothing is stored, so nothing can be changed from anywhere else — and a
browser form writing a value nothing reads would be the worst kind of control.

**Plan limits are not knowable from here.** Nothing under `~/.claude` records a rate
limit or a window. Those are facts about the account, held by Anthropic.

---

## 2. Measurement, in Orc

### 2.1 What is counted, and against whom

Per turn, from the transcript: `input`, `output`, `cache_create_1h`,
`cache_create_5m`, `cache_read`, `web_calls`. A derived **new tokens** = input +
output + cache-create is the headline figure — it is what the turn caused to be
produced, and it is not swamped by cache reads.

Per tool call, from the same pass: `reads`, `read_lines`, `edits`, `lines_added`,
`lines_removed`, `writes`, `write_lines`, and the paths touched.

Attribution is by **session id**, and it is load-bearing rather than tidy.
`~/.claude/projects/<slug>/` holds every session in that directory including the
operator's own — this plan was written in one. Orc mints the id it resumes, so a turn
counts only when its `sessionId` is one Orc started.

A sidechain turn belongs to the identity whose session spawned it. Worth naming:
Claude's own subagents cost tokens and are invisible to `spawn(n)`, which budgets
*employed identities*. The tab shows the gap; §4 is what to do about it.

### 2.2 Two sources, and which to believe

**Files come from Orc's feed. Lines come from the transcript.** Different kinds of
fact, and the difference belongs on screen:

- The feed is Orc's own record, written by its own hook, with a line for every tool
  call whether or not Claude's file format ever changes. It knows *which files* and
  *how many calls*, and it cannot know lines: the hook records the path and never the
  content, deliberately, because a feed carrying the text of every edit would be a
  second copy of the repository.
- The transcript knows the lines, and it is Claude's file. Every rule
  `internal/view/transcript.go` already follows applies: unknown fields ignored,
  everything degrades, nothing fails.

So a file count is always right and a line count is best-effort. The screen says which
is which rather than blending them into a number nobody can audit.

### 2.3 Buckets, not events

Totals per **hour bucket**, UTC, per identity, split by model and effort:

    identities/<name>/activity.jsonl   one line per (bucket, model, effort), totals
    identities/<name>/activity.cursor  per session: transcript path, size, offset

Beside the identity's journal rather than inside `session/`, because a rollup outlives
any one session and `orc refresh` mints a new id under the same identity.

- **A bucket total only grows**, so it is idempotent to send, to receive, and to write
  twice. Deltas would need de-duplicating, and a lost acknowledgement would either
  double-count or lose an hour.
- **The cursor makes reading incremental.** 8.5 MB and 1391 turns is cheap once and
  not cheap every thirty seconds.
- **A shrinking file is a rotation, not a mystery.** Smaller than the cursor means
  start from zero and write a `reset` marker: double-counting an hour is something an
  operator can see, and silently losing one is not.

Kept 90 days, pruned by rewriting the tail.

### 2.4 Where the reader runs

    orc activity                    the fleet's last 24 hours, per identity
    orc activity <identity>         one card: state, turn, counts, cost
    orc activity --since <dur>      a window
    orc activity --json             the buckets and the live state, for cq

`orc tend` advances the rollup as a side effect, which is what makes measurement
continuous without a daemon — `tend` already runs under almost every other command.
Not gated: reading what a fleet you are in has done is not a privilege, and the rollup
it writes is a measurement rather than a policy.

### 2.5 What it will not measure

- **Money.** Under a subscription a dollar figure is a fabrication, and API prices are
  account facts this tree does not hold.
- **Plan percentages.** §7.1.
- **Anything before the first read**, beyond what is still on disk.

---

## 3. State, in Orc

The live half, and the one the tab is named for. Five states, every one already
decidable from what Orc has:

| State        | From |
|--------------|--------------------------------------------------------------|
| `generating` | a live session whose last event is not a waiting one |
| `waiting`    | `view.Session.Waiting` — Claude stopped, or asked for something |
| `stuck`      | waiting, past the wake threshold, and already woken once for this silence (`store.Woken`) |
| `down`       | employed with no live session, including a start being paced (`store.StartDue`) |
| `idle`       | not employed and not running. The ordinary resting state |

None of this is a new derivation: `orc wake` decides four of the five today and `orc
doctor` the fifth. What is new is that the answer travels, so the browser stops
inferring "employed and not running" from two booleans.

Alongside it, per session: the turn number, how long it has been in this state, the
last handful of `view.Row`s (tool, path, verdict), and the tail of the prose
`view.ReadProse` already reads for `orc attach`. That is the **view** — the attach
pane, in a browser, read-only.

Read-only deliberately: `attach` hands over a terminal and a queued action minutes old
cannot. `poke` is the write half, and it exists.

---

## 4. The tariff, in Orc

The weights are the fleet's judgement about what thinking is worth, and today they are
`const` in Go. They become a stored record, journaled and amendable exactly as a
permission is: `tariff/tariff.json`, `tariff/tariff.jsonl`, `tariff/lock`.

| Setting                     | Today         | Does                               |
|-----------------------------|---------------|------------------------------------|
| `haiku`, `sonnet`, `opus`   | 1, 2, 3       | the model half of a session's load  |
| `low` … `max`               | 1, 2, 3, 4, 6 | the effort half                     |
| `crowd-base`, `crowd-scale` | 9, 10         | the count multiplier                |

Every value stays an integer, because the load model is integer arithmetic and a float
would be a machine that rounds differently.

    orc tariff                          what it is, and what it makes things cost
    orc tariff <setting> <n> [--yes]    change one
    orc tariff --calibrate              what measurement suggests instead

**A tariff change is felt by everything at once.** Every budget is derived, so raising
`opus` re-prices every running opus session, and an actor inside its budget can be over
it without anybody touching that actor. `edit permission` set the precedent: say who is
affected, then do it. It prints the identities that would go over and requires `--yes`
when that list is not empty.

Gated on a new orc verb, `tariff`, at the policy floor. Calibration proposes and never
applies, weighs **new tokens** only, and says which combinations it has no observations
for rather than inventing a number from nothing.

---

## 5. Settings, in Orc and cq

The controls the tab needs, and the largest new thing here: none of these values exists
anywhere today.

### 5.1 The shape

Three cycles, two stores, one rule.

| Cycle | Settings                              | Lives in | Per agent |
|-------|---------------------------------------|----------|-----------|
| wake  | `after`, `every`, `message`, `enabled` | Orc      | **yes**   |
| tend  | `watch`, `enabled`                     | Orc      | **yes**   |
| sync  | `watch`, `ttl`                         | cq       | no — it is one mirror |

Wake and tend are about *an agent*, so they override the way the wake message already
does: **the identity's, else its role's, else the fleet's, else the built-in.** That
chain exists, it is tested, and an operator already knows it. Sync is about the mirror
between two machines and has nothing per-agent to say.

    orc pace                            every cycle's settings, and where each came from
    orc pace wake --after 20m           the fleet's
    orc pace wake ember --after 5m      one agent's
    orc pace tend --watch 30s
    orc pace wake ember --off           stop waking this one
    orc pace <cycle> <target> --clear   fall back to the layer above

`pace` rather than `set`, because `orc set` would be a verb whose meaning depended
entirely on its object. Gated on a new orc verb, `pace`, at the agents floor: how often
an agent is nudged is directing it, not policy.

### 5.2 How a running cycle notices

**Each pass re-reads.** `orc wake --every` and `orc tend --watch` already loop; the
change is that the interval and threshold come from the store at the top of each pass
rather than from the flags once at startup.

A flag, when given, still wins for that process. Somebody debugging with `--after 1m`
is making a decision about this run, and a stored value silently overriding it would be
the tool arguing with the operator.

That gives the tab the property it needs: **a change made in the browser takes effect on
the next pass**, with no restart and nothing to signal. Worst case it is one interval
late, which for a cycle measured in minutes is what "immediately" means.

`--off` is a real state and not a zero. A disabled cycle says so in `orc pace` and in
the tab, because an agent nobody is waking must look different from one that is being
woken and not answering.

### 5.3 The sync half, in cq

`cq sync --watch` is the same problem on the other machine, and cq's store holds the
answer — the settings ride **back on the sync response**, so the watcher learns its own
interval from the server it is syncing with. That is the only direction that works: the
browser is talking to the server, and the watcher is the thing that has to change.

A watcher that cannot reach the server keeps the interval it has. A mirror that slowed
down because it could not ask how fast to go would be the wrong failure.

---

## 6. The wire, in cq

`protocol.Fleet` gains the live state, the buckets, and the pacing:

```go
type FleetActivity struct {
    Identity string           `json:"identity"`
    State    string           `json:"state"`             // §3
    Turn     int              `json:"turn,omitempty"`
    Since    string           `json:"since,omitempty"`   // in this state since
    Rows     []ActivityRow    `json:"rows,omitempty"`    // the last few events
    Prose    []ProseLine      `json:"prose,omitempty"`   // the tail of what was said
    Buckets  []ActivityBucket `json:"buckets,omitempty"` // §2.3
}

type ActivityBucket struct {
    At     string        `json:"at"`                  // the hour
    Model  string        `json:"model,omitempty"`
    Effort string        `json:"effort,omitempty"`
    New    int64         `json:"new"`                 // input + output + cache-create
    Cached int64         `json:"cached"`
    Turns  int           `json:"turns"`
    Tools  int           `json:"tools"`
    Blocks int           `json:"blocks,omitempty"`
    Files  ActivityFiles `json:"files,omitzero"`
}

type ActivityFiles struct {
    Read, Wrote               int   `json:"read,omitempty"`       // distinct paths
    ReadLines, Added, Removed int64 `json:"read_lines,omitempty"`
    // Lines are best-effort (§2.2) and absent rather than zero when the transcript
    // could not be read. Zero is a measurement; absent is not.
    Partial bool `json:"partial,omitempty"`
}
```

`FleetPace` carries each cycle's effective value **and where it came from**, so the
browser shows "20m, from the role" rather than a number with no provenance — the same
thing the clause cards already do.

**Merging.** Buckets are the one part of a snapshot that is *not* replaced wholesale:
they merge by `(machine, identity, bucket, model, effort)`, last write wins. Because a
bucket total only grows, that is idempotent, order-independent and self-backfilling — a
machine that could not sync for six hours delivers six buckets and the series has no
hole. Live state is replaced wholesale, because it is a fact about now.

Kept in `activity/<machine>/<yyyy-mm>.jsonl`, folded on read, a year's retention.

**Verbs:** `orc.tariff` and `orc.pace` are new; `orc.budget` and `orc.model` exist.
Neither new one is idempotent-by-class — two queued changes to one setting are two
changes, and the queue must not coalesce them.

---

## 7. The screen

### 7.1 What replaces plan percentages

Asked for once and deliberately not built: a percentage needs a denominator, Anthropic
holds the real limits, and nothing local exposes them. A bar filling toward a number cq
invented would be the most authoritative-looking thing on the page and the least true.
What is shown instead is a percentage of something the fleet owns — load against budget
— and rates against the fleet's own recent history.

### 7.2 Now

One row per agent, at the top, because it is why somebody opened the tab:

    ember   generating   turn 24   3m    opus/high    ▪ Edit internal/cli/wake.go
    atlas   waiting      turn 8    41m   sonnet/med   ▪ woken once, still silent
    nib     down         —         12m   —            ▪ start failed twice, next in 5s

State, turn, how long, what it runs on, and the last thing it did. Opening a row shows
the view: recent rows and the prose tail — `orc attach`'s pane without the terminal.

### 7.3 Over time

A window selector — hour, day, week — and block-glyph charts on the character grid, the
way `anno index` and `orc budget` already draw. Two token series, never summed: new
tokens and cache reads. Turns and tool calls underneath at the same scale, because a
flat token line over a rising turn line is a fleet that has started spinning.

### 7.4 What it read and wrote

Per identity and per window: files read, files written, lines read, lines added, lines
removed, and the busiest paths. Line figures carry §2.2's mark when the transcript
behind them could not be fully read — an estimate that looks like a measurement is
worse than no figure at all.

### 7.5 The controls

Every one goes through the queue, with a confirmation saying what will happen and who
it affects.

- **Pacing** (§5): wake and tend, fleet-wide at the top and per agent on each row, each
  showing where its current value comes from. Sync's interval sits with the fleet-wide
  pair.
- **The tariff** (§4): the calibration table with an "apply what measurement suggests"
  button, and a per-setting form. The confirmation names everybody who goes over budget.
- **Budgets**: `orc budget <role> <load>`, which exists, with the context that makes it
  a decision rather than a guess — what the role's holders are spending now.
- **Poke and wake**, per agent, which exist, on the row where somebody has just read
  that an agent has been waiting forty minutes.

### 7.6 Productivity, defined narrowly

What this tree can honestly count: turns, tool calls, files and lines, blocked calls,
tasks completed with their difficulty (`muff`'s journals carry `at` and `by`), and mail
sent. One ratio is worth showing — **new tokens per completed task** — per fleet and per
window.

Not per agent, on purpose. Tokens per agent looks like a ranking and is not one: a hard
task costs more than an easy one, and a fleet where the cheapest agent is the most
rewarded is a fleet that will do the easy work. The tab says so where the numbers are.

---

## 8. Milestones

| | What | Where |
|---|--------------------------------------------------|------------------------|
| **0** ✓ | Rename the tab to `activity`. | cq web |
| **1** ✓ | State on the wire: §3's five states, the turn, and the recent rows, from the reader `orc attach` already uses. The tab stops being empty. | Orc `internal/cli/activity.go`, `internal/view`, cq `activity.js` |
| **2** ✓ | The turn reader: usage and `toolUseResult` out of a transcript, by session id, with the cursor and the rotation marker. | `Orc/internal/activity` |
| **3** ✓ | The rollup: buckets, files, lines, pruning. `orc activity --json`. `tend` advances it. | `Orc/internal/store/activity.go`, `internal/cli` |
| **4** ✓ | Buckets on the wire, merge-by-bucket in cq's store, retention, and a route to read the series back. | Orc `cli/json.go`, cq `store/activity.go`, `server/activity.go` |
| **5** ✓ | The screen: §7.2–§7.4, read-only. | `Communique/internal/web/app/activity.js` |
| **6** ✓ | Pacing: the stored settings, `orc pace`, the override chain, cycles re-reading each pass, sync's interval on the response, and the browser's controls. | Orc `store/pace.go`, `cli/pace.go`; cq `protocol`, `server/{fleet,activity}.go`, `activity.js` |
| **7** ✓ | The tariff: stored, journaled, gated, with calibration, and settable from the browser. | Orc `model/tariff.go`, `store/tariff.go`, `cli/tariff.go`; cq `protocol`, `server/fleet.go`, `activity.js` |
| **8** ✓ | Productivity: task completions and mail volume in the window, the one ratio, the caveat beside it. | Macmuffin `cli/json.go`; cq `protocol`, `source/wire.go`, `activity.js` |

**0 and 1 are done.** As built, three things came out differently from the sketch:

- **The state lives in `internal/cli`, not `internal/view`.** Three of the five states
  need the store and the fleet — employed, paced, woken — and `view` is a leaf package
  that reads a feed and nothing else. Putting them there would have dragged the store
  into it.
- **`stuck` is decided through `silence()`**, the wake cycle's own reading, rather than
  beside it. The mark it returns is the string a wake is recorded under, so asking it
  is what makes "stuck" mean the same on the screen as in the cycle that put the agent
  there. Two functions computing that separately would agree until one was fixed.
- **The prose tail is not carried yet.** Rows travel, bounded at eight; prose is a
  second reader over a compatibility surface and belongs with milestone 2, which builds
  that reader anyway.

**2 and 3 are done too.** The reader was checked against a real 8.5 MB transcript —
1,904 turns — and every figure it produced matched an independent implementation
exactly: all four token counters, and reads, edits, writes with their line counts.

Two things worth recording from building them:

- **The tool's name is not on the line that carries its result.** The name is on the
  assistant's message and `toolUseResult` is on the user's, so file work is
  classified by the *shape* of the result — a `file` object is a read, a
  `structuredPatch` is an edit, a `create` is a write. That needs no cross-referencing
  between two lines, and it survives a tool being renamed.
- **`Touched` is distinct paths within a bucket and does not sum honestly across
  them.** Adding two hours' distinct-counts over the same file claims two files. The
  field carries the caveat and a screen showing a day has to say so.

**4 is done.** Two decisions in it are worth recording because they are trades
rather than derivations:

- **A snapshot carries 48 hours, capped at 240 buckets per identity.** Everything
  would be megabytes per sync for a series the far end already has. The cost is
  stated rather than hidden: a machine offline for longer than the window loses the
  older buckets *from the mirror* and never from Orc's own rollup, so
  `orc activity` on the agent machine still has them.
- **Last write wins on what the mirror saw, not on file order.** Two months' files
  are read in order, but a clock that jumped would otherwise let an older reading
  overwrite a newer one, so each line carries when it was received.

The series has a route of its own — `GET /api/v1/activity?since=` — because it is
the one thing here that is not in a snapshot. Retention drops whole months rather
than rewriting them: a month is the unit the file is written in.

**5 is done.** The series is fetched only while the tab is on screen — the same
bargain the admin view already makes, and for the same reason: it is a year deep at
the server and a window of it every few seconds for a screen nobody is looking at
would be paying continuously for something seen occasionally.

Two things the drawing settled:

- **Each chart is scaled to its own peak**, not to a shared one. New tokens, cache
  reads and turns differ by orders of magnitude, and one scale across all three
  draws two of them flat. The cost is that the charts are not comparable to each
  other, which is why each says its own peak in words beside it.
- **The window selector is a fetch, not a filter.** The server bounds what it hands
  over, so a chart cannot be widened from what the browser already has.

**6 is half done: Orc stores and honours the pacing; cq cannot set it yet.**

One thing in §5.2 turned out to be wrong as written, and the correction is worth
keeping. The plan said a flag always wins over a stored value. That is right for
`--after` and wrong for `--every`, and the two are different in kind:

- `--after` is *this pass's judgement* about what counts as silence. Somebody who
  typed it is deciding about the run in front of them.
- `--every` is *how long this process sleeps*, and a cycle started on Tuesday in a
  shell nobody has open is exactly what a stored setting has to be able to reach.
  There is no other way to tell it. So the stored interval wins, and the loop says
  when its pace changes rather than changing it in silence.

If the flag won there too, the browser control would be a form that writes a value
nothing reads — which is the failure §5 opens by naming.

**The browser half is done too**, for wake and tend: `POST /api/v1/fleet/pace`
carries the cycle, whose layer, and the settings; the queue maps it to
`orc pace <cycle> [<who>] [flags]`; and the tab has a form per cycle on each row
and one for the fleet. Every form opens on what is in force and says which layer it
came from, because a number with no provenance sends somebody looking in the wrong
layer for a value they did not set on that agent.

Two things worth recording:

- **One op and one route for both cycles and all three layers.** A verb per cycle
  per layer would have been six that differ only in what they name. The body says
  which; neither `identity` nor `role` means the fleet's own.
- **A pace that changes nothing is refused** at the protocol, not at the form. A
  queued action that ran `orc pace wake` and reported success for having done
  nothing is the worst kind of no-op: the operator watched it succeed.

**Sync is done, and §5.3 was right about the direction.** The interval lives at the
server, rides back on the sync *response*, and a watcher resets its ticker when it
changes. That is the only shape that works: the browser talks to the server, and a
watcher on an agent machine is something the server can never call — a response is
the one moment the two are in contact.

Three things fell out of building it:

- **Sync is not a queued action**, unlike every other control on this tab. A queued
  action is a thing an agent machine does; this is a setting the server holds, and
  queueing it would send it to the machine that cannot act on it.
- **A round that failed says nothing about the pace.** The interval stands, because
  a mirror that sped up or slowed down because it could not reach the server would
  be reacting to the wrong fact.
- **The floor travels with the answer**, so the form can say what it will take
  rather than making somebody discover it by being refused.

All of §5 is now built.

**7 is half done: Orc prices, journals and calibrates; cq cannot set it yet.**

The invasive part was not the record — it was that *nothing may compute a load
without being handed a tariff*. `model.Identity.Load()` prices at the built-in rate
and cannot do otherwise, since an identity does not know what fleet it is in, so
every screen and every check now asks the fleet: `Fleet.Price`, `Fleet.LoadOf`,
`Fleet.Multiplier`. A global would have been smaller and wrong — a load computed
against whichever price list happened to be loaded is one nobody can reproduce, and
two processes disagreeing about what a session costs is how a budget stops meaning
anything.

Live, that thread is the difference between a feature and a decoration: before it
was pulled, `orc tariff sonnet 10` wrote the record, said "priced", and left the
status table showing a load of 4. Two bugs of the same shape — a table drawn from the
fleet derived *before* the write, and a load priced at the built-in rate — each of
which made the tariff look like it had done nothing.

**7 is done.** The price list and the proposal both travel in the snapshot, and one
setting at a time is queued back — the same rule the store follows, because a
whole-list write from a stale form would revert whatever somebody else set in
between.

The suggestion is computed **once, in Orc**, and carried. The browser has the same
buckets and could normalise them itself; doing so would be a second opinion about
what a fleet should charge, and the two would drift the first time either rounded
differently. Same reason the derived clauses and the vocabulary travel rather than
being recomputed.

**8 is done, and it needed almost nothing new.** The plan assumed completions would
have to be rolled up like tokens. They do not: `task.Task` already carried a
`completedAt`, it was simply never published — so the whole of it was one field in
Macmuffin's JSON, one in cq's protocol, and arithmetic in the browser over data it
already mirrors. Mail was free; `Message.Sent` has been mirrored all along.

Three things the block refuses to say, and each is the point:

- **No ratio without a denominator.** A fleet that completed nothing gets "there is
  no ratio to draw", not a very large number.
- **Nothing per agent.** Tokens per agent looks like a ranking and is not one, and
  the caveat is on the screen rather than in the source, because a number that looks
  like a ranking will be read as one unless something says otherwise.
- **Nothing whose timestamp cannot be read.** A completion with no time is not
  counted; counting it would be inventing work.

The one honest limit, worth writing down: completions are counted from the *mirrored
task list*, so a task deleted or pruned after it was finished stops being counted.
For a window of hours or days — which is every window this tab offers — that is the
same list a person would count by hand.

---

**Every milestone is done.** The tab is what §1 asked for: what each agent is doing
now, what it has done, what it cost, and the controls for all three cycles.

---

## 9. Decisions, and why

**Renamed to activity.** Spend is one of four things and the least urgent. A tab called
`tokens` would have had to grow a name for the rest anyway.

**Measured tokens, with the tariff beside them.** Not measurement alone, because a
budget nobody can check against reality stays uncheckable. Not the load model alone,
because it holds no token count and never will.

**Orc rolls up; cq mirrors buckets.** The source data is on the agent machine, the
append-only discipline is already there, and a series sampled by the server would have a
hole wherever a sync was missed.

**Files from the feed, lines from the transcript.** Different sources with different
guarantees, kept apart on screen rather than blended into one figure.

**Live state is derived once, in Orc.** The browser could almost compute `generating`
from a session id and a timestamp, and it would be a second opinion about what an agent
is doing — wrong in exactly the cases that matter.

**Settings become state, on the wake message's override chain.** A browser cannot set a
flag on a running process, and a second override scheme beside the one that exists would
be a second thing to explain and to get wrong.

**A flag still beats the stored value for that process.** Somebody debugging is making a
decision about this run.

**Rates, not plan percentages. No dollars, no per-agent ranking, no per-identity
quota.** §7.1, §2.5, §7.6 — and because a second budget model competing with `spawn(n)`
is one too many.
