import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const view = await import("../activity.js");

// `manage › activity` answers "is anything happening", which the browser could not
// answer before: it knew `employed` and a session id and inferred the rest, so one
// label — "employed, not running" — covered a start being paced, a killed
// supervisor, and an agent nobody had employed.
//
// Orc decides the state and it travels. What is worth pinning here is that the tab
// *shows what it was sent* — an ordering that puts trouble first, a state this
// build has never heard of surviving, and the two kinds of silence not looking
// alike.

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function text(nodes) {
  return nodes.map((n) => n.textContent).join("\n");
}

function fleet(...identities) {
  return {
    fleet: [{
      machine: "sandy",
      operator: "rdm",
      identities: [{ name: "rdm", operator: true, activity: "idle" }, ...identities],
    }],
  };
}

const working = {
  name: "ember", activity: "generating", turn: 24, populated: true,
  model: "opus", effort: "high", since: new Date().toISOString(),
  doing: [
    { at: new Date().toISOString(), kind: "action", tool: "Edit", detail: "internal/cli/wake.go" },
  ],
};

test("an agent's state is shown as the word orc sent", () => {
  const drawn = view.activity(fleet(working), null);
  assert.match(text(drawn), /generating/);
  assert.match(text(drawn), /turn 24/);
});

test("what it is doing is shown, tool and path", () => {
  const drawn = view.activity(fleet(working), null);
  const got = text(drawn);
  assert.match(got, /Edit/);
  assert.match(got, /internal\/cli\/wake\.go/);
});

// The row somebody has to act on must not be the one they scroll to.
test("trouble sorts above work, and work above rest", () => {
  const drawn = view.activity(fleet(
    { name: "quiet", activity: "waiting" },
    working,
    { name: "gone", activity: "down", why: "start failed 2 times" },
    { name: "wedged", activity: "stuck" },
  ), null);

  const names = all(drawn[0], (n) => (n.className || "") === "name").map((n) => n.textContent);
  assert.deepEqual(names, ["gone", "wedged", "ember", "quiet"]);
});

test("a down agent says why", () => {
  const drawn = view.activity(fleet({ name: "gone", activity: "down", why: "start failed 2 times" }), null);
  assert.match(text(drawn), /start failed 2 times/);
});

// A refusal is the row anybody scanning this is looking for.
test("a blocked call is drawn as refused", () => {
  const drawn = view.activity(fleet({
    ...working,
    doing: [{ at: "", kind: "action", tool: "Write", detail: "/etc/passwd", blocked: true, reason: "outside the workspace" }],
  }), null);
  assert.match(text(drawn), /refused/);
});

// Two different silences, and a reader has to be able to tell them apart.
test("no events yet reads differently from a feed that would not open", () => {
  const nothing = text(view.activity(fleet({ name: "new", activity: "waiting", populated: true, doing: [] }), null));
  assert.match(nothing, /nothing yet this session/);

  const broken = text(view.activity(fleet(
    { name: "new", activity: "waiting", populated: true, doing: [], feed_read: false }), null));
  assert.match(broken, /could not be read/);
});

// An older orc sends no state at all. Drawing "0 of 8 working" over a busy fleet
// would be worse than saying so.
test("a fleet with no states says so rather than reporting nothing works", () => {
  const drawn = view.activity(fleet({ name: "ember", populated: true }), null);
  const got = text(drawn);
  assert.match(got, /unknown/);
  assert.match(got, /orc is older/);
});

// A newer orc may grow a sixth state. It should read as unfamiliar, not as absent.
test("a state this build has never heard of survives", () => {
  const drawn = view.activity(fleet({ name: "ember", activity: "compacting" }), null);
  assert.match(text(drawn), /compacting/);
});

test("the summary counts what is working", () => {
  const drawn = view.activity(fleet(working, { name: "quiet", activity: "waiting" }), null);
  assert.match(text(drawn), /1 of 2 working/);
  assert.match(text(drawn), /1 waiting/);
});

// The operator is not an agent: it runs no session and would sit at the top of
// every list as a row nobody can act on.
test("the operator is not listed", () => {
  const drawn = view.activity(fleet(working), null);
  const names = all(drawn[0], (n) => (n.className || "") === "name").map((n) => n.textContent);
  assert.deepEqual(names, ["ember"]);
});

// Read-only for now, except the one control that already queues something. The
// window selector is a button too, so this asks about the row's controls rather
// than about every button on the page.
test("only a running agent is offered a poke", () => {
  const pokes = (state) =>
    view.activity(state, { poke() {}, setActivityWindow() {} })
      .flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "poke…")).length;

  assert.equal(pokes(fleet(working)), 1);
  assert.equal(pokes(fleet({ name: "gone", activity: "down" })), 0);
});

// --- the series -----------------------------------------------------------

// The lower half of the tab is history, and history is the one thing a snapshot
// cannot carry: a snapshot is replaced whole on every sync. So it comes from its
// own route, and what is worth pinning is that the tab tells apart the three
// different ways it can be empty.

function withSeries(identities, extra = {}) {
  return {
    activityWindow: "48h",
    activity: { period: "1h0m0s", machines: [{ machine: "sandy", identities }] },
    ...fleet(working),
    ...extra,
  };
}

// slots is one chart's columns — the first one drawn. Every chart shares an axis,
// so counting across all three would be counting the same window three times.
function slots(nodes) {
  const rows = nodes.flatMap((n) => all(n, (x) => x.className === "chart-bars"));
  return rows.length === 0 ? [] : all(rows[0], (x) => x.className && x.className.startsWith("bar"));
}

// Relative to now, and it has to be: the axis is the window ending at the newest
// bucket, so a fixture pinned to a date in 2026 would fall outside every window the
// moment the calendar moved past it. Ordering is preserved — hour(10) is still
// before hour(11) — so the assertions below read the same as they did.
const anchor = Math.floor(Date.now() / 3600000) * 3600000 - 20 * 3600000;
const hour = (n) => new Date(anchor + (n - 10) * 3600000).toISOString();

const busy = [
  { at: hour(10), turns: 3, tokens: { input: 10, output: 90, cache_create: 100, cache_read: 5000 },
    files: { reads: 2, read_lines: 400, edits: 1, added: 12, removed: 3 } },
  { at: hour(11), turns: 5, tokens: { input: 20, output: 180, cache_create: 300, cache_read: 9000 },
    files: { writes: 1, write_lines: 60 } },
];

test("the series is drawn as charts with their own peaks", () => {
  const got = text(view.activity(withSeries({ ember: busy }), null));
  assert.match(got, /new tokens/);
  assert.match(got, /cache reads/);
  assert.match(got, /turns/);
  // Each chart says its own peak, because they are scaled separately and a reader
  // comparing two charts needs to know they are not on one scale.
  assert.match(got, /peak/);
});

// The gaps are half of what a chart says. Drawing only the buckets that exist put
// two scattered hours side by side as two full bars — a picture of continuous work
// assembled out of a quiet day.
test("a chart draws the whole window, not only the buckets that exist", () => {
  const bars = slots(view.activity(withSeries({ ember: busy }), null));
  // Two days at an hour each, and two of them have anything in them.
  assert.ok(bars.length > 40, `only ${bars.length} slots for a 48h window`);
  assert.equal(bars.filter((b) => b.className === "bar").length, 2);
  assert.ok(bars.some((b) => b.className === "bar empty"), "no empty slots were drawn");
});

// Height and colour carry the same number on purpose: a bar an eighth of the way up
// a short chart is hard to judge against its neighbours, and green against red is
// not. Blocks on the text grid had eight heights inside one line and read as flat.
test("a bar has a real height and a colour that runs with it", () => {
  const bars = slots(view.activity(withSeries({ ember: busy }), null))
    .filter((b) => b.className === "bar")
    .map((b) => b.attributes.get("style"));
  assert.equal(bars.length, 2);
  // The taller of the two is the peak, and the peak is at the red end.
  const heights = bars.map((s) => Number(/--fill:([\d.]+)%/.exec(s)[1]));
  assert.equal(Math.max(...heights), 100);
  assert.ok(Math.min(...heights) < 100 && Math.min(...heights) >= 4,
    `a non-peak bar was ${Math.min(...heights)}%`);
  const hues = bars.map((s) => Number(/hsl\((\d+)/.exec(s)[1]));
  assert.equal(Math.min(...hues), 0, "the peak is not at the red end");
  assert.ok(Math.max(...hues) > 0, "a smaller bar is not further towards green");
});

// Fitting to the peak is the right default and a poor fixed rule: it re-scales on
// every sync, so the same height means a different number a minute later, and two
// windows of one quantity cannot be put side by side.
test("a set ceiling is what bars are drawn against, not the peak", () => {
  const state = withSeries({ ember: busy });
  // The peak of "new tokens" across `busy` is 500; half that clips the taller bar.
  const bars = slots(view.activity({ ...state, chartScale: { "new tokens": 250 } }, null))
    .filter((b) => b.className.startsWith("bar") && b.className !== "bar empty");
  const heights = bars.map((b) => Number(/--fill:([\d.]+)%/.exec(b.attributes.get("style"))[1]));
  // 200 of 250 is 80%, and 500 is clipped to the ceiling rather than drawn past it.
  assert.ok(heights.includes(100), `heights were ${heights}`);
  assert.ok(heights.some((n) => n > 75 && n < 85), `heights were ${heights}`);
});

// A scale set to exclude a spike must not restate the spike as the largest ordinary
// thing on the chart, which is the one thing the person who set it was stopping.
test("a bar over the ceiling is marked as clipped, and the chart says so", () => {
  const drawn = view.activity({ ...withSeries({ ember: busy }), chartScale: { turns: 3 } }, null);
  // Across every chart, because only the one that was given a ceiling should have
  // a clipped bar and this is what checks the others were left alone.
  const over = drawn.flatMap((n) => all(n, (x) => (x.className || "").includes("bar over")));
  assert.equal(over.length, 1, "expected exactly the one bar past the ceiling");
  assert.match(text(drawn), /clipped at 3 — peak was 5/);
});

test("a ceiling can be set per chart and offered from the chart", () => {
  let asked = null;
  const drawn = view.activity(withSeries({ ember: busy }), {
    setChartScale: (label, ceiling, peak) => { asked = { label, ceiling, peak }; },
    poke() {}, pace() {}, paceSync() {}, setActivityWindow() {},
  });
  const buttons = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "scale…"));
  assert.equal(buttons.length, 3, "one control per chart");
  for (const fn of buttons[2].listeners.click) fn({});
  // Opened on this chart, and told what the peak is, so the form can say what a
  // ceiling would be relative to.
  assert.deepEqual(asked, { label: "turns", ceiling: undefined, peak: 5 });
});

// A period this build cannot read is not a reason to lay out a hundred thousand
// elements, or none: the server picks it, and the two can disagree.
test("a series with no period draws no chart rather than an infinite one", () => {
  const state = withSeries({ ember: busy });
  state.activity.period = "";
  const got = view.activity(state, null);
  assert.equal(slots(got).length, 0);
  // And the rest of the tab survives it.
  assert.match(text(got), /what it read and wrote/);
});

test("what was read and written is totalled per agent", () => {
  const got = text(view.activity(withSeries({ ember: busy }), null));
  assert.match(got, /2 read/);
  assert.match(got, /2 written/);   // one edit and one write
  assert.match(got, /400 lines read/);
  assert.match(got, /\+72/);        // 12 added + 60 written
  assert.match(got, /-3/);
});

// The guarantee behind the numbers is different for the two halves, and the tab
// says so rather than letting a line count pass for a measurement.
test("the tab says where files and lines come from", () => {
  const got = text(view.activity(withSeries({ ember: busy }), null));
  assert.match(got, /files are counted by orc/);
  assert.match(got, /claude's transcript/);
});

// The wire leaves an all-zero group out, so an hour of file work with no usage line
// arrives with no `tokens` at all. Reading through it threw, and one such bucket took
// down every chart, the totals and the ratio — over an hour contributing nought to
// all of them.
test("a bucket with nothing to say about tokens does not take the tab down", () => {
  const bare = [
    { at: hour(10), files: { reads: 1, read_lines: 40 } },
    { at: hour(11), turns: 2, tokens: { output: 500 }, files: {} },
    { at: hour(12), turns: 1 },
  ];
  const got = text(view.activity(withSeries({ ember: bare }), null));
  assert.match(got, /new tokens/);
  assert.match(got, /1 read/);
  assert.match(got, /40 lines read/);
});

// The tab is opened to find something out far more often than to change something,
// and the charts are the answer to the question that brings anybody here. The cycles
// were on top because they were built first.
test("the charts come before the controls that pace the fleet", () => {
  const drawn = view.activity(withSeries({ ember: busy }),
    { pace() {}, poke() {}, paceSync() {}, setActivityWindow() {} });
  const found = drawn.flatMap((n) => all(n,
    (x) => x.className === "chart-bars" || x.className === "controls"));
  assert.ok(found.length >= 2, "expected both the charts and the pace controls");
  assert.equal(found[0].className, "chart-bars", "the pace controls came first");
});

test("the window selector offers the windows the server takes", () => {
  const buttons = view.activity(withSeries({ ember: busy }), { setActivityWindow() {} })
    .flatMap((n) => all(n, (x) => x.tagName === "BUTTON")).map((b) => b.textContent);
  for (const label of ["6 hours", "2 days", "a week", "a month"]) {
    assert.ok(buttons.includes(label), `${label} missing from ${buttons.join(",")}`);
  }
});

test("choosing a window asks for it rather than filtering what is there", () => {
  let asked = null;
  const drawn = view.activity(withSeries({ ember: busy }), {
    setActivityWindow: (w) => { asked = w; },
  });
  const week = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "a week"))[0];
  for (const fn of week.listeners.click) fn({});
  assert.equal(asked, "168h", "the window was not requested from the server");
});

// Three ways to be empty, and only one of them is a problem.
test("an empty series says which kind of empty it is", () => {
  const nothing = text(view.activity(withSeries({}), null));
  assert.match(nothing, /no measurements in this window/);
  assert.match(nothing, /predates the rollup/);

  const broken = text(view.activity({
    ...fleet(working), activityWindow: "48h",
    activity: { unreachable: "the series could not be fetched" },
  }, null));
  assert.match(broken, /could not be read/);
});

// A machine with no series at all must not draw another machine's.
test("a machine only draws its own series", () => {
  const state = {
    ...fleet(working), activityWindow: "48h",
    activity: { machines: [{ machine: "other", identities: { ember: busy } }] },
  };
  const got = text(view.activity(state, null));
  assert.match(got, /no measurements in this window/);
});

// --- pacing ---------------------------------------------------------------

// The controls are the half of the tab that changes something. What is worth
// pinning is that each one names the layer it edits: a form that quietly wrote the
// fleet's setting when somebody meant one agent would be the worst kind of wrong,
// because it would look like it worked.

const paced = {
  name: "ember", activity: "waiting", populated: true,
  pace: { wake_after: "5m", tend_watch: "1m", from: { wake_after: "identity", tend_watch: "system" } },
};

test("an agent says what its cycles do, and where each came from", () => {
  const got = text(view.activity(fleet(paced), null));
  assert.match(got, /woken after 5m/);
  assert.match(got, /tended every 1m \(fleet\)/);
});

test("a paused agent looks different from a quiet one", () => {
  const got = text(view.activity(fleet({ ...paced, pace: { wake_off: true } }), null));
  assert.match(got, /not woken/);
});

test("pacing an agent names that agent", () => {
  let asked = null;
  const drawn = view.activity(fleet(paced), {
    pace: (f, cycle, who) => { asked = { cycle, who: who && who.name }; },
    poke() {}, setActivityWindow() {},
  });
  const button = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "wake…"))[1];
  for (const fn of button.listeners.click) fn({});
  assert.deepEqual(asked, { cycle: "wake", who: "ember" });
});

// The fleet's own layer is a different button, and it must not be the same one.
test("pacing the fleet names nobody", () => {
  let asked = null;
  const drawn = view.activity(fleet(paced), {
    pace: (f, cycle, who) => { asked = { cycle, who: who && who.name }; },
    poke() {}, setActivityWindow() {},
  });
  const button = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "wake…"))[0];
  for (const fn of button.listeners.click) fn({});
  assert.deepEqual(asked, { cycle: "wake", who: null });
});

// An agent that is down is often exactly the one somebody wants to stop waking, so
// the pacing buttons are offered whether or not it is running.
test("a down agent can still be paced", () => {
  const drawn = view.activity(fleet({ name: "gone", activity: "down" }), {
    pace() {}, poke() {}, setActivityWindow() {},
  });
  const labels = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON")).map((b) => b.textContent);
  assert.ok(labels.filter((l) => l === "wake…").length >= 2, labels.join(","));
  assert.ok(!labels.includes("poke…"), "a down agent was offered a poke");
});

test("a fleet with nothing set says so rather than showing blanks", () => {
  const state = { fleet: [{ machine: "sandy", operator: "rdm", identities: [{ name: "ember", activity: "idle" }] }] };
  const got = text(view.activity(state, { pace() {}, poke() {}, setActivityWindow() {} }));
  assert.match(got, /nothing set — the built-in pace/);
  assert.match(got, /default pace/);
});

// All three cycles are one question — how often does any of this happen — and the
// answer has to be readable without opening the forms that set it.
test("the fleet says what its own cycles do", () => {
  const state = {
    fleet: [{
      machine: "sandy", operator: "rdm",
      pace: { wake_after: "20m", wake_every: "5m", tend_watch: "30s" },
      identities: [{ name: "ember", activity: "idle" }],
    }],
    syncPace: { watch: "2m", floor: "10s" },
  };
  const got = text(view.activity(state, { pace() {}, poke() {}, paceSync() {}, setActivityWindow() {} }));
  assert.match(got, /wake after 20m/);
  assert.match(got, /look every 5m/);
  assert.match(got, /tend every 30s/);
  assert.match(got, /sync every 2m/);
});

// A cycle nobody has set and a cycle somebody has stopped look alike as blanks and
// are not alike at all: one is waiting for a default and the other is a decision.
test("a stopped fleet cycle is not shown as an unset one", () => {
  const state = {
    fleet: [{
      machine: "sandy", operator: "rdm",
      pace: { wake_off: true, wake_after: "20m", tend_watch: "30s" },
      identities: [{ name: "ember", activity: "idle" }],
    }],
  };
  const got = text(view.activity(state, { pace() {}, poke() {}, paceSync() {}, setActivityWindow() {} }));
  assert.match(got, /not woken/);
  assert.ok(!/wake after 20m/.test(got), "a stopped cycle still advertised its interval");
});

// Sync is the third cycle and the odd one: it belongs to the link between two
// machines rather than to a fleet, so it is set at the server and is not per agent.
test("sync is offered once, beside the fleet's cycles", () => {
  let asked = 0;
  const drawn = view.activity(fleet(paced), {
    pace() {}, poke() {}, setActivityWindow() {}, paceSync: () => { asked++; },
  });
  const buttons = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "sync…"));
  assert.equal(buttons.length, 1, "sync should be one control, not one per agent");
  for (const fn of buttons[0].listeners.click) fn({});
  assert.equal(asked, 1);
});

// --- what thinking costs --------------------------------------------------

// The tariff is the fleet's judgement about money, and the tab's job is to show it
// beside what measurement says — without becoming a second opinion about either.

function withTariff(...rows) {
  return {
    fleet: [{
      machine: "sandy", operator: "rdm",
      identities: [{ name: "rdm", operator: true, activity: "idle" }, working],
      tariff: rows,
    }],
  };
}

test("the price list is shown with what measurement suggests", () => {
  const got = text(view.activity(withTariff(
    { setting: "opus", weight: 3, suggested: 5, measured: 4.6, turns: 120 },
    { setting: "haiku", weight: 1 },
  ), null));

  assert.match(got, /opus/);
  assert.match(got, /suggests 5/);
  // A combination nobody ran proposes nothing rather than a number from none.
  assert.match(got, /not measured/);
});

test("the suggestion is marked only where it disagrees", () => {
  const drawn = view.activity(withTariff(
    { setting: "opus", weight: 5, suggested: 5, measured: 5.0, turns: 90 },
  ), null);
  const marked = [];
  const walk = (n) => {
    if ((n.className || "").includes("pending")) marked.push(n.textContent);
    for (const k of n.childNodes || []) walk(k);
  };
  drawn.forEach(walk);
  assert.deepEqual(marked, [], "a suggestion that agrees was drawn as a disagreement");
});

test("pricing one setting names that setting", () => {
  let asked = null;
  const drawn = view.activity(withTariff({ setting: "opus", weight: 3 }), {
    tariff: (f, setting) => { asked = setting; },
    pace() {}, poke() {}, setActivityWindow() {}, paceSync() {},
  });
  const button = drawn.flatMap((n) => all(n, (x) => x.tagName === "BUTTON" && x.textContent === "price…"))[0];
  for (const fn of button.listeners.click) fn({});
  assert.equal(asked, "opus");
});

// A fleet from an older orc sends no tariff. The card is absent rather than empty.
test("a fleet with no tariff draws no card", () => {
  const got = text(view.activity(fleet(working), null));
  assert.doesNotMatch(got, /what thinking costs/);
});

test("the card says what it counts and who decides", () => {
  const got = text(view.activity(withTariff({ setting: "opus", weight: 3 }), null));
  assert.match(got, /new tokens only/);
  assert.match(got, /it proposes; you decide/);
});

// --- what it came to ------------------------------------------------------

// The narrowest honest answer to "was any of this worth it". What is worth pinning
// is mostly what it refuses to say: no ratio without a denominator, nothing per
// agent, and nothing counted whose timestamp cannot be read.

const recent = (mins) => new Date(Date.now() - mins * 60000).toISOString();

function withWork(tasks, sent, buckets) {
  return {
    activityWindow: "48h",
    activity: { machines: [{ machine: "sandy", identities: { ember: buckets || [] } }] },
    tasks, sent,
    ...fleet(working),
  };
}

test("it counts what was finished, sent, and thought in the window", () => {
  const got = text(view.activity(withWork(
    [{ name: "parser", completed: true, completed_at: recent(60) },
      { name: "lexer", completed: true, completed_at: recent(30) },
      { name: "old", completed: true, completed_at: recent(60 * 24 * 9) },
      { name: "open", completed: false }],
    [{ sent: recent(10) }, { sent: recent(20) }],
    [{ at: hour(10), turns: 40, tokens: { output: 1000, cache_create: 1000 }, files: {} }],
  ), null));

  assert.match(got, /2 tasks completed/);   // the nine-day-old one is outside it
  assert.match(got, /2 messages sent/);
  assert.match(got, /40 turns/);
});

test("the ratio is new tokens over completed tasks", () => {
  const got = text(view.activity(withWork(
    [{ name: "parser", completed: true, completed_at: recent(60) }],
    [],
    [{ at: hour(10), turns: 10, tokens: { input: 0, output: 3000, cache_create: 1000 }, files: {} }],
  ), null));
  assert.match(got, /4000 new tokens per completed task/);
});

// A cost with no denominator is a number, not a rate.
test("nothing completed means no ratio rather than a big number", () => {
  const got = text(view.activity(withWork([], [],
    [{ at: hour(10), turns: 10, tokens: { output: 90000 }, files: {} }]), null));
  assert.match(got, /no ratio to draw/);
  assert.doesNotMatch(got, /per completed task/);
});

// The caveat is on the screen, not only in the source: a number that looks like a
// ranking will be read as one unless something says otherwise.
test("it says why the ratio is not per agent", () => {
  const got = text(view.activity(withWork(
    [{ name: "parser", completed: true, completed_at: recent(60) }], [], []), null));
  assert.match(got, /not per agent/);
  assert.match(got, /do the easy work/);
});

// A completion whose time cannot be read is not counted: counting it would be
// inventing work.
test("a completion with no timestamp is not counted", () => {
  const got = text(view.activity(withWork(
    [{ name: "parser", completed: true }, { name: "lexer", completed: true, completed_at: "not a date" }],
    [], []), null));
  assert.match(got, /0 tasks completed/);
});

// --- the scale a chart chooses for itself ----------------------------------

// The complaint this answers: "all the bars are tiny, and they don't change no
// matter the scale."
//
// Fitting to the peak sounds right and is wrong for this data. One compaction
// writes a cache in a minute and an ordinary minute does not, so a real window
// almost always holds an outlier a hundred times the bulk — and measured on a real
// 48 hours, that put 47 of 48 bars at exactly the 4% floor. Two distinct heights on
// the whole chart, in nearly one colour. Every question somebody opens this to ask
// was unanswerable, and the hand-set ceiling only helped somebody who already knew
// what number to type, which is what they came to find out.
// Its own clock: `hour` above is anchored twenty hours back so the older fixtures
// keep their ordering, and these want a full window ending now.
const ago = (h) => new Date(Math.floor(Date.now() / 3600000) * 3600000 - h * 3600000).toISOString();

function spiky() {
  const out = [];
  for (let h = 47; h >= 0; h--) {
    const spike = h === 20;
    const n = spike ? 100 : 1 + (h % 5) * 0.3;
    out.push({
      at: ago(h), turns: Math.round(4 * n),
      tokens: { input: Math.round(200 * n), output: Math.round(300 * n),
        cache_create: Math.round(500 * n), cache_read: Math.round(4000 * n) },
    });
  }
  return out;
}

function heightsOf(state) {
  return slots(view.activity(state, null))
    .filter((b) => b.className === "bar")
    .map((b) => Number(/--fill:([\d.]+)%/.exec(b.attributes.get("style"))[1]));
}

test("a spike does not flatten every other bar onto the floor", () => {
  const heights = heightsOf(withSeries({ ember: spiky() }));
  const floored = heights.filter((n) => n === 4).length;
  assert.equal(floored, 0, `${floored} of ${heights.length} bars were pinned to the floor`);
  // And they carry information: a chart of one repeated height is a chart that has
  // told the reader nothing.
  assert.ok(new Set(heights).size > 3,
    `only ${new Set(heights).size} distinct heights across ${heights.length} bars`);
});

// Fitting hides nothing. The number that was left off the scale is the one most
// worth seeing, so it stays in the heading and the axis says what was done.
test("a fitted chart still says the peak and what stands above the fit", () => {
  const got = text(view.activity(withSeries({ ember: spiky() }), null));
  assert.match(got, /peak/, got);
  assert.match(got, /fitted to .* above it/, got);
});

// Only when there is an outlier. A chart whose largest bar is the shape of the
// data must be drawn at that scale — clipping something for the sake of it would
// throw away the one bar the reader was looking for.
test("a chart with no outlier is still drawn to its own peak", () => {
  const even = [];
  for (let h = 12; h >= 0; h--) {
    even.push({ at: ago(h), turns: 4,
      tokens: { input: 900 + h * 40, output: 100, cache_create: 0, cache_read: 50 } });
  }
  const heights = heightsOf(withSeries({ ember: even }));
  assert.equal(Math.max(...heights), 100);
  assert.doesNotMatch(text(view.activity(withSeries({ ember: even }), null)), /fitted to/);
});

// And a ceiling somebody set by hand still wins, because they looked at the chart
// and decided; a rule that overrode that would be the tool arguing.
test("a ceiling set by hand outranks the fitted one", () => {
  const state = { ...withSeries({ ember: spiky() }), chartScale: { "new tokens": 500 } };
  // That one chart, not the page: the other two are still fitting for themselves,
  // which is the point of a ceiling being per chart.
  const charts = view.activity(state, null).flatMap((n) => all(n, (x) => x.className === "chart"));
  const tokens = charts.find((c) => text(all(c, (x) => x.className === "chart-label")) === "new tokens");
  const got = text([tokens]);
  assert.match(got, /top 500/, got);
  assert.match(got, /clipped at 500/, got);
  assert.doesNotMatch(got, /fitted to/, got);
});

// A bucket that is missing a counter loses that counter, not itself.
//
// The three token readers add fields together, so one `undefined` made the sum NaN
// — and the guard that turned a missing bucket into zero turned the whole bucket
// into zero with it. An agent machine whose orc predates a counter would have shown
// an empty chart, which reads as "nothing happened" rather than "one number is not
// being reported".
test("a bucket missing one token field still counts the fields it has", () => {
  const partial = [
    { at: ago(2), turns: 1, tokens: { input: 400 } },
    { at: ago(1), turns: 1, tokens: { input: 600, output: 200 } },
  ];
  const heights = heightsOf({ ...withSeries({ ember: partial }), activityWindow: "6h" });
  assert.ok(heights.length >= 2, `only ${heights.length} bars were drawn`);
  assert.equal(Math.max(...heights), 100, "nothing was drawn at all");
});

// The bar is painted, not sized, and that is load-bearing rather than stylistic.
//
// A percentage height on one of these is a percentage on a flex item whose cross
// size is content-based, which WebKit resolves to zero. Every bar that had a value
// vanished; the empty ones kept their 2px because 2px is an absolute length. The
// chart was a dashed line of idle slots with a gap exactly where the work was.
test("a bar carries no percentage height for a browser to disagree about", () => {
  const styles = slots(view.activity(withSeries({ ember: busy }), null))
    .filter((b) => b.className === "bar")
    .map((b) => b.attributes.get("style"));
  assert.ok(styles.length > 0, "no bars were drawn at all");
  for (const style of styles) {
    assert.doesNotMatch(style, /(^|;)\s*height\s*:/,
      `a bar was sized with a height again: ${style}`);
    assert.match(style, /linear-gradient/, style);
  }
});
