// Tests for the views. They run under `node --test` with the same minimal DOM
// the markdown tests use.
//
// Importing views.js at all is half the value: it is the largest module in the
// interface, and a syntax error in it blanks the whole site with nothing but a
// console message to say so. The rest is about the mailbox, which is the one
// view whose meaning changes with which box it is drawing.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const views = await import("../views.js");

// text renders a view and returns everything it says, which is what a reader
// would see. Asserting on structure would test the renderer; this tests the
// view.
// Nulls are how a view says "not this one" — nav drops the admin link that
// way — and mount() skips them, so this does too.
function text(nodes) {
  return nodes.filter(Boolean).map((n) => n.textContent).join(" ");
}

const state = {
  machines: [],
  queue: [],
  inbox: [{
    puid: 0, mid: "m0", sent: "2026-07-25T03:30:09Z", from: "bob",
    to: ["redjive"], subject: "the parser", read: false, machine: "studio",
    convo: { uid: "c1", title: "the parser", index: 1 },
  }],
  archive: [{
    puid: 1, mid: "m1", sent: "2026-07-25T03:30:09Z", from: "bob",
    to: ["redjive"], subject: "old news", read: false, machine: "studio",
  }],
  sent: [{
    puid: 2, mid: "m2", sent: "2026-07-25T03:30:09Z", from: "redjive",
    to: ["bob"], subject: "my own note", read: true, machine: "studio",
  }],
};

test("each box draws its own mail and no one else's", () => {
  assert.match(text(views.mailbox(state, { box: "inbox" })), /the parser/);
  assert.doesNotMatch(text(views.mailbox(state, { box: "inbox" })), /old news/);
  assert.match(text(views.mailbox(state, { box: "archive" })), /old news/);
  assert.match(text(views.mailbox(state, { box: "sent" })), /my own note/);
});

// Sent mail shows the recipient, because the sender is always you and a column
// of your own name tells the reader nothing.
test("sent mail names the recipient rather than the sender", () => {
  const out = text(views.mailbox(state, { box: "sent" }));
  assert.match(out, /bob/);
  assert.doesNotMatch(out, /redjive/);
});

test("an empty box says which box is empty", () => {
  const empty = { machines: [], queue: [], inbox: [], archive: [], sent: [] };
  assert.match(text(views.mailbox(empty, { box: "inbox" })), /no mail/);
  assert.match(text(views.mailbox(empty, { box: "archive" })), /nothing archived/);
  assert.match(text(views.mailbox(empty, { box: "sent" })), /nothing sent/);
});

// An unknown box is a routing bug, not a reason to throw: the router is the
// URL bar, and a typed hash must not blank the interface.
test("an unrecognised box falls back to the inbox rather than throwing", () => {
  assert.match(text(views.mailbox(state, { box: "nonsense" })), /no mail/);
});

// --- compose -----------------------------------------------------------

// recipients is where a typo is caught. It is checked here rather than only on
// the server because a queued message is applied minutes later on another
// machine: caught now it is a line of text, caught then it is a failure the
// writer must come back for and cannot fix without rewriting the message.
test("recipients accepts the separators people actually type", () => {
  for (const typed of ["bob, carol", "bob carol", "bob,carol", " bob , carol "]) {
    const got = views.recipients(typed);
    assert.equal(got.error, "", typed);
    assert.deepEqual(got.names, ["bob", "carol"], typed);
  }
});

test("recipients normalises case and drops repeats", () => {
  const got = views.recipients("Bob, BOB, carol");
  assert.equal(got.error, "");
  assert.deepEqual(got.names, ["bob", "carol"]);
});

test("recipients names the offending token rather than only refusing", () => {
  const got = views.recipients("bob, carol!, -nope");
  assert.match(got.error, /carol!/, "the writer has to be told which one is wrong");
  assert.match(got.error, /-nope/, "and told about all of them, not just the first");
  assert.doesNotMatch(got.error, /\bbob\b/, "the good name is not the problem");
  assert.deepEqual(got.names, [], "nothing is sent when part of the list is wrong");
});

test("recipients refuses an empty field", () => {
  for (const typed of ["", "   ", ",,,"]) {
    assert.match(views.recipients(typed).error, /no recipients/, JSON.stringify(typed));
  }
});

const composeState = { ...state, machines: [{ machine: "studio", last_sync: "2026-07-25T03:30:00Z" }] };

test("compose offers the fields a message needs", () => {
  const out = text(views.compose(composeState, {}));
  for (const label of ["to", "subject", "message"]) {
    assert.match(out, new RegExp(label));
  }
});

// Nothing leaves the browser until the agent machine syncs, and a form that let
// "queued" read as "sent" would be lying about the one thing that matters here.
test("compose says the message has not left yet", () => {
  assert.match(text(views.compose(composeState, {})), /leaves on the next sync/);
});

test("compose with nowhere to send from says so instead of offering a form", () => {
  const out = text(views.compose({ ...state, machines: [] }, {}));
  assert.match(out, /nothing has synced yet/);
});

// One machine is the ordinary case, and a picker with one option is a question
// with one answer.
test("compose only asks which machine when there is a choice", () => {
  const one = views.compose(composeState, {});
  assert.doesNotMatch(text(one), /from/);

  const two = views.compose({
    ...composeState,
    machines: [{ machine: "studio" }, { machine: "laptop" }],
  }, {});
  assert.match(text(two), /from/);
  assert.match(text(two), /laptop/);
});

test("compose shows the messages still waiting to go out", () => {
  const out = text(views.compose({
    ...composeState,
    queue: [{
      action: { op: "send", machine: "studio", args: { subject: "hello", body: "a body" } },
      state: "queued",
    }],
  }, {}));
  assert.match(out, /waiting/);
  assert.match(out, /a body/);
});

// --- cc ------------------------------------------------------------------

const threaded = {
  puid: 0, subject: "the parser", from: "bob", to: ["redjive"], machine: "studio",
  body: "hello", sent: "2026-07-25T03:30:09Z", read: false,
  convo: { uid: "c1", title: "the parser", index: 1 },
};
const unthreaded = { ...threaded, convo: null };

test("a threaded message offers the cc control", () => {
  const out = text(views.message(state, { message: threaded, thread: [] }, {}));
  assert.match(out, /add to conversation/);
});

// `cc` addresses a conversation, so a message that is not in one has nothing to
// add anyone to. Offering the control would offer a button that cannot work.
test("a message with no conversation does not offer it", () => {
  const out = text(views.message(state, { message: unthreaded, thread: [] }, {}));
  assert.doesNotMatch(out, /add to conversation/);
});

// --- the queue ---------------------------------------------------------

const refused = {
  action: { id: "a".repeat(32), op: "reply", machine: "studio", args: { puid: 3, subject: "RE: hi" } },
  state: "failed", error: "mailman said no",
};
const doubtfulSend = {
  action: { id: "b".repeat(32), op: "send", machine: "studio", args: { to: ["bob"], subject: "hi" } },
  state: "in_doubt", error: "interrupted; it may or may not have been applied",
};
const doubtfulRead = {
  action: { id: "c".repeat(32), op: "read", machine: "studio", args: { puid: 4 } },
  state: "in_doubt", error: "interrupted; it may or may not have been applied",
};

test("an empty queue says so rather than showing an empty frame", () => {
  assert.match(text(views.queue({ queue: [] }, {})), /nothing queued/);
});

test("a refused action offers both ways out", () => {
  const out = text(views.queue({ queue: [refused] }, {}));
  assert.match(out, /refused/);
  assert.match(out, /try again/);
  assert.match(out, /forget it/);
  assert.match(out, /mailman said no/, "the reason is the whole point of the row");
});

// Sending twice is a second message to a real person, and cq cannot tell whether
// an interrupted send arrived. The control is absent, not merely refused.
test("an interrupted send is not offered a retry", () => {
  const out = text(views.queue({ queue: [doubtfulSend] }, {}));
  assert.doesNotMatch(out, /try again/);
  assert.match(out, /check your sent mail/, "the reader is told how to settle it themselves");
  assert.match(out, /forget it/, "it can still be cleared");
});

// Marking mail read twice is marking it read, so refusing here would strand the
// reader for no reason.
test("an interrupted read is offered a retry", () => {
  assert.match(text(views.queue({ queue: [doubtfulRead] }, {})), /try again/);
});

test("something already on its way offers nothing to do", () => {
  const waiting = { action: { id: "d".repeat(32), op: "read", args: { puid: 1 } }, state: "queued" };
  const out = text(views.queue({ queue: [waiting] }, {}));
  assert.match(out, /waiting/);
  assert.doesNotMatch(out, /try again/);
  assert.doesNotMatch(out, /forget it/);
});

test("the queue leads with what needs a decision", () => {
  const done = { action: { id: "e".repeat(32), op: "read", args: { puid: 9 } }, state: "done" };
  const out = text(views.queue({ queue: [done, refused] }, {}));
  assert.ok(out.indexOf("refused") < out.indexOf("done"),
    `history should come last:\n${out}`);
});

// A count that included things merely on their way would never reach zero, so
// the badge would stop meaning anything.
test("the badge counts only what needs a decision", () => {
  const waiting = { action: { id: "f".repeat(32), op: "read", args: { puid: 1 } }, state: "queued" };
  const withStuck = text(views.nav({ inbox: [], queue: [refused, waiting] }, "/inbox"));
  assert.match(withStuck, /queue\s*1/);

  const clean = text(views.nav({ inbox: [], queue: [waiting] }, "/inbox"));
  assert.doesNotMatch(clean, /queue\s*\d/);
});

test("the navigation offers every box", () => {
  const out = text(views.nav({ ...state, adminEnabled: false }, "/inbox"));
  for (const box of ["inbox", "compose", "sent", "archive", "queue"]) {
    assert.match(out, new RegExp(box));
  }
});

// The admin panel lists accounts by name alone, because a name is all Mailman
// keeps: it once showed a creation time and the column was always blank.
test("the account list renders from names alone", () => {
  const out = text(views.admin({
    ...state,
    admin: {
      machines: [{
        machine: "studio",
        state: {
          users: [{ name: "bob" }, { name: "redjive" }],
          messages: [], receipts: [], metadata_only: true,
        },
      }],
    },
  }));
  assert.match(out, /bob/);
  assert.match(out, /redjive/);
});

// A machine that syncs without the admin view is a normal state, not an error.
test("a machine with no admin view says so", () => {
  const out = text(views.admin({
    ...state,
    admin: { machines: [{ machine: "studio", state: null }] },
  }));
  assert.match(out, /syncs without the admin view/);
});
