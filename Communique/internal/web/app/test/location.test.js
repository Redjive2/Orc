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

// A no-op action set, for the rows that draw controls. Each test that cares
// about a click replaces the one method it is about.
const actions = { moveWorkspace() {}, moveLibrary() {} };

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

// The machine's own checkout: the directory the code and docs tabs show, and the
// only one cq may write to.
//
// It comes from the library rather than the fleet, because it is cq's own setting
// for the machine and not something orc knows about — so a screen drawn from the
// fleet alone would have nowhere to get it.
test("the machine's mirrored checkout is shown above its agents", () => {
  const withLibrary = { ...state, library: { roots: { sandy: "/srv/checkouts/Orc" } } };
  const got = text(location.location(withLibrary, null));

  assert.match(got, /mirrored checkout/);
  assert.match(got, /\/srv\/checkouts\/Orc/);
});

test("a machine mirroring nothing says so rather than showing a blank", () => {
  const got = text(location.location({ ...state, library: { roots: {} } }, null));

  assert.match(got, /nothing mirrored/);
  // And the offer is to choose one, not to "move" a directory that is not there.
  const buttons = all(
    { childNodes: location.location({ ...state, library: { roots: {} } }, actions) },
    (n) => n.tagName === "BUTTON");
  assert.ok(buttons.some((b) => b.textContent === "choose one"),
    "a machine with no checkout should offer to choose one");
});

// The move is per machine, so it must carry that machine's own root — not
// whichever one the library happened to report first.
test("moving the checkout hands the action that machine's root", () => {
  const two = {
    fleet: [{ machine: "sandy", operator: "rdm", identities: [] },
      { machine: "buildbox", operator: "rdm", identities: [] }],
    library: { roots: { sandy: "/srv/Orc", buildbox: "/opt/Orc" } },
  };
  const moved = [];
  const nodes = location.location(two, { ...actions, moveLibrary: (f) => moved.push(f) });

  const buttons = all({ childNodes: nodes }, (n) => n.tagName === "BUTTON" && n.textContent === "move");
  assert.equal(buttons.length, 2, "each machine offers its own move");
  buttons[1].listeners.click.forEach((fn) => fn({}));
  assert.equal(moved[0].machine, "buildbox");
  assert.equal(moved[0].libraryRoot, "/opt/Orc");
});
