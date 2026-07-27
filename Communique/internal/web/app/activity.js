// `manage › activity`: what each agent is doing, right now.
//
// The fleet tab answers "what is employed, and what should I do about it". This
// one answers the question somebody actually has when they open a phone at ten at
// night: *is anything happening?* They are different questions, and the second one
// was unanswerable — the browser knew `employed` and a session id, and inferred
// the rest, which meant "employed, not running" covered a start being paced, a
// killed supervisor, and an agent nobody had ever employed.
//
// Orc decides the state now and the answer travels (see cli/activity.go). Nothing
// here re-derives it: a browser working out `generating` from a session id and a
// timestamp would be a second opinion about what an agent is doing, wrong in
// exactly the cases the tab exists for.
//
// Under the live rows is the series: what the fleet has cost and touched, by the
// hour. It comes from its own route rather than the snapshot, because a snapshot is
// replaced whole on every sync and a rate is a fact about history.
//
// The controls — pacing wake, tend and sync — are Activity.md §5 and are not built.

import { h, since } from "./dom.js";
import { perMachine, agents } from "./fleet.js";

// STATES is every state Orc can send, what it means, and how it is drawn.
//
// A word this build does not know is shown as itself rather than dropped: a newer
// orc that grows a sixth state should make this tab say something unfamiliar, not
// make it say nothing.
const STATES = {
  generating: { look: "ok", means: "working — mid-turn" },
  waiting: { look: "", means: "finished a turn and is waiting to be spoken to" },
  stuck: { look: "pending", means: "woken once already and has not moved since" },
  down: { look: "failed", means: "employed, with no session" },
  idle: { look: "muted", means: "not employed" },
};

// order puts what needs a person first, then what is working, then the rest.
//
// Not alphabetical and not by name: a fleet of twenty is read from the top, and the
// row somebody has to act on should not be the one they scroll to.
const RANK = { down: 0, stuck: 1, generating: 2, waiting: 3, idle: 4 };

function rank(id) {
  const at = RANK[id.activity];
  return at === undefined ? 2.5 : at;
}

export function activity(state, actions) {
  return perMachine(state, "activity", (f) => {
    const list = [...agents(f)].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
    const series = seriesFor(state, f.machine);
    return [
      h("div", { class: "meta" }, summary(list)),
      h("div", { class: "body" },
        ...(f.problems || []).map((p) => h("p", { class: "warn" }, p)),
        fleetPace(f, actions),
        ...(list.length === 0
          ? [h("p", { class: "muted" }, "nobody yet")]
          : list.map((id) => row(f, id, actions))),
        ...over(state, series, actions),
        productivity(state, f, series),
        tariff(f, actions)),
    ];
  });
}

// seriesFor pulls one machine's buckets out of what the series route returned.
//
// The route answers per machine and per identity; the panel is drawn per machine,
// so this is the join. A machine with nothing is an empty map rather than null, so
// every reader below can iterate without asking.
function seriesFor(state, machine) {
  const got = state.activity;
  if (!got || got.unreachable) return { error: got ? got.unreachable : "", identities: {} };
  const found = (got.machines || []).find((m) => m.machine === machine);
  return { error: "", identities: found ? found.identities || {} : {} };
}

// summary counts the states worth counting. Idle is left out of the sentence and
// not out of the list: "3 of 8 working" is the fact, and eleven idle identities
// padding the number would hide it.
function summary(list) {
  const count = (state) => list.filter((id) => id.activity === state).length;
  const parts = [`${count("generating")} of ${list.length} working`];
  for (const state of ["waiting", "stuck", "down"]) {
    const n = count(state);
    if (n > 0) parts.push(`${n} ${state}`);
  }
  // A fleet from an older orc sends no state at all. Saying so beats drawing
  // "0 of 8 working" over a fleet that is perfectly busy.
  if (list.some((id) => !id.activity)) parts.push("some states unknown — orc is older than this page");
  return parts.join(" · ");
}

function row(f, id, actions) {
  const known = STATES[id.activity] || null;
  return h("div", { class: `agent${id.activity === "idle" ? " idle" : ""}` },
    h("div", { class: "agent-head" },
      h("span", { class: "name" }, id.name),
      h("span", { class: known ? known.look : "pending", title: known ? known.means : "" },
        id.activity || "unknown"),
      // The turn is what tells a long turn from a stopped one at a glance, which
      // is the whole difference between an agent thinking and an agent gone.
      h("span", { class: "muted" }, id.turn ? `turn ${id.turn}` : ""),
      h("span", { class: "muted" }, id.since ? since(id.since) : ""),
      h("span", { class: "muted" },
        id.populated ? `${id.model || "?"}/${id.effort || "?"}` : ""),
    ),
    id.why ? h("p", { class: "muted why" }, id.why) : null,
    feed(id),
    // What its cycles do is shown whether or not this reader may change them:
    // an agent nobody is waking looks fine until somebody says it is not being
    // woken, and that is worth knowing from a phone with no controls on it.
    h("div", { class: "controls" },
      actions && id.populated
        ? h("button", { class: "quiet", onclick: () => actions.poke(f, id) }, "poke…")
        : null,
      // Pacing is offered whether or not it is running: an agent that is down is
      // often exactly the one somebody wants to stop waking.
      actions ? h("button", { class: "quiet", onclick: () => actions.pace(f, "wake", id) }, "wake…") : null,
      actions ? h("button", { class: "quiet", onclick: () => actions.pace(f, "tend", id) }, "tend…") : null,
      paceNote(id),
    ),
  );
}

// feed is the last few things an agent did — the attach pane's rows, without the
// terminal.
//
// A blocked call is drawn as one, because it is the row anybody scanning this is
// looking for: an agent trying to do something it may not is worth more attention
// than ten it may.
function feed(id) {
  const rows = id.doing || [];
  if (rows.length === 0) {
    // Two different silences, and they must not look alike. A session with no feed
    // is one whose events could not be read; a session with an empty feed has done
    // nothing yet.
    if (id.populated && id.feed_read === false) {
      return h("p", { class: "muted" }, "its event feed could not be read");
    }
    if (id.populated) return h("p", { class: "muted" }, "nothing yet this session");
    return null;
  }
  return h("div", { class: "feed" }, ...rows.map(line));
}

function line(r) {
  const what = [r.tool, r.detail].filter(Boolean).join(" ");
  return h("div", { class: `feed-row${r.blocked ? " blocked" : ""}` },
    h("span", { class: "muted" }, r.kind),
    h("span", {}, what || "—"),
    r.blocked ? h("span", { class: "failed", title: r.reason || "" }, "refused") : null,
  );
}

// --- over time ------------------------------------------------------------

// WINDOWS are what the selector offers. The value is what the server parses, and
// the label is what a person calls it.
const WINDOWS = [
  { value: "6h", label: "6 hours" },
  { value: "48h", label: "2 days" },
  { value: "168h", label: "a week" },
  { value: "720h", label: "a month" },
];

// BLOCKS are the bars, on the character grid like everything else cq draws.
const BLOCKS = ["▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"];

// over is the whole lower half: the window selector, the charts, and what the
// fleet read and wrote.
function over(state, series, actions) {
  const names = Object.keys(series.identities).sort();
  const head = h("div", { class: "controls window" },
    h("span", { class: "muted" }, "over"),
    ...WINDOWS.map((w) => h("button", {
      class: w.value === state.activityWindow ? "" : "quiet",
      onclick: actions ? () => actions.setActivityWindow(w.value) : null,
    }, w.label)));

  if (series.error) {
    return [head, h("p", { class: "warn" }, `the series could not be read: ${series.error}`)];
  }
  if (names.length === 0) {
    // Three different nothings, and only one of them is a problem. This is the
    // benign pair: nothing has happened, or the agent machine's orc is older than
    // the measurement.
    return [head, h("p", { class: "muted" },
      "no measurements in this window — either nothing ran, or the agent machine's orc " +
      "predates the rollup")];
  }

  const all = names.flatMap((name) => series.identities[name] || []);
  return [
    head,
    chart("new tokens", byHour(all, (b) => tok(b).input + tok(b).output + tok(b).cache_create)),
    chart("cache reads", byHour(all, (b) => tok(b).cache_read)),
    chart("turns", byHour(all, (b) => b.turns || 0)),
    h("h3", {}, "what it read and wrote"),
    ...work(names, series),
  ];
}

// tok is a bucket's tokens, and is why nothing here reads `b.tokens` directly.
//
// The wire leaves an all-zero group out, and an hour of file work with no usage line
// is an ordinary bucket rather than a broken one. Reading the field straight turned
// that into a thrown error, which took the whole tab down over a bucket whose
// contribution to every figure on it was nought. `sumFiles` already did this and
// this is the same rule stated once.
function tok(b) {
  return b.tokens || {};
}

// byHour collapses every identity's buckets into one series, hour by hour.
//
// Keyed by the hour as the agent machine spelled it: two spellings of one instant
// would be two columns, and the string is what the merge keys on anyway.
function byHour(buckets, of) {
  const total = new Map();
  for (const b of buckets) {
    total.set(b.at, (total.get(b.at) || 0) + (of(b) || 0));
  }
  return [...total.entries()].sort((a, b) => (a[0] < b[0] ? -1 : 1));
}

// chart draws one series as blocks, with the largest value beside it.
//
// Scaled to its own maximum rather than to the others': the three series differ by
// orders of magnitude, and one scale across all of them would draw two of them flat.
// What that costs is comparability between charts, which is why each says its own
// peak in words.
function chart(label, points) {
  if (points.length === 0) return null;
  const peak = Math.max(...points.map(([, v]) => v));
  const bars = points.map(([at, v]) => h("span", {
    class: v > 0 ? "" : "muted",
    title: `${at} · ${number(v)}`,
  }, peak > 0 ? BLOCKS[Math.min(BLOCKS.length - 1, Math.round((v / peak) * (BLOCKS.length - 1)))] : "▁"));

  return h("div", { class: "chart" },
    h("span", { class: "chart-label muted" }, label),
    h("span", { class: "chart-bars" }, ...bars),
    h("span", { class: "chart-peak muted" }, `peak ${number(peak)}`));
}

// work is the per-agent table of files and lines.
//
// Lines are marked as coming from Claude's transcript, because they do and because
// a file count and a line count have different guarantees: the first is Orc's own
// record and always right, the second is missing wherever the transcript could not
// be read. An estimate that looks like a measurement is worse than no figure.
function work(names, series) {
  const rows = names.map((name) => {
    const files = sumFiles(series.identities[name] || []);
    return h("div", { class: "work-row" },
      h("span", { class: "name" }, name),
      h("span", {}, `${number(files.reads)} read`),
      h("span", {}, `${number(files.edits + files.writes)} written`),
      h("span", { class: "muted" }, `${number(files.read_lines)} lines read`),
      h("span", { class: "ok" }, `+${number(files.added + files.write_lines)}`),
      h("span", { class: "failed" }, `-${number(files.removed)}`));
  });
  return [
    h("div", { class: "work" }, ...rows),
    h("p", { class: "muted hint" },
      "files are counted by orc; lines come from claude's transcript and are missing " +
      "where it could not be read"),
  ];
}

function sumFiles(buckets) {
  const out = { reads: 0, edits: 0, writes: 0, read_lines: 0, added: 0, removed: 0, write_lines: 0 };
  for (const b of buckets) {
    for (const key of Object.keys(out)) out[key] += (b.files || {})[key] || 0;
  }
  return out;
}

// number renders a count with thin separators, because these are the figures nobody
// can read at a glance otherwise: 879281631 and 87928163 look alike.
function number(n) {
  const digits = String(Math.round(n || 0));
  if (digits.length < 5) return digits;
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

// --- pacing ---------------------------------------------------------------

// paceNote says what an agent's cycles are doing, beside the buttons that change
// them.
//
// Where each value came from is said too, because a number with no provenance sends
// somebody looking in the wrong layer for a setting they did not make on this agent.
function paceNote(id) {
  const pace = id.pace || {};
  const from = pace.from || {};
  const parts = [];
  if (pace.wake_off) parts.push("not woken");
  else if (pace.wake_after) parts.push(`woken after ${pace.wake_after}${layer(from.wake_after)}`);
  if (pace.tend_off) parts.push("not tended");
  else if (pace.tend_watch) parts.push(`tended every ${pace.tend_watch}${layer(from.tend_watch)}`);

  if (parts.length === 0) return h("span", { class: "muted" }, "default pace");
  return h("span", { class: pace.wake_off || pace.tend_off ? "pending" : "muted" }, parts.join(" · "));
}

// layer marks a value that came from somewhere other than this agent's own layer.
function layer(from) {
  if (!from || from === "identity") return "";
  return from === "role" ? " (role)" : " (fleet)";
}

// fleetPace is the row of controls for the fleet's own layer, which is what
// somebody is setting when they mean "all of them" rather than "this one".
export function fleetPace(f, actions) {
  if (!actions) return null;
  const pace = f.pace || {};
  const said = [];
  if (pace.wake_after) said.push(`wake after ${pace.wake_after}`);
  if (pace.wake_every) said.push(`look every ${pace.wake_every}`);
  if (pace.tend_watch) said.push(`tend every ${pace.tend_watch}`);

  return h("div", { class: "controls" },
    h("span", { class: "muted" }, "the fleet:"),
    h("button", { class: "quiet", onclick: () => actions.pace(f, "wake", null) }, "wake…"),
    h("button", { class: "quiet", onclick: () => actions.pace(f, "tend", null) }, "tend…"),
    // Sync is the third cycle and the odd one: it belongs to the link between the
    // two machines rather than to this fleet, so it is set at the server and is
    // not per agent. It sits here because this is where somebody asking "how often
    // does any of this happen" is already looking.
    h("button", { class: "quiet", onclick: () => actions.paceSync() }, "sync…"),
    h("span", { class: "muted" }, said.length ? said.join(" · ") : "nothing set — the built-in pace"),
  );
}

// --- what thinking costs --------------------------------------------------

// tariff is the price list, with what measurement suggests beside it.
//
// The suggestion is orc's, carried in the snapshot rather than worked out here from
// the same buckets: a second implementation of the normalisation would be a second
// opinion about what a fleet should charge, and the two would drift the first time
// either rounded differently.
export function tariff(f, actions) {
  const rows = f.tariff || [];
  if (rows.length === 0) return null;

  return h("details", { class: "cheatsheet" },
    h("summary", {}, "what thinking costs"),
    h("p", { class: "muted hint" },
      "a session costs model × effort; a set of n costs ⌈sum × (crowd-base + n) / crowd-scale⌉. " +
      "changing one re-prices every running session at once."),
    h("table", { class: "cheat" },
      h("tbody", {},
        ...rows.map((row) => h("tr", {},
          h("td", {}, row.setting),
          h("td", {}, String(row.weight)),
          // A suggestion is shown only where there is one: a combination nobody
          // ran proposes nothing rather than a number from none.
          h("td", { class: suggests(row) ? "pending" : "muted" },
            row.suggested ? `measured ${row.measured.toFixed(1)}× · suggests ${row.suggested}` : "not measured"),
          h("td", {},
            actions
              ? h("button", { class: "quiet", onclick: () => actions.tariff(f, row.setting) }, "price…")
              : null))))),
    h("p", { class: "muted hint" },
      "measurement counts new tokens only — a tariff that counted cache reads would price " +
      "context rather than work. it proposes; you decide."),
  );
}

// suggests reports whether measurement disagrees with what is charged, which is the
// only case worth colouring.
function suggests(row) {
  return Boolean(row.suggested) && row.suggested !== row.weight;
}

// --- what it came to ------------------------------------------------------

// productivity is the narrowest honest answer to "was any of this worth it".
//
// What this tree can count: turns, tool calls, files and lines, blocked calls,
// tasks completed with their difficulty, and mail sent. It can count none of what
// anybody actually means by productive, and the gap between those two lists is why
// this block is four numbers and a caveat rather than a score.
//
// One ratio — new tokens per completed task — and it is per *fleet*. Not per agent,
// on purpose: tokens per agent looks like a ranking and is not one. A hard task
// costs more than an easy one, and a fleet where the cheapest agent is the most
// rewarded is a fleet that will do the easy work.
export function productivity(state, f, series) {
  const window = hours(state.activityWindow);
  if (!window) return null;
  const since = Date.now() - window * 3600 * 1000;

  const done = (state.tasks || []).filter((t) => t.completed && after(t.completed_at, since));
  const sent = (state.sent || []).filter((m) => after(m.sent, since));

  let tokens = 0;
  let turns = 0;
  for (const buckets of Object.values(series.identities)) {
    for (const b of buckets) {
      tokens += (tok(b).input || 0) + (tok(b).output || 0) + (tok(b).cache_create || 0);
      turns += b.turns || 0;
    }
  }

  const per = done.length > 0 ? Math.round(tokens / done.length) : 0;
  return h("div", { class: "came-to" },
    h("h3", {}, "what it came to"),
    // Its own row rather than the file table's columns: four figures of very
    // different widths, which a fixed grid wraps into a shape nobody can read.
    h("div", { class: "came-row" },
      h("span", { class: "muted" }, "in this window"),
      h("span", {}, `${number(done.length)} tasks completed`),
      h("span", {}, `${number(sent.length)} messages sent`),
      h("span", {}, `${number(turns)} turns`),
    ),
    done.length > 0
      ? h("p", {}, `${number(per)} new tokens per completed task`)
      : h("p", { class: "muted" },
        "nothing was completed in this window, so there is no ratio to draw — " +
        "a cost with no denominator is a number, not a rate"),
    h("p", { class: "muted hint" },
      "per fleet, not per agent: tokens per agent looks like a ranking and is not one. " +
      "a hard task costs more than an easy one, and a fleet where the cheapest agent is " +
      "the most rewarded is a fleet that will do the easy work."),
  );
}

// hours reads the window the series was fetched for. It is the same window the
// charts are drawn over, so the two halves of the tab describe one stretch of time.
function hours(window) {
  const got = /^(\d+)h$/.exec(String(window || ""));
  return got ? Number(got[1]) : 0;
}

// after reports whether a timestamp is inside the window. An absent or unparseable
// one is *outside* it: counting a task whose completion time cannot be read would
// be inventing work.
function after(stamp, since) {
  if (!stamp) return false;
  const at = new Date(stamp).getTime();
  return Number.isFinite(at) && at >= since;
}
