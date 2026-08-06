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
const check = await import("../check.js");

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

// said is every complaint currently on the sheet, wherever it is shown.
function said() {
  return all(document.body, (n) => n.className === "field-error" || n.className === "trouble")
    .map((n) => n.textContent).join(" ");
}

// marked is the boxes flagged as wrong, which is what makes one findable among
// six.
function marked() {
  return all(document.body, (n) => n.getAttribute && n.getAttribute("aria-invalid") === "true");
}

// blur runs a field's leave handler, which is when the first complaint is due.
function blur(el) {
  for (const fn of (el.listeners.blur || [])) fn({ target: el });
}

function type(el, text) {
  el.value = text;
  for (const fn of (el.listeners.input || [])) fn({ target: el });
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

  // Beside the field, not at the foot of the form: with six boxes on a sheet, a
  // message at the bottom means reading it and then working out which one it
  // meant.
  assert.match(said(), /priority.*1 to 5/, "nothing was said about the bad value");
  assert.equal(marked().length, 1, "the offending box was not marked");
  assert.ok(dialog.isOpen(), "the sheet closed on a value it refused");

  // And it accepts the correction rather than having to be reopened.
  boxes[1].value = "5";
  submit();
  assert.equal((await done).priority, 5);
});

test("an empty required field is refused, and says which", async () => {
  const done = dialog.ask({ title: "a new task", fields: taskFields });
  submit();
  assert.match(said(), /name cannot be empty/);

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

// --- saying what is wrong, before it is sent -------------------------------
//
// The complaint these answer: things failed with opaque errors instead of the
// form saying what it wanted. A name with a space in it used to be accepted,
// queued, and refused by orc minutes later on a machine nobody was watching —
// so the person who typed it read the refusal, in a terminal's words, long after
// they had moved on.

test("a name that the tool would refuse is refused here, with the reason", async () => {
  dialog.ask({ title: "hire", fields: [{ name: "who", label: "name", check: "mailbox" }] });
  const box = inputs()[0];
  box.value = "my/agent";
  submit();

  assert.match(said(), /position 3/, `it did not say where: ${said()}`);
  assert.match(said(), /letters, digits/, said());
  assert.ok(dialog.isOpen(), "it queued a name that would have been refused");
  press("Escape");
});

// The two reserved sets differ, and a check that used the wrong one would refuse
// a name that works — the one failure mode worse than the original problem.
test("a reserved name is caught, and only in the right sheet", async () => {
  dialog.ask({ title: "hire", fields: [{ name: "who", label: "name", check: "mailbox" }] });
  inputs()[0].value = "system";
  submit();
  assert.match(said(), /reserved/, said());
  press("Escape");

  // `system` is a perfectly good role name, and must not be refused here.
  const done = dialog.ask({ title: "new role", fields: [{ name: "n", label: "name", check: "label" }] });
  inputs()[0].value = "system";
  submit();
  assert.deepEqual(await done, { n: "system" });
});

// clock.ParseSpan is not time.ParseDuration, so this is what somebody fluent in
// Go types. The generic message would send them hunting for a typo.
test("a compound duration is refused with the spelling that works", async () => {
  dialog.ask({
    title: "grant", submit: "grant",
    fields: [{ name: "until", label: "until", check: "span" }],
  });
  inputs()[0].value = "1h30m";
  submit();
  assert.match(said(), /90m/, `it should offer the spelling that works: ${said()}`);
  press("Escape");
});

// A field allowed to be empty is still checked once it has something in it.
test("an optional field is checked when it is filled in", async () => {
  const done = dialog.ask({
    title: "grant",
    fields: [{ name: "until", label: "until", required: false, check: "span" }],
  });
  const box = inputs()[0];
  box.value = "2 hours";
  submit();
  assert.match(said(), /30m or 2h/, said());

  // And blank still goes through, because blank is what "optional" means.
  box.value = "";
  submit();
  assert.deepEqual(await done, { until: "" });
});

// Stopping at the first problem means a form with three of them is submitted
// three times, each attempt revealing one more — and the third refusal reads as
// the site being broken rather than the form having said what it wanted.
test("every problem is reported at once, not one per attempt", async () => {
  dialog.ask({
    title: "a new task",
    fields: [
      { name: "name", label: "name", check: "label" },
      { name: "priority", label: "priority", kind: "number", value: 3, min: 1, max: 5 },
      { name: "difficulty", label: "difficulty", kind: "number", value: 3, min: 1, max: 5 },
    ],
  });
  const boxes = inputs();
  boxes[0].value = "not a name!";
  boxes[1].value = "9";
  boxes[2].value = "0";
  submit();

  assert.equal(marked().length, 3, `only ${marked().length} of 3 bad fields were marked`);
  const text = said();
  for (const want of [/name/, /priority/, /difficulty/]) {
    assert.match(text, want, `it did not mention every bad field: ${text}`);
  }
  press("Escape");
});

// A summary only when there is more than one. With a single problem the line
// beside the box is the whole story, and repeating it below is noise.
test("one problem is said once", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  inputs()[0].value = "bad name!";
  submit();

  const summary = find(document.body, (n) => n.className === "trouble");
  assert.equal(summary.textContent, "", `a single problem was also summarised: ${summary.textContent}`);
  press("Escape");
});

// Where the keyboard lands. Somebody who cannot see the sheet has otherwise
// pressed a button and been told that something, somewhere, is wrong.
test("focus goes to the first field that needs fixing", async () => {
  dialog.ask({
    title: "t",
    fields: [
      { name: "a", label: "a", required: false },
      { name: "b", label: "b", check: "label" },
      { name: "c", label: "c", check: "label" },
    ],
  });
  const boxes = inputs();
  boxes[1].value = "bad!";
  boxes[2].value = "also bad!";
  submit();
  assert.equal(boxes[1].focused, true, "focus was not put on the first problem");
  press("Escape");
});

// When to complain, which is most of whether this helps or nags. A name is
// invalid for as long as it is half-written, so a box that reddens on the first
// keystroke is red the whole time it is being filled in.
// An empty box is left alone. A sheet that opens with every required field
// already red has told the reader nothing except that they have not started.
test("an untouched field is not marked", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  const box = inputs()[0];
  type(box, "r");
  type(box, "re");
  type(box, "rev");
  assert.equal(marked().length, 0, "it complained about a name that is only half-written");
  type(box, "");
  assert.equal(marked().length, 0, "it complained about an empty box");
  press("Escape");
});

// The rule that replaced "wait until they leave the field".
//
// Waiting made sense against a length rule, where a good value passes through
// bad ones on the way. It makes none against these: every prefix of a good name
// is a good name, so the only way to see this while typing one is to type a
// character that will never be allowed — which is the moment to say so, not four
// fields later.
test("a character that can never be allowed is named as it is typed", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  const box = inputs()[0];
  type(box, "rev%");
  assert.equal(marked().length, 1, "it waited for the field to be left");
  assert.match(said(), /position 4/, said());
  press("Escape");
});

// And once marked, it clears as the value is fixed — rather than sitting there
// accusing until the next attempt to submit.
test("a complaint goes away as it is fixed", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  const box = inputs()[0];
  type(box, "bad name!");
  blur(box);
  assert.equal(marked().length, 1, "a bad value was not marked on leaving the field");

  type(box, "goodname");
  assert.equal(marked().length, 0, "the complaint stayed after the value was corrected");
  assert.equal(said().trim(), "", `the message stayed: ${said()}`);
  press("Escape");
});

// Leaving an empty field alone is not a mistake yet — somebody tabbing through a
// sheet to see its shape should not be scolded for every box they pass.
test("tabbing past an empty field says nothing", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  const box = inputs()[0];
  blur(box);
  assert.equal(marked().length, 0, "an untouched empty field was marked on the way past");
  press("Escape");
});

// A misspelled check must not stop the sheet opening: validating one field less
// is a small loss, a dialog that will not appear is the whole action lost.
test("a check that does not exist does not break the sheet", async () => {
  const done = dialog.ask({ title: "t", fields: [{ name: "n", label: "n", check: "nosuchthing" }] });
  inputs()[0].value = "whatever";
  submit();
  assert.deepEqual(await done, { n: "whatever" });
});

// one() builds its field spec by hand, so it is where a field property goes
// missing — and it did: `check` was dropped, leaving eight single-field sheets
// asking for names with the validation quietly discarded. The sheets looked
// right, the rules were declared, and nothing enforced them.
test("one() carries the field's check, and not only its label", async () => {
  dialog.one({ title: "hire an agent", label: "name", check: "mailbox" });
  const box = inputs()[0];
  box.value = "my/agent";
  submit();

  assert.match(said(), /position 3/, `one() dropped the check: ${said()}`);
  assert.equal(marked().length, 1, "the box was not marked");
  press("Escape");
});

test("one() carries required, so an optional single field may be left blank", async () => {
  const done = dialog.one({ title: "t", label: "note", required: false });
  submit();
  assert.equal(await done, "");
});

// --- what cannot be queued, and what a name becomes ------------------------

// said is a summary; this is the button. The complaint on the field is only
// half the answer to "why did nothing happen when I pressed queue" — the other
// half is that the button says so before it is pressed.
function submitButton() {
  return find(document.body, (n) => n.tagName === "BUTTON"
    && n.getAttribute("aria-disabled") !== null);
}

test("the submit refuses while a field is wrong, and offers itself when it is not", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  const box = inputs()[0];

  // Empty and required: nothing to queue yet.
  assert.equal(submitButton().getAttribute("aria-disabled"), "true",
    "an unfilled form offered to queue itself");

  type(box, "rev%");
  assert.equal(submitButton().getAttribute("aria-disabled"), "true",
    "a form with a bad name offered to queue itself");

  type(box, "reviewer");
  assert.equal(submitButton().getAttribute("aria-disabled"), "false",
    "a sound form still refused to queue");
  press("Escape");
});

// aria-disabled rather than disabled, so it is still reachable — and pressing it
// has to do something, or it is a wall with no explanation for anybody who
// cannot see the red field.
test("pressing a refusing submit says what is wrong rather than nothing", async () => {
  let settled = false;
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] })
    .then(() => { settled = true; });

  submit();
  assert.equal(settled, false, "it queued a form it had said was not ready");
  assert.match(said(), /cannot be empty/, said());
  assert.equal(marked().length, 1, "it refused without marking the field");
  press("Escape");
});

// The lift, end to end: a name written the way a person writes one goes through,
// and what goes out is what the tools spell.
test("a name typed with capitals and spaces is queued as the tools spell it", async () => {
  const done = dialog.ask({
    title: "a new task",
    fields: [{ name: "name", label: "name", check: "label" }],
  });
  const box = inputs()[0];
  type(box, "Fix The Parser");

  assert.equal(marked().length, 0, `it refused a name a person would write: ${said()}`);
  // And says so, because the board will show the tidied name afterwards and
  // finding that out from the board reads as cq having renamed it.
  const becomes = all(document.body, (n) => n.className === "field-becomes")
    .map((n) => n.textContent).join(" ");
  assert.match(becomes, /fix-the-parser/, `it did not say what the name becomes: ${becomes}`);

  submit();
  assert.deepEqual(await done, { name: "fix-the-parser" });
});

test("a name that needs no tidying says nothing about it", async () => {
  dialog.ask({ title: "t", fields: [{ name: "n", label: "name", check: "label" }] });
  type(inputs()[0], "reviewer");
  const becomes = all(document.body, (n) => n.className === "field-becomes")
    .map((n) => n.textContent).join("");
  assert.equal(becomes, "", `it explained a name that was already what it will be: ${becomes}`);
  press("Escape");
});

// --- picking a permission rather than remembering its name -------------------

// The mirror already carries every permission and every clause in it, so a form
// that asked somebody to type one from memory was asking for the one thing it
// could have shown them.
const somePerms = [
  { name: "upgrade", floor: 60, patterns: ["orc(upgrade)"] },
  { name: "edit", floor: 20, patterns: ["edit(**)"] },
];

function permissionSheet(extra = {}) {
  return dialog.ask({
    title: "give engineer permissions",
    fields: [{
      name: "permissions", label: "permissions", kind: "permissions",
      known: somePerms, words: [], ...extra,
    }],
  });
}

function rows() {
  return all(document.body, (n) => n.className && String(n.className).startsWith("permission-row"));
}

test("every permission the fleet has is shown, with what it allows", () => {
  permissionSheet();
  const got = rows();
  assert.equal(got.length, 2, `${got.length} rows drawn`);
  const text = got.map((r) => r.textContent).join(" ");
  for (const want of ["edit", "upgrade", "orc(upgrade)", "edit(**)", "60", "20"]) {
    assert.ok(text.includes(want), `the list does not show ${want}: ${text}`);
  }
  // Sorted, so the same fleet draws the same list twice.
  assert.ok(got[0].textContent.startsWith("edit"), `unsorted: ${got[0].textContent}`);
  press("Escape");
});

test("clicking a row puts the name in the box, and clicking it again takes it out", () => {
  permissionSheet();
  const box = inputs()[0];
  const [edit, upgrade] = rows();

  click(edit);
  assert.equal(box.value, "edit");
  click(upgrade);
  assert.equal(box.value, "edit upgrade");
  click(edit);
  assert.equal(box.value, "upgrade", "a second click did not remove it");
  press("Escape");
});

// The two reasons a row cannot be used are different, and only one of them tells
// somebody what to change.
test("a row says when the role has it already, and when its authority is too low", () => {
  permissionSheet({ held: ["edit"], authority: 30, roleName: "engineer" });
  const got = rows();
  const held = got.find((r) => r.textContent.startsWith("edit"));
  const barred = got.find((r) => r.textContent.startsWith("upgrade"));

  assert.ok(String(held.className).includes("held"), `not marked as held: ${held.className}`);
  assert.ok(held.textContent.includes("held"), `no word for it: ${held.textContent}`);
  assert.ok(String(barred.className).includes("barred"), `not marked: ${barred.className}`);
  assert.match(barred.textContent, /authority/);
  press("Escape");
});

// Several at once is the whole point of the box staying a box.
test("several names are accepted and come back as typed", async () => {
  const done = permissionSheet({
    check: (raw) => (String(raw).trim() === "" ? "empty" : ""),
  });
  type(inputs()[0], "edit upgrade");
  submit();
  assert.deepEqual(await done, { permissions: "edit upgrade" });
});

// And the check runs against the list, live, like every other field.
test("a name the fleet does not have is marked as it is typed", () => {
  permissionSheet({ check: check.permissions(somePerms, { authority: 99 }) });
  type(inputs()[0], "editt");
  assert.equal(marked().length, 1, "an unknown permission was not marked");
  assert.match(said(), /did you mean “edit”/, said());
  press("Escape");
});
