// Tests for carrying the cursor across a redraw.
//
// The draft tests cover the text surviving a sync. These cover the reader's
// place in it, which is the other half: text restored with the caret at the end
// of it is still a sentence somebody has to repair.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const { remember, restore } = await import("../focus.js");
const { h } = await import("../dom.js");

// typing builds a field as though somebody were part-way through it.
function typing(name, value, caret) {
  const el = h("textarea", { name });
  el.value = value;
  el.selectionStart = caret;
  el.selectionEnd = caret;
  return el;
}

test("a caret mid-sentence is remembered and put back", () => {
  const before = typing("body", "half a sen", 4);
  const memo = remember(before);
  assert.deepEqual(memo, { name: "body", start: 4, end: 4 });

  // The redraw: a new element, same name, same text from the draft.
  const after = h("div", {}, typing("body", "half a sen", 0));
  assert.equal(restore(after, memo), true);

  const field = after.childNodes[0];
  assert.equal(field.focused, true, "the reader was not put back in the box");
  assert.deepEqual(field.selection, [4, 4], "the caret moved");
});

test("a selection is put back as a selection", () => {
  const el = typing("body", "half a sentence", 5);
  el.selectionEnd = 9;
  const memo = remember(el);

  const after = h("div", {}, typing("body", "half a sentence", 0));
  restore(after, memo);
  assert.deepEqual(after.childNodes[0].selection, [5, 9]);
});

// --- when there is nothing to put back -----------------------------------

test("nothing focused is remembered as nothing", () => {
  assert.equal(remember(null), null);
  assert.equal(remember(undefined), null);
  // The body has focus more often than any field does, and it has no name.
  assert.equal(remember(h("div", {})), null);
});

// A redraw that changed route must not drag focus into a field that happens to
// share a name with the one the reader left.
test("a field that is gone after the redraw is left alone", () => {
  const memo = remember(typing("body", "half a sen", 4));
  const elsewhere = h("div", {}, h("p", {}, "another view entirely"));
  assert.equal(restore(elsewhere, memo), false);
});

test("restoring without a memo does nothing and says so", () => {
  assert.equal(restore(h("div", {}), null), false);
  assert.equal(restore(null, { name: "body", start: 0, end: 0 }), false);
});

// A field that refuses a caret — a select, a checkbox — throws in some browsers
// rather than ignoring the call. Focus was the part that mattered, and a render
// that threw would blank the page.
test("a field that refuses a caret still gets the focus", () => {
  const picker = h("select", { name: "machine" });
  picker.setSelectionRange = () => { throw new TypeError("does not support selection"); };

  const after = h("div", {}, picker);
  assert.equal(restore(after, { name: "machine", start: 0, end: 0 }), true);
  assert.equal(picker.focused, true);
});

test("an element with no focus method is not one to restore to", () => {
  const odd = h("span", { name: "body" });
  odd.focus = undefined;
  assert.equal(restore(h("div", {}, odd), { name: "body", start: 0, end: 0 }), false);
});

// --- the reader's place on the page ---------------------------------------

// Putting the caret back must not move the page. Focusing an element scrolls it
// into view, and the field being restored to is often a long way down: a reply
// box under a forty-message thread sits about 7700px from the top. Somebody who
// scrolled up to re-read the thread while their reply waits below is still
// *focused* on that box, so a sync landing while they read used to throw them
// back down to it — measured at 6431px in a browser before this was passed.
//
// They kept their words and lost their place, which is the same complaint the
// drafts were written for, one layer out.
test("putting the caret back does not move the page", () => {
  const memo = remember(typing("body", "half a sen", 4));
  const after = h("div", {}, typing("body", "half a sen", 0));
  restore(after, memo);

  const field = after.childNodes[0];
  assert.ok(field.focusedWith,
    "focus() was called bare, so the browser will scroll the field into view " +
    "and take the reader with it");
  assert.equal(field.focusedWith.preventScroll, true,
    "focus() must be told not to scroll: the field is often far down the page, " +
    "and the reader may be reading somewhere else on it");
});

// The same for a field that will not take a caret. It leaves restore() by a
// different path, and the scrolling happens either way.
test("a field that refuses a caret does not move the page either", () => {
  const picker = h("select", { name: "machine" });
  picker.setSelectionRange = () => { throw new TypeError("does not support selection"); };

  restore(h("div", {}, picker), { name: "machine", start: 0, end: 0 });
  assert.equal(picker.focusedWith && picker.focusedWith.preventScroll, true);
});
