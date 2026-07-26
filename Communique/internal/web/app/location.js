// Where each agent works.
//
// Every other fleet screen is about *what* an agent is — its job, its permissions,
// what it is running on. This one is about where it stands: the directory its
// session was started in, which is the thing its scopes are relative to and its
// compiled permissions are written against.
//
// It sits under `project` rather than `manage` because that is the question it
// answers. `project/code` and `project/docs` show what is in the repository; this
// shows which copy of it each agent has its hands on.
//
// It is its own file for the same reason library.js is: a screen with a form on it
// is more code than a row, and a tab that grows should not grow inside the file that
// holds every other tab.

import { h } from "./dom.js";
import { agents } from "./fleet.js";

// location is the `project/location` screen.
export function location(state, actions) {
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
        h("h2", {}, `${f.machine} — location`),
        h("div", { class: "body" }, h("p", { class: "warn" }, f.unreachable)))];
    }
    return [h("article", { class: "card" },
      h("h2", {}, `${f.machine} — location`),
      h("div", { class: "meta" }, shared(f)),
      h("div", { class: "body" }, ...places(f, actions)))];
  });
}

// shared says how many agents share a directory, which is the fact this screen
// exists to make visible.
//
// Two agents in one tree is a decision somebody may have made deliberately; two
// agents in one tree by accident is how a scope stops meaning anything. Either way
// it is worth seeing at the top rather than working out by reading down a column.
function shared(f) {
  const list = agents(f);
  const seen = new Map();
  for (const id of list) {
    if (!id.workspace) continue;
    seen.set(id.workspace, (seen.get(id.workspace) || 0) + 1);
  }
  const together = [...seen.values()].filter((n) => n > 1).length;

  const where = `${seen.size} ${seen.size === 1 ? "directory" : "directories"}`;
  if (together === 0) return `${list.length} agents · ${where}`;
  return `${list.length} agents · ${where} · ${together} shared by more than one`;
}

// places is one row per identity.
function places(f, actions) {
  const list = agents(f);
  if (list.length === 0) return [h("p", { class: "muted" }, "nobody yet")];

  // Grouped by directory, so agents sharing one are next to each other rather than
  // scattered down an alphabetical list.
  const order = [...list].sort((a, b) =>
    (a.workspace || "").localeCompare(b.workspace || "") || a.name.localeCompare(b.name));

  return order.map((id) => h("div", { class: id.employed ? "agent" : "agent idle" },
    h("div", { class: "agent-head" },
      h("strong", {}, id.name),
      h("span", { class: "muted" }, id.employed ? "employed" : "idle"),
    ),
    h("div", { class: "path" }, id.workspace || "—"),
    actions ? h("div", { class: "controls" },
      h("button", {
        class: "quiet",
        onclick: () => actions.moveWorkspace(f, id),
      }, "move"),
      h("span", { class: "muted" }, note(id)),
    ) : null,
  ));
}

// note is the one sentence beside the button.
//
// A running session keeps the directory it started in, so moving an employed agent
// is a change that does not take effect until the session is replaced. Saying that
// here, before the click, is better than saying it in the confirmation — somebody
// deciding whether to move an agent wants to know the cost while they are deciding.
function note(id) {
  if (!id.employed) return "it will start there next time it is employed";
  return "its session keeps the old directory until it is refreshed";
}
