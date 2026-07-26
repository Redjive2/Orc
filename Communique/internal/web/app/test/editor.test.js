// Tests for the inline editor.
//
// The property worth testing is the one it exists for: an open editor is not
// inside the container the application redraws, so a sync arriving mid-sentence
// cannot replace the textarea somebody is typing into. Everything else here is
// the small contract around it — what save and cancel resolve to, and that a
// cancelled edit resolves to nothing rather than to an empty file.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const editor = await import("../editor.js");
const { mount, h } = await import("../dom.js");

// press sends a key the way the document listener sees it. The list is copied
// first because a handler that acts on the key unregisters itself.
function press(key, mods = {}) {
  for (const fn of [...(document.listeners.keydown || [])]) {
    fn({ key, preventDefault() {}, ...mods });
  }
}

function textarea() {
  return find(document.body, (n) => n.tagName === "TEXTAREA");
}

function button(label) {
  return find(document.body, (n) => n.tagName === "BUTTON" && n.textContent === label);
}

function find(node, ok) {
  if (ok(node)) return node;
  for (const child of node.childNodes || []) {
    const got = find(child, ok);
    if (got) return got;
  }
  return null;
}

function click(node) {
  for (const fn of (node.listeners.click || [])) fn({ target: node });
}

test("saving resolves with what was typed", async () => {
  const done = editor.open({ title: "editing a.go", text: "before" });
  const area = textarea();
  assert.equal(area.value, "before");

  area.value = "after";
  click(button("queue this edit"));
  assert.equal(await done, "after");
});

// Cancelling resolves with nothing, which is not the same as an empty file — a
// caller that could not tell them apart would truncate a file on a stray tap.
test("cancelling resolves with nothing, not with an empty file", async () => {
  const done = editor.open({ title: "editing a.go", text: "before" });
  click(button("cancel"));
  assert.equal(await done, null);
});

test("escape cancels and the platform chord saves", async () => {
  const cancelled = editor.open({ title: "t", text: "x" });
  press("Escape");
  assert.equal(await cancelled, null);

  const saved = editor.open({ title: "t", text: "x" });
  textarea().value = "typed";
  press("Enter", { metaKey: true });
  assert.equal(await saved, "typed");

  // Enter on its own is a newline, not a submit: this is a text editor.
  const still = editor.open({ title: "t", text: "x" });
  press("Enter");
  assert.ok(editor.isOpen(), "a bare Enter closed the editor");
  click(button("cancel"));
  await still;
});

// The property the whole design rests on. #view is redrawn on every sync; the
// editor is not in it, so it survives.
test("a redraw of the view does not disturb an open editor", async () => {
  const view = h("main", { id: "view" }, h("p", {}, "the tree"));
  document.body.append(view);

  const done = editor.open({ title: "editing a.go", text: "half a sentence" });
  const area = textarea();
  area.value = "half a sentence and more";

  // What refresh() does on every sync.
  mount(view, [h("p", {}, "a redrawn tree")]);

  assert.ok(editor.isOpen(), "the editor was closed by a redraw");
  assert.equal(textarea().value, "half a sentence and more", "the text was lost to a redraw");
  assert.equal(textarea(), area, "the textarea was replaced, so the cursor went with it");

  click(button("queue this edit"));
  assert.equal(await done, "half a sentence and more");
  view.remove();
});

// Covering the page is not the same as taking it out of reach: without this,
// tab walks off the textarea straight into the delete button of the very file
// being edited, and a screen reader reads the tree behind the sheet.
test("the page behind an open editor is out of reach", async () => {
  const view = document.getElementById("view");
  const done = editor.open({ title: "t", text: "x" });

  assert.equal(view.inert, true, "the view is still tabbable behind the editor");
  assert.equal(view.getAttribute("aria-hidden"), "true");

  click(button("cancel"));
  await done;
  assert.equal(view.inert, false, "the view was left inert after the editor closed");
  assert.equal(view.getAttribute("aria-hidden"), null);
});

test("it is closed once it settles, and reports as much", async () => {
  assert.equal(editor.isOpen(), false);
  const done = editor.open({ title: "t", text: "x" });
  assert.equal(editor.isOpen(), true);
  click(button("cancel"));
  await done;
  assert.equal(editor.isOpen(), false);
});

// Opening twice must not leave two editors on the page, each holding a different
// version of the same file.
test("opening again replaces the first rather than stacking", async () => {
  const first = editor.open({ title: "one", text: "a" });
  click(button("cancel"));
  await first;

  const second = editor.open({ title: "two", text: "b" });
  const boxes = [];
  find(document.body, (n) => {
    if (n.tagName === "TEXTAREA") boxes.push(n);
    return false;
  });
  assert.equal(boxes.length, 1, "two editors are open at once");
  click(button("cancel"));
  await second;
});

test("the note says what queueing means", async () => {
  const done = editor.open({ title: "t", text: "x", note: "it leaves on the next sync" });
  assert.match(document.body.textContent, /leaves on the next sync/);
  click(button("cancel"));
  await done;
});
