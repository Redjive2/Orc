// Carrying the cursor across a redraw.
//
// The view is re-rendered whenever a sync lands, and a sync lands while
// somebody is typing. Drafts keep the *text* through that (see app.js); this
// keeps their place in it. Without both, a mirror updating mid-sentence either
// empties the box or moves the caret to the end of it, and the next letter goes
// somewhere the writer did not put it.
//
// A field is identified by its name, which is unique within a view. That is
// deliberately weaker than an identity: if the redraw changed route, the field
// is gone and focus stays where the browser left it rather than being dragged
// into whatever happens to share the name.

import { findByName } from "./dom.js";

// remember notes which field is being typed into, and where in it.
//
// It takes the element rather than reading the document, so it is a function of
// its argument and can be tested without a browser.
export function remember(active) {
  if (!active || typeof active.getAttribute !== "function") return null;

  const name = active.getAttribute("name");
  if (!name) return null;

  return {
    name,
    start: Number.isInteger(active.selectionStart) ? active.selectionStart : null,
    end: Number.isInteger(active.selectionEnd) ? active.selectionEnd : null,
  };
}

// restore puts the reader back, and reports whether it could.
//
// Every failure here is silent and returns false: a field that is no longer on
// screen, a browser that will not take a caret on that kind of input, an
// element with no focus method at all. None of them is worth interrupting
// somebody's typing over, and the alternative — throwing inside a render — would
// blank the page.
export function restore(root, memo) {
  if (!memo || !root) return false;

  const el = findByName(root, memo.name);
  if (!el || typeof el.focus !== "function") return false;

  el.focus();

  if (memo.start === null || typeof el.setSelectionRange !== "function") return true;
  try {
    el.setSelectionRange(memo.start, memo.end ?? memo.start);
  } catch {
    // A field with no caret — a select, a checkbox, a number input in some
    // browsers — throws rather than ignoring it. Focus was the part that
    // mattered, and it has already happened.
  }
  return true;
}
