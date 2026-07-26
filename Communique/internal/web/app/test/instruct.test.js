// Tests for the instruct tab.
//
// The tab draws two things that look identical and behave oppositely — layers
// compose, wake messages override — so most of what is worth pinning is that the
// screen keeps them apart, and that a layer nobody has written yet is still
// reachable. A row that only appears once something exists is a row you cannot use
// to make the first one.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const view = await import("../instruct.js");

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function text(nodes) {
  return nodes.map((n) => n.textContent).join("\n");
}

function buttons(nodes) {
  return nodes.flatMap((n) => all(n, (x) => x.tagName === "BUTTON"));
}

const actions = { editInstruct() {}, clearInstruct() {}, showInstruct() {} };

function fleet(prompts) {
  return {
    fleet: [{
      machine: "sandy",
      roles: [{ name: "engineer", authority: 40 }],
      identities: [{ name: "ember", role: "engineer", employed: true }],
      prompts,
    }],
  };
}

test("the two mechanisms are drawn apart and say which is which", () => {
  const drawn = view.instruct(fleet([]), actions);
  const body = text(drawn);

  assert.match(body, /layers/);
  assert.match(body, /additive/);
  assert.match(body, /wake messages/);
  assert.match(body, /overriding/);
});

test("a layer nobody has written yet is still a row that can be written", () => {
  const drawn = view.instruct(fleet([]), actions);
  const labels = buttons(drawn).map((b) => b.textContent);

  // Six rows — three layers and three wake messages — each offering a first write.
  assert.equal(labels.filter((l) => l === "write").length, 6, labels.join(" "));
  // And nothing offers to clear what is not there.
  assert.ok(!labels.includes("clear"), labels.join(" "));
});

test("every layer in the mirror is reachable, whichever thing it belongs to", () => {
  const drawn = view.instruct(fleet([
    { kind: "system", text: "ask before you guess", size: 20 },
    { kind: "role", name: "engineer", text: "you write the parser", size: 20 },
    { kind: "identity", name: "ember", text: "covering for atlas", size: 18 },
    { kind: "identity", name: "ember", wake: true, text: "finish the lexer", size: 16 },
  ]), actions);
  const body = text(drawn);

  for (const want of ["the fleet", "the engineer role", "ember"]) {
    assert.match(body, new RegExp(want), body);
  }
  // The opening line stands in for the paragraph.
  assert.match(body, /ask before you guess/);
  assert.equal(buttons(drawn).filter((b) => b.textContent === "clear").length, 4);
});

test("the composition can be seen for an agent, and only for an agent", () => {
  const drawn = view.instruct(fleet([
    { kind: "system", text: "ask before you guess", size: 20 },
    { kind: "identity", name: "ember", text: "covering for atlas", size: 18 },
  ]), actions);

  // A role has no composition of its own: the composed prompt is what one agent
  // is told, and a role is one layer of several agents' worth.
  assert.equal(buttons(drawn).filter((b) => b.textContent === "show composed").length, 1);
});

test("a wake message is not offered a composition", () => {
  const drawn = view.instruct(fleet([
    { kind: "identity", name: "ember", wake: true, text: "finish the lexer", size: 16 },
  ]), actions);

  const rows = buttons(drawn).filter((b) => b.textContent === "show composed");
  // Only the agent's prompt row has one; the wake row beneath it does not, because
  // wake messages do not compose.
  assert.equal(rows.length, 1);
});

test("it says what a prompt is not", () => {
  const body = text(view.instruct(fleet([]), actions));
  assert.match(body, /a prompt asks and a permission enforces/);
});

test("a machine that cannot be read says so instead of an empty tab", () => {
  const drawn = view.instruct({ fleet: [{ machine: "sandy", unreachable: "orc is not installed" }] }, actions);
  assert.match(text(drawn), /orc is not installed/);
});

test("no fleet at all is explained rather than blank", () => {
  assert.match(text(view.instruct({ fleet: [] }, actions)), /no machine/);
});
