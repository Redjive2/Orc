// The views. Each takes the current state and returns elements; none of them
// fetches anything or mutates state, so what is on screen is always a function
// of what the store holds.

import { h, mount, clock, since, ellipsis } from "./dom.js";
import { render } from "./markdown.js";
import { survey } from "./survey.js";
import { fleet } from "./fleet.js";

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

export function mailbox(state, { box }, actions) {
  const shape = BOXES[box] || BOXES.inbox;
  const messages = state[box];
  if (!messages || messages.length === 0) {
    return [h("p", { class: "muted" }, shape.empty)];
  }
  const many = (state.machines || []).length > 1;
  return [h("div", { class: "rows" },
    ...messages.map((m) => messageLine(m, many, shape.outgoing, state, actions)))];
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
  const waiting = pendingFor(state.queue, m.puid, m.machine)
    .some((e) => e.action.op === "reply");

  return h("div", { class: "line" },
    messageRow(m, showMachine, outgoing),
    waiting
      // Said where the reply was written, rather than only in the status bar: a
      // queued answer that leaves no mark reads as an answer that did not send.
      ? h("span", { class: "muted replied" }, "replied")
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
    composer(m, actions),
    ccForm(m, actions),
    h("p", {},
      h("button", { class: "quiet", onclick: () => actions.markRead(m) }, "mark read"),
      " ",
      h("button", { class: "quiet", onclick: () => actions.archive(m) }, "archive"),
    ),
  ];
}

// ccForm adds someone to the conversation.
//
// Only for mail that is in one: a conversation is what `cc` addresses, so a
// message with no thread has nothing to add anyone to, and offering the control
// anyway would be offering a button that cannot work.
function ccForm(m, actions) {
  if (!m.convo || !m.convo.uid) return null;

  const who = h("input", { name: "cc", placeholder: "carol", autocomplete: "off" });
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
        .then((ok) => { if (ok) { who.value = ""; problem.textContent = ""; } })
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
function queuedCard(entry) {
  const failed = entry.state === "failed";
  return h("article", { class: `card ${failed ? "failed" : "pending"}` },
    h("h2", {}, verb(entry.action.op)),
    h("div", { class: "meta" },
      h("span", { class: "badge" }, failed ? "failed" : "queued"),
      failed
        ? ` ${entry.error || "the agent refused it"}`
        : " will leave on the next sync"),
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

function composer(m, actions) {
  const subject = h("input", { name: "subject", value: reSubject(m.subject) });
  const body = h("textarea", { name: "body", placeholder: "…" });
  const button = h("button", { type: "submit" }, "queue reply");

  return h("form", {
    class: "compose",
    onsubmit: (e) => {
      e.preventDefault();
      button.disabled = true;
      actions.reply(m, subject.value, body.value).finally(() => { button.disabled = false; });
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
const NAME = /^[a-z0-9][a-z0-9._-]{0,63}$/;

// recipients splits what was typed and says what is wrong with it, if anything.
//
// Commas or spaces, either way: the field says "comma separated" and someone
// will use spaces regardless, and guessing right costs one character of regexp.
export function recipients(text) {
  const names = String(text).split(/[,\s]+/).filter(Boolean).map((n) => n.toLowerCase());
  if (names.length === 0) return { names: [], error: "no recipients" };

  const bad = names.filter((n) => !NAME.test(n));
  if (bad.length > 0) {
    return {
      names: [],
      error: `not a name: ${bad.join(", ")} — letters, digits, and . _ -`,
    };
  }
  // Duplicates are the writer's slip, not an error worth stopping for.
  return { names: [...new Set(names)], error: "" };
}

export function compose(state, actions) {
  const machines = state.machines || [];
  if (machines.length === 0) {
    return [h("p", { class: "muted" }, "nothing has synced yet, so there is nowhere to send from")];
  }

  const to = h("input", { name: "to", placeholder: "bob, carol", autocomplete: "off" });
  const subject = h("input", { name: "subject", autocomplete: "off" });
  const body = h("textarea", { name: "body", placeholder: "…" });
  const button = h("button", { type: "submit" }, "queue message");
  const problem = h("p", { class: "error" });

  // Only asked when there is a real choice. One machine is the ordinary case,
  // and a select with one option is a question with one answer.
  const picker = machines.length > 1
    ? h("select", { name: "machine" },
        ...machines.map((m) => h("option", { value: m.machine }, m.machine)))
    : null;
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
    ...(waiting.length > 0
      ? [h("h2", {}, "waiting"), ...waiting.map((e) => queuedCard(e))]
      : []),
  ];
}

// --- the queue -----------------------------------------------------------

// STATES orders the queue by what the reader can do about each row: the things
// needing a decision first, then what is on its way, then history.
const STATES = [
  { state: "failed", title: "refused", note: "nothing happened, so it can be tried again" },
  { state: "in_doubt", title: "in doubt", note: "started, and its outcome was never reported" },
  { state: "queued", title: "waiting", note: "leaves on the next sync" },
  { state: "sent", title: "with the agent", note: "collected, and not yet reported on" },
  { state: "done", title: "done", note: "" },
];

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
  if (entries.length === 0) {
    return [h("p", { class: "muted" }, "nothing queued")];
  }

  const out = [];
  for (const group of STATES) {
    const rows = entries.filter((e) => e.state === group.state);
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
    for (const entry of rows) out.push(queueRow(entry, actions));
  }
  return out;
}

function queueRow(entry, actions) {
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
  // Every *settled* row, not only the unresolved ones. A done action is a record
  // of something that already happened, and the record was previously impossible
  // to remove from the browser at all — so the queue only ever grew, and the rows
  // worth reading sank below the ones that had worked.
  //
  // In-flight rows have no button, and that is the server's rule rather than a
  // choice made here: an action the agent may still report on cannot be dropped,
  // because the report would have nowhere to land.
  if (settled(entry) && actions) {
    controls.push(h("button", {
      class: "quiet",
      onclick: (e) => hold(e.target, () => actions.drop(entry)),
    }, unresolved ? "forget it" : "remove"));
  }

  return h("article", { class: `card ${unresolved ? "failed" : "pending"}` },
    h("h2", {}, verb(entry.action.op)),
    h("div", { class: "meta" },
      h("span", { class: "badge" }, entry.state.replace("_", " ")),
      ` ${describe(entry.action)}`),
    entry.error ? h("div", { class: "body" }, h("pre", {}, entry.error)) : null,
    controls.length > 0 ? h("div", { class: "meta" }, ...spaced(controls)) : null,
  );
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

export function nav(state, route) {
  const unread = (state.inbox || []).filter((m) => !m.read).length;
  // Only what needs a decision is counted. Something on its way is not a
  // problem, and a badge that counts it would never reach zero.
  const stuck = (state.queue || []).filter(
    (e) => e.state === "failed" || e.state === "in_doubt").length;
  const link = (href, label, extra) => h("a", {
    href,
    "aria-current": route.startsWith(href.slice(1)) ? "page" : null,
  }, label, extra);

  return [
    link("#/inbox", "inbox", unread > 0 ? h("span", { class: "count" }, ` ${unread}`) : null),
    link("#/compose", "compose"),
    link("#/sent", "sent"),
    link("#/archive", "archive"),
    link("#/queue", "queue", stuck > 0 ? h("span", { class: "count" }, ` ${stuck}`) : null),
    link("#/docs", "docs"),
    link("#/code", "code"),
    link("#/tasks", "tasks"),
    link("#/tree", "tree"),
    state.adminEnabled ? link("#/admin", "admin") : null,
    h("span", { class: "spacer" }),
    h("a", { href: "#/inbox", onclick: (e) => { e.preventDefault(); logout(); } }, "logout"),
  ];
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
  // than one, and the picker is the same shape compose uses for the same reason.
  let picker = null;
  if (many) {
    picker = h("select", { class: "machine" },
      ...machines.map((m) => h("option", { value: m.machine }, m.machine)));
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
  return h("a", {
    class: `task row${t.draft ? " draft" : ""}`,
    href: `#/tasks/${encodeURIComponent(t.name)}`,
  },
    h("span", { class: s.cls }, s.glyph),
    h("span", { class: "name" }, t.name, t.draft ? h("span", { class: "badge" }, "draft") : null),
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
function upgrading(state, actions) {
  if (!actions) return [];
  const got = state.upgrading;
  return [h("article", { class: "card" },
    h("h2", {}, "the build"),
    h("div", { class: "meta" },
      "pull the tree, rebuild every orc tool, and restart — here and on every agent machine"),
    h("div", { class: "body" },
      h("div", { class: "controls" },
        h("button", { class: "danger", onclick: () => actions.upgradeEverything(state) },
          "rebuild everything"),
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
        : null)),
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

export function admin(state, actions) {
  const blocks = state.admin && state.admin.machines ? state.admin.machines : [];
  // The survey used to be here and is now its own section — see `tree` below.
  // It was in the admin panel because that is the tab about the state of things,
  // but it is about the *repository* and the admin panel is about the *mail*, and
  // one screen answering two unrelated questions is a screen people scroll past.
  // The fleet first, then the queue, then the mail.
  //
  // Who exists and what they may do is what this panel is opened for, and unlike
  // the mail view below it needs no whole-store permission — so it answers even on
  // a machine that has never run `mailman admin owner`. The queue used to come
  // first and is a health check: worth having, not worth twenty rows of resolved
  // history between somebody and the thing they came to change.
  const out = [...fleet(state, actions), ...upgrading(state, actions), queueHealth(state)];

  if (blocks.length === 0) {
    out.push(h("p", { class: "muted" }, "nothing has synced an admin view yet"));
    return out;
  }

  for (const block of blocks) {
    const s = block.state;
    if (!s) {
      out.push(h("article", { class: "card" },
        h("h2", {}, block.machine),
        h("div", { class: "body" },
          h("p", { class: "muted" }, "this machine syncs without the admin view"))));
      continue;
    }
    out.push(h("article", { class: "card" },
      h("h2", {}, block.machine),
      h("div", { class: "meta" },
        `${(s.users || []).length} users · ${(s.messages || []).length} messages · `,
        `${(s.receipts || []).length} receipts`,
        s.metadata_only ? h("span", { class: "badge" }, "metadata only") : null),
      h("div", { class: "body" },
        users(s.users || []),
        traffic(s.messages || [], s.receipts || [])),
    ));
  }
  return out;
}

function users(list) {
  if (list.length === 0) return h("p", { class: "muted" }, "no accounts");
  return h("div", { class: "grid users" },
    h("div", { class: "muted" }, "account"),
    ...list.map((u) => h("div", { class: "who" }, u.name)));
}

// traffic pairs each message with who has read it — which is the question an
// admin panel exists to answer.
function traffic(messages, receipts) {
  if (messages.length === 0) return h("p", { class: "muted" }, "no messages");

  const byMID = new Map();
  for (const r of receipts) {
    if (!byMID.has(r.mid)) byMID.set(r.mid, []);
    byMID.get(r.mid).push(r);
  }

  return h("div", { class: "grid traffic" },
    h("div", { class: "muted" }, "from"),
    h("div", { class: "muted" }, "to"),
    h("div", { class: "muted" }, "subject"),
    h("div", { class: "muted" }, "read by"),
    ...messages.flatMap((m) => {
      const seen = (byMID.get(m.mid) || []).filter((r) => r.read).map((r) => r.recipient);
      const total = (m.to || []).length + (m.cc || []).length;
      return [
        h("div", { class: "who" }, m.from),
        h("div", {}, ellipsis((m.to || []).join(", "), 18)),
        h("div", {}, ellipsis(m.subject, 34)),
        h("div", { class: seen.length === 0 ? "muted" : seen.length >= total ? "ok" : "pending" },
          seen.length === 0 ? "nobody" : `${seen.length}/${total || seen.length} · ${seen.join(", ")}`),
      ];
    }));
}

// queueHealth is the panel's most operational view: what the user asked for and
// what became of it.
function queueHealth(state) {
  const queue = state.queue || [];
  if (queue.length === 0) {
    return h("article", { class: "card" },
      h("h2", {}, "queue"),
      h("div", { class: "body" }, h("p", { class: "muted" }, "nothing queued")));
  }
  return h("article", { class: "card" },
    h("h2", {}, "queue"),
    h("div", { class: "body" },
      h("div", { class: "grid queue" },
        h("div", { class: "muted" }, "state"),
        h("div", { class: "muted" }, "action"),
        h("div", { class: "muted" }, "machine"),
        h("div", { class: "muted" }, "detail"),
        ...queue.flatMap((e) => [
          h("div", { class: stateClass(e.state) }, e.state),
          h("div", {}, verb(e.action.op)),
          h("div", { class: "muted" }, e.action.machine),
          h("div", { class: e.state === "failed" ? "failed" : "muted" },
            e.error || (e.action.args && e.action.args.subject) || "—"),
        ]))));
}

function stateClass(s) {
  switch (s) {
    case "failed": return "failed";
    case "done": return "ok";
    default: return "pending";
  }
}
