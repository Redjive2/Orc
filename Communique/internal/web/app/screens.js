// What each route draws.
//
// Kept apart from routes.js so that the description of the site imports nothing
// and can be read on its own — and kept apart from app.js so that a test can
// check every tab in routes.js has something behind it. A tab that renders
// nothing is a tab somebody clicks once and never again.

import * as views from "./views.js";
import * as logs from "./logs.js";
import * as library from "./library.js";
import * as location from "./location.js";
import * as fleet from "./fleet.js";
import * as activityView from "./activity.js";
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
  "manage/activity": (s, a) => activityView.activity(s, a),

  "admin/identities": (s, a) => fleet.tree(s, a),
  "admin/roles": (s, a) => fleet.roleList(s, a),
  "admin/perms": (s, a) => fleet.permissionList(s, a),
  "admin/instruct": (s, a) => instruct.instruct(s, a),

  "tooling/queue": (s, a) => views.queue(s, a),
  "tooling/logs": (s, a) => logs.screen(s, a),
  "tooling/rebuild": (s, a) => views.rebuild(s, a),
};

// render returns the nodes for one route, or null when nothing matches — which
// the router treats as "go home" rather than guessing.
export function render(major, sub, state, actions) {
  const screen = SCREENS[`${major}/${sub}`];
  return screen ? screen(state, actions) : null;
}
