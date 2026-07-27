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
    activity: { machines: [{ machine: "sandy", identities }] },
    ...fleet(working),
    ...extra,
  };
}

const hour = (n) => new Date(Date.UTC(2026, 6, 26, n)).toISOString();

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
