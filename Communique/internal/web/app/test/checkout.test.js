import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const { checkout } = await import("../checkout.js");

// The light above the button that takes the site down.
//
// The server decides the verdict. What is worth pinning here is that the panel
// *shows what it was sent*: a colour that never travels alone, a verdict this build
// has never heard of not drawing as green, and a reason arriving with its fix.

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

const textOf = (nodes) => nodes.map((n) => n.textContent).join("\n");

const clean = {
  verdict: "go", branch: "main", upstream: "origin/main", head: "abc1234",
  behind: 2, ahead: 0, dirty: 0, toolchain: true, script: true,
};

function look(nodes) {
  const [state] = nodes.flatMap((n) => all(n, (x) => String(x.className).startsWith("tree-state")));
  return state ? String(state.className).split(/\s+/)[1] : null;
}

test("a clear checkout is drawn as clear, and says where it is", () => {
  const drawn = checkout(clean);
  assert.equal(look(drawn), "go");
  const said = textOf(drawn);
  assert.match(said, /main → origin\/main/);
  assert.match(said, /abc1234/);
  assert.match(said, /2 behind/);
});

// This panel is a colour telling somebody whether to press a destructive control.
// A reader who cannot tell red from green has to get the same answer, so the word is
// never optional and the dot's shape differs too.
test("the verdict is a word as well as a colour", () => {
  for (const [verdict, word] of [["go", /clear to rebuild/], ["caution", /should not/], ["stop", /cannot rebuild/]]) {
    assert.match(textOf(checkout({ ...clean, verdict })), word);
  }
  const dots = ["go", "caution", "stop"].map((verdict) => {
    const [dot] = checkout({ ...clean, verdict }).flatMap((n) => all(n, (x) => x.className === "dot"));
    return dot.textContent;
  });
  assert.equal(new Set(dots).size, 3, `the three lights share a glyph: ${dots.join("")}`);
});

// A newer server may grow a fourth verdict. The one thing that must not happen is an
// unfamiliar word drawing as clear-to-go over a checkout that is not.
test("a verdict this build has never heard of is not drawn as go", () => {
  const drawn = checkout({ ...clean, verdict: "rebasing" });
  assert.notEqual(look(drawn), "go");
  assert.match(textOf(drawn), /rebasing/);
});

// A screen that says what is wrong and not what to do about it sends somebody to a
// terminal to work out a command this already knows.
test("a reason carries its fix", () => {
  const drawn = checkout({
    ...clean, verdict: "stop",
    reasons: [{ level: "stop", text: "the branch has no upstream", fix: "git branch --set-upstream-to=origin/main main" }],
  });
  const said = textOf(drawn);
  assert.match(said, /no upstream/);
  assert.match(said, /--set-upstream-to/);
});

// Each reason is marked with its own level, not the verdict's: a stop and a caution
// in one answer are two different things to do about it.
test("reasons are marked one by one", () => {
  const drawn = checkout({
    ...clean, verdict: "stop",
    reasons: [{ level: "stop", text: "no toolchain" }, { level: "caution", text: "3 uncommitted changes" }],
  });
  const marks = drawn.flatMap((n) => all(n, (x) => String(x.className).startsWith("tree-why")))
    .map((x) => String(x.className).split(/\s+/)[1]);
  assert.deepEqual(marks, ["stop", "caution"]);
});

// The button also queues every agent machine, and the server cannot reach one to
// ask — that is the architecture. A green light here must not read as a green light
// for the fleet.
test("it says whose checkout this is", () => {
  const said = textOf(checkout(clean));
  assert.match(said, /machine serving the site/);
  assert.match(said, /last fetch/);
});

// Nothing yet, or a route that would not answer. The tab keeps its button either
// way: a panel that cannot draw is one panel, not a lost control.
test("no answer draws nothing rather than an empty verdict", () => {
  assert.deepEqual(checkout(null), []);
  assert.deepEqual(checkout(undefined), []);
});

// A server older than this page sends a verdict and no reasons, and a detached head
// has no branch to name. Neither is a reason to throw.
test("a sparse answer still draws", () => {
  const drawn = checkout({ verdict: "stop", detached: true });
  assert.equal(look(drawn), "stop");
  assert.match(textOf(drawn), /not on a branch/);
});

// --- what a wire can send that a struct cannot ------------------------------
//
// The panel's whole job is to be the calm thing on a page about a destructive
// button. Everything below is a shape Go would never produce and a proxy, an older
// build, or a hand-written client might — and none of them may take the tab down.

test("a route that would not answer says so rather than drawing nothing", () => {
  const drawn = checkout({ unreachable: "the server could not be reached" });
  assert.equal(look(drawn), "caution");
  assert.match(textOf(drawn), /cannot tell/);
  assert.match(textOf(drawn), /could not be reached/);
});

test("reasons that are not a list do not take the panel down", () => {
  for (const reasons of ["stop", 3, { level: "stop" }, null]) {
    const drawn = checkout({ ...clean, reasons });
    assert.equal(look(drawn), "go", `reasons=${JSON.stringify(reasons)}`);
  }
});

// A level goes into a class list, so an unbounded string arrives styled as whatever
// it happens to spell — and one this build has never heard of picking up no colour
// at all is worse than it reading as the caution it is.
test("a level this build does not style is drawn as a caution", () => {
  const marks = checkout({
    ...clean,
    reasons: [{ level: "catastrophe", text: "a" }, { level: "go stop", text: "b" }, { text: "c" }],
  }).flatMap((n) => all(n, (x) => String(x.className).startsWith("tree-why")))
    .map((x) => String(x.className).split(/\s+/).slice(1).join(" "));
  assert.deepEqual(marks, ["caution", "caution", "caution"]);
});

test("a reason with no text is dropped rather than drawn empty", () => {
  const drawn = checkout({ ...clean, reasons: [{ level: "stop" }, null, { level: "stop", text: "real" }] });
  const marks = drawn.flatMap((n) => all(n, (x) => String(x.className).startsWith("tree-why")));
  assert.equal(marks.length, 1);
  assert.match(marks[0].textContent, /real/);
});
