// `tooling › logs`: what everything has been saying.
//
// Four sources, and they are not the same kind of thing, so the screen keeps them
// apart rather than pouring them into one river:
//
//   - **the server**, from memory, current to this instant. Lost on a restart,
//     which is said out loud — a server up for a week showing four lines is
//     otherwise a mystery rather than a fact.
//   - **sync, wake and tend**, from each agent machine, as old as its last sync.
//     These are the three loops that keep a fleet alive, and until they were
//     written down none of it was kept: `watch.Spawn` sends a detached watcher's
//     streams to the null device, so a sync failing for a day looked exactly like
//     one that had never started.
//
// Folded, and closed to begin with. A log is what somebody opens when they have a
// question, and four open logs is a page nobody can find the top of.
//
// **Colour comes from the level and never carries the meaning alone.** Every line
// that has one shows the word — INFO, WARN, ERROR — beside the colour, because a
// reader who cannot tell red from amber would otherwise be looking at an
// undifferentiated wall. Lines with no level are drawn plain rather than guessed
// at: output from a child process is not slog's and inventing a severity for it
// would be the screen making something up.

import { h, since } from "./dom.js";

// KINDS is the order they are read in: the mirror first, because a fleet whose
// sync has stopped has no other symptom worth reading.
const KINDS = ["sync", "wake", "tend"];

const WHAT = {
  sync: "mirrors this machine to the server",
  wake: "pokes agents that have gone quiet",
  tend: "reconciles the work list with what is running",
};

// screen is the whole tab.
export function screen(state, actions, now = Date.now()) {
  const got = state.logs;
  if (state.logsError) {
    return [h("p", { class: "warn" }, `the logs could not be read: ${state.logsError}`)];
  }
  if (!got) return [h("p", { class: "muted" }, "reading…")];

  return [
    serverCard(got, state, actions),
    ...(got.machines || []).map((m) => machineCard(m, state, actions, now)),
    ...((got.machines || []).length === 0
      ? [h("p", { class: "muted" },
          "no machine has synced, so there are no cycles to read")]
      : []),
  ];
}

function serverCard(got, state, actions) {
  const lines = got.server || [];
  return h("article", { class: "card" },
    h("h2", {}, "the server"),
    h("div", { class: "meta" },
      "what this process has been doing — kept in memory, so a restart clears it"),
    h("div", { class: "body" },
      fold("logs:server", "server", `${count(lines)} · current`, lines, state, actions)));
}

function machineCard(m, state, actions, now) {
  const machine = (state.machines || []).find((x) => x.machine === m.machine);
  const age = machine && machine.last_sync
    ? `mirrored ${since(machine.last_sync, now)}`
    : "never synced";

  if (m.unreachable) {
    return h("article", { class: "card" },
      h("h2", {}, m.machine),
      h("div", { class: "body" }, h("p", { class: "warn" }, m.unreachable)));
  }

  const tails = m.tails || [];
  return h("article", { class: "card" },
    h("h2", {}, m.machine),
    h("div", { class: "meta" }, `the fleet's three cycles · ${age}`),
    h("div", { class: "body" },
      ...KINDS.map((kind) => {
        const tail = tails.find((t) => t.kind === kind) || { kind };
        return fold(`logs:${m.machine}:${kind}`, kind, note(tail), tail.lines || [],
          state, actions, tail.note);
      })));
}

// note is the one line worth reading without opening anything: how much there is,
// and whether anything in it went wrong.
function note(tail) {
  const lines = tail.lines || [];
  const bits = [WHAT[tail.kind] || ""];
  if (tail.note) return `${bits[0]} · could not be read`;
  if (lines.length === 0) return `${bits[0]} · has not run here`;

  const bad = lines.filter((l) => l.level === "ERROR").length;
  const warn = lines.filter((l) => l.level === "WARN").length;
  bits.push(count(lines));
  // Named rather than only coloured, and on the closed row: a reader scanning
  // four folds for the one with trouble in it should not have to open all four.
  if (bad) bits.push(`${bad} error${bad === 1 ? "" : "s"}`);
  else if (warn) bits.push(`${warn} warning${warn === 1 ? "" : "s"}`);
  return bits.filter(Boolean).join(" · ");
}

function count(lines) {
  return `${lines.length} line${lines.length === 1 ? "" : "s"}`;
}

// fold is one collapsible log.
function fold(key, title, summary, lines, state, actions, problem) {
  const open = state && state.open ? Boolean(state.open[key]) : false;

  const worst = lines.some((l) => l.level === "ERROR") ? "bad"
    : lines.some((l) => l.level === "WARN") ? "warn" : "";

  const head = h("button", {
    class: open ? "fold picked" : "fold",
    "aria-expanded": open ? "true" : "false",
    onclick: () => actions && actions.toggle(key),
  },
    h("span", { class: "twist" }, open ? "▾" : "▸"),
    h("span", { class: "sect" }, title),
    h("span", { class: `muted note ${worst}` }, summary));

  if (!open) return head;

  const body = problem
    ? [h("p", { class: "warn" }, problem)]
    : lines.length === 0
      ? [h("p", { class: "muted" }, "nothing here yet")]
      : [h("div", { class: "log" }, ...lines.map(line))];

  return h("div", { class: "folded" }, head, h("div", { class: "inner" }, ...body));
}

// line is one entry.
//
// The level is its own element so it can be coloured and so it lines up down the
// left; the text is a separate one that scrolls sideways *within the block* when
// it is long. A log line is the one kind of content that must not wrap — a wrapped
// `key=value` line reads as two entries — and it must not push the page sideways
// either. Those two together mean the overflow belongs here and nowhere else.
function line(l) {
  return h("div", { class: `log-line ${rank(l.level)}` },
    h("span", { class: "log-level" }, l.level || ""),
    h("span", { class: "log-text" }, l.text));
}

// rank maps a level to a class. Unknown and absent are the same thing — plain —
// because a level this build has never heard of is not a reason to invent a
// severity for the line carrying it.
function rank(level) {
  switch (level) {
    case "ERROR": return "bad";
    case "WARN": return "warn";
    case "DEBUG": return "faint";
    case "INFO": return "ok";
    default: return "";
  }
}
