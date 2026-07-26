// What agents are told.
//
// Four kinds and two mechanisms, and the screen has to keep them apart because they
// are edited in the same place and mean opposite things:
//
//   - **system, role, identity** are prompt *layers*. They compose additively — the
//     fleet's, then the role's, then the agent's — into what an agent is told at the
//     start of a session.
//   - **wake** is a *message*, sent to an agent that has gone quiet. Those override:
//     the most specific one wins and the others are not sent.
//
// The panel says which is which beside each group, because somebody who edits a wake
// message expecting it to add to the fleet's has made a mistake nothing else will
// tell them about.
//
// It is its own file, as `location.js` is: a tab with an editor on it is more code
// than a row.

import { h } from "./dom.js";
import { agents } from "./fleet.js";

// instruct is the `manage/instruct` screen.
export function instruct(state, actions) {
  const fleets = state.fleet || [];

  if (state.fleetError) {
    return [h("p", { class: "warn" }, `the fleet could not be read: ${state.fleetError}`)];
  }
  if (fleets.length === 0) {
    return [h("p", { class: "muted" },
      "no machine mirrors an orc fleet — a machine that runs agents will carry one")];
  }

  return fleets.flatMap((f) => {
    if (f.unreachable) {
      return [h("article", { class: "card" },
        h("h2", {}, `${f.machine} — instruct`),
        h("div", { class: "body" }, h("p", { class: "warn" }, f.unreachable)))];
    }
    return [h("article", { class: "card" },
      h("h2", {}, `${f.machine} — instruct`),
      h("div", { class: "meta" }, summary(f)),
      h("div", { class: "body" },
        // The sentence that stops somebody using a prompt where they needed a
        // permission. It is first because that mistake is silent and looks like it
        // is working right up until it does not.
        h("p", { class: "muted" },
          "a prompt asks and a permission enforces — nothing here can stop an agent doing anything"),
        ...layers(f, actions),
        ...wakes(f, actions)),
    )];
  });
}

function summary(f) {
  const prompts = f.prompts || [];
  const set = prompts.filter((p) => !p.wake).length;
  const messages = prompts.filter((p) => p.wake).length;
  if (set === 0 && messages === 0) {
    return "nothing is set — every agent runs on claude's own instructions";
  }
  return `${set} ${set === 1 ? "layer" : "layers"} · ${messages} wake ${messages === 1 ? "message" : "messages"}`;
}

// find is one layer out of the mirror, or a blank one so the row can offer to write
// the first version of it.
function find(f, kind, name, wake) {
  const prompts = f.prompts || [];
  return prompts.find((p) =>
    p.kind === kind && (p.name || "") === (name || "") && Boolean(p.wake) === wake)
    || { kind, name, wake, text: "", size: 0 };
}

// layers draws the three that compose, in the order they compose.
function layers(f, actions) {
  const rows = [row(f, "system", "", false, "the fleet", actions)];

  for (const role of f.roles || []) {
    rows.push(row(f, "role", role.name, false, `the ${role.name} role`, actions));
  }
  for (const id of agents(f)) {
    rows.push(row(f, "identity", id.name, false, id.name, actions));
  }

  return [
    h("h3", {}, "layers"),
    h("p", { class: "muted" },
      "additive: an agent gets the fleet's, then its role's, then its own, in that order"),
    ...rows,
  ];
}

// wakes draws the messages, which override rather than compose.
function wakes(f, actions) {
  const rows = [row(f, "system", "", true, "the fleet", actions)];

  for (const role of f.roles || []) {
    rows.push(row(f, "role", role.name, true, `the ${role.name} role`, actions));
  }
  for (const id of agents(f)) {
    rows.push(row(f, "identity", id.name, true, id.name, actions));
  }

  return [
    h("h3", {}, "wake messages"),
    h("p", { class: "muted" },
      "overriding: an agent is sent its own, else its role's, else the fleet's, else “continue”"),
    ...rows,
  ];
}

// row is one layer: what it is, how big, when it last moved, and what can be done.
function row(f, kind, name, wake, label, actions) {
  const got = find(f, kind, name, wake);
  const empty = !got.text;

  return h("div", { class: empty ? "agent idle" : "agent" },
    h("div", { class: "agent-head" },
      h("strong", {}, label),
      h("span", { class: "muted" }, empty ? "not set" : size(got.size)),
      got.by ? h("span", { class: "muted" }, `${got.by}${got.changed ? ` · ${when(got.changed)}` : ""}`) : null,
    ),
    empty ? null : h("div", { class: "prompt" }, first(got.text)),
    actions ? h("div", { class: "controls" },
      h("button", { class: "quiet", onclick: () => actions.editInstruct(f, got) },
        empty ? "write" : "edit"),
      empty ? null : h("button", { class: "quiet", onclick: () => actions.clearInstruct(f, got) }, "clear"),
      !wake && kind === "identity"
        ? h("button", { class: "quiet", onclick: () => actions.showInstruct(f, got) }, "show composed")
        : null,
    ) : null,
  );
}

// first is the opening line, so a row says what a layer is about without being the
// layer. The whole text is a click away and most of them are paragraphs.
function first(text) {
  const line = String(text).split("\n").find((l) => l.trim() !== "") || "";
  return line.length > 90 ? `${line.slice(0, 89)}…` : line;
}

function size(n) {
  if (!n) return "empty";
  return n < 1024 ? `${n} B` : `${(n / 1024).toFixed(1)} KiB`;
}

// when is how long ago, for a column where the exact time is not the point.
function when(stamp) {
  const then = Date.parse(stamp);
  if (Number.isNaN(then)) return stamp;

  const mins = Math.floor((Date.now() - then) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  if (mins < 60 * 24) return `${Math.floor(mins / 60)}h ago`;
  return `${Math.floor(mins / 1440)}d ago`;
}
