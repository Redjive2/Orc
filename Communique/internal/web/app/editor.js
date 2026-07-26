// The inline editor.
//
// One rule shapes all of it: **a redraw must not touch an open editor.** The
// view is re-rendered on every sync, and a sync happens while somebody is
// typing. A textarea inside that view would be replaced mid-sentence — losing
// the text, or at best the cursor — so the editor lives in its own container
// that the view's render never reaches.
//
// It is deliberately not a code editor. No line numbers, no autocomplete, no
// bracket matching: this is for changing a paragraph or a function from a phone,
// and every one of those features is a thing to go wrong between somebody and
// their own file.

import { h, mount } from "./dom.js";

// panel is where an editor lives: outside #view, so nothing the application
// redraws can disturb it.
let panel = null;

// behind is everything the editor covers.
//
// It is made inert while an editor is up, so the page underneath stops being
// tabbable and stops being read aloud. Covering it visually is not the same as
// taking it out of reach: without this, tab walks off the textarea into the
// delete button of the file being edited.
function behind() {
  return [document.getElementById("nav"), document.getElementById("view"),
    document.getElementById("status"), document.getElementById("dialog")].filter(Boolean);
}

function reachable(yes) {
  for (const el of behind()) {
    el.inert = !yes;
    // Kept in step for readers whose browser has no `inert`; where it exists,
    // it already implies this.
    if (yes) el.removeAttribute("aria-hidden");
    else el.setAttribute("aria-hidden", "true");
  }
}

function container() {
  if (panel && panel.isConnected) return panel;
  panel = h("div", { id: "editor", hidden: true });
  document.body.append(panel);
  return panel;
}

// open puts text in front of the reader to change.
//
// It resolves when they save or cancel: the edited text, or null. Nothing is
// queued here — the caller decides what to do with what comes back, which keeps
// this a text box rather than a second place that knows about actions.
export function open({ title, text, note }) {
  const box = container();

  return new Promise((resolve) => {
    let settled = false;
    const done = (value) => {
      if (settled) return;
      settled = true;
      box.hidden = true;
      mount(box, []);
      reachable(true);
      document.removeEventListener("keydown", onKey);
      resolve(value);
    };

    const area = h("textarea", { spellcheck: "false", autocapitalize: "off", autocorrect: "off" });
    // Set as a property rather than an attribute: an attribute is the *default*
    // value, and a long file would be re-read from it on any reset.
    area.value = String(text ?? "");

    const save = h("button", { onclick: () => done(area.value) }, "queue this edit");
    const cancel = h("button", { class: "quiet", onclick: () => done(null) }, "cancel");

    // Escape leaves, and the platform chord saves. Both are what somebody with a
    // keyboard will try first, and the buttons are what somebody with a thumb
    // will use.
    const onKey = (e) => {
      if (e.key === "Escape") { e.preventDefault(); done(null); }
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); done(area.value); }
    };
    document.addEventListener("keydown", onKey);

    mount(box,
      h("div", { class: "sheet" },
        h("h2", {}, title),
        note ? h("p", { class: "muted" }, note) : null,
        area,
        h("div", { class: "controls" }, save, cancel,
          h("span", { class: "muted" }, "esc cancels · ⌘/ctrl-enter queues")),
      ));
    box.hidden = false;
    reachable(false);
    area.focus();
    // The caret at the start rather than the end: a reader opening a file wants
    // to see its beginning, and scrolling to the bottom of a long one is a
    // surprise.
    area.setSelectionRange(0, 0);
    // Both axes, and after focusing. Focusing a box that does not wrap leaves it
    // scrolled sideways on a narrow screen — far enough that a phone opens the
    // editor on blank space and it reads as an empty file.
    area.scrollTop = 0;
    area.scrollLeft = 0;
  });
}

// isOpen reports whether an editor is up, so a redraw can leave it alone.
export function isOpen() {
  return Boolean(panel && panel.isConnected && !panel.hidden);
}
