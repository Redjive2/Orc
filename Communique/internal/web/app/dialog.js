// Asking for something, inside cq.
//
// This replaces `window.prompt`, `window.confirm`, and `window.alert`. Those are
// the browser's furniture, not cq's: they arrive in the system font at the top of
// the window, they cannot be styled, they cannot say what a value is *for*, and
// they can only ask one question at a time — so making a task took three of them
// in a row, each one a modal interruption that looked like the site had been
// taken over by something else.
//
// The rule the editor is built on holds here too: **a redraw must not touch an
// open dialog.** The view is re-rendered on every sync, and a sync happens while
// somebody is halfway through typing a name. So this lives in its own container
// outside #view, and the view's render never reaches it.
//
// The sheet is deliberately small. It asks, it validates what it was given, and
// it resolves — it queues nothing and knows about no actions, which is what keeps
// it a question rather than a second place that decides what happens.

import { h, mount } from "./dom.js";
import * as clauses from "./clauses.js";

// panel is where a dialog lives: outside #view, so nothing the application
// redraws can disturb it.
let panel = null;

// behind is everything the dialog covers, made inert while it is up.
//
// The editor is in this list because the two are separate containers and the
// tab order does not care which one you meant: without it, tab walks off the
// dialog into a textarea nobody can see.
function behind() {
  return [document.getElementById("nav"), document.getElementById("view"),
    document.getElementById("status"), document.getElementById("editor")].filter(Boolean);
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
  panel = h("div", { id: "dialog", hidden: true });
  document.body.append(panel);
  return panel;
}

// sheet is the machinery every dialog below shares: the panel, the inert page
// behind it, escape to leave, and the promise.
//
// `build` is handed a `done` and returns the elements. Everything specific to
// one kind of question lives in the caller.
function sheet(build) {
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

    // Escape always leaves with nothing. It is the one key every modal in every
    // program agrees about, and a dialog that ignored it would trap somebody who
    // opened it by accident.
    const onKey = (e) => {
      if (e.key === "Escape") { e.preventDefault(); done(null); }
    };
    document.addEventListener("keydown", onKey);

    const { elements, focus } = build(done);
    mount(box, h("div", { class: "sheet dialog" }, ...elements));
    box.hidden = false;
    reachable(false);
    if (focus) focus.focus();
    if (focus && focus.select) focus.select();
  });
}

// ask puts one form in front of the reader and resolves with what they typed, or
// null if they left.
//
// Several fields at once rather than one question after another: a task has a
// name, a priority, and a difficulty, and asking for those in three sequential
// modals makes a person answer the first before they can see what the third even
// is. One sheet shows the whole shape of what is being made.
//
// fields is a list of { name, label, value, kind, hint, min, max, options }.
// `kind` is "text", "number", or "choice"; anything else is treated as text.
export function ask({ title, note, fields, submit = "queue it", danger = false }) {
  return sheet((done) => {
    // A container that is empty when nothing is wrong, rather than a paragraph
    // that is hidden. Toggling `hidden` means keeping an attribute and a property
    // in step, and an element that is present-but-hidden is still in the tab order
    // and still read aloud on the browsers where `hidden` is only a style.
    const trouble = h("div", { class: "trouble" });
    const inputs = new Map();

    const rows = fields.map((f) => {
      let input;
      let mirror = null;
      let extras = [];
      if (f.kind === "choice") {
        input = h("select", {},
          ...f.options.map((o) => h("option", { value: String(o.value) }, o.label)));
        input.value = String(f.value ?? (f.options[0] && f.options[0].value));
      } else if (f.kind === "lines") {
        // Prose rather than a value: a message body, where the newlines are part
        // of what is being written. It wraps, unlike the code editor, because
        // this is a paragraph and a paragraph has no columns to keep.
        input = h("textarea", { rows: String(f.rows || 5), placeholder: f.placeholder });
        input.value = String(f.value ?? "");
      } else if (f.kind === "clauses") {
        // A line of permission clauses, drawn coloured underneath as it is typed.
        //
        // Underneath rather than in place: highlighting *inside* a text box means
        // reimplementing the caret, and a caret that is one pixel wrong is worse
        // than no colour. This keeps the real input exactly as it is — selection,
        // undo, autocorrect off, a phone keyboard that behaves — and puts the
        // reading of it below, where it can also say what it could not read.
        input = h("input", {
          type: "text", spellcheck: "false", autocapitalize: "off",
          autocorrect: "off", placeholder: f.placeholder,
        });
        input.value = String(f.value ?? "");
        mirror = h("div", { class: "clause-mirror", "aria-hidden": "true" });
        const trouble = h("div", { class: "clause-trouble" });
        const redraw = () => {
          mount(mirror, ...clauses.highlight(input.value, f.words));
          // Two kinds of remark, told apart because they mean different things.
          // A problem is a clause Orc will refuse; a note is one it will accept
          // and that does nothing — `orc(policy)` names no verb anybody checks.
          // Both report rather than refuse: Orc has the final say, and being
          // unable to explain something is not grounds for refusing it.
          mount(trouble,
            ...clauses.problems(input.value, f.words).map((b) => h("p", { class: "warn" }, b)),
            ...clauses.notes(input.value, f.words).map((n) => h("p", { class: "muted" }, n)));
        };
        input.addEventListener("input", redraw);
        redraw();
        extras = [mirror, trouble, clauses.cheatsheet(f.words, f.cheatsheet !== false)];
      } else {
        input = h("input", {
          type: f.kind === "number" ? "number" : "text",
          spellcheck: "false", autocapitalize: "off", autocorrect: "off",
          min: f.min, max: f.max, placeholder: f.placeholder,
        });
        // A property, not an attribute: an attribute is the *default* value, and
        // a reset would put it back rather than clearing the box.
        input.value = String(f.value ?? "");
      }
      inputs.set(f.name, { field: f, input });
      return h("label", { class: "field" },
        h("span", {}, f.label),
        input,
        f.hint ? h("span", { class: "muted hint" }, f.hint) : null,
        ...extras);
    });

    // Validated here, in the sheet, with the message beside the box that is
    // wrong. `alert` used to do this, which meant a second modal on top of the
    // first telling you about a value you could no longer see.
    const check = () => {
      const out = {};
      for (const [name, { field, input }] of inputs) {
        const raw = String(input.value ?? "").trim();
        if (field.kind === "number") {
          const n = Number.parseInt(raw, 10);
          if (!Number.isInteger(n) || (field.min != null && n < field.min) ||
              (field.max != null && n > field.max)) {
            return { error: `${field.label} must be a whole number from ${field.min} to ${field.max}` };
          }
          out[name] = n;
          continue;
        }
        if (field.required !== false && raw === "") {
          return { error: `${field.label} is needed` };
        }
        out[name] = field.kind === "choice" ? Number.parseInt(raw, 10) || raw : raw;
      }
      return { values: out };
    };

    const go = () => {
      const got = check();
      if (got.error) {
        // role=alert so it is announced when it appears: somebody who cannot see
        // the sheet has otherwise pressed a button and had nothing happen.
        mount(trouble, h("p", { role: "alert" }, got.error));
        return;
      }
      done(got.values);
    };

    // novalidate, deliberately. `min` and `max` stay on the inputs — they drive the
    // spinner and the numeric keypad on a phone — but without this the browser
    // enforces them itself and refuses to submit, showing its own bubble in its
    // own font. That is the exact furniture this dialog exists to replace, and it
    // swallowed the submit rather than showing the message below.
    const form = h("form", { novalidate: true, onsubmit: (e) => { e.preventDefault(); go(); } },
      ...rows,
      trouble,
      h("div", { class: "controls" },
        h("button", { type: "submit", class: danger ? "danger" : "" }, submit),
        h("button", { type: "button", class: "quiet", onclick: () => done(null) }, "cancel"),
        h("span", { class: "muted" }, "esc cancels")));

    return {
      elements: [h("h2", {}, title), note ? h("p", { class: "muted" }, note) : null, form],
      focus: inputs.values().next().value.input,
    };
  });
}

// one is the common case: a single value, asked for by name.
export async function one({ title, label, value, note, hint, submit }) {
  const got = await ask({
    title, note, submit,
    fields: [{ name: "value", label, value, hint }],
  });
  return got ? got.value : null;
}

// confirm asks before something that cannot be taken back, and resolves true or
// false.
//
// It says what will happen rather than asking "are you sure": somebody who has
// read "this cannot be undone" has been told something, and somebody who has read
// "are you sure?" has not.
export function confirm({ title, body, submit = "do it", danger = true }) {
  return sheet((done) => {
    const go = h("button", { class: danger ? "danger" : "", onclick: () => done(true) }, submit);
    return {
      elements: [
        h("h2", {}, title),
        body ? h("p", {}, body) : null,
        h("div", { class: "controls" }, go,
          h("button", { class: "quiet", onclick: () => done(false) }, "cancel"),
          h("span", { class: "muted" }, "esc cancels")),
      ],
      // The cancel is not focused and the confirm is: focusing cancel makes the
      // enter key mean "no", which surprises anybody who opened this on purpose.
      // Escape is still one key away, and it is the one people reach for.
      focus: go,
    };
  }).then((got) => got === true);
}

// isOpen reports whether a dialog is up, so a redraw can leave it alone.
export function isOpen() {
  return Boolean(panel && panel.isConnected && !panel.hidden);
}
