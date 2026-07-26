// Tests for drafts: the text somebody has typed and not yet queued.
//
// This is the bug they exist for. The view is re-rendered on every sync, and a
// sync happens while somebody is typing — so a form that keeps its text only in
// the DOM loses it the moment the mirror updates. The editor avoids that by
// living outside the redraw; a form in the page flow cannot, so its text is
// state and a redraw restores it.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const views = await import("../views.js");
const { findByName } = await import("../dom.js");

const message = {
  puid: 0, mid: "m0", sent: "2026-07-25T03:30:09Z", from: "bob",
  to: ["redjive"], subject: "the parser", read: false, machine: "studio",
  convo: { uid: "c1", title: "the parser", index: 1 },
  body: "have a look",
};

// detail is what the message view is handed: the message and its thread.
const detail = { message, thread: [] };

const world = (drafts = {}) => ({
  machines: [{ machine: "studio", last_sync: "2026-07-25T03:30:00Z" }],
  queue: [], inbox: [message], archive: [], sent: [], tasks: [],
  drafts,
});

// actions records what the view asked for, so a keystroke can be checked
// without an application.
function recorder() {
  const calls = [];
  return {
    calls,
    draft: (key, field, value) => calls.push({ key, field, value }),
    forget: (key) => calls.push({ forget: key }),
    reply: async () => {},
    cc: async () => true,
    send: async () => true,
    markRead: () => {},
    archive: () => {},
  };
}

// field digs a named input or textarea out of a rendered view.
function field(nodes, name) {
  for (const node of nodes.filter(Boolean)) {
    const found = findByName(node, name);
    if (found) return found;
  }
  return null;
}

// valueOf reads what a field would show: a textarea carries its text as a
// property, an input as its attribute, exactly as the browser does.
function valueOf(el) {
  if (!el) return null;
  return el.value !== undefined ? el.value : el.getAttribute("value");
}

// --- the bug -------------------------------------------------------------

test("a reply survives the redraw a sync causes", () => {
  const actions = recorder();

  // Somebody is part-way through a reply.
  const before = views.message(world(), detail, actions);
  const body = field(before, "body");
  assert.equal(valueOf(body), "", "a fresh reply box starts empty");

  // They type. The view records it rather than keeping it in the DOM.
  body.value = "half a sen";
  actions.draft(views.draftKey("reply", "m0"), "body", "half a sen");

  // A sync lands and the whole view is rendered again, from state.
  const drafts = { [views.draftKey("reply", "m0")]: { body: "half a sen" } };
  const after = views.message(world(drafts), detail, actions);

  assert.equal(valueOf(field(after, "body")), "half a sen",
    "the redraw threw away what was being written");
});

test("a new message survives the same redraw", () => {
  const actions = recorder();
  const key = views.draftKey("compose");
  const drafts = { [key]: { to: "bob", subject: "the parser", body: "as discussed" } };

  const after = views.compose(world(drafts), actions);
  assert.equal(valueOf(field(after, "to")), "bob");
  assert.equal(valueOf(field(after, "subject")), "the parser");
  assert.equal(valueOf(field(after, "body")), "as discussed");
});

test("an unsent cc survives it too", () => {
  const actions = recorder();
  const key = views.draftKey("cc", "c1");
  const after = views.message(world({ [key]: { cc: "carol" } }), detail, actions);
  assert.equal(valueOf(field(after, "cc")), "carol");
});

// --- what a keystroke does ----------------------------------------------

test("typing records a draft rather than redrawing", () => {
  const actions = recorder();
  const nodes = views.message(world(), detail, actions);
  const body = field(nodes, "body");

  // The handler h() attached for `oninput`, called as the browser would.
  const handlers = body.listeners.input || [];
  assert.equal(handlers.length, 1, "the reply box does not record what is typed");
  handlers[0]({ target: { value: "half a sen" } });

  assert.deepEqual(actions.calls[0],
    { key: views.draftKey("reply", "m0"), field: "body", value: "half a sen" });
});

// --- keys ----------------------------------------------------------------

// Two half-written replies in two threads must not overwrite each other, which
// is the whole reason a draft is keyed by its message rather than by its form.
test("each thread keeps its own reply", () => {
  const actions = recorder();
  const drafts = {
    [views.draftKey("reply", "m0")]: { body: "for bob" },
    [views.draftKey("reply", "m9")]: { body: "for carol" },
  };
  const nodes = views.message(world(drafts), detail, actions);
  assert.equal(valueOf(field(nodes, "body")), "for bob");
});

test("a reply keeps its subject once it has been changed", () => {
  const actions = recorder();
  const drafts = { [views.draftKey("reply", "m0")]: { subject: "RE: something else" } };
  const nodes = views.message(world(drafts), detail, actions);
  assert.equal(valueOf(field(nodes, "subject")), "RE: something else");

  // And falls back to the reply subject when it has not.
  const fresh = views.message(world(), detail, actions);
  assert.equal(valueOf(field(fresh, "subject")), "RE: the parser");
});

// An empty draft is not the same as no draft: somebody who cleared the box
// meant to clear it, and a redraw must not helpfully put the text back.
test("a cleared box stays cleared", () => {
  const actions = recorder();
  const drafts = { [views.draftKey("reply", "m0")]: { subject: "" } };
  const nodes = views.message(world(drafts), detail, actions);
  assert.equal(valueOf(field(nodes, "subject")), "");
});

// --- the caret ------------------------------------------------------------

// Restoring the text is half the job. A sync landing mid-sentence must leave
// the cursor where it was too, or the next letter lands in the wrong place —
// which is the same loss, one keystroke later.
test("findByName finds the field a redraw has to hand focus back to", () => {
  const actions = recorder();
  const nodes = views.compose(world({
    [views.draftKey("compose")]: { body: "as discussed" },
  }), actions);

  const holder = { childNodes: nodes.filter(Boolean) };
  const body = findByName(holder, "body");
  assert.ok(body, "a redraw could not find the field to restore focus to");
  assert.equal(valueOf(body), "as discussed");

  // The stub records what a browser would do, which is what app.js calls.
  body.focus();
  body.setSelectionRange(3, 3);
  assert.equal(body.focused, true);
  assert.deepEqual(body.selection, [3, 3]);
});

// A name that is not on screen is not an error: a redraw that changed route has
// no business dragging focus to a field that happens to share a name.
test("a field that is gone after the redraw is left alone", () => {
  const nodes = views.compose(world(), recorder());
  const holder = { childNodes: nodes.filter(Boolean) };
  assert.equal(findByName(holder, "not-a-field"), null);
});

// --- the next field somebody adds ------------------------------------------

// The tests above pin the fields that exist. This one pins the *class*, because
// the loss is silent and the fix is easy to forget: a field added without a draft
// looks right, works right, and is emptied by whichever sync lands first.
//
// It is structural rather than behavioural on purpose. Asserting "the value comes
// back" for an arbitrary field would need a DOM that models what a `<select>` does
// with its options, and the stub does not — so the test would be checking the
// stub. What it checks instead is the two things a field needs to be carried at
// all: a **name**, which is how findByName gets focus back to it, and an **input
// or change handler**, which is how what it holds reaches state. A field with
// neither is DOM-only, and DOM-only is exactly what the redraw destroys.

// A fleet with two machines, because the machine pickers only appear when there is
// a real choice to make — and a picker that does not render is a picker untested.
const fleet = (drafts = {}) => ({
  ...world(drafts),
  machines: [
    { machine: "studio", last_sync: "2026-07-25T03:30:00Z" },
    { machine: "laptop", last_sync: "2026-07-25T03:29:00Z" },
  ],
});

const ENTRY = ["INPUT", "TEXTAREA", "SELECT"];

// every collects the fields in a rendered view, however deeply nested. Buttons and
// hidden inputs are not places anybody types, and carrying them would mean asking
// for handlers on fields that have nothing to keep.
function every(nodes) {
  const out = [];
  const visit = (node) => {
    if (!node || typeof node !== "object") return;
    if (ENTRY.includes(node.tagName)) {
      const type = node.getAttribute && node.getAttribute("type");
      if (type !== "hidden" && type !== "submit" && type !== "button") out.push(node);
    }
    for (const child of node.childNodes || []) visit(child);
  };
  for (const node of [].concat(nodes).filter(Boolean)) visit(node);
  return out;
}

const nameOf = (el) => (el.getAttribute ? el.getAttribute("name") : null);

const records = (el) => {
  const on = el.listeners || {};
  return (on.input || []).length > 0 || (on.change || []).length > 0;
};

// The views that carry fields, rendered as the application renders them, so a
// field added to any of them is caught here without this list changing.
const screens = (actions) => [
  ["compose", views.compose(fleet(), actions)],
  ["a message", views.message(fleet(), detail, actions)],
  ["the task board", views.tasks(fleet(), actions)],
];

test("every field is named, so a redraw can find it again", () => {
  for (const [where, rendered] of screens(recorder())) {
    for (const el of every(rendered)) {
      assert.ok(nameOf(el),
        `${where}: a <${el.tagName.toLowerCase()}> with no name. findByName is how ` +
        `focus gets back to a field after the redraw, and it looks it up by name — ` +
        `so this one loses the cursor on every sync.`);
    }
  }
});

test("every field records what is entered into it", () => {
  for (const [where, rendered] of screens(recorder())) {
    for (const el of every(rendered)) {
      assert.ok(records(el),
        `${where}: <${el.tagName.toLowerCase()} name="${nameOf(el)}"> has no input or ` +
        `change handler, so what is entered lives only in the DOM — and the redraw ` +
        `on the next sync throws it away. Draft it, as the other fields here do.`);
    }
  }
});

// The two the structural test caught: text was drafted when drafts were added and
// the selects were not, which is quieter and worse. An emptied textarea is obvious.
// A machine picker snapped back to the first option still submits — to a machine
// nobody chose.
test("a machine picker keeps the machine that was picked", () => {
  const actions = recorder();

  const picker = every(views.compose(fleet(), actions)).find((el) => nameOf(el) === "machine");
  assert.ok(picker, "compose offers no machine picker even with two machines");
  assert.equal(picker.tagName, "SELECT");

  // Choosing drafts, rather than leaving the choice in the DOM.
  for (const fn of picker.listeners.change) fn({ target: { value: "laptop" } });
  assert.deepEqual(actions.calls[0],
    { key: views.draftKey("compose"), field: "machine", value: "laptop" });

  // And the redraw a sync causes puts it back.
  const after = every(views.compose(fleet({
    [views.draftKey("compose")]: { machine: "laptop" },
  }), actions)).find((el) => nameOf(el) === "machine");
  assert.equal(after.value, "laptop", "the redraw reset the machine to the first one");
});

test("the task board keeps its machine too", () => {
  const actions = recorder();

  const picker = every(views.tasks(fleet(), actions)).find((el) => nameOf(el) === "machine");
  assert.ok(picker, "the task board offers no machine picker even with two machines");

  for (const fn of picker.listeners.change) fn({ target: { value: "laptop" } });
  const key = actions.calls[0].key;
  assert.notEqual(key, views.draftKey("compose"),
    "the board drafts under compose's key, so choosing on one changes the other");

  const after = every(views.tasks(fleet({ [key]: { machine: "laptop" } }), actions))
    .find((el) => nameOf(el) === "machine");
  assert.equal(after.value, "laptop", "the redraw reset the machine to the first one");
});
