// Tests for the fleet panel, kept to what the panel is *for*.
//
// It draws only what Orc derived, so there is little arithmetic here to check.
// What is worth pinning is that every verb a permission has is reachable from
// the screen — a permission that can be made and deleted but not edited sends
// somebody to a terminal on the agent machine, which is the thing this panel
// exists to avoid.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const fleetView = await import("../fleet.js");

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function buttons(nodes) {
  return nodes.flatMap((n) => all(n, (x) => x.tagName === "BUTTON")).map((b) => b.textContent);
}

const state = {
  fleet: [{
    machine: "sandy",
    operator: "rdm",
    identities: [],
    roles: [{ name: "hand", authority: 40, permissions: ["docs"] }],
    permissions: [{ name: "docs", floor: 10, patterns: ["read(Docs/**)", "write(Docs/**)"] }],
  }],
};

test("a permission can be edited as well as deleted", () => {
  const drawn = fleetView.fleet(state, { editPermission() {}, removePermission() {} });
  const labels = buttons(drawn);
  assert.ok(labels.includes("edit…"), labels.join(" "));
  assert.ok(labels.includes("delete"));
});

test("a reader with no say is offered no verbs", () => {
  assert.deepEqual(buttons(fleetView.fleet(state, null)), []);
});

test("edit… hands over the permission it sits under", () => {
  let got = null;
  const drawn = fleetView.fleet(state, { editPermission: (f, p) => { got = [f, p]; } });
  const edit = drawn.flatMap((n) =>
    all(n, (x) => x.tagName === "BUTTON" && x.textContent === "edit…"))[0];
  for (const fn of edit.listeners.click) fn({});
  assert.equal(got[0].machine, "sandy");
  assert.equal(got[1].name, "docs");
  // Both halves, because Orc's edit keeps whichever one it is not given and a
  // form that opened without the current clauses would quietly propose dropping
  // them.
  assert.equal(got[1].floor, 10);
  assert.deepEqual(got[1].patterns, ["read(Docs/**)", "write(Docs/**)"]);
});

test("a permission's clauses are drawn coloured by kind", () => {
  const drawn = fleetView.fleet(state, null);
  const kinds = drawn.flatMap((n) =>
    all(n, (x) => (x.className || "").includes("cl-kind"))).map((x) => x.className);
  assert.ok(kinds.some((c) => c.includes("cl-read")), kinds.join(" "));
  assert.ok(kinds.some((c) => c.includes("cl-write")));
});

test("the clause text is the clause, colour and all", () => {
  // A role's chips carry permission *names*, not clauses, so the one to read is
  // the first that contains a clause's parentheses.
  const drawn = fleetView.fleet(state, null);
  const chip = drawn.flatMap((n) =>
    all(n, (x) => x.className === "clause" && x.textContent.includes("(")))[0];
  assert.equal(chip.textContent, "read(Docs/**)");
});

test("a permission nothing holds says so", () => {
  const lonely = { fleet: [{ ...state.fleet[0], roles: [] }] };
  const text = fleetView.fleet(lonely, null).map((n) => n.textContent).join(" ");
  assert.match(text, /held by nothing/);
});
