// What each route draws.
//
// Kept apart from routes.js so that the description of the site imports nothing
// and can be read on its own — and kept apart from app.js so that a test can
// check every tab in routes.js has something behind it. A tab that renders
// nothing is a tab somebody clicks once and never again.

import * as views from "./views.js";
import * as library from "./library.js";
import * as location from "./location.js";
import * as fleet from "./fleet.js";
import * as instruct from "./instruct.js";
import { h } from "./dom.js";

// SCREENS is keyed `major/sub`, matching routes.js exactly. The test that says
// so is the point of the two being separate files.
export const SCREENS = {
  "mail/inbox": (s, a) => views.mailbox(s, { box: "inbox" }, a),
  "mail/compose": (s, a) => views.compose(s, a),
  "mail/sent": (s, a) => views.mailbox(s, { box: "sent" }, a),
  "mail/archive": (s, a) => views.mailbox(s, { box: "archive" }, a),
  "mail/store": (s, a) => views.store(s, a),

  "project/code": (s, a) => [
    library.libraryHeader(s, "code"),
    ...library.library(s, a, { kind: "code" }),
  ],
  "project/docs": (s, a) => [
    library.libraryHeader(s, "docs"),
    ...library.library(s, a, { kind: "docs" }),
  ],
  "project/location": (s, a) => location.location(s, a),

  "manage/fleet": (s, a) => fleet.running(s, a),
  "manage/tasks": (s, a) => views.tasks(s, a),
  "manage/tree": (s) => views.tree(s),
  "manage/tokens": () => tokens(),

  "admin/identities": (s, a) => fleet.tree(s, a),
  "admin/roles": (s, a) => fleet.roleList(s, a),
  "admin/perms": (s, a) => fleet.permissionList(s, a),
  "admin/instruct": (s, a) => instruct.instruct(s, a),

  "tooling/queue": (s, a) => views.queue(s, a),
  "tooling/rebuild": (s, a) => views.rebuild(s, a),
};

// tokens is a tab with nothing behind it yet, and says so.
//
// It is here rather than absent because the shape of the site is a decision and
// this is part of it — but nothing in Orc measures what an agent spends. Claude
// writes the numbers into its own transcript, Orc records where that file is and
// reads only its tail for the session pane, and no total is kept anywhere.
//
// An empty tab that explains itself is worth more than one that draws a zero:
// a zero is a measurement, and there is no measurement.
function tokens() {
  return [
    h("article", { class: "card" },
      h("h2", {}, "what the fleet is spending"),
      h("div", { class: "meta" }, "nothing measures this yet"),
      h("div", { class: "body" },
        h("p", {}, "Claude records what each turn cost in its own transcript, and orc " +
          "knows where every session's transcript is. Nothing adds them up."),
        h("p", { class: "muted" }, "Reaching this needs a reader over the whole of each " +
          "transcript rather than its tail, an orc command to report the totals, and a " +
          "field on the snapshot so a sync carries them. Plan percentages are not in " +
          "there at all — those are facts about the account, held by Anthropic."),
        h("p", { class: "muted" }, "Budgets already exist and are set per role: " +
          "manage › fleet says what each identity may employ.")),
    ),
  ];
}

// render returns the nodes for one route, or null when nothing matches — which
// the router treats as "go home" rather than guessing.
export function render(major, sub, state, actions) {
  const screen = SCREENS[`${major}/${sub}`];
  return screen ? screen(state, actions) : null;
}
