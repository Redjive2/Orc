// Tests for a task's description in the tasks tab.
//
// A description is the only thing on a task that says what to *do*, and it is the
// one field that can be present in the mirror and absent in the snapshot — the board
// says a task has one, and the detail call that carries the prose can fail. Most of
// what is worth pinning here is that those two are never confused: offering to write
// a first description over the top of one that merely could not be read is how
// somebody's specification disappears.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const views = await import("../views.js");

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function text(nodes) {
  return nodes.filter(Boolean).map((n) => n.textContent).join("\n");
}

function buttons(nodes) {
  return nodes.filter(Boolean)
    .flatMap((n) => all(n, (x) => x.tagName === "BUTTON"))
    .map((b) => b.textContent);
}

const actions = {
  describeTask() {}, undescribeTask() {}, setStatus() {}, claimTask() {},
  assignTask() {}, inviteToTask() {}, kickFromTask() {}, leaveTask() {},
  pushTask() {}, scopeTask() {}, worktreeTask() {}, addSubtask() {},
  completeTask() {}, deleteTask() {}, completeSubtask() {}, deleteSubtask() {},
};

function state(task) {
  return {
    tasks: [{
      name: "fix-the-parser", machine: "studio", owner: "alice",
      priority: 3, difficulty: 3, status: 3, done: 0, total: 0,
      scope: ["internal/tree"], ...task,
    }],
    queue: [],
  };
}

test("a description is rendered as markdown, not as its source", () => {
  const drawn = views.task(
    state({ described: true, description: "# the parser\n\nIt drops the last token." }),
    "fix-the-parser", actions);
  const body = text(drawn);

  assert.match(body, /the parser/);
  assert.match(body, /It drops the last token/);
  // The heading is a heading rather than a line beginning with a hash.
  assert.doesNotMatch(body, /# the parser/);
  // An h2, not an h1: this prose sits inside a page that already has a heading.
  assert.ok(all(drawn[1], (n) => n.tagName === "H2").length > 0, "no heading element was made");
});

test("a task with no description says so and offers to write one", () => {
  const drawn = views.task(state({ described: false }), "fix-the-parser", actions);

  assert.match(text(drawn), /no description/);
  assert.ok(buttons(drawn).includes("write one…"), buttons(drawn).join(" "));
  // Nothing to clear when there is nothing there.
  assert.ok(!buttons(drawn).includes("clear"), buttons(drawn).join(" "));
});

test("a description that has one can be edited and cleared", () => {
  const drawn = views.task(
    state({ described: true, description: "what to do" }), "fix-the-parser", actions);
  const labels = buttons(drawn);

  assert.ok(labels.includes("edit…"), labels.join(" "));
  assert.ok(labels.includes("clear"), labels.join(" "));
});

// TestAnUnreadableDescriptionIsNotAnAbsentOne. The board says the task has one and
// the detail call failed. Saying "no description" here would invite writing a first
// one over the top of prose nobody has seen.
test("a description the mirror could not read says that, not that there is none", () => {
  const drawn = views.task(state({ described: true, description: "" }), "fix-the-parser", actions);
  const body = text(drawn);

  assert.match(body, /could not read/);
  assert.doesNotMatch(body, /no description/);
  // And the control still says edit, not write-one: there is something there.
  assert.ok(buttons(drawn).includes("edit…"), buttons(drawn).join(" "));
});

test("a reader with no say is offered no buttons", () => {
  const drawn = views.task(
    state({ described: true, description: "what to do" }), "fix-the-parser", null);

  assert.match(text(drawn), /what to do/);
  assert.equal(buttons(drawn).length, 0);
});

// The board marks which tasks have one, because "which of these can I pick up
// without asking somebody what it means" is a question about the pool.
test("the board marks a described task", () => {
  const s = state({ described: true, description: "what to do" });
  s.machines = [{ machine: "studio" }];
  const drawn = views.tasks(s, actions);

  assert.match(text(drawn), /spec/);
});

test("the board does not mark an undescribed one", () => {
  const s = state({ described: false });
  s.machines = [{ machine: "studio" }];
  assert.doesNotMatch(text(views.tasks(s, actions)), /spec/);
});

// A mirror taken before descriptions existed carries neither field. That is a task
// with no description, not a broken one.
test("a task from an older mirror reads as undescribed", () => {
  const drawn = views.task(state({}), "fix-the-parser", actions);

  assert.match(text(drawn), /no description/);
  assert.doesNotMatch(text(drawn), /could not read/);
});

// Markdown from somewhere else is text, not elements: the renderer builds nodes and
// never sets innerHTML, and this is the view that shows prose an agent wrote.
test("a description cannot smuggle markup into the page", () => {
  const drawn = views.task(
    state({ described: true, description: "<img src=x onerror=alert(1)>" }),
    "fix-the-parser", actions);

  assert.equal(all(drawn[1], (n) => n.tagName === "IMG").length, 0);
  assert.match(text(drawn), /<img/);
});
