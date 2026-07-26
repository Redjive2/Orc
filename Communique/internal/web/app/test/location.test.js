// Tests for the location panel: where each agent works.
//
// The screen has one job — say which copy of the repository each agent has its
// hands on — and one risk, which is that two agents share a directory without
// anybody noticing. Both are what is checked here.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const location = await import("../location.js");

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function text(nodes) {
  return nodes.map((n) => n.textContent).join("\n");
}

const state = {
  fleet: [{
    machine: "sandy",
    operator: "rdm",
    identities: [
      { name: "ember", employed: true, workspace: "/Users/rdm/trees/parser" },
      { name: "quill", employed: false, workspace: "/Users/rdm/.orc/identities/quill/workspace" },
      { name: "atlas", employed: true, workspace: "/Users/rdm/trees/parser" },
    ],
  }],
};

test("every agent's directory is shown", () => {
  const got = text(location.location(state, null));

  for (const want of ["ember", "quill", "atlas", "/Users/rdm/trees/parser"]) {
    assert.ok(got.includes(want), `the screen does not show ${want}:\n${got}`);
  }
});

// The fact the screen exists for. Two agents in one tree may be deliberate; two by
// accident is how a scope stops meaning anything, and either way it should not have
// to be worked out by reading down a column.
test("a shared directory is counted at the top", () => {
  const got = text(location.location(state, null));
  assert.ok(got.includes("shared by more than one"), `sharing is not reported:\n${got}`);
});

test("nothing shared says nothing about sharing", () => {
  const apart = {
    fleet: [{
      machine: "sandy",
      identities: [
        { name: "ember", workspace: "/a" },
        { name: "quill", workspace: "/b" },
      ],
    }],
  };
  const got = text(location.location(apart, null));
  assert.ok(!got.includes("shared"), `sharing was reported where there is none:\n${got}`);
});

// Moving is only offered when there is somebody to do it — the screen is readable
// without an actions object, which is what the machine-unreachable case renders as.
test("the move button appears only with actions", () => {
  const withActions = all({ childNodes: location.location(state, { moveWorkspace() {} }) },
    (n) => n.tagName === "BUTTON");
  assert.ok(withActions.length >= 3, "no move button per agent");

  const without = all({ childNodes: location.location(state, null) }, (n) => n.tagName === "BUTTON");
  assert.equal(without.length, 0, "a read-only render drew buttons");
});

// An employed agent's session keeps the directory it started in, and somebody
// deciding whether to move one wants that before they click, not after.
test("the cost of moving a running agent is said beside the button", () => {
  const got = text(location.location(state, { moveWorkspace() {} }));
  assert.ok(got.includes("until it is refreshed"), `the running case is not explained:\n${got}`);
  assert.ok(got.includes("next time it is employed"), `the idle case is not explained:\n${got}`);
});

test("a machine that could not be read says so", () => {
  const broken = { fleet: [{ machine: "sandy", unreachable: "the fleet did not answer" }] };
  const got = text(location.location(broken, null));
  assert.ok(got.includes("did not answer"), `the failure is not shown:\n${got}`);
});

test("no fleet at all is explained rather than empty", () => {
  const got = text(location.location({ fleet: [] }, null));
  assert.ok(got.includes("no machine mirrors an orc fleet"), got);
});
