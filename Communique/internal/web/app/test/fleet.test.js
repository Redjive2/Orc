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
  const drawn = fleetView.permissionList(state, { editPermission() {}, removePermission() {} });
  const labels = buttons(drawn);
  assert.ok(labels.includes("edit…"), labels.join(" "));
  assert.ok(labels.includes("delete"));
});

test("a reader with no say is offered no verbs", () => {
  assert.deepEqual(buttons(fleetView.permissionList(state, null)), []);
});

test("edit… hands over the permission it sits under", () => {
  let got = null;
  const drawn = fleetView.permissionList(state, { editPermission: (f, p) => { got = [f, p]; } });
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
  const drawn = fleetView.permissionList(state, null);
  const kinds = drawn.flatMap((n) =>
    all(n, (x) => (x.className || "").includes("cl-kind"))).map((x) => x.className);
  assert.ok(kinds.some((c) => c.includes("cl-read")), kinds.join(" "));
  assert.ok(kinds.some((c) => c.includes("cl-write")));
});

test("the clause text is the clause, colour and all", () => {
  // A role's chips carry permission *names*, not clauses, so the one to read is
  // the first that contains a clause's parentheses.
  const drawn = fleetView.permissionList(state, null);
  const chip = drawn.flatMap((n) =>
    all(n, (x) => x.className === "clause" && x.textContent.includes("(")))[0];
  assert.equal(chip.textContent, "read(Docs/**)");
});

test("a permission nothing holds says so", () => {
  const lonely = { fleet: [{ ...state.fleet[0], roles: [] }] };
  const text = fleetView.permissionList(lonely, null).map((n) => n.textContent).join(" ");
  assert.match(text, /held by nothing/);
});

// --- the operator is not an agent ------------------------------------------

// It is a person. It is never employed, runs no session, holds no standing
// instructions, and works wherever it likes — so a row for it in a list *about
// agents* is a row that can never be acted on, which is a row people learn to
// skip past. It is still named on every card; it is just not one of the things
// being listed.
const FLEET = {
  machine: "studio",
  operator: "redjive2",
  identities: [
    { name: "redjive2", operator: true, authority: 100, chain: ["redjive2"], workspace: "/w/op" },
    { name: "ember", authority: 60, role: "builder", chain: ["redjive2", "ember"], workspace: "/w/a" },
    { name: "flint", authority: 60, role: "builder", employed: true, load: 4,
      chain: ["redjive2", "flint"], workspace: "/w/b" },
  ],
  roles: [{ name: "builder", authority: 60, held_by: ["redjive2", "ember", "flint"] }],
  permissions: [],
};

const withFleet = { fleet: [FLEET], files: {}, open: {} };

function said(nodes) {
  return nodes.filter(Boolean).map((n) => n.textContent).join(" ");
}

test("agents are everybody but the operator", () => {
  assert.deepEqual(fleetView.agents(FLEET).map((id) => id.name), ["ember", "flint"]);
  assert.deepEqual(fleetView.agents({}), []);
});

test("no list about agents names the operator", () => {
  for (const [where, drawn] of [
    ["manage › fleet", fleetView.running(withFleet, null)],
    ["admin › identities", fleetView.tree(withFleet, null)],
    ["admin › roles", fleetView.roleList(withFleet, null)],
  ]) {
    const text = said(drawn);
    assert.match(text, /ember/, `${where} dropped an agent`);
    // The card still says who the operator is; no row is drawn for it.
    const rows = text.split("operator redjive2").join("");
    assert.doesNotMatch(rows, /redjive2/, `${where} lists the operator as an agent`);
  }
});

// The card names it, because who the operator is is worth knowing.
test("the operator is named, not hidden", () => {
  assert.match(said(fleetView.running(withFleet, null)), /operator redjive2/);
  assert.match(said(fleetView.tree(withFleet, null)), /operator redjive2/);
});

// Everybody reports to the operator, so leaving the chain as Orc counts it would
// indent the whole fleet one step under a row that is not on screen.
test("the tree hangs from nothing once its root is gone", () => {
  const text = said(fleetView.tree(withFleet, null));
  const line = text.split("\n").find((l) => l.includes("ember")) || text;
  assert.ok(!/\s{2,}ember/.test(line), `a top-level agent is still indented: ${JSON.stringify(line)}`);
});

test("counts say agents, and count agents", () => {
  assert.match(said(fleetView.running(withFleet, null)), /1 of 2 agents employed/);
  assert.match(said(fleetView.tree(withFleet, null)), /2 agents/);
});
