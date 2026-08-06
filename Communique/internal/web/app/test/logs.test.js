import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const view = await import("../logs.js");
const routes = await import("../routes.js");

// `tooling › logs` is the first screen in cq that shows the three cycles at all.
// Until they were written down none of it was kept, so the risk is not that it
// draws badly — it is that it draws something reassuring over an absence.
//
// Three states have to stay apart, and every one of them looks like an empty box
// if it is got wrong: a cycle that has never run here, a log that could not be
// read, and a cycle running fine with nothing to report. Colour is the other
// half: it must be there, and it must never be the only thing carrying a level.

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function text(nodes) {
  return nodes.map((n) => n.textContent).join("\n");
}

const NOW = Date.parse("2026-08-06T12:00:00Z");

function drawn(state, actions = null) {
  return text(view.screen(state, actions, NOW).flatMap((n) => all(n, () => true)));
}

function nodes(state, actions = null) {
  return view.screen(state, actions, NOW).flatMap((n) => all(n, () => true));
}

function state(patch = {}, tails = undefined) {
  return {
    machines: [{ machine: "sandy", last_sync: "2026-08-06T11:59:30Z" }],
    open: {},
    logs: {
      server: [{ level: "INFO", text: 'level=INFO msg=sync machine=sandy' }],
      machines: [{
        machine: "sandy",
        tails: tails === undefined
          ? [
              { kind: "sync", lines: [{ level: "INFO", text: "level=INFO msg=mirrored" }] },
              { kind: "wake", lines: [] },
              { kind: "tend", lines: [{ level: "ERROR", text: "level=ERROR msg=\"orc is missing\"" }] },
            ]
          : tails,
      }],
    },
    ...patch,
  };
}

test("logs is a sub-tab of tooling", () => {
  const area = routes.AREAS.find((a) => a.major === "tooling");
  assert.ok(area.subs.some((s) => s.sub === "logs"), "tooling has no logs sub-tab");
});

test("the server and each cycle each get a fold", () => {
  const out = drawn(state());
  for (const word of ["server", "sync", "wake", "tend"]) {
    assert.match(out, new RegExp(word), `no fold for ${word}`);
  }
});

test("the folds start closed", () => {
  // Four open logs is a page nobody can find the top of.
  const out = drawn(state());
  assert.doesNotMatch(out, /msg=mirrored/);
});

test("an opened fold shows its lines", () => {
  const out = drawn(state({ open: { "logs:sandy:sync": true } }));
  assert.match(out, /msg=mirrored/);
});

test("a cycle that has never run here says so", () => {
  // The whole point. Most machines run one of these three and not the other two,
  // and an empty box would read as a cycle that is running and silent.
  const out = drawn(state());
  assert.match(out, /wake[\s\S]*has not run here|has not run here/);
});

test("a log that could not be read is told apart from one with nothing in it", () => {
  const broken = state({}, [{ kind: "sync", note: "permission denied" }]);
  const out = drawn(broken);
  assert.match(out, /could not be read/);
  assert.doesNotMatch(out, /sync · [^·]*· has not run here/);
});

test("trouble is named on the closed fold, not only inside it", () => {
  // A reader scanning four folds for the one with the problem should not have to
  // open all four.
  const out = drawn(state());
  assert.match(out, /1 error/);
});

test("every level is a word as well as a colour", () => {
  // The house rule. A reader who cannot tell red from amber must be reading the
  // same thing as everybody else.
  const out = view.screen(state({ open: { "logs:sandy:tend": true } }), null, NOW);
  const levels = out.flatMap((n) => all(n, (x) => x.className === "log-level"));
  assert.ok(levels.length > 0, "no level element was drawn");
  assert.ok(levels.some((n) => n.textContent === "ERROR"),
    "the level is not written out, so colour is carrying it alone");
});

test("a line with no level is drawn plain rather than given one", () => {
  // Output from a child process is not slog's, and inventing a severity for it
  // would be the screen making something up.
  const raw = state({ open: { "logs:sandy:sync": true } },
    [{ kind: "sync", lines: [{ text: "orc: restarting into the new build" }] }]);
  const out = view.screen(raw, null, NOW);
  const lines = out.flatMap((n) => all(n, (x) => String(x.className || "").startsWith("log-line")));
  assert.equal(lines.length, 1);
  for (const bad of ["bad", "warn", "ok", "faint"]) {
    assert.ok(!String(lines[0].className).includes(bad),
      `an unlevelled line was given the ${bad} class`);
  }
});

test("the long text sits in its own scrolling block, not the page", () => {
  // A log line must not wrap — a wrapped key=value reads as two entries — and it
  // must not push the page sideways either. Both at once means the overflow
  // belongs to `.log`, which the stylesheet gives `min-width: 0; overflow-x`.
  const out = view.screen(state({ open: { "logs:sandy:sync": true } }), null, NOW);
  const blocks = out.flatMap((n) => all(n, (x) => x.className === "log"));
  assert.equal(blocks.length, 1, "the lines are not inside a .log block");
});

test("the server's log says it does not survive a restart", () => {
  // A server up for a week showing four lines is otherwise a mystery rather than
  // a fact about where the lines are kept.
  assert.match(drawn(state()), /restart/);
});

test("a request that failed is not drawn as a fleet with nothing to say", () => {
  const out = drawn({ machines: [], open: {}, logsError: "503 from the server" });
  assert.match(out, /could not be read/);
  assert.match(out, /503/);
});

test("no machine having synced is said, rather than drawn as an empty page", () => {
  const out = drawn({ machines: [], open: {}, logs: { server: [], machines: [] } });
  assert.match(out, /no machine has synced/);
});
