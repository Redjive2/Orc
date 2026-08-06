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
// The series is read three ways, and they answer three different questions rather
// than being three views of one:
//
//   - **over time**, as charts, for the whole machine or one agent — is it busy,
//     and was it busier yesterday;
//   - **by agent**, as shares of one total — whose spend was that;
//   - **by model and effort** — what it was spent on, which is the only one of the
//     three with a command attached to the answer.
//
// The charts cannot do the last two. Each is fitted to its own maximum, so two
// series drawn at the same height mean different numbers — right for reading one
// thing over time and useless for reading two against each other.

import { h, since } from "./dom.js";
import { sessionAt } from "./routes.js";
import { perMachine, agents } from "./fleet.js";
import { cost, priced, money, PRICED } from "./prices.js";

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
        // The dashboard first, and the controls under it. This tab is opened to
        // find something out far more often than to change something, and the
        // charts are the answer to the question that brings anybody here; the
        // cycles are what you set once and then leave alone. They were on top
        // because they were built first, which is not a reason.
        ...over(state, series, actions),
        productivity(state, f, series),
        h("h3", {}, "who is doing what"),
        fleetPace(f, actions, state.syncPace),
        ...(list.length === 0
          ? [h("p", { class: "muted" }, "nobody yet")]
          : list.map((id) => row(f, id, actions))),
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
  // The period travels with the series because the server chose it, from the window
  // and from what the mirror actually holds at that age. A browser that guessed it
  // from the gaps between buckets would draw a quiet fortnight and a busy hour at
  // the same width.
  return { error: "", period: got.period || "", identities: found ? found.identities || {} : {} };
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
      // The whole session, rather than the last few events this row has room for.
      // A link and not a button: it goes somewhere, and it is worth having on a
      // reader with no controls at all.
      //
      // "attach" is orc's own word for joining a session, and this is that through
      // a different window.
      h("a", { class: "quiet button", href: sessionAt(f.machine, id.name) }, "attach"),
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
// The two short ones are the reason the reading is taken by the minute: "is it
// working right now" and "did that change help" are questions about the last few
// minutes, and every window here used to be too wide to answer either.
const WINDOWS = [
  { value: "15m", label: "15 min" },
  { value: "1h", label: "an hour" },
  { value: "6h", label: "6 hours" },
  { value: "48h", label: "2 days" },
  { value: "168h", label: "a week" },
  { value: "720h", label: "a month" },
];

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

  // Whose series the charts are drawn from. The whole machine by default; one
  // agent when somebody has picked one, and an agent that has since gone quiet
  // falls back to everyone rather than drawing an empty chart with a name on it.
  const who = names.includes(state.activityWho) ? state.activityWho : "";
  const shown = who ? (series.identities[who] || []) : all;

  // The axis stays the *machine's*, not the selection's. An agent that worked for
  // ten minutes of a two-day window should be drawn as ten busy minutes in two
  // quiet days — which is the fact somebody is looking for — and an axis fitted to
  // its own buckets would redraw those ten minutes as the whole chart.
  const slots = axis(state.activityWindow, series.period, all);

  // The scale key is the chart's label and deliberately not the agent's name, so a
  // ceiling set once holds while the selection moves. That is what makes the
  // charts comparable between agents: same height, same number. Left fitted they
  // scale to whichever agent is shown, and two agents cannot be told apart by eye
  // — which is why the panel below exists as well as this.
  const drawn = (label, of) =>
    chart(label, slots, shown, of, series.period, (state.chartScale || {})[label], actions);

  return [
    head,
    whose(names, who, actions),
    drawn("new tokens", (b) => tok(b).input + tok(b).output + tok(b).cache_create),
    drawn("cache reads", (b) => tok(b).cache_read),
    drawn("turns", (b) => b.turns || 0),
    h("h3", {}, "who is generating"),
    ...generation(names, series, state),
    // Whose spend, then what it was spent on. The second is the one with an
    // action attached, so it reads better after the first has said whose it was.
    h("h3", {}, who ? `what ${who} ran on` : "what it ran on"),
    ...onModels(shown, state, who),
    h("h3", {}, "what it read and wrote"),
    ...work(names, series),
  ];
}

// whose picks the agent the charts above are drawn for.
//
// A filter and not a fetch, unlike the window beside it: the route already answers
// per identity, so every agent's series is in the browser and choosing one costs
// nothing. It is the same distinction `chartScale` draws, and worth keeping visible
// — the window button is slow because it has to be, and this one is not.
function whose(names, who, actions) {
  if (names.length < 2) return null;
  const pick = (value, label) => h("button", {
    class: value === who ? "" : "quiet",
    "aria-pressed": value === who ? "true" : "false",
    onclick: actions ? () => actions.setActivityWho(value) : null,
  }, label);
  return h("div", { class: "controls window" },
    h("span", { class: "muted" }, "for"),
    pick("", "everyone"),
    ...names.map((name) => pick(name, name)));
}

// MaxSlots is the most columns a chart will lay out.
//
// The server picks a period for the window and aims well under this; the cap is here
// because the two can disagree — an old server, a hand-typed window — and a browser
// that trusted the arithmetic would lay out a hundred thousand elements rather than
// draw a chart it could not draw.
const MaxSlots = 400;

// axis is the timeline a chart is drawn on: every period in the window, whether
// anything happened in it or not.
//
// The gaps are the point. A series is only written where there was work, so drawing
// the buckets that exist puts four scattered minutes side by side as four full-height
// bars — a picture of continuous effort assembled from a quiet afternoon. Laying out
// the window and dropping the buckets into it is what makes the empty stretches
// visible as empty.
//
// It ends at the newest bucket rather than at now, when the newest is older: the
// series is as fresh as the last sync, and running the axis to the current instant
// would draw the delay as an outage.
function axis(window, period, buckets) {
  const step = span(period);
  if (!step) return [];
  let last = 0;
  for (const b of buckets) {
    const at = Date.parse(b.at);
    if (Number.isFinite(at) && at > last) last = at;
  }
  const end = last > 0 ? Math.min(Date.now(), last + step) : Date.now();
  const from = Math.floor((end - span(window)) / step) * step;
  const to = Math.floor(end / step) * step;
  const out = [];
  for (let t = from; t <= to && out.length < MaxSlots; t += step) out.push(t);
  return out;
}

// span reads a Go duration — "15m", "1h0m0s", "24h0m0s" — as milliseconds.
//
// Both ends of this wire spell durations that way: the window is what the button
// sent and the period is what the server chose, and neither is worth a second format
// to avoid one small parser.
function span(spec) {
  if (!spec) return 0;
  const units = { ms: 1, s: 1000, m: 60000, h: 3600000 };
  let total = 0;
  for (const [, n, unit] of String(spec).matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)) {
    total += Number(n) * units[unit];
  }
  return total;
}

// tok is a bucket's tokens, and is why nothing here reads `b.tokens` directly.
//
// The wire leaves an all-zero group out, and an hour of file work with no usage line
// is an ordinary bucket rather than a broken one. Reading the field straight turned
// that into a thrown error, which took the whole tab down over a bucket whose
// contribution to every figure on it was nought. `sumFiles` already did this and
// this is the same rule stated once.
function tok(b) {
  const got = b.tokens || {};
  // Each field coerced, because the readers add three of them together and one
  // missing field made the sum NaN — which `|| 0` then turned into a zero for the
  // *whole* bucket. An agent machine whose orc predates a counter would have drawn
  // an empty chart rather than a chart missing one term, and nothing on the screen
  // would have said which had happened.
  return {
    input: Number(got.input) || 0,
    output: Number(got.output) || 0,
    cache_create: Number(got.cache_create) || 0,
    cache_read: Number(got.cache_read) || 0,
  };
}

// into drops every identity's buckets onto the axis, summing what lands in each slot.
//
// Keyed by parsed instant rather than by the string the machine wrote: the slots are
// computed and the buckets are spelled, and two spellings of one moment would put a
// bar in a slot that is not there.
function into(slots, buckets, of) {
  const step = slots.length > 1 ? slots[1] - slots[0] : 0;
  const total = new Map(slots.map((t) => [t, 0]));
  for (const b of buckets) {
    const at = Date.parse(b.at);
    if (!Number.isFinite(at)) continue;
    const slot = step > 0 ? Math.floor(at / step) * step : at;
    if (!total.has(slot)) continue;
    total.set(slot, total.get(slot) + (of(b) || 0));
  }
  return slots.map((t) => total.get(t));
}

// chart draws one series as bars with real height and a colour that runs with the
// value.
//
// Scaled to its own maximum rather than to the others': the three series differ by
// orders of magnitude, and one scale across all of them would draw two of them flat.
// What that costs is comparability between charts, which is why each says its own
// peak in words.
//
// The colour carries the same number as the height, which is deliberate redundancy
// rather than decoration. A bar an eighth of the way up a short chart is hard to
// judge against its neighbours; the same bar in green against a red one is not. It
// runs green through amber to red because that is what the reading means here —
// spend — and nobody has to be told which end is which.
function chart(label, slots, buckets, of, period, ceiling, actions) {
  if (slots.length === 0) return null;
  const values = into(slots, buckets, of);
  const peak = Math.max(...values);
  const step = slots.length > 1 ? slots[1] - slots[0] : span(period);
  // The ceiling is whatever was set for this chart, and a fitted one when nothing
  // was. Fitting to the *peak* was the old default and it made spiky data
  // unreadable — which is most real data here, because one compaction writes a
  // cache in a minute and an ordinary minute does not.
  //
  // Measured on a real 48 hours: one spike a hundred times the rest put 47 of 48
  // bars at exactly the 4% floor. Two distinct heights on the whole chart, in
  // nearly one colour, and every question somebody opens this to ask — when was it
  // busy, is it busier than yesterday, did that agent ever stop — unanswerable.
  // Setting a ceiling by hand fixed it, but only for somebody who already knew
  // roughly what number to type, which is the thing they came here to find out.
  const fitted = ceiling > 0 ? 0 : fit(values);
  const top = ceiling > 0 ? ceiling : (fitted || peak);

  const bars = values.map((v, i) => {
    const share = top > 0 ? v / top : 0;
    // A bar past the ceiling is clipped and marked. Drawing it at full height like
    // an ordinary maximum would be the scale quietly lying about the number it was
    // set to exclude.
    const over = v > top;
    return h("span", {
      class: `bar${v > 0 ? "" : " empty"}${over ? " over" : ""}`,
      // Every bar says its own slot and its own number. There is no room for an
      // axis label per column and no honest way to leave the reading out — a
      // shape without figures is a mood.
      title: `${when(slots[i], step)} · ${number(v)}${over ? ` (over the ${number(top)} ceiling)` : ""}`,
      // A floor under any bar that is not zero, because the interesting minute on
      // a chart with one huge peak is usually one of the small ones, and a bar
      // rounded to nothing is indistinguishable from an idle slot. An empty slot
      // carries no inline style at all and takes its sliver from the sheet.
      // Painted, not sized. The height is a gradient stop rather than the element's
      // own height, because a percentage height on one of these resolves to zero on
      // WebKit — see the note in app.css. `--fill` carries the number so there is
      // still one place it is written down.
      //
      // An object, applied through the CSSOM. This site's content policy is
      // `default-src 'self'` with no `unsafe-inline`, so a `style` *attribute* is
      // discarded by the browser without a word — which is how these bars went on
      // being invisible after they had been given a shape that works. See dom.js.
      style: v > 0
        ? {
          "--fill": `${Math.min(100, Math.max(4, share * 100)).toFixed(1)}%`,
          background: `linear-gradient(to top,${heat(share)} 0 var(--fill),transparent var(--fill))`,
        }
        : null,
    });
  });

  return h("div", { class: "chart" },
    h("div", { class: "chart-head" },
      h("span", { class: "chart-label" }, label),
      h("span", { class: "chart-scale muted" },
        ceiling > 0
          ? `top ${number(ceiling)}`
          : fitted
            ? `top ${number(fitted)} · peak ${number(peak)}`
            : `peak ${number(peak)}`,
        ` per ${every(step)}`),
      actions
        ? h("button", {
          class: "quiet",
          onclick: () => actions.setChartScale(label, ceiling, peak),
        }, "scale…")
        : null),
    h("div", { class: "chart-bars" }, ...bars),
    h("div", { class: "chart-axis muted" },
      h("span", {}, when(slots[0], step)),
      // The ceiling belongs on the axis and not only in the heading: it is the one
      // number that says what a full-height bar means, and a chart read without it
      // is a shape.
      h("span", {}, top > 0 && peak > top
        ? (ceiling > 0
          ? `clipped at ${number(ceiling)} — peak was ${number(peak)}`
          : `fitted to ${number(fitted)} — ${clipped(values, fitted)} above it, peak ${number(peak)}`)
        : ""),
      h("span", {}, when(slots[slots.length - 1], step))));
}

// fit chooses a ceiling that leaves the ordinary values readable, or 0 to say the
// peak is a fine one.
//
// The rule is the one an eye applies: a value far above the bulk of the data is an
// outlier, and a scale that accommodates it describes the outlier rather than the
// data. So the bulk is measured — the 95th percentile of the slots that have
// anything in them — and the peak is only refused as a ceiling when it towers over
// that. Below OutlierRatio the peak *is* the shape of the data and fitting to it is
// right, which is why this returns 0 there rather than always clipping something.
//
// Non-zero slots only. An idle night is most of a 48-hour window and counting those
// zeroes into the percentile would make the ceiling a statement about how long the
// agent was asleep.
//
// Nothing is hidden by this. The peak stays in the heading, the axis says what was
// fitted and how many bars are above it, and each of those bars is drawn clipped
// and marked and says its own number when pointed at.
const OutlierRatio = 4;
const FitQuantile = 0.95;

function fit(values) {
  const busy = values.filter((v) => v > 0).sort((a, b) => a - b);
  if (busy.length < 4) return 0;

  const peak = busy[busy.length - 1];
  const at = busy[Math.min(busy.length - 1, Math.floor(busy.length * FitQuantile))];
  // The quantile can land on the outlier itself when there are several of them, in
  // which case there is no bulk to protect and the peak is the honest scale.
  if (at <= 0 || peak <= at * OutlierRatio) return 0;
  return at;
}

// clipped says how many bars stand above a fitted ceiling, in words, because "one
// bar is above this" and "half the chart is" are different situations and a reader
// deciding whether to trust the scale needs to know which they are looking at.
function clipped(values, top) {
  const over = values.filter((v) => v > top).length;
  return over === 1 ? "1 bar" : `${over} bars`;
}

// heat runs green to red across the chart's own range.
const heat = (share) =>
  `hsl(${Math.round(140 - 140 * Math.min(1, Math.max(0, share)))} 58% 45%)`;

// when spells a slot, at the precision the slot is worth.
//
// A minute-wide bar wants a clock and a day-wide bar wants a date, and printing
// both on either is how an axis stops being readable at a glance.
function when(at, step) {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return String(at);
  const clock = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (step >= 6 * 3600000) {
    return `${d.toLocaleDateString([], { day: "numeric", month: "short" })} ${clock}`;
  }
  return clock;
}

// every names the bar width in words, so "peak 40,000" says what it is a peak of.
// The same number over a minute and over a day are different facts.
function every(step) {
  if (step >= 86400000 && step % 86400000 === 0) {
    const days = step / 86400000;
    return days === 1 ? "day" : `${days} days`;
  }
  if (step >= 3600000 && step % 3600000 === 0) {
    const hours = step / 3600000;
    return hours === 1 ? "hour" : `${hours}h`;
  }
  const minutes = Math.round(step / 60000);
  return minutes === 1 ? "minute" : `${minutes}m`;
}

// work is the per-agent table of files and lines.
//
// Lines are marked as coming from Claude's transcript, because they do and because
// a file count and a line count have different guarantees: the first is Orc's own
// record and always right, the second is missing wherever the transcript could not
// be read. An estimate that looks like a measurement is worse than no figure.
// generation is what each agent cost, side by side.
//
// The charts above cannot answer this and are not meant to. Each one is fitted to
// its own maximum, so a quiet agent and a busy one are drawn the same height with
// different numbers under them — which is right for reading one series over time
// and useless for reading two against each other. This panel is the other
// question: of everything the machine spent, whose was it?
//
// So the bar is a **share of the machine's total**, which is comparable by
// construction — every row is drawn against the same denominator, and no scale
// setting can make two rows mean different things. The figures are beside it
// because a share is a proportion of an amount nobody has been told yet, and
// because colour and length are never the only thing carrying a number here.
function generation(names, series, state) {
  const rows = names
    .map((name) => ({ label: name, ...sumTokens(series.identities[name] || []) }))
    .sort((a, b) => b.fresh - a.fresh || a.label.localeCompare(b.label));

  return shareTable("agents", rows, state, "nothing was generated in this window",
    "new tokens are input, output and cache writes — what the turn caused to be " +
    "produced. cache reads are left out of that figure and of the share: they run " +
    "orders of magnitude larger and would make every row a reading of context " +
    "size. the money does count them, at the tenth-rate they are billed at, " +
    "because they are a real cost. the rate is across the whole window, so an " +
    "agent that worked in one burst reads low.");
}

// onModels is the same spend split the other way: by what was thinking rather
// than by who.
//
// It is the question the panel above cannot answer and the one that decides what
// to do about a number. An agent at 80% of the machine's tokens is a fact with no
// action attached; the same agent at 80% *on opus at high effort* has an obvious
// one, and `orc model` is the command. Every bucket has carried the pair all
// along — orc keys its rollups on them — and nothing had ever shown it.
//
// It follows the selection above, so picking an agent turns this into that agent's
// own mix. That is the pairing worth having: whose spend, then what they spent it
// on.
function onModels(buckets, state, who) {
  const by = new Map();
  for (const b of buckets) {
    // Absent is not "unknown model", it is a reading taken before orc recorded
    // one — so it says that rather than inventing a name or dropping the tokens,
    // which would leave these shares adding up to less than the panel above.
    const label = b.model ? `${b.model}${b.effort ? `/${b.effort}` : ""}` : "not recorded";
    if (!by.has(label)) {
      by.set(label, { label, fresh: 0, output: 0, cacheRead: 0, turns: 0, spend: 0, unpriced: 0 });
    }
    const into = by.get(label);
    const t = tok(b);
    into.fresh += t.input + t.output + t.cache_create;
    into.output += t.output;
    into.cacheRead += t.cache_read;
    into.turns += b.turns || 0;
    const spent = cost(t, b.model);
    if (spent === null) into.unpriced += 1;
    else into.spend += spent;
  }

  const rows = [...by.values()].sort((a, b) => b.fresh - a.fresh || a.label.localeCompare(b.label));
  return shareTable("models", rows, state, "nothing ran in this window",
    `what each model and effort cost, for ${who || "the whole machine"}. ` +
    "`orc model <agent> <model> --effort <e>` is what changes it.");
}

// shareTable draws labelled rows against one denominator.
//
// One implementation for both panels, because they are the same table asked of two
// different groupings, and two copies would drift — a share computed one way
// beside a share computed another, on one screen, under headings that do not say
// they differ.
//
// The bar is a share of the rows' own total, so it compares by construction: every
// row is drawn against the same number, and no scale setting can make two of them
// mean different things.
// `kind` names which grouping this is, and reaches the markup. Two tables with
// identical rows and no way to tell them apart is a screen where "the top row" is
// ambiguous — to a reader skimming, and to anything reading the page.
function shareTable(kind, rows, state, empty, note) {
  const kept = rows.filter((r) => r.turns > 0 || r.fresh > 0);
  if (kept.length === 0) return [h("p", { class: "muted" }, empty)];

  const total = kept.reduce((sum, r) => sum + r.fresh, 0);
  // Per hour across the whole window, idle time included. That is the honest
  // denominator for "what is this costing me a day" and the wrong one for "how
  // hard does it work when it works" — something that burned its whole budget in
  // ten minutes of a two-day window reads as almost nothing here. The sentence
  // under the rows says so rather than leaving it to be discovered.
  const hours = span(state.activityWindow) / 3600000;

  return [
    h("div", { class: `gen ${kind}` }, ...kept.map((r) => {
      const share = total > 0 ? r.fresh / total : 0;
      return h("div", { class: "gen-row" },
        h("span", { class: "name" }, r.label),
        h("span", {
          class: "gen-bar",
          // The reading in words, on the bar itself, so a shape somebody cannot
          // measure by eye still says its own number on hover and to a reader.
          title: `${r.label}: ${(share * 100).toFixed(1)}% of ${number(total)} new tokens`,
          // Painted rather than sized, and through the CSSOM rather than as a
          // `style` attribute — the same two reasons as the bars in `chart`: a
          // percentage height does not resolve on WebKit here, and the content
          // policy discards the attribute without a word. See dom.js.
          style: {
            "--fill": `${Math.max(1, share * 100).toFixed(1)}%`,
            background: "linear-gradient(to right,var(--primary) 0 var(--fill),transparent var(--fill))",
          },
        }),
        // A row that did work must never read as `0%`. Rounding puts every small
        // one there — 0.4% of a busy machine is still thousands of tokens — and a
        // share of nothing beside a figure of something is the row contradicting
        // itself.
        h("span", { class: "gen-share" },
          share > 0 && share < 0.005 ? "<1%" : `${Math.round(share * 100)}%`),
        h("span", {}, `${number(r.fresh)} new`),
        h("span", { class: "muted" }, `${number(r.output)} out`),
        h("span", { class: "muted" }, hours > 0 ? `${number(r.fresh / hours)}/h` : ""),
        h("span", { class: "muted" }, `${number(r.turns)} ${r.turns === 1 ? "turn" : "turns"}`),
        // Money last, because it is the figure a reader stops on — and it is a
        // conversion of the tokens beside it rather than a separate measurement,
        // so it reads better after the thing it was computed from.
        h("span", {
          class: r.unpriced > 0 ? "muted" : "",
          title: r.unpriced > 0
            ? `${r.unpriced} ${r.unpriced === 1 ? "reading" : "readings"} on a model with no published rate — not counted`
            : "at list api rates",
        }, r.unpriced > 0 && r.spend === 0 ? "not priced" : money(r.spend)));
    })),
    spendLine(kept),
    h("p", { class: "muted hint" }, note),
  ];
}

// spendLine is the money line under a share table.
//
// Separate from the rows because it is the figure somebody came for, and because
// it is the only place the caveats fit: what the prices are, when they were
// taken, and how much of the window could not be priced at all. A total printed
// bare would be read as a bill.
function spendLine(rows) {
  const spend = rows.reduce((sum, r) => sum + r.spend, 0);
  const missing = rows.reduce((sum, r) => sum + r.unpriced, 0);

  // Nothing priceable at all is not "$0" — that is the module's whole point
  // stated as a total. A fleet whose every model is unknown to the price list
  // has spent an unknown amount, and saying zero would be the one reading that
  // is certainly wrong.
  const parts = [spend === 0 && missing > 0
    ? "nothing here could be priced"
    : `${money(spend)} at list api rates`];
  if (missing > 0) {
    // Named rather than folded in as zero. An unpriced model added as nothing
    // would make a fleet look cheaper the more it used the model nobody had a
    // rate for — which is the wrong way round for every decision made from this.
    parts.push(`${number(missing)} ${missing === 1 ? "reading" : "readings"} on unpriced models, not counted`);
  }
  parts.push(`prices as of ${PRICED}`);

  return h("p", { class: "spend" }, parts.join(" · "));
}

// sumTokens totals one agent's buckets. `fresh` is activity.Tokens.New — input,
// output and cache writes — under a name that cannot be read as "recent".
function sumTokens(buckets) {
  const out = { fresh: 0, output: 0, cacheRead: 0, turns: 0, spend: 0, unpriced: 0 };
  for (const b of buckets) {
    const t = tok(b);
    out.fresh += t.input + t.output + t.cache_create;
    out.output += t.output;
    out.cacheRead += t.cache_read;
    out.turns += b.turns || 0;
    // Priced per bucket rather than per row, because a row is one agent and an
    // agent's buckets span models: the same tokens cost five times as much on
    // opus as on haiku, so a row-level rate would be an average of whatever mix
    // happened to run.
    const spent = cost(t, b.model);
    if (spent === null) out.unpriced += 1;
    else out.spend += spent;
  }
  return out;
}

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
export function fleetPace(f, actions, sync) {
  if (!actions) return null;
  const pace = f.pace || {};
  const said = [];
  if (pace.wake_off) said.push("not woken");
  else if (pace.wake_after) said.push(`wake after ${pace.wake_after}`);
  if (pace.wake_every) said.push(`look every ${pace.wake_every}`);
  if (pace.tend_off) said.push("not tended");
  else if (pace.tend_watch) said.push(`tend every ${pace.tend_watch}`);
  // Beside the other two rather than only inside its own dialog. All three are the
  // answer to one question — how often does any of this happen — and an interval
  // somebody has to open a form to read is one they cannot check at a glance.
  if (sync && sync.watch) said.push(`sync every ${sync.watch}`);
  else if (sync) said.push("sync as each watcher was started");

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
