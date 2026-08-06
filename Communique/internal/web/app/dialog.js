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
import * as check from "./check.js";

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
      } else if (f.kind === "pick") {
        // A name, with the list it has to come from under it.
        //
        // Half the forms in cq ask for something the mirror is already holding: a
        // role, an agent, a permission. Asking somebody to type its name from
        // memory is asking for the one thing a browser is placed to make
        // unnecessary — and it is where the typos come from, which then travel to
        // a machine nobody is watching and fail there.
        //
        // The box stays a text box rather than becoming a menu. Somebody who knows
        // the name types it and is done, and a `multiple` field takes several on
        // one line, which reads back as one line.
        input = h("input", {
          type: "text", spellcheck: "false", autocapitalize: "off",
          autocorrect: "off", placeholder: f.placeholder,
        });
        input.value = String(f.value ?? "");
        extras = [picker(f, input)];
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
      // Each field carries its own place to be wrong, beside the box rather than
      // at the foot of the form. A single message at the bottom means reading it,
      // then looking back up and working out which of five boxes it meant — and
      // with several things wrong it could only ever name one of them.
      const problem = h("p", { class: "field-error" });
      // What the value will actually be, when that is not what was typed.
      //
      // A name may be written with capitals and spaces now, and is sent
      // lower-cased with dashes. That is a kindness only if it is visible: a task
      // somebody called "Fix The Parser" appears in every listing afterwards as
      // `fix-the-parser`, and finding that out later — from a board that seems to
      // have renamed it — is worse than the refusal this replaced.
      const becomes = h("p", { class: "field-becomes" });
      const row = { field: f, input, problem, becomes, marked: false };
      inputs.set(f.name, row);

      // verify marks this one field, and says whether it is wrong.
      const verify = () => {
        const bad = fault(f, input);
        mount(problem, bad ? h("span", { role: "alert" }, bad) : null);
        // The box itself is marked too. The message says what is wrong; this is
        // what makes it findable in a sheet with six fields in it, and what a
        // screen reader reads as part of the field rather than as loose prose.
        if (bad) {
          input.setAttribute("aria-invalid", "true");
        } else {
          input.removeAttribute("aria-invalid");
        }
        row.marked = Boolean(bad);
        return bad;
      };
      row.verify = verify;
      // bad is the same question asked without marking anything, for the submit
      // button — which has to know whether the form is sound on every keystroke,
      // and must not turn the whole sheet red to find out.
      row.bad = () => Boolean(fault(f, input));

      // showBecomes draws the "queued as" line, and nothing when the value goes
      // through unchanged — which is the overwhelming majority of the time, and a
      // line saying so on every field would be noise standing in for information.
      const tidier = check.tidierOf(f.check);
      const showBecomes = () => {
        const raw = String(input.value ?? "").trim();
        const as = tidier ? tidier(raw) : raw;
        mount(becomes, tidier && as !== "" && as !== raw && !fault(f, input)
          ? h("span", {}, `queued as “${as}”`)
          : null);
      };
      row.showBecomes = showBecomes;

      // When to complain, which is most of whether this is help or nagging.
      //
      // As it is typed, once there is something in the box. The earlier rule waited
      // for the field to be left, on the reasoning that a half-written name is
      // invalid and a box that reddens on the first keystroke stays red the whole
      // time. That is true of a length rule and false of these: every prefix of a
      // good name is a good name, so the only way to see the message while typing
      // one is to type a character that will never be allowed — which is exactly
      // when somebody should be told, rather than four fields later.
      //
      // An *empty* box is still left alone until it is left or submitted. A sheet
      // that opens with every required field already marked has told the reader
      // nothing except that they have not filled it in yet.
      const live = () => {
        if (String(input.value ?? "").trim() !== "" || row.marked) verify();
        showBecomes();
        settle();
      };
      input.addEventListener("blur", () => { if (String(input.value ?? "") !== "") verify(); settle(); });
      input.addEventListener("input", live);
      input.addEventListener("change", live);

      return h("label", { class: "field" },
        h("span", {}, f.label),
        input,
        f.hint ? h("span", { class: "muted hint" }, f.hint) : null,
        problem,
        becomes,
        ...extras);
    });

    // Validated here, in the sheet, with the message beside the box that is
    // wrong. `alert` used to do this, which meant a second modal on top of the
    // first telling you about a value you could no longer see.
    //
    // Every field is checked, not just up to the first bad one. Stopping early
    // means a form with three problems is submitted three times, each attempt
    // revealing one more — and the third refusal reads as the site being broken
    // rather than as the form having said what it wanted all along.
    const inspect = () => {
      const out = {};
      const problems = [];
      for (const [name, row] of inputs) {
        if (row.verify()) {
          problems.push(row);
          continue;
        }
        out[name] = value(row.field, row.input);
      }
      return problems.length > 0 ? { problems } : { values: out };
    };

    // The button, held out of `go` so `settle` can reach it.
    //
    // aria-disabled rather than disabled, deliberately. A `disabled` button cannot
    // be focused, is skipped by the tab order, and announces nothing — so somebody
    // who cannot see the four red fields gets a form with no way forward and no
    // explanation. This one stays reachable and still refuses: pressing it marks
    // every problem and moves the cursor to the first, which is the answer to
    // "why can I not queue this?" rather than a wall.
    const submitButton = h("button", { type: "submit", class: danger ? "danger" : "" }, submit);

    // settle keeps the button in step with the form, on every keystroke.
    const settle = () => {
      let bad = false;
      for (const [, row] of inputs) {
        if (row.bad()) { bad = true; break; }
      }
      submitButton.setAttribute("aria-disabled", bad ? "true" : "false");
      submitButton.classList.toggle("off", bad);
    };

    const go = () => {
      const got = inspect();
      if (got.problems) {
        // A summary only when there is more than one, and never instead of the
        // messages themselves: with a single problem the line beside the box is
        // the whole story, and repeating it at the foot of the form is noise.
        mount(trouble, got.problems.length > 1
          ? h("p", { role: "alert" }, `${got.problems.length} fields need fixing`)
          : null);
        // Focus goes to the first one, so the keyboard is already where the work
        // is — and so somebody who cannot see the sheet is taken to the field
        // rather than told that something, somewhere, is wrong.
        const first = got.problems[0].input;
        if (typeof first.focus === "function") first.focus();
        settle();
        return;
      }
      mount(trouble);
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
        submitButton,
        h("button", { type: "button", class: "quiet", onclick: () => done(null) }, "cancel"),
        h("span", { class: "muted" }, "esc cancels")));

    // Once at the start, so a sheet whose defaults are already sound opens with a
    // live button and one that needs filling in opens saying so.
    settle();
    for (const [, row] of inputs) row.showBecomes();

    return {
      elements: [h("h2", {}, title), note ? h("p", { class: "muted" }, note) : null, form],
      focus: inputs.values().next().value.input,
    };
  });
}

// one is the common case: a single value, asked for by name.
//
// The field spec is built here rather than passed through, which makes this the
// place a field property goes missing: anything not named below is silently
// dropped, and the caller sees a dialog that works and a rule that does nothing.
// `check` was exactly that for a while — eight single-field sheets asking for
// names with the validation quietly discarded — so anything added to a field
// belongs in this list as well as in `ask`.
export async function one({ title, label, value, note, hint, submit, check, required, placeholder }) {
  const got = await ask({
    title, note, submit,
    fields: [{ name: "value", label, value, hint, check, required, placeholder }],
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

// show is a sheet with nothing to decide: a document to read and a way out.
//
// Separate from confirm because confirm asks a question, and a sheet with a
// "do it" button on a thing that does nothing is how somebody learns not to trust
// the buttons. The text keeps its own line breaks — what it shows is prose that
// was written in paragraphs.
export function show({ title, text, note }) {
  return sheet((done) => {
    const close = h("button", { onclick: () => done(true) }, "close");
    return {
      elements: [
        h("h2", {}, title),
        note ? h("p", { class: "muted" }, note) : null,
        h("pre", { class: "reading" }, String(text ?? "")),
        h("div", { class: "controls" }, close, h("span", { class: "muted" }, "esc closes")),
      ],
      focus: close,
    };
  }).then(() => undefined);
}

// isOpen reports whether a dialog is up, so a redraw can leave it alone.
export function isOpen() {
  return Boolean(panel && panel.isConnected && !panel.hidden);
}

// fault is what is wrong with one field, or "" when nothing is.
//
// The order is the order somebody discovers things in, and it matters: a field
// left empty is *missing*, not malformed, and telling somebody their blank box
// has a bad character at position 1 is the kind of message that makes a form feel
// hostile. So emptiness is settled first, and only a value that is actually there
// is put to the field's own check.
export function fault(field, input) {
  const raw = String(input.value ?? "").trim();

  if (field.kind === "number") {
    return check.whole(raw, { min: field.min, max: field.max, what: field.label });
  }
  if (raw === "") {
    return field.required === false ? "" : `${field.label} cannot be empty`;
  }
  // A field that is allowed to be empty is still checked once it has something
  // in it: `--until` may be left blank, and "2 hours" in it is a mistake either
  // way.
  const rule = check.of(field.check);
  return rule ? rule(raw, field.label) : "";
}

// value is what a field contributes once it is known to be sound.
//
// A choice is parsed as a number when it looks like one because the fleet panel's
// selects carry authorities and loads; `Number.parseInt(...) || raw` would turn a
// legitimate zero into a string, which is why this asks rather than relying on
// the falsiness of 0.
function value(field, input) {
  const raw = String(input.value ?? "").trim();
  if (field.kind === "number") return Number.parseInt(raw, 10);
  if (field.kind === "choice" && /^-?[0-9]+$/.test(raw)) return Number.parseInt(raw, 10);
  // A name goes as the tools spell it, not as it was typed. The sheet has already
  // said so under the box; this is where it becomes true.
  const tidier = check.tidierOf(field.check);
  return tidier ? tidier(raw) : raw;
}

// picker draws everything a field may name, and what each one is.
//
// One picker for roles, agents, and permissions, because they are the same problem:
// a name the mirror is already holding, and a box that used to ask for it from
// memory. What differs between them is what a row *says* — a permission's clauses, a
// role's description, an agent's job — so the caller builds the rows and this draws
// them.
//
// A row carries: `name`, the value itself; `badge`, one short column of number or
// word; `detail`, a line of prose; `chips`, clauses drawn with the colouring the
// permissions tab uses; `held`, already true of the thing being edited; and
// `barred` with a `note`, for a row that cannot be used and the reason.
function picker(field, input) {
  const known = [...(field.known || [])];
  if (known.length === 0) {
    return h("p", { class: "muted hint" },
      field.empty || "there is nothing to choose from yet");
  }

  // Toggling for a field that takes several, replacing for one that takes one. A
  // click that was a mistake is undone by the same click either way.
  const toggle = (name) => {
    const want = check.tidy(name);
    if (!field.multiple) {
      input.value = input.value.trim() === want ? "" : want;
    } else {
      // Compared as the tools spell them, so clicking `edit` after typing `EDIT`
      // removes it rather than adding a second copy of the same thing.
      const names = check.names(input.value);
      const at = names.indexOf(want);
      if (at >= 0) names.splice(at, 1);
      else names.push(want);
      input.value = names.join(" ");
    }
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
  };

  const rows = known.map((row) => h("button", {
    type: "button",
    class: "pick-row" + (row.held ? " held" : "") + (row.barred ? " barred" : ""),
    // Said on the row rather than only on refusal: the reason something cannot be
    // used is a fact about it, and hiding that until somebody tries makes them try.
    title: row.note || (row.held ? `${row.name} — ${row.mark || "held"}` : `choose ${row.name}`),
    onclick: () => toggle(row.name),
  },
    h("span", { class: "pick-name" }, row.name),
    h("span", { class: "pick-badge muted" }, row.badge == null ? "" : String(row.badge)),
    row.chips
      ? h("span", { class: "clauses" }, ...row.chips.map((c) => clauses.chip(c, field.words)))
      : h("span", { class: "pick-detail muted" }, row.detail || ""),
    row.held ? h("span", { class: "pick-note muted" }, row.mark || "held") : null,
    row.barred ? h("span", { class: "pick-note warn" }, row.note || "not available") : null));

  return h("div", { class: "pick-list" },
    h("p", { class: "muted hint" }, field.multiple
      ? "click to add or remove; several may be queued at once, separated by spaces"
      : "click to choose"),
    ...rows);
}
