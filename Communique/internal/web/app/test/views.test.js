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

// --- whose turn an unfinished action is on ------------------------------

// Waiting for a sync to collect it and waiting for the agent to report on it are
// different facts, and every screen but the queue tab used to call both "queued".
// They differ in the only way a reader cares about: one is fixed by a sync from
// here, and the other is somebody else's machine taking its time.

const reply = (state) => ({
  action: { id: "a".repeat(32), op: "reply", machine: "studio", args: { puid: 0, body: "sure" } },
  state,
});

const withQueue = (...entries) => ({ ...state, queue: entries });
const detail = (m) => ({ message: m, thread: [] });
const inboxItem = state.inbox[0];

test("a reply waiting to be collected and one with the agent read differently", () => {
  const here = text(views.message(withQueue(reply("queued")), detail(inboxItem), {}));
  const there = text(views.message(withQueue(reply("sent")), detail(inboxItem), {}));

  assert.match(here, /waiting/);
  assert.match(here, /leaves on the next sync/);

  assert.match(there, /with the agent/);
  assert.match(there, /not yet reported on/);
  assert.doesNotMatch(there, /leaves on the next sync/,
    "an action the agent already has is not waiting on a sync from here");
});

// The word for `sent` matters more than the others. In a mailbox "sent" means it
// reached a person; here it means a machine picked it up, and the two are a long
// way apart when somebody is deciding whether to write the message again.
test("an action the agent holds is never called sent", () => {
  const out = text(views.message(withQueue(reply("sent")), detail(inboxItem), {}));
  assert.doesNotMatch(out, /\bsent\b/);
});

// The marker beside a mailbox row said "replied" for every pending state — which
// for a refusal is the opposite of what happened.
test("the row says which state the reply is in", () => {
  for (const [entry, want] of [
    [reply("queued"), /reply waiting/],
    [reply("sent"), /reply with the agent/],
    [{ ...reply("failed"), error: "no such mailbox" }, /reply refused/],
  ]) {
    assert.match(text(views.mailbox(withQueue(entry), { box: "inbox" }, {})), want);
  }
});

test("a refusal outranks a reply still on its way", () => {
  const out = text(views.mailbox(
    withQueue(reply("queued"), { ...reply("failed"), error: "no such mailbox" }),
    { box: "inbox" }, {}));
  assert.match(out, /reply refused/, "the refusal was hidden behind a later reply");
});

// Both screens take their words from one table, so neither can start calling a
// state something the other does not.
test("the queue tab and the cards agree on what a state is called", () => {
  for (const state of ["queued", "sent", "failed"]) {
    const onCard = text(views.message(withQueue(reply(state)), detail(inboxItem), {}));
    const inTab = text(views.queue({ queue: [reply(state)] }, {}));
    const word = { queued: "waiting", sent: "with the agent", failed: "refused" }[state];
    assert.match(onCard, new RegExp(word));
    assert.match(inTab, new RegExp(word));
  }
});

// A state this build has never heard of is a newer server, not a reason to draw a
// blank badge — and certainly not to throw inside a render.
test("an unknown state is named rather than drawn blank", () => {
  const out = text(views.queue({ queue: [reply("tomorrows_state")] }, {}));
  // The row is drawn, under a group that says why it cannot be described. Before
  // this, every group filtered it out and the tab read "nothing queued".
  assert.match(out, /tomorrows state/);
  assert.match(out, /not recognised/);
  assert.doesNotMatch(out, /nothing queued/);
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

// An action still waiting has never left this machine. Cancelling means it never
// goes — nothing was attempted, so there is nothing to reason about afterwards.
test("something still waiting can be cancelled", () => {
  const waiting = { action: { id: "d".repeat(32), op: "read", args: { puid: 1 } }, state: "queued" };
  const out = text(views.queue({ queue: [waiting] }, {}));
  assert.match(out, /waiting/);
  assert.match(out, /cancel/);
  // Not the words for a thing that already happened: it has not.
  assert.doesNotMatch(out, /try again/);
  assert.doesNotMatch(out, /forget it/);
  assert.doesNotMatch(out, /remove/);
});

// The one row with no button. It may be applying this second, so the server
// refuses to delete it and the button is absent rather than failing.
test("something already with the agent offers nothing to do", () => {
  const gone = { action: { id: "d".repeat(32), op: "read", args: { puid: 1 } }, state: "sent" };
  const out = text(views.queue({ queue: [gone] }, {}));
  assert.match(out, /with the agent/);
  assert.doesNotMatch(out, /cancel/);
  assert.doesNotMatch(out, /remove/);
  assert.doesNotMatch(out, /forget it/);
});

// "remove" on something that has not gone is a lie about what the press does,
// and it is the press somebody makes in a hurry.
test("the word matches what pressing it would do", () => {
  const rows = [
    { state: "queued", want: "cancel" },
    { state: "failed", want: "forget it" },
    { state: "in_doubt", want: "forget it" },
    { state: "done", want: "remove" },
  ];
  for (const { state, want } of rows) {
    const entry = { action: { id: "d".repeat(32), op: "read", args: { puid: 1 } }, state };
    const out = text(views.queue({ queue: [entry] }, {}));
    assert.match(out, new RegExp(want), `a ${state} row should offer "${want}": ${out}`);
  }
});

test("cancelling hands the entry to the action", () => {
  const waiting = { action: { id: "d".repeat(32), op: "send", args: { to: ["bob"], subject: "oops" } }, state: "queued" };
  const asked = [];
  const nodes = views.queue({ queue: [waiting] }, { drop: (e) => asked.push(e) });

  const button = findAll(nodes, (n) => n.tagName === "BUTTON" && n.textContent === "cancel")[0];
  assert.ok(button, "no cancel button");
  button.listeners.click.forEach((fn) => fn({ target: button }));
  assert.equal(asked.length, 1);
  assert.equal(asked[0].action.id, "d".repeat(32));
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
  const withStuck = text(views.nav({ inbox: [], queue: [refused, waiting] }, "/tooling/queue"));
  assert.match(withStuck, /queue\s*1/);

  const clean = text(views.nav({ inbox: [], queue: [waiting] }, "/tooling/queue"));
  assert.doesNotMatch(clean, /queue\s*\d/);
});

// A badge on a sub-tab is invisible whenever another area is open — which is
// most of the time, and exactly when it matters. So the area carries the sum.
test("a count reaches the area above it", () => {
  const refusedTwice = [refused, { ...refused, action: { ...refused.action, id: "c".repeat(32) } }];
  const out = text(views.nav({ inbox: [], queue: refusedTwice }, "/mail/inbox"));
  // Tooling is closed, so only the area is on screen — and it still says two.
  assert.match(out, /tooling\s*2/);
  assert.doesNotMatch(out, /queue/, "a closed area should not show its sub-tabs");
});

test("the navigation shows the areas, and the open one's tabs", () => {
  const out = text(views.nav({ ...state, adminEnabled: true }, "/mail/inbox"));
  for (const area of ["mail", "project", "manage", "admin", "tooling"]) {
    assert.match(out, new RegExp(area), `no ${area}`);
  }
  for (const sub of ["inbox", "compose", "sent", "archive", "store"]) {
    assert.match(out, new RegExp(sub), `no ${sub}`);
  }
  // Another area's contents are not on screen.
  assert.doesNotMatch(out, /identities/);
});

// `--no-admin` leaves two areas with nothing behind them, and an area with no
// visible tabs is an area that does nothing when pressed.
test("an area with nothing behind it is not shown", () => {
  const out = text(views.nav({ ...state, adminEnabled: false }, "/mail/inbox"));
  assert.doesNotMatch(out, /admin/);
  assert.doesNotMatch(out, /store/);
  assert.match(out, /mail/);
});

// The admin panel lists accounts by name alone, because a name is all Mailman
// keeps: it once showed a creation time and the column was always blank.
test("the account list renders from names alone", () => {
  const out = text(views.store({
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
  const out = text(views.store({
    ...state,
    admin: { machines: [{ machine: "studio", state: null }] },
  }));
  assert.match(out, /syncs without the admin view/);
});

// --- reading the whole store ----------------------------------------------

// Two machines, four messages, one of them read. Enough to tell a filter that
// works from one that happens to be looking at a single card.
const stored = (open = {}, filter = "") => ({
  ...state,
  open,
  filters: { store: filter },
  admin: {
    machines: [
      {
        machine: "studio",
        state: {
          users: [{ name: "bob" }, { name: "redjive" }],
          messages: [
            { puid: 1, mid: "m1", sent: "2026-07-25T03:30:09Z", from: "bob",
              to: ["redjive"], subject: "the parser", body: "it drops the last token" },
            { puid: 2, mid: "m2", sent: "2026-07-25T04:00:00Z", from: "redjive",
              to: ["bob"], subject: "lunch", body: "at one" },
          ],
          receipts: [{ mid: "m1", recipient: "redjive", read: true }],
        },
      },
      {
        machine: "loft",
        state: {
          users: [{ name: "carol" }],
          messages: [
            { puid: 1, mid: "m3", sent: "2026-07-25T05:00:00Z", from: "carol",
              to: ["bob"], subject: "the roster", body: "the parser is on it too" },
          ],
          receipts: [],
        },
      },
    ],
  },
});

// The whole point of the change: the store held every body and showed none of
// them, so reading mail the browser already had meant going to the machine.
test("an opened message shows its text", () => {
  const shut = text(views.store(stored()));
  assert.match(shut, /the parser/, "the subject should be on screen closed");
  assert.doesNotMatch(shut, /drops the last token/, "a shut row should not show the body");

  const open = text(views.store(stored({ "store:studio:m1": true })));
  assert.match(open, /drops the last token/);
  // And what a closed row said is still said: opening adds, it does not replace.
  assert.match(open, /redjive/);
});

// A filter that only searched subjects would be a filter that answers the easy
// question and quietly misses the message that actually mentions the thing.
test("the filter reaches the bodies, on every machine", () => {
  const out = text(views.store(stored({}, "parser")));
  assert.match(out, /the parser/, "matched on its subject");
  assert.match(out, /the roster/, "matched on its body, on the other machine");
  assert.doesNotMatch(out, /lunch/);
});

// Every word, not any: `bob parser` is a question about what bob said, and a
// filter that answered it with everything either word touches is a filter people
// stop typing two words into.
test("the filter takes all the words it is given", () => {
  assert.equal(views.matches({ from: "bob", subject: "the parser" }, "bob parser"), true);
  assert.equal(views.matches({ from: "bob", subject: "the parser" }, "carol parser"), false);
  assert.equal(views.matches({ from: "bob", subject: "the parser" }, "  "), true);
  assert.equal(views.matches({ from: "bob", subject: "The Parser" }, "parser"), true,
    "case is not something anybody remembers about a subject line");
});

// A machine with nothing left to show says so, rather than looking like a
// machine that has stopped syncing.
test("a card that filters down to nothing says which it is", () => {
  const out = text(views.store(stored({}, "zzz")));
  assert.match(out, /nothing here matches/);
  assert.doesNotMatch(out, /no messages/);
});

// The counts follow the filter, and say so — "3 messages" beside one row is a
// number that reads as a bug.
test("the counts say how much of the store survived the filter", () => {
  assert.match(text(views.store(stored({}, "parser"))), /2 of 3 messages match/);
  assert.match(text(views.store(stored())), /3 messages/);
  // Per card as well as over the fleet: 1 of 2 on studio, 1 of 1 on loft.
  assert.match(text(views.store(stored({}, "parser"))), /1 of 2 messages/);
});

// A metadata-only snapshot has no bodies to show, and that is the operator's
// decision on the agent machine rather than anything to click here.
test("a metadata-only machine says why there is no text", () => {
  const s = stored({ "store:studio:m1": true });
  s.admin.machines[0].state = { ...s.admin.machines[0].state, metadata_only: true };
  const out = text(views.store(s));
  assert.match(out, /--admin-metadata-only/);
});

// --- replying from the list ----------------------------------------------

function findAll(nodes, ok, out = []) {
  for (const n of nodes.filter(Boolean)) {
    if (ok(n)) out.push(n);
    findAll(n.childNodes || [], ok, out);
  }
  return out;
}

const replyButtons = (nodes) =>
  findAll(nodes, (n) => n.tagName === "BUTTON" && n.className === "quiet reply");

const noop = { quickReply() {} };

// Answering a short message should not cost a page of navigation. That is the
// whole of why this exists.
test("every message offers a reply without being opened", () => {
  for (const box of ["inbox", "archive", "sent"]) {
    const got = replyButtons(views.mailbox(state, { box }, noop));
    assert.equal(got.length, 1, `${box} offered ${got.length} replies`);
    assert.equal(got[0].textContent, "reply");
  }
});

// A column of identical "reply" buttons is what a screen reader hears without
// this, and it has no way to tell which row it is on.
test("a reply control names the message it answers", () => {
  const [button] = replyButtons(views.mailbox(state, { box: "inbox" }, noop));
  assert.equal(button.getAttribute("aria-label"), "reply to the parser");
});

test("pressing it hands the whole message to the action", () => {
  const asked = [];
  const [button] = replyButtons(
    views.mailbox(state, { box: "inbox" }, { quickReply: (m) => asked.push(m) }));

  button.listeners.click.forEach((fn) => fn({ target: button }));
  assert.equal(asked.length, 1);
  assert.equal(asked[0].puid, 0);
  assert.equal(asked[0].machine, "studio");
});

// A queued answer that leaves no mark on the row it was written from reads as an
// answer that did not send — and the second one would be a second message.
test("a row with a reply already queued says so instead of offering another", () => {
  const queued = {
    ...state,
    queue: [{
      state: "queued",
      action: { op: "reply", machine: "studio", args: { puid: 0, subject: "RE: the parser", body: "yes" } },
    }],
  };
  const nodes = views.mailbox(queued, { box: "inbox" }, noop);
  assert.equal(replyButtons(nodes).length, 0, "a second reply was offered");
  // "reply waiting" rather than "replied": the mark is there for the same reason
  // it always was, and now says which unfinished state it is in.
  assert.match(text(nodes), /reply waiting/);

  // Only the row it belongs to. A queued reply to one message must not silence
  // the others.
  assert.equal(replyButtons(views.mailbox(queued, { box: "archive" }, noop)).length, 1);
});

// Something else queued against the same message is not a reply, and must not
// be read as one.
test("another queued action against the message is not a reply", () => {
  const archiving = {
    ...state,
    queue: [{ state: "queued", action: { op: "archive", machine: "studio", args: { puid: 0 } } }],
  };
  assert.equal(replyButtons(views.mailbox(archiving, { box: "inbox" }, noop)).length, 1);
});

test("a subject is prefixed once however far the thread runs", () => {
  assert.equal(views.reSubject("the parser"), "RE: the parser");
  assert.equal(views.reSubject("RE: the parser"), "RE: the parser");
  assert.equal(views.reSubject(""), "RE: ");
});

// --- choosing several, and deleting ---------------------------------------

// A selection has to live in state, not in the DOM. The view is redrawn on every
// sync, and a checkbox that lived only in the page would come back empty while
// the button beside it still said "delete 12" — the count and the ticks
// disagreeing about what is about to happen, over the one action that cannot be
// undone.

// mail is a state with the same message in the inbox and, optionally, ticked.
const boxed = (selection = []) => ({
  ...state, selection,
  machines: [{ machine: "studio", last_sync: "2026-07-25T03:30:00Z" }],
});

const acts = () => {
  const calls = [];
  return {
    calls,
    quickReply() {},
    pick: (m, on) => calls.push({ pick: views.pickKey(m), on }),
    pickAll: (list, on) => calls.push({ pickAll: list.map(views.pickKey), on }),
    unpickAll: () => calls.push({ unpickAll: true }),
    readPicked: (list) => calls.push({ read: list.length }),
    archivePicked: (list) => calls.push({ archived: list.length }),
    remove: (list, archived) => calls.push({ remove: [].concat(list).length, archived }),
    markRead() {}, archive() {}, reply: async () => {}, cc: async () => true,
    draft() {}, forget() {},
  };
};

// buttons pulls the pressable things out of a rendered view.
function buttons(nodes) {
  const out = [];
  const visit = (n) => {
    if (!n || typeof n !== "object") return;
    if (n.tagName === "BUTTON") out.push(n);
    for (const c of n.childNodes || []) visit(c);
  };
  for (const n of [].concat(nodes).filter(Boolean)) visit(n);
  return out;
}

// ticks pulls the checkboxes out of a rendered mailbox.
function ticks(nodes) {
  const out = [];
  const visit = (n) => {
    if (!n || typeof n !== "object") return;
    if (n.tagName === "INPUT" && n.getAttribute && n.getAttribute("type") === "checkbox") out.push(n);
    for (const c of n.childNodes || []) visit(c);
  };
  for (const n of [].concat(nodes).filter(Boolean)) visit(n);
  return out;
}

test("every row can be ticked, and the tick is state rather than the page", () => {
  const a = acts();
  const nodes = views.mailbox(boxed(), { box: "inbox" }, a);
  const boxes = ticks(nodes);
  // One per message, plus the one that ticks the lot.
  assert.equal(boxes.length, state.inbox.length + 1);

  const row = boxes.find((b) => b.getAttribute("name") !== "pick-all");
  assert.ok(row.getAttribute("name"), "a checkbox with no name cannot be found again after a redraw");
  for (const fn of row.listeners.change) fn({ target: { checked: true } });
  assert.deepEqual(a.calls[0], { pick: "studio/0", on: true },
    "ticking a row does not record anything, so the next sync would clear it");
});

// The key carries the machine. Two mirrored machines both have a message 3, and
// keying on the number alone would tick two messages when somebody ticked one.
test("a tick names the machine as well as the number", () => {
  assert.equal(views.pickKey({ machine: "studio", puid: 3 }), "studio/3");
  assert.notEqual(views.pickKey({ machine: "laptop", puid: 3 }),
    views.pickKey({ machine: "studio", puid: 3 }));
});

test("a ticked row comes back ticked after the redraw", () => {
  const picked = views.pickKey(state.inbox[0]);
  const nodes = views.mailbox(boxed([picked]), { box: "inbox" }, acts());
  const row = ticks(nodes).find((b) => b.getAttribute("name") === `pick-${picked}`);
  assert.equal(row.getAttribute("checked"), "", "the redraw cleared the selection");
});

// The count is the thing somebody checks before pressing delete, so it is said in
// words rather than left to the ticks.
test("the bar says how many are chosen", () => {
  const out = text(views.mailbox(boxed([views.pickKey(state.inbox[0])]), { box: "inbox" }, acts()));
  assert.match(out, /1 of 1 chosen/);
  assert.match(out, /delete 1/);
});

test("with nothing chosen there is nothing to press", () => {
  const out = text(views.mailbox(boxed(), { box: "inbox" }, acts()));
  assert.doesNotMatch(out, /delete/, "a delete button with no selection is one to press by accident");
  // But the box that ticks everything is still there: a control that only
  // appears once you have found it is a control nobody finds.
  assert.equal(ticks(views.mailbox(boxed(), { box: "inbox" }, acts()))
    .filter((b) => b.getAttribute("name") === "pick-all").length, 1);
});

// Deleting from the archive is one operation; deleting live mail is an archive
// and then a prune. The view says which by where it is.
test("the archive offers no archive button, and says the mail is already filed", () => {
  const a = acts();
  const archived = { ...boxed([`studio/${state.archive[0].puid}`]) };
  const nodes = views.mailbox(archived, { box: "archive" }, a);
  const out = text(nodes);
  assert.doesNotMatch(out, /archive<|>archive/, "the archive offers to archive what is archived");

  const del = buttons(nodes).find((b) => b.textContent.startsWith("delete"));
  del.listeners.click[0]({});
  assert.equal(a.calls[0].archived, true,
    "deleting from the archive was queued as though the mail were still live");
});

test("an open message offers to delete it", () => {
  const a = acts();
  const m = state.inbox[0];
  const nodes = views.message(boxed(), { message: m, thread: [] }, a);
  const del = buttons(nodes).find((b) => b.textContent === "delete");
  assert.ok(del, "there is no way to delete the message you are reading");

  del.listeners.click[0]({});
  assert.deepEqual(a.calls[0], { remove: 1, archived: false });
});

// Whether it is already filed is asked of the mirror rather than assumed from
// which list it was opened from: a message reached by its own URL was opened
// from no list at all.
test("a message that is already archived is known to be", () => {
  const m = state.archive[0];
  assert.equal(views.inArchive(state, m), true);
  assert.equal(views.inArchive(state, state.inbox[0]), false);
});

// --- addressee checking ---------------------------------------------------

// A name this fleet has never heard of is almost always a typo, and the moment
// to say so is while the cursor is still in the field — not after a sync, as a
// refusal worded for a terminal.

const rostered = {
  ...state,
  machines: [{ machine: "studio", user: "redjive" }],
  admin: { machines: [{ machine: "studio", state: { users: [{ name: "bob" }], messages: [], receipts: [] } }] },
  fleet: [{ machine: "studio", operator: "redjive", identities: [{ name: "ember" }, { name: "atlas" }] }],
};

test("the roster is drawn from the panel, the fleet, and who is mirrored", () => {
  const got = views.known(rostered);
  for (const name of ["bob", "redjive", "ember", "atlas"]) {
    assert.ok(got.has(name), `${name} should be a known mailbox`);
  }
  assert.equal(got.has("nobody"), false);
});

test("a recipient nobody has heard of is named", () => {
  assert.deepEqual(views.unknownTo(rostered, ["ember", "embr"]), ["embr"]);
  assert.deepEqual(views.unknownTo(rostered, ["bob", "redjive"]), []);
});

// The rule that keeps this a warning rather than a wrong answer. Most fleets run
// with the admin panel off, and a browser with no roster knows nothing about who
// exists — saying so for every name would read as "this fleet has no accounts",
// which is a claim about the fleet made from a fact about what was fetched.
test("with no roster at all, nothing is unknown", () => {
  const bare = { ...state, machines: [], admin: null, fleet: [] };
  assert.equal(views.known(bare).size, 0);
  assert.deepEqual(views.unknownTo(bare, ["anyone", "at", "all"]), []);
});

test("the check is case-insensitive, as mailbox names are", () => {
  assert.deepEqual(views.unknownTo(rostered, ["EMBER", "Bob"]), []);
});

// It warns; it does not refuse. The roster is as old as the last sync, and
// Mailman decides on the machine.
test("compose warns about a stranger without blocking the send", () => {
  const drawn = views.compose({ ...rostered, drafts: { compose: { to: "embr" } } }, null);
  const out = text(drawn);
  assert.match(out, /embr/);
  assert.match(out, /not a mailbox/);
  assert.match(out, /still send/);
  // The send control is still on the form: this warns, it does not take the
  // action away.
  assert.match(out, /queue message/);
});

test("compose says nothing when every recipient is known", () => {
  const out = text(views.compose({ ...rostered, drafts: { compose: { to: "ember, bob" } } }, null));
  assert.doesNotMatch(out, /not a mailbox/);
});
