// Tests for the in-cq dialog.
//
// This exists because asking a question used to mean `window.prompt`, which is
// the browser's furniture rather than cq's — unstyleable, one question at a time,
// and unable to say what a value is for. So the properties worth pinning are the
// ones that made replacing it worthwhile: several fields at once, a complaint
// that appears *in* the sheet, and the redraw rule the editor already holds.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const dialog = await import("../dialog.js");
const { mount, h } = await import("../dom.js");

function press(key, mods = {}) {
  for (const fn of [...(document.listeners.keydown || [])]) {
    fn({ key, preventDefault() {}, ...mods });
  }
}

function find(node, ok) {
  if (ok(node)) return node;
  for (const child of node.childNodes || []) {
    const got = find(child, ok);
    if (got) return got;
  }
  return null;
}

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function button(label) {
  return find(document.body, (n) => n.tagName === "BUTTON" && n.textContent === label);
}

function inputs() {
  return all(document.body, (n) => n.tagName === "INPUT");
}

function click(node) {
  for (const fn of (node.listeners.click || [])) fn({ target: node });
}

function submit() {
  const form = find(document.body, (n) => n.tagName === "FORM");
  for (const fn of (form.listeners.submit || [])) fn({ preventDefault() {} });
}

const taskFields = [
  { name: "name", label: "name", value: "" },
  { name: "priority", label: "priority", kind: "number", value: 3, min: 1, max: 5 },
  { name: "difficulty", label: "difficulty", kind: "number", value: 3, min: 1, max: 5 },
];

// The whole reason this is not three prompts in a row.
test("one sheet asks for every field at once", async () => {
  const done = dialog.ask({ title: "a new task", fields: taskFields });

  const boxes = inputs();
  assert.equal(boxes.length, 3, "the fields were not all on one sheet");
  assert.equal(boxes[1].value, "3", "a default was not filled in");

  boxes[0].value = "parser";
  boxes[1].value = "4";
  submit();
  assert.deepEqual(await done, { name: "parser", priority: 4, difficulty: 3 });
});

// The complaint belongs beside the box that is wrong. `alert` used to do this,
// which meant a second modal on top of the first, about a value you could no
// longer see.
test("a value out of range is refused in the sheet, not in another popup", async () => {
  const done = dialog.ask({ title: "a new task", fields: taskFields });
  const boxes = inputs();
  boxes[0].value = "parser";
  boxes[1].value = "9";
  submit();

  const trouble = find(document.body, (n) => n.className === "trouble");
  assert.match(trouble.textContent, /priority.*1 to 5/, "nothing was said about the bad value");
  assert.ok(dialog.isOpen(), "the sheet closed on a value it refused");

  // And it accepts the correction rather than having to be reopened.
  boxes[1].value = "5";
  submit();
  assert.equal((await done).priority, 5);
});

test("an empty required field is refused, and says which", async () => {
  const done = dialog.ask({ title: "a new task", fields: taskFields });
  submit();
  const trouble = find(document.body, (n) => n.className === "trouble");
  assert.match(trouble.textContent, /name is needed/);

  inputs()[0].value = "parser";
  submit();
  assert.equal((await done).name, "parser");
});

// Cancelling resolves with nothing, which is not the same as an empty answer — a
// caller that could not tell them apart would queue a nameless task on a stray tap.
test("cancelling resolves with nothing, not with an empty answer", async () => {
  const cancelled = dialog.ask({ title: "t", fields: [{ name: "v", label: "v" }] });
  click(button("cancel"));
  assert.equal(await cancelled, null);

  const escaped = dialog.ask({ title: "t", fields: [{ name: "v", label: "v" }] });
  press("Escape");
  assert.equal(await escaped, null);
});

test("one() is the single-value shorthand", async () => {
  const done = dialog.one({ title: "assign parser", label: "to", value: "bob" });
  const box = inputs()[0];
  assert.equal(box.value, "bob", "the current value was not offered");
  box.value = "carol";
  submit();
  assert.equal(await done, "carol");

  const cancelled = dialog.one({ title: "t", label: "v" });
  click(button("cancel"));
  assert.equal(await cancelled, null);
});

// confirm resolves a boolean, and says what will happen rather than "are you
// sure": somebody who has read "this cannot be undone" has been told something.
test("confirm resolves true only when the reader said so", async () => {
  const yes = dialog.confirm({ title: "delete parser?", body: "this cannot be undone.", submit: "delete it" });
  click(button("delete it"));
  assert.equal(await yes, true);

  const no = dialog.confirm({ title: "delete parser?", submit: "delete it" });
  click(button("cancel"));
  assert.equal(await no, false);

  // Escape means no. A dialog whose escape hatch confirmed would be a trap.
  const escaped = dialog.confirm({ title: "delete parser?", submit: "delete it" });
  press("Escape");
  assert.equal(await escaped, false);
});

// The property the whole design rests on, and the same one the editor holds:
// #view is redrawn on every sync, and the dialog is not in it.
test("a redraw of the view does not disturb an open dialog", async () => {
  const view = document.getElementById("view");
  mount(view, [h("p", {}, "the board")]);

  const done = dialog.one({ title: "a step of parser", label: "name" });
  const box = inputs()[0];
  box.value = "half a nam";

  // What refresh() does on every sync.
  mount(view, [h("p", {}, "a redrawn board")]);

  assert.ok(dialog.isOpen(), "the dialog was closed by a redraw");
  assert.equal(inputs()[0], box, "the input was replaced, so the cursor went with it");
  assert.equal(inputs()[0].value, "half a nam", "what was typed was lost to a redraw");

  box.value = "half a name";
  submit();
  assert.equal(await done, "half a name");
});

// Covering the page is not the same as taking it out of reach: without this, tab
// walks off the sheet into the delete button of the very task being asked about.
test("the page behind an open dialog is out of reach", async () => {
  const view = document.getElementById("view");

  const done = dialog.one({ title: "t", label: "v" });
  assert.equal(view.inert, true, "the page behind was left tabbable");
  assert.equal(view.getAttribute("aria-hidden"), "true", "the page behind was left readable aloud");

  click(button("cancel"));
  await done;
  assert.equal(view.inert, false, "the page was left inert after the dialog closed");
  assert.equal(view.getAttribute("aria-hidden"), null, "the page was left hidden from readers");
});

// A clause field is a plain input with a reading of itself underneath. The point
// of the pair is that the box stays a box — nothing reimplements the caret — so
// what is checked is that the mirror follows the box and that neither one stops
// the value getting through.
test("a clause field mirrors what is typed, and complains without refusing", async () => {
  const done = dialog.ask({
    title: "a permission",
    fields: [{ name: "patterns", label: "clauses", kind: "clauses", value: "read(Docs/**)" }],
  });

  const mirror = find(document.body, (n) => n.className === "clause-mirror");
  const trouble = find(document.body, (n) => n.className === "clause-trouble");
  assert.equal(mirror.textContent, "read(Docs/**)");
  assert.equal(trouble.textContent, "");

  const box = inputs()[0];
  box.value = "read(Docs/**) nope";
  for (const fn of box.listeners.input) fn({});
  assert.equal(mirror.textContent, "read(Docs/**) nope", "the mirror lost what was typed");
  assert.match(trouble.textContent, /nope/);

  // Orc has the final say, so an unreadable clause is still queued.
  submit();
  assert.deepEqual(await done, { patterns: "read(Docs/**) nope" });
});

test("a clause field brings its cheat sheet", async () => {
  const done = dialog.ask({
    title: "a permission",
    fields: [{ name: "patterns", label: "clauses", kind: "clauses" }],
  });
  const sheet = find(document.body, (n) => n.className === "cheatsheet");
  assert.ok(sheet, "no cheat sheet beside a clause field");
  assert.match(sheet.textContent, /spawn\(24\)/);
  click(button("cancel"));
  await done;
});
