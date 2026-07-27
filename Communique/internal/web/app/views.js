// The views. Each takes the current state and returns elements; none of them
// fetches anything or mutates state, so what is on screen is always a function
// of what the store holds.

import { h, mount, clock, since, ellipsis } from "./dom.js";
import { render } from "./markdown.js";
import { survey } from "./survey.js";
import * as routes from "./routes.js";
import * as check from "./check.js";

// worst picks the entry a reader should be told about when several concern the
// same thing, in the order the queue tab uses: a refusal outranks anything still
// on its way. Undefined when there is nothing pending at all.
function worst(entries) {
  const rank = (e) => STATES.findIndex((s) => s.state === e.state);
  return entries.slice().sort((a, b) => rank(a) - rank(b))[0];
}

// pendingFor finds the queue entries that concern one message, so a reply the
// user just sent appears beside the thread it belongs to — marked queued.
export function pendingFor(queue, puid, machine) {
  return (queue || []).filter((e) =>
    e.action && e.action.machine === machine &&
    e.action.args && e.action.args.puid === puid &&
    (e.state === "queued" || e.state === "sent" || e.state === "failed"));
}

// The three boxes, and what an empty one says. Sent mail shows the recipient
// rather than the sender, since the sender is always you.
const BOXES = {
  inbox: { empty: "no mail" },
  archive: { empty: "nothing archived" },
  sent: { empty: "nothing sent", outgoing: true },
};

// pickKey identifies a ticked row.
//
// The machine as well as the number: a puid is unique within one machine's
// mailbox and two mirrored machines both have a message 3. Keying on the number
// alone would tick two messages when somebody ticked one.
export const pickKey = (m) => `${m.machine}/${m.puid}`;

// inArchive reports whether a message is already filed.
//
// Asked of the mirror rather than assumed from which list it was opened from,
// because a message reached by its own URL was opened from no list at all. It
// decides whether deleting is one operation or two — Mailman prunes the archive
// and refuses a query that touches anything live — and being wrong the safe way
// means an archive that is already archived, which changes nothing.
export function inArchive(state, m) {
  return (state.archive || []).some((other) =>
    other.machine === m.machine && other.puid === m.puid);
}

// picked reports whether a row is ticked, from state rather than from the DOM.
export function picked(state, m) {
  return (state.selection || []).includes(pickKey(m));
}

export function mailbox(state, { box }, actions) {
  const shape = BOXES[box] || BOXES.inbox;
  const messages = state[box];
  if (!messages || messages.length === 0) {
    return [h("p", { class: "muted" }, shape.empty)];
  }
  const many = (state.machines || []).length > 1;
  const chosen = messages.filter((m) => picked(state, m));
  return [
    // Above the rows, because it is about them and because on a phone the bottom
    // of a long mailbox is a scroll away from the row somebody just ticked.
    chooser(messages, chosen, box, actions),
    h("div", { class: "rows" },
      ...messages.map((m) => messageLine(m, many, shape.outgoing, state, actions))),
  ];
}

// chooser is the bar over a mailbox: tick everything, and what to do with what is
// ticked.
//
// It is always present rather than appearing with the first tick. A control that
// materialises is a control nobody knows is there, and the box that ticks the
// whole list is also the affordance that says the rows can be ticked at all.
//
// The verbs are the ones that are the same whether they act on one message or
// forty: read, archive, delete. Reply is not among them — a reply is written, and
// forty replies are forty different messages.
function chooser(messages, chosen, box, actions) {
  if (!actions) return null;

  const all = chosen.length === messages.length && messages.length > 0;
  const some = chosen.length > 0;
  const archived = box === "archive";

  return h("div", { class: "chooser" },
    h("label", { class: "pick-all" },
      h("input", {
        type: "checkbox", name: "pick-all", checked: all ? "" : null,
        "aria-label": all ? "untick every message" : "tick every message",
        onchange: (e) => actions.pickAll(messages, e.target.checked),
      }),
      // The count carries the meaning, so a screen with no colour still says how
      // much is about to happen — and it names the number rather than "all",
      // which is the thing somebody checks before pressing delete.
      h("span", { class: "muted" }, some
        ? `${chosen.length} of ${messages.length} chosen`
        : `${messages.length} ${messages.length === 1 ? "message" : "messages"}`)),
    some
      ? h("div", { class: "controls" },
        h("button", { class: "quiet", onclick: () => actions.readPicked(chosen) }, "mark read"),
        archived
          ? null
          : h("button", { class: "quiet", onclick: () => actions.archivePicked(chosen) }, "archive"),
        h("button", { class: "danger", onclick: () => actions.remove(chosen, archived) },
          `delete ${chosen.length}`),
        h("button", { class: "quiet", onclick: () => actions.unpickAll() }, "clear"))
      : null,
  );
}

// A row and the one thing worth doing to it without opening it.
//
// Reply is the only action that earns a place here. Reading, archiving and the
// rest are decisions somebody makes *after* reading the message, and this is the
// list of messages they have not read yet — but a short answer to a short mail
// is the common case, and making it cost a page of navigation is what makes a
// mailbox tiring on a phone.
//
// The button sits beside the row rather than inside it, because the row is a
// link, and a button inside a link is a target nobody can hit reliably.
function messageLine(m, showMachine, outgoing, state, actions) {
  // The most alarming one, where there is more than one: a refusal beside a row
  // that also has a fresh reply waiting is the half worth reading first, and it is
  // the half the old marker hid. `pendingFor` carries refusals, and every one of
  // them used to render as "replied" — which is the opposite of what happened.
  const reply = worst(pendingFor(state.queue, m.puid, m.machine)
    .filter((e) => e.action.op === "reply"));

  return h("div", { class: "line" },
    // Outside the row for the same reason the reply button is: the row is a link,
    // and a checkbox inside a link is a target that navigates when it is missed.
    actions
      ? h("input", {
        type: "checkbox", class: "pick", name: `pick-${pickKey(m)}`,
        checked: picked(state, m) ? "" : null,
        // Named, because a column of identical "checkbox" is not a list anybody
        // can navigate by ear.
        "aria-label": `choose ${m.subject || "this message"}`,
        onchange: (e) => actions.pick(m, e.target.checked),
      })
      : null,
    messageRow(m, showMachine, outgoing),
    reply
      // Said where the reply was written, rather than only in the status bar: a
      // queued answer that leaves no mark reads as an answer that did not send.
      // Which of the unfinished states it is in belongs here too — "waiting" is
      // this browser's turn and "with the agent" is not, and somebody deciding
      // whether to write it again needs to know whose turn it is.
      ? h("span", { class: "muted replied" }, `reply ${words(reply.state).title}`)
      : h("button", {
        class: "quiet reply",
        // The subject is in the label because a screen reader hears a column of
        // identical "reply" buttons otherwise.
        "aria-label": `reply to ${m.subject}`,
        onclick: () => actions.quickReply(m),
      }, "reply"),
  );
}

function messageRow(m, showMachine, outgoing) {
  const convo = m.convo && m.convo.uid
    ? `${ellipsis(m.convo.title || m.convo.uid, 12)} · ${m.convo.index}`
    : "—";
  return h("a", {
    class: `row${m.read ? " read" : ""}`,
    href: `#/message/${encodeURIComponent(m.puid)}?machine=${encodeURIComponent(m.machine)}`,
  },
    h("span", { class: "flag" }, m.read ? " " : "*"),
    h("span", { class: "puid" }, m.puid),
    h("span", { class: "when" }, clock(m.sent)),
    h("span", { class: "who" },
      ellipsis(outgoing ? (m.to || []).join(", ") || "—" : m.from, 12)),
    h("span", { class: "subject" }, m.subject),
    h("span", { class: "convo" }, showMachine ? `${m.machine} · ${convo}` : convo),
  );
}

export function message(state, detail, actions) {
  if (!detail) return [h("p", { class: "muted" }, "loading…")];

  const m = detail.message;
  const thread = detail.thread && detail.thread.length > 1 ? detail.thread : [];
  const queued = pendingFor(state.queue, m.puid, m.machine);

  const cards = [card(m)];
  for (const other of thread) {
    if (other.puid !== m.puid) cards.push(card(other));
  }
  for (const entry of queued) cards.push(queuedCard(entry));

  return [
    h("p", {},
      h("a", { href: "#/inbox", class: "muted" }, "← inbox"),
    ),
    ...cards,
    composer(state, m, actions),
    ccForm(state, m, actions),
    h("p", {},
      h("button", { class: "quiet", onclick: () => actions.markRead(m) }, "mark read"),
      " ",
      h("button", { class: "quiet", onclick: () => actions.archive(m) }, "archive"),
      " ",
      // Whether this message is already filed decides whether deleting it is one
      // operation or two, and the archive is the list that knows.
      h("button", {
        class: "danger",
        onclick: () => actions.remove(m, inArchive(state, m)),
      }, "delete"),
    ),
  ];
}

// ccForm adds someone to the conversation.
//
// Only for mail that is in one: a conversation is what `cc` addresses, so a
// message with no thread has nothing to add anyone to, and offering the control
// anyway would be offering a button that cannot work.
function ccForm(state, m, actions) {
  if (!m.convo || !m.convo.uid) return null;

  const key = draftKey("cc", m.convo.uid);
  const draft = drafted(state, key);

  // Singular, because this form takes one name and says so if given two. Same
  // reasoning as the compose field: a format rather than somebody's name.
  const who = h("input", {
    name: "cc", placeholder: "name", autocomplete: "off",
    value: draft.cc ?? "",
    oninput: (e) => actions.draft(key, "cc", e.target.value),
  });
  const button = h("button", { class: "quiet", type: "submit" }, "add to conversation");
  const problem = h("p", { class: "error" });

  return h("form", {
    class: "compose",
    onsubmit: (e) => {
      e.preventDefault();
      // One name, checked the same way the recipients of a new message are.
      const got = recipients(who.value);
      const trouble = got.error || (got.names.length > 1 ? "one name at a time" : "");
      problem.textContent = trouble;
      if (trouble) return;

      button.disabled = true;
      actions.cc(m, got.names[0])
        .then((ok) => { if (ok) { who.value = ""; actions.forget(key); problem.textContent = ""; } })
        .finally(() => { button.disabled = false; });
    },
  },
    h("label", { for: "cc" }, "cc"),
    who,
    problem,
    button,
  );
}

function card(m) {
  return h("article", { class: "card" },
    h("h2", {}, `${m.puid} · ${m.subject}`),
    h("div", { class: "meta" },
      `${m.from} → ${(m.to || []).join(", ")}`,
      m.cc && m.cc.length ? ` · cc ${m.cc.join(", ")}` : "",
      ` · ${clock(m.sent)}`),
    h("div", { class: "body" }, render(m.body || "")),
  );
}

// A queued action is drawn as what it is: something that has not happened yet.
// The word carries the meaning, not only the colour.
//
// Which word: waiting to be collected and waiting to be reported on are different
// facts, and this used to call both of them "queued". They are different in the way
// that matters to somebody deciding whether to worry — the first is cq's turn and a
// sync fixes it, the second is the agent machine's turn and nothing here will. The
// queue tab has always told them apart; every other screen said "queued" until a
// result arrived, so an action stuck with an agent was indistinguishable from one
// that had not left.
//
// The words come from the queue tab's own table, so the two screens cannot drift
// into calling the same state different things.
function queuedCard(entry) {
  const said = words(entry.state);
  const wrong = entry.state === "failed" || entry.state === "in_doubt";
  return h("article", { class: `card ${wrong ? "failed" : "pending"}` },
    h("h2", {}, verb(entry.action.op)),
    h("div", { class: "meta" },
      h("span", { class: "badge" }, said.title),
      // A refusal carries its own reason, which is worth more than the general
      // note about what a refusal is.
      entry.state === "failed"
        ? ` ${entry.error || "the agent refused it"}`
        : ` ${said.note}`),
    entry.action.args && entry.action.args.body
      ? h("div", { class: "body" }, render(entry.action.args.body))
      : null,
  );
}

function verb(op) {
  switch (op) {
    case "reply": return "your reply";
    case "send": return "your message";
    case "read": return "mark read";
    case "archive": return "archive";
    case "cc": return "add to conversation";
    case "write": return "your edit";
    case "create": return "a new file";
    case "delete": return "a deletion";
    case "mkdir": return "a new folder";
    case "rmdir": return "a folder to remove";
    default: return op;
  }
}

// reSubject is what a reply is called, and it is prefixed once however many
// times the thread goes back and forth: `RE: RE: RE:` is noise that grows.
export function reSubject(subject) {
  const s = String(subject ?? "");
  return s.startsWith("RE: ") ? s : `RE: ${s}`;
}

// draftKey names the form a draft belongs to.
//
// One key per thing being written: a reply belongs to its message, a cc to its
// conversation, and there is only ever one new message. Keying by message means
// two half-written replies in two threads do not overwrite each other.
export function draftKey(kind, id = "") {
  return id ? `${kind}:${id}` : kind;
}

// drafted reads one form's draft. Absent is empty rather than missing, so a
// view can ask for a draft that has never been typed into.
export function drafted(state, key) {
  return (state && state.drafts && state.drafts[key]) || {};
}

function composer(state, m, actions) {
  const key = draftKey("reply", m.mid || m.puid);
  const draft = drafted(state, key);

  const subject = h("input", {
    name: "subject",
    value: draft.subject ?? reSubject(m.subject),
    oninput: (e) => actions.draft(key, "subject", e.target.value),
  });
  // Set as a property rather than an attribute, for the reason editor.js gives:
  // a textarea's attribute is its *default*, and the text somebody is part-way
  // through is not a default.
  const body = h("textarea", {
    name: "body", placeholder: "…",
    oninput: (e) => actions.draft(key, "body", e.target.value),
  });
  body.value = draft.body ?? "";
  const button = h("button", { type: "submit" }, "queue reply");

  return h("form", {
    class: "compose",
    onsubmit: (e) => {
      e.preventDefault();
      button.disabled = true;
      actions.reply(m, subject.value, body.value)
        .then(() => { actions.forget(key); })
        .finally(() => { button.disabled = false; });
    },
  },
    h("label", { for: "subject" }, "subject"),
    subject,
    h("label", { for: "body" }, "reply"),
    body,
    button,
  );
}

// --- compose ------------------------------------------------------------

// The name shape every Orc tool agrees on. Checking it here is not security —
// the server and Mailman both check again — it is timing. A queued action is
// applied minutes later on another machine, so a typo caught now is a sentence
// on screen, and a typo caught then is a failure the writer has to come back
// for and cannot correct without writing the message again.

// recipients splits what was typed and says what is wrong with it, if anything.
//
// Commas or spaces, either way: the field says "comma separated" and someone
// will use spaces regardless, and guessing right costs one character of regexp.
export function recipients(text) {
  const names = String(text).split(/[,\s]+/).filter(Boolean).map((n) => n.toLowerCase());
  if (names.length === 0) return { names: [], error: "no recipients" };

  // Each name put to the same rule Mailman will apply, rather than to a regexp
  // that happens to agree with most of it. The old one accepted `system` and
  // `all` — reserved names that queue happily and come back as a refusal after
  // the next sync, worded for a terminal.
  //
  // Every bad name, each with its own reason. Both halves matter: a list without
  // reasons cannot say that one name has a space in it and another is reserved,
  // and one reason without the list means fixing them one send at a time.
  const problems = names
    .map((n) => check.mailbox(n, `“${n}”`))
    .filter(Boolean);
  if (problems.length > 0) return { names: [], error: problems.join("; ") };
  // Duplicates are the writer's slip, not an error worth stopping for.
  return { names: [...new Set(names)], error: "" };
}

export function compose(state, actions) {
  const machines = state.machines || [];
  if (machines.length === 0) {
    return [h("p", { class: "muted" }, "nothing has synced yet, so there is nowhere to send from")];
  }

  const key = draftKey("compose");
  const draft = drafted(state, key);

  // The placeholder is a format, not an example. It used to be two invented
  // names, which read as real accounts on a fleet where the recipients *are*
  // people and agents with short names — and a hint somebody might try to send to
  // is worse than no hint. What it has to convey is the only thing the old one
  // actually did: that this field takes more than one, separated by commas.
  const to = h("input", {
    name: "to", placeholder: "name, name", autocomplete: "off",
    value: draft.to ?? "",
    oninput: (e) => actions.draft(key, "to", e.target.value),
  });
  const subject = h("input", {
    name: "subject", autocomplete: "off",
    value: draft.subject ?? "",
    oninput: (e) => actions.draft(key, "subject", e.target.value),
  });
  const body = h("textarea", {
    name: "body", placeholder: "…",
    oninput: (e) => actions.draft(key, "body", e.target.value),
  });
  body.value = draft.body ?? "";
  const button = h("button", { type: "submit" }, "queue message");
  const problem = h("p", { class: "error" });

  // Only asked when there is a real choice. One machine is the ordinary case,
  // and a select with one option is a question with one answer.
  //
  // Drafted like the text fields beside it, and for the same reason: the view is
  // remounted whenever a sync lands, and a select rebuilt from its options comes
  // back on the *first* one. Losing a choice is quieter than losing a sentence —
  // the form still submits, just to a machine nobody picked — which makes it the
  // worse of the two to leave unfixed.
  const picker = machines.length > 1
    ? h("select", {
        name: "machine",
        onchange: (e) => actions.draft(key, "machine", e.target.value),
      }, ...machines.map((m) => h("option", { value: m.machine }, m.machine)))
    : null;
  if (picker) picker.value = draft.machine ?? machines[0].machine;
  const machineOf = () => (picker ? picker.value : machines[0].machine);

  const form = h("form", {
    class: "compose",
    onsubmit: (e) => {
      e.preventDefault();
      const who = recipients(to.value);
      const trouble = who.error || (subject.value.trim() ? "" : "no subject")
        || (body.value.trim() ? "" : "nothing to send");
      problem.textContent = trouble;
      if (trouble) return;

      button.disabled = true;
      actions.send(machineOf(), who.names, subject.value.trim(), body.value)
        .then((ok) => {
          if (!ok) return;
          // Cleared only on success. A message that failed to queue is still
          // the writer's message, and losing it to a network error would be
          // the worst thing this form could do.
          to.value = subject.value = body.value = "";
          actions.forget(key);
          problem.textContent = "";
        })
        .finally(() => { button.disabled = false; });
    },
  },
    h("label", { for: "to" }, "to"),
    to,
    ...(picker ? [h("label", { for: "machine" }, "from"), picker] : []),
    h("label", { for: "subject" }, "subject"),
    subject,
    h("label", { for: "body" }, "message"),
    body,
    problem,
    button,
  );

  const waiting = (state.queue || []).filter(
    (e) => e.action && e.action.op === "send" && (e.state === "queued" || e.state === "sent"));

  return [
    form,
    // Nothing leaves the browser until the agent machine next syncs, so the
    // form says so rather than letting "queued" look like "sent".
    h("p", { class: "muted" }, "queued here; it leaves on the next sync"),
    // "not gone yet" rather than "waiting", now that waiting is one of the two
    // states a card underneath can be in and the other is not it.
    ...(waiting.length > 0
      ? [h("h2", {}, "not gone yet"), ...waiting.map((e) => queuedCard(e))]
      : []),
  ];
}

// --- the queue -----------------------------------------------------------

// STATE_WORDS is what each state is called, and it is the only place any screen
// gets that from.
//
// It mirrors store.State, whose distinctions are the ones worth showing: `queued`
// and `sent` are both "not finished", but one is waiting on a sync from here and
// the other is with the agent, and `failed` and `in_doubt` demand opposite
// responses — one can simply be retried and one cannot.
//
// `sent` is deliberately not called "sent". In a mailbox that word means delivered
// to a person, and this means collected by a machine.
const STATE_WORDS = {
  failed: { title: "refused", note: "nothing happened, so it can be tried again" },
  in_doubt: { title: "in doubt", note: "started, and its outcome was never reported" },
  queued: { title: "waiting", note: "leaves on the next sync" },
  sent: { title: "with the agent", note: "collected, and not yet reported on" },
  done: { title: "done", note: "" },
};

// words is the fallback for a state this build has never heard of — a newer server
// with a state added since. Naming it is better than drawing a card with a blank
// badge, and far better than throwing inside a render.
function words(state) {
  return STATE_WORDS[state] || { title: String(state || "unknown"), note: "" };
}

// STATES orders the queue by what the reader can do about each row: the things
// needing a decision first, then what is on its way, then history.
const STATES = ["failed", "in_doubt", "queued", "sent", "done"].map(
  (state) => ({ state, ...STATE_WORDS[state] }));

// Sending twice is a second message to a real person. cq cannot tell whether an
// interrupted send arrived, so it does not offer to repeat one — the same rule
// the server enforces, said here so the button is absent rather than failing.
const REPEATABLE = ["read", "archive", "cc"];

// settled mirrors store.State.Settled: an action the agent has reported on. The
// two lists have to agree — a button the server refuses is worse than no button —
// and this is the one place the browser needs to know.
function settled(entry) {
  return entry.state === "done" || entry.state === "failed" || entry.state === "in_doubt";
}

function mayRetry(entry) {
  if (entry.state === "failed") return true;
  return entry.state === "in_doubt" && REPEATABLE.includes(entry.action.op);
}

export function queue(state, actions) {
  const entries = state.queue || [];
  const many = (state.machines || []).length > 1;
  if (entries.length === 0) {
    return [h("p", { class: "muted" }, "nothing queued")];
  }

  // Every group this build knows, and then one for anything it does not.
  //
  // Without the last one a row in a state added since this browser was built is
  // filtered out by every group and drawn by none — the tab says "nothing queued"
  // over a queue with something in it, which is the one thing this screen exists
  // not to do. It is listed last because an unrecognised state is not a call to
  // action; it is a note that this page is older than the server.
  const unknown = entries.filter((e) => !STATE_WORDS[e.state]);
  const groups = unknown.length === 0 ? STATES
    : [...STATES, { state: null, title: "not recognised",
      note: "queued in a state this page does not know — it was built before the server was" }];

  const out = [];
  for (const group of groups) {
    const rows = group.state === null ? unknown : entries.filter((e) => e.state === group.state);
    if (rows.length === 0) continue;

    out.push(h("div", { class: "row-actions" },
      h("h2", {}, `${group.title} · ${rows.length}`),
      // Only on the done pile. Failed and in-doubt rows carry the only record of
      // why they failed, so sweeping those is a per-row decision rather than one
      // button that takes them all.
      group.state === "done" && actions
        ? h("button", {
            class: "quiet",
            onclick: (e) => hold(e.target, () => actions.clearDone(rows.length)),
          }, "clear them")
        : null));
    if (group.note) out.push(h("p", { class: "muted" }, group.note));
    for (const entry of rows) out.push(queueRow(entry, actions, many));
  }
  return out;
}

// showMachine follows the mailbox's rule: name the machine only when there is
// more than one, or every row carries a word that never varies.
function queueRow(entry, actions, showMachine) {
  const unresolved = entry.state === "failed" || entry.state === "in_doubt";
  const controls = [];

  if (mayRetry(entry)) {
    controls.push(h("button", {
      class: "quiet",
      onclick: (e) => hold(e.target, () => actions.retry(entry)),
    }, "try again"));
  } else if (entry.state === "in_doubt") {
    // Not a button: the operator has to settle this outside cq, and saying so
    // is more use than a control that refuses.
    controls.push(h("span", { class: "muted" }, "check your sent mail, then write it again"));
  }
  // Three things somebody means by "get rid of this", and the word says which.
  //
  //   - *waiting*: it has never left this machine. Cancelling means it never
  //     goes, and nothing has happened that anybody has to reason about.
  //   - *failed* or *in doubt*: forgetting the record, not the effect.
  //   - *done*: removing a note about something that already happened.
  //
  // A row that is *with the agent* has no button, and that is the server's rule
  // rather than a choice made here: it may be applying this second, and the
  // result would have nowhere to land.
  if ((settled(entry) || entry.state === "queued") && actions) {
    controls.push(h("button", {
      // Quiet, like the others. Cancelling is the direction that makes *less*
      // happen, and colouring it as a hazard would be telling somebody to
      // hesitate over the one press here that cannot cost them anything.
      class: "quiet",
      onclick: (e) => hold(e.target, () => actions.drop(entry)),
    }, label(entry)));
  }

  return h("article", { class: `card ${unresolved ? "failed" : "pending"}` },
    h("h2", {}, verb(entry.action.op)),
    h("div", { class: "meta" },
      h("span", { class: "badge" }, entry.state.replace("_", " ")),
      showMachine ? h("span", { class: "machine" }, ` ${entry.action.machine}`) : null,
      ` ${describe(entry.action)}`),
    entry.error ? h("div", { class: "body" }, h("pre", {}, entry.error)) : null,
    controls.length > 0 ? h("div", { class: "meta" }, ...spaced(controls)) : null,
  );
}

// label is what the button offers to do, which depends on whether the action has
// happened yet. "remove" on something that has not gone is a lie about what the
// press does, and it is the press somebody makes in a hurry.
function label(entry) {
  if (entry.state === "queued") return "cancel";
  return entry.state === "failed" || entry.state === "in_doubt" ? "forget it" : "remove";
}

// describe says what an action was for, in the terms the reader used.
// describe says what one queued action is *about*, in the words of whichever tool
// will run it.
//
// The default used to be `#${args.puid}`, which is right for the mail verbs and
// nothing else — so every task and fleet row read `#undefined`, naming nothing, on
// the one screen where somebody is deciding which rows to keep.
function describe(action) {
  const args = action.args || {};
  switch (action.op) {
    case "send": return `to ${(args.to || []).join(", ")} — ${args.subject || ""}`;
    case "reply": return `#${args.puid} — ${args.subject || ""}`;
    case "cc": return `${args.user} into ${ellipsis(args.convo_uid || "", 16)}`;
    case "read": case "archive": return `#${args.puid}`;
    case "write": case "create": case "delete": case "mkdir": case "rmdir":
      return args.path || "";
    case "system.upgrade": return "pull, rebuild, and restart this machine";
    case "system.library": return `mirror ${action.args?.workspace || "somewhere else"}`;
    case "orc.tend": return "the whole work list";
    default: return describeSubject(action.op, args);
  }
}

// describeSubject names the thing a task or fleet action is about, and the operand
// that distinguishes one from another of the same verb.
function describeSubject(op, args) {
  const subject = args.task || args.identity || args.role || args.permission || "";
  const detail = [];
  if (args.sub) detail.push(args.sub);
  if (args.user) detail.push(args.user);
  if (args.boss) detail.push(`under ${args.boss}`);
  if (args.status) detail.push(`status ${args.status}`);
  if (args.priority || args.difficulty) detail.push(`P${args.priority} D${args.difficulty}`);
  if (args.authority) detail.push(`authority ${args.authority}`);
  if (typeof args.load === "number") detail.push(`load ${args.load}`);
  if (args.model || args.effort) detail.push(`${args.model || "?"}/${args.effort || "?"}`);
  if (args.until) detail.push(`until ${args.until}`);
  if ((args.paths || []).length) detail.push(args.paths.join(" "));
  if ((args.patterns || []).length) detail.push(args.patterns.join(" "));
  if (args.path) detail.push(args.path);

  if (!subject) return detail.join(" · ");
  return detail.length ? `${subject} — ${detail.join(" · ")}` : subject;
}

// hold disables a control while its action is in flight, so an impatient second
// click cannot queue a second retry of the same thing.
function hold(button, run) {
  button.disabled = true;
  Promise.resolve(run()).finally(() => { button.disabled = false; });
}

function spaced(nodes) {
  const out = [];
  nodes.forEach((n, i) => {
    if (i > 0) out.push(" ");
    out.push(n);
  });
  return out;
}

// --- chrome -------------------------------------------------------------

// nav is two rows: what kind of thing, then which one.
//
// Both are drawn from routes.js, so a tab that exists is a tab that appears and
// a tab that appears is one the router can reach. The alternative — a list of
// links here and a chain of route matches there — is two places to remember, and
// the one people forget is the second.
//
// Only the open area's sub-tabs are shown. Showing all seventeen at once is the
// flat row this replaced.
export function nav(state, route) {
  const here = routes.resolve(route) || routes.resolve(routes.MOVED[route.split("?")[0]] || "");
  const open = here ? here.major : null;

  const majors = [];
  for (const area of routes.AREAS) {
    const subs = routes.visible(area, state);
    // An area with nothing behind it is not shown. `--no-admin` empties two.
    if (subs.length === 0) continue;
    majors.push(tab(routes.home(area.major, state), area.major,
      routes.areaCount(area, state), area.major === open));
  }

  const rows = [h("div", { class: "majors" }, ...majors,
    h("span", { class: "spacer" }),
    h("a", { href: "#" + routes.HOME, onclick: (e) => { e.preventDefault(); logout(); } }, "logout"))];

  const area = routes.AREAS.find((a) => a.major === open);
  if (area) {
    rows.push(h("div", { class: "subs" },
      ...routes.visible(area, state).map((s) =>
        tab(`/${area.major}/${s.sub}`, s.sub, routes.count(s, state), here && here.sub === s.sub))));
  }
  return rows;
}

function tab(href, label, n, current) {
  return h("a", {
    href: "#" + href,
    "aria-current": current ? "page" : null,
  }, label, n > 0 ? h("span", { class: "count" }, ` ${n}`) : null);
}

async function logout() {
  const { api } = await import("./api.js");
  try { await api.logout(); } finally { window.location.assign("/login"); }
}

// status is the staleness clock: on screen at all times, because a mirror that
// looks live when it is an hour old is worse than no mirror.
export function status(state, now = Date.now()) {
  const bits = [];
  const machines = state.machines || [];

  if (machines.length === 0) {
    bits.push(h("span", { class: "muted" }, "nothing has synced yet"));
  }
  for (const m of machines) {
    const age = now - new Date(m.last_sync).getTime();
    const cls = age > 10 * 60 * 1000 ? "very-stale" : age > 2 * 60 * 1000 ? "stale" : "";
    bits.push(h("span", { class: cls },
      machines.length > 1 ? `${m.machine}: ` : "",
      `synced ${since(m.last_sync, now)}`));
  }

  const waiting = (state.queue || []).filter((e) => e.state === "queued" || e.state === "sent").length;
  if (waiting > 0) bits.push(h("span", { class: "pending" }, `${waiting} waiting to send`));

  const failed = (state.queue || []).filter((e) => e.state === "failed").length;
  if (failed > 0) bits.push(h("span", { class: "failed" }, `${failed} failed`));

  return bits;
}

export function error(err) {
  return [h("p", { class: "error" }, err && err.message ? err.message : String(err))];
}

export { mount };

// --- tasks ---------------------------------------------------------------

// Macmuffin's scale, drawn as Macmuffin draws it: a glyph *and* a word, so the
// state survives a monochrome screen and anyone who cannot tell green from red.
const STATUS = {
  1: { glyph: "✗", word: "broken", cls: "failed" },
  2: { glyph: "~", word: "slow", cls: "pending" },
  3: { glyph: "●", word: "nominal", cls: "ok" },
  4: { glyph: "✓", word: "done", cls: "ok" },
};

// Named for the scale it reads, not just "status": the status bar at the foot
// of the page already owns that word, and two declarations of it in one module
// is a syntax error that takes the whole interface down.
function taskStatus(n) {
  return STATUS[n] || { glyph: "?", word: "unknown", cls: "muted" };
}

// meter reads at a glance and still carries its own numbers, for when colour
// and width are both gone.
export function meter(done, total, width = 7) {
  if (total <= 0) return h("span", { class: "muted" }, "—");
  // Clamped once, and both halves drawn from the clamped value: a count
  // outside 0..total would otherwise widen the bar and break the column grid
  // every other row depends on.
  const filled = Math.max(0, Math.min(Math.round((done / total) * width), width));
  return h("span", { class: "meter" },
    h("span", { class: "filled" }, "▓".repeat(filled)),
    h("span", { class: "empty" }, "░".repeat(width - filled)),
    ` ${done}/${total}`);
}

export function tasks(state, actions) {
  const list = state.tasks || [];
  const machines = state.machines || [];
  const many = machines.length > 1;

  // Which machine a new task belongs to only has to be asked when there is more
  // than one, and the picker is the same shape compose uses — drafted for the same
  // reason, and named so the cursor comes back to it after a redraw.
  const key = draftKey("tasks");
  const draft = drafted(state, key);
  let picker = null;
  if (many) {
    picker = h("select", {
      class: "machine", name: "machine",
      onchange: (e) => actions && actions.draft(key, "machine", e.target.value),
    }, ...machines.map((m) => h("option", { value: m.machine }, m.machine)));
    picker.value = draft.machine ?? machines[0].machine;
  }
  const machineOf = () => (picker ? picker.value : (machines[0] && machines[0].machine));
  const head = h("div", { class: "row-actions" },
    picker,
    actions && machines.length
      ? h("button", { onclick: () => actions.createTask(machineOf()) }, "new task")
      : null);

  if (list.length === 0) {
    return [head, h("p", { class: "muted" }, "no tasks")];
  }
  return [head, h("div", { class: "board" },
    h("div", { class: "task head" },
      h("span", {}, ""), h("span", {}, "task"), h("span", {}, "owner"),
      h("span", { class: "num" }, "P"), h("span", { class: "num" }, "D"),
      h("span", {}, "progress"), h("span", {}, "status"), h("span", {}, "")),
    ...list.map((t) => taskRow(t, many)))];
}

function taskRow(t, showMachine) {
  const s = taskStatus(t.status);
  // A described task is marked on the board, because "which of these can I pick up
  // without asking somebody what it means" is a question about the pool.
  return h("a", {
    class: `task row${t.draft ? " draft" : ""}`,
    href: `#/tasks/${encodeURIComponent(t.name)}`,
  },
    h("span", { class: s.cls }, s.glyph),
    h("span", { class: "name" }, t.name,
      t.draft ? h("span", { class: "badge" }, "draft") : null,
      t.described ? h("span", { class: "badge described", title: "has a description" }, "spec") : null),
    h("span", { class: "who" }, t.owner || "—"),
    h("span", { class: "num" }, t.priority),
    h("span", { class: "num" }, t.difficulty),
    meter(t.done, t.total),
    h("span", { class: s.cls }, s.word, showMachine ? h("span", { class: "muted" }, ` · ${t.machine}`) : null),
    h("span", {}),
  );
}

export function task(state, name, actions) {
  const t = (state.tasks || []).find((x) => x.name === name);
  if (!t) return [h("p", { class: "muted" }, `no task called ${name}`)];
  const s = taskStatus(t.status);

  return [
    h("p", {}, h("a", { href: "#/tasks", class: "muted" }, "← tasks")),
    h("article", { class: "card" },
      h("h2", {}, t.name, " ",
        h("span", { class: "muted" }, `P${t.priority} D${t.difficulty} `),
        h("span", { class: s.cls }, `${s.glyph} ${s.word}`), " ", meter(t.done, t.total)),
      h("div", { class: "meta" },
        `owner ${t.owner || "unclaimed"}`,
        t.collaborators && t.collaborators.length ? ` · with ${t.collaborators.join(", ")}` : "",
        t.worktree ? ` · worktree ${t.worktree}` : "",
        ` · ${t.machine}`),
      h("div", { class: "body" },
        ...description(t, actions),
        t.scope && t.scope.length
          ? h("div", { class: "scope" },
              h("div", { class: "muted" }, "scope"),
              ...t.scope.map((p) => h("div", {}, p)))
          : h("p", { class: "muted" }, "no scope yet — this task cannot be edited or completed")),
      ...subtaskList(t, actions),
    ),
    ...taskControls(t, actions),
    ...taskPending(state, t),
  ];
}

// description is what the work actually is, rendered.
//
// It comes first in the card, above the scope and the steps, because it is the only
// thing here that says what to *do*: everything else is a fact with a shape. A task
// with a specification and a reader who has to scroll past its metadata to find it
// is a task whose specification does not get read.
//
// Markdown, through the same renderer the library uses — which builds nodes rather
// than HTML, so prose somebody else wrote cannot become elements nobody intended.
//
// The three states are kept apart on purpose:
//
//   - **described, with text** — render it.
//   - **described, no text** — the mirror could not read it. Saying "no description"
//     there would invite writing a first one *over* something, so it says what it
//     actually knows.
//   - **not described** — say so, and offer to write one.
function description(t, actions) {
  const controls = actions
    ? h("div", { class: "controls" },
        h("button", { class: "quiet", onclick: () => actions.describeTask(t) },
          t.described ? "edit…" : "write one…"),
        t.described
          ? h("button", { class: "quiet", onclick: () => actions.undescribeTask(t) }, "clear")
          : null)
    : null;

  if (!t.described) {
    return [h("div", { class: "description empty" },
      h("div", { class: "muted" }, "no description — nothing here says what the work is"),
      controls)];
  }
  if (!t.description) {
    return [h("div", { class: "description" },
      h("p", { class: "warn" },
        "this task has a description, but the last sync could not read it"),
      controls)];
  }
  return [h("div", { class: "description" },
    h("div", { class: "prose" }, render(t.description)),
    controls)];
}

// subtaskList draws the steps, each with the two things that can be done to one.
//
// Names and not just a count, which is why the agent asks Macmuffin twice per
// task: `muff pool` gives 2/5 and `muff info` gives the five. Completing a step
// from a phone needs the name.
function subtaskList(t, actions) {
  if (!t.subtasks || t.subtasks.length === 0) return [];
  return [h("div", { class: "subtasks" },
    h("div", { class: "muted" }, "steps"),
    ...t.subtasks.map((sub) => h("div", { class: "subtask" },
      h("span", { class: sub.done ? "ok" : "muted" }, sub.done ? "✓" : "·"),
      h("span", { class: sub.done ? "done" : "" }, sub.name),
      actions && !sub.done
        ? h("button", { class: "quiet", onclick: () => actions.completeSubtask(t, sub.name) }, "done")
        : null,
      actions
        ? h("button", { class: "quiet", onclick: () => actions.deleteSubtask(t, sub.name) }, "delete")
        : null,
    )))];
}

// taskControls is every Macmuffin verb that changes something, grouped the way
// the work is: what state it is in, who is on it, what it may touch, and the two
// that end it.
//
// A control is shown when the verb applies and left out when it does not — a
// draft can be pushed and a pooled task cannot, and a button that only ever
// produces a refusal is worse than no button.
function taskControls(t, actions) {
  if (!actions) return [];

  const button = (label, fn, cls) => h("button", { class: cls || "", onclick: fn }, label);

  const state = h("div", { class: "controls" },
    h("span", { class: "muted" }, "status"),
    ...[1, 2, 3, 4].map((n) => {
      const st = taskStatus(n);
      return h("button", {
        class: n === t.status ? "on" : "quiet",
        onclick: () => actions.setStatus(t, n),
      }, `${st.glyph} ${st.word}`);
    }));

  const people = h("div", { class: "controls" },
    h("span", { class: "muted" }, "who"),
    t.owner ? null : button("claim", () => actions.claimTask(t)),
    button("assign…", () => actions.assignTask(t)),
    button("invite…", () => actions.inviteToTask(t)),
    ...(t.collaborators || []).map((who) =>
      button(`kick ${who}`, () => actions.kickFromTask(t, who), "quiet")),
    button("leave", () => actions.leaveTask(t), "quiet"));

  const work = h("div", { class: "controls" },
    h("span", { class: "muted" }, "work"),
    t.draft ? button("push to the pool", () => actions.pushTask(t)) : null,
    button("scope…", () => actions.scopeTask(t)),
    button("worktree…", () => actions.worktreeTask(t)),
    button("add a step…", () => actions.addSubtask(t)));

  const ending = h("div", { class: "controls" },
    h("span", { class: "muted" }, "end it"),
    t.status === 4 ? null : button("complete", () => actions.completeTask(t)),
    button("delete", () => actions.deleteTask(t), "danger"));

  return [h("section", { class: "task-controls" }, state, people, work, ending,
    h("p", { class: "muted" },
      "every one of these queues here and leaves on the next sync"))];
}

// taskPending shows what is already queued for this task, so an action taken a
// minute ago is visible rather than apparently lost — the mirror is minutes old
// and the queue is the only honest account of what is on its way.
function taskPending(state, t) {
  const waiting = (state.queue || []).filter((e) =>
    e.action && e.action.machine === t.machine &&
    e.action.args && e.action.args.task === t.name &&
    (e.state === "queued" || e.state === "sent" || e.state === "failed"));
  if (waiting.length === 0) return [];
  return [h("div", { class: "pending" },
    h("div", { class: "muted" }, "waiting for the next sync"),
    ...waiting.map((e) => h("div", { class: e.state === "failed" ? "failed" : "pending" },
      `${e.action.op}${e.action.args.sub ? ` · ${e.action.args.sub}` : ""} — ${e.state}`,
      e.error ? h("span", { class: "muted" }, ` ${e.error}`) : null)))];
}

// upgrading is the button that rebuilds the whole fleet, and what the server said
// when it was last pressed.
//
// Its own block at the foot of the panel rather than a control beside the fleet:
// it is the only thing in cq that takes the site down, and a button that does that
// should not sit next to `tend`.
// rebuild is `tooling › rebuild`.
//
// The card is drawn whether or not there is anything to press. It was a section
// of the admin panel before and could simply be absent; as a tab of its own an
// empty render is a blank page, and a blank page is indistinguishable from a
// broken one.
export function rebuild(state, actions) {
  const got = state.upgrading;
  return [h("article", { class: "card" },
    h("h2", {}, "the build"),
    h("div", { class: "meta" },
      "pull the tree, rebuild every orc tool, and restart — here and on every agent machine"),
    h("div", { class: "body" },
      h("div", { class: "controls" },
        actions
          ? h("button", { class: "danger", onclick: () => actions.upgradeEverything(state) },
              "rebuild everything")
          : null,
        h("span", { class: "muted" }, "the site restarts; agents rebuild on their next sync")),
      // What the server said, kept on screen. It is the last thing this page hears
      // before the server goes away, so throwing it out on the next redraw would
      // leave somebody watching a page that had stopped explaining itself.
      got
        ? h("div", { class: "pending" },
            h("div", {}, got.server),
            got.queued && got.queued.length
              ? h("div", {}, `queued for ${got.queued.join(", ")}`)
              : h("div", { class: "muted" }, "no agent machine was queued"),
            got.restarting
              ? h("div", { class: "muted" }, "this page will fail to reach the server until it is back")
              : null)
        : null,
      ...builtHere(state.built))),
  ];
}

// builtHere is how the server's own build actually went.
//
// The answer to the button is a promise: the build runs after the response, on a
// goroutine that ends with the process going away. So a build that *failed* left
// this page saying "pulling, building, and restarting" for as long as anybody
// looked at it — indistinguishable from one still running and from a restart about
// to happen — while the reason sat in a log on a machine you would have to log into.
//
// The one case nobody could see is the one case the server stays up to explain.
function builtHere(last) {
  if (!last) return [];
  if (last.state === "building") {
    return [h("p", { class: "pending" }, "the server is pulling and building…")];
  }
  if (last.state === "restarting") {
    const report = last.report || {};
    return [h("p", { class: "muted" },
      report.changed
        ? `built ${report.before || "?"} → ${report.after || "?"}; restarting`
        : "nothing new to pull; rebuilt and restarting")];
  }

  const report = last.report || {};
  return [
    h("p", { class: "failed" }, `the build failed — the server is still on the old one`),
    h("p", { class: "warn" }, last.error || "no reason was recorded"),
    // The steps, because the reason is usually in the output of one of them and a
    // summary that dropped it would send somebody to the log this exists to replace.
    ...(report.steps || []).filter((s) => s.output || s.error).map((s) =>
      h("details", {},
        h("summary", { class: s.error ? "failed" : "muted" }, s.what),
        h("pre", {}, s.output || s.error))),
  ];
}

// --- tree ----------------------------------------------------------------

// The repository as shape and size: where the weight sits, what it is made of,
// and which files carry the most of it.
//
// Its own section rather than a block inside the admin panel. Admin is about the
// mail store — who has an account, what has been sent, what the queue is doing —
// and this is about the checkout. They shared a tab because both are "the state
// of things", which is a category, not a subject, and a screen that answers two
// unrelated questions is one people learn to scroll past.
//
// It is also the wrong tab in the other direction: the survey needs no admin
// view, so putting it there hid it from every machine that had not run `mailman
// admin owner` — an operator with no admin panel had no way to see the shape of
// their own repository.
//
// Counts here, contents in docs and code. A survey that could show a file's text
// would be a second, worse code tab, and the two would drift.
export function tree(state) {
  const body = survey(state);
  if (body.length === 0) {
    return [h("p", { class: "muted" }, "no repository is mirrored — set $CQ_LIBRARY on the agent machine")];
  }
  return body;
}

// --- admin ---------------------------------------------------------------

// store is `mail › store`: the whole mailbox, not just mine.
//
// This screen and three others used to be one tab called `admin`, stacked in the
// order they were written — the fleet, the build, the queue, and this. Each has
// its own place now. The reasoning that moved the repository survey out first is
// the same one that moved the rest: a screen answering two unrelated questions is
// a screen people scroll past.
//
// It needs a whole-store permission the others do not, which is why it is the one
// that can say "nothing has synced" on a machine where every other tab works:
// `mailman admin owner <name>` is what grants it.
//
// One filter over every machine, and every row opens to the message itself. The
// snapshot has always carried the bodies; this screen used to show a subject and
// stop, which made the answer to "what did bob actually say" a trip to the agent
// machine for mail the browser was already holding.
export function store(state, actions) {
  const blocks = state.admin && state.admin.machines ? state.admin.machines : [];
  const out = [];

  if (blocks.length === 0) {
    out.push(h("p", { class: "muted" },
      "nothing has synced a whole-store view — `mailman admin owner` on the agent machine grants it"));
    return out;
  }

  const query = storeQuery(state);
  const cards = [];
  let shown = 0;
  let held = 0;

  for (const block of blocks) {
    const s = block.state;
    if (!s) {
      cards.push(h("article", { class: "card" },
        h("h2", {}, block.machine),
        h("div", { class: "body" },
          h("p", { class: "muted" }, "this machine syncs without the admin view"))));
      continue;
    }
    const all = s.messages || [];
    const kept = all.filter((m) => matches(m, query));
    shown += kept.length;
    held += all.length;

    cards.push(h("article", { class: "card" },
      h("h2", {}, block.machine),
      h("div", { class: "meta" },
        `${(s.users || []).length} users · ${counted(kept.length, all.length, "message")} · `,
        `${(s.receipts || []).length} receipts`,
        s.metadata_only ? h("span", { class: "badge" }, "metadata only") : null),
      h("div", { class: "body" },
        users(s.users || []),
        traffic(kept, s.receipts || [], {
          state, actions, machine: block.machine, withheld: !!s.metadata_only,
        })),
    ));
  }

  out.push(filterBox(query, shown, held, actions));
  out.push(...cards);
  return out;
}

// --- filtering the store -------------------------------------------------

// The one box, over every machine at once.
//
// Whole-store is the point of this screen: an operator asking "who has been
// talking about the parser" is not asking it of one agent machine, and a filter
// per card would make them ask it once per machine and add the answers up
// themselves. So the query lives in one place and every card is drawn through
// it, with each still saying how much of its own traffic survived.
export const STORE_FILTER = "store";

export function storeQuery(state) {
  return (state.filters && state.filters[STORE_FILTER]) || "";
}

// matches is what the box means: every word typed has to appear somewhere in the
// message — sender, recipients, subject, or the text itself.
//
// Words rather than a phrase, and all of them rather than any: `bob parser`
// finds what bob wrote about the parser, which is the question people actually
// arrive with. A phrase search would make that query find nothing and give no
// hint why. Case is ignored because nobody remembers how they capitalised a
// subject line six weeks ago.
export function matches(m, query) {
  const terms = String(query || "").toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return true;
  const hay = [
    m.from, (m.to || []).join(" "), (m.cc || []).join(" "),
    m.subject, m.body, m.mid, `#${m.puid}`,
  ].join(" ").toLowerCase();
  return terms.every((t) => hay.includes(t));
}

// counted says how much of something is on screen, and only mentions the total
// when the two differ — "12 of 12 messages" is a number that reads as a filter
// having gone wrong.
function counted(shown, total, word) {
  const plural = total === 1 ? word : `${word}s`;
  return shown === total ? `${total} ${plural}` : `${shown} of ${total} ${plural}`;
}

function filterBox(query, shown, total, actions) {
  // A form so a phone keyboard offers a search key rather than a newline, and
  // one that submits nothing: filtering happens on every keystroke, so pressing
  // it should do nothing at all rather than reload the page.
  return h("form", {
    class: "compose filter",
    onsubmit: (e) => e.preventDefault(),
  },
    h("label", { for: "store-filter" }, "filter"),
    h("input", {
      // The name is what carries the cursor across a redraw — see focus.js.
      // Without it, a sync landing mid-word would move the caret to the end of
      // what had been typed so far.
      id: "store-filter", name: "store-filter", type: "search",
      autocomplete: "off", value: query,
      placeholder: "words from the sender, the subject, or the body",
      oninput: (e) => actions && actions.filter(STORE_FILTER, e.target.value),
    }),
    h("p", { class: "muted" },
      query ? `${counted(shown, total, "message")} match` : `${total} message${total === 1 ? "" : "s"}`),
  );
}

function users(list) {
  if (list.length === 0) return h("p", { class: "muted" }, "no accounts");
  return h("div", { class: "grid users" },
    h("div", { class: "muted" }, "account"),
    ...list.map((u) => h("div", { class: "who" }, u.name)));
}

// traffic pairs each message with who has read it — which is the question an
// admin panel exists to answer — and opens to the message itself.
//
// It was a four-column table and could not answer the second question anybody
// asks. "bob wrote to carol and carol has not read it" is only half of what an
// operator needs: the other half is what bob actually said, and the whole store
// is already in this browser's hands. A table that shows a subject and hides the
// text sends somebody to the agent machine to read mail they had already been
// sent.
//
// So each row folds. Closed, it says exactly what the table said; open, it adds
// the addressing and the body. Openness is state rather than DOM for the same
// reason the library's folds are: a sync lands every minute, and a redraw that
// collapsed what somebody was reading would be a page that fights them.
function traffic(messages, receipts, ctx) {
  if (messages.length === 0) {
    return h("p", { class: "muted" },
      storeQuery(ctx.state) ? "nothing here matches" : "no messages");
  }

  const byMID = new Map();
  for (const r of receipts) {
    if (!byMID.has(r.mid)) byMID.set(r.mid, []);
    byMID.get(r.mid).push(r);
  }

  return h("div", { class: "rows" },
    ...messages.map((m) => storedMessage(m, byMID.get(m.mid) || [], ctx)));
}

// readers is the old "read by" column: how many of the people it went to have
// opened it, and which. It keeps its three colours, because "nobody" and
// "everybody" are the two answers worth seeing without reading the row.
function readers(m, got) {
  const seen = got.filter((r) => r.read).map((r) => r.recipient);
  const total = (m.to || []).length + (m.cc || []).length;
  return h("span", {
    class: `note ${seen.length === 0 ? "muted" : seen.length >= total ? "ok" : "pending"}`,
  }, seen.length === 0 ? "nobody" : `${seen.length}/${total || seen.length} · ${seen.join(", ")}`);
}

function storedMessage(m, got, ctx) {
  const key = `store:${ctx.machine}:${m.mid}`;
  const open = ctx.state.open ? !!ctx.state.open[key] : false;

  const head = h("button", {
    class: "fold",
    "aria-expanded": open ? "true" : "false",
    onclick: () => ctx.actions && ctx.actions.toggle(key),
  },
    h("span", { class: "twist" }, open ? "▾" : "▸"),
    h("span", { class: "who" }, ellipsis(m.from, 12)),
    h("span", { class: "subject" }, ellipsis(m.subject, 40)),
    readers(m, got),
  );

  if (!open) return head;
  return h("div", { class: "folded" }, head,
    h("div", { class: "inner" },
      h("div", { class: "meta" },
        `${m.from} → ${(m.to || []).join(", ") || "—"}`,
        m.cc && m.cc.length ? ` · cc ${m.cc.join(", ")}` : "",
        ` · ${clock(m.sent)}`,
        m.archived ? " · archived" : ""),
      storedBody(m, ctx)));
}

// storedBody says why there is no text when there is none, because the two
// reasons are different and only one of them is worth doing anything about.
//
// A snapshot taken with --admin-metadata-only carries no bodies at all: the
// operator on the agent machine decided that, and no amount of clicking here
// will produce them. An empty body on a snapshot that does carry them is just a
// message somebody sent with a subject and nothing else.
function storedBody(m, ctx) {
  if (ctx.withheld) {
    return h("p", { class: "muted" },
      "this machine syncs with --admin-metadata-only, so it sends subjects without bodies");
  }
  if (!m.body) return h("p", { class: "muted" }, "no body");
  return h("div", { class: "body" }, render(m.body));
}

