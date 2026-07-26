// Tests for the shape of the site.
//
// routes.js says what tabs exist and screens.js says what each one draws. They
// are separate files so that this can check they agree — a tab with no screen is
// a tab somebody clicks once and never again, and a screen with no tab is code
// nobody can reach.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const routes = await import("../routes.js");
const { SCREENS, render } = await import("../screens.js");

const everything = {
  adminEnabled: true, machines: [], fleet: [], inbox: [], archive: [], sent: [],
  queue: [], tasks: [], library: null, files: {}, open: {}, drafts: {},
};

test("every tab has a screen, and every screen has a tab", () => {
  const tabs = routes.AREAS.flatMap((a) => a.subs.map((s) => `${a.major}/${s.sub}`));
  assert.deepEqual(tabs.filter((t) => !SCREENS[t]), [], "tabs with nothing behind them");
  assert.deepEqual(Object.keys(SCREENS).filter((k) => !tabs.includes(k)), [],
    "screens nothing can reach");
});

// Every screen is rendered against a state where nothing has synced, because
// that is what a new deployment looks like and it is when a crash is worst.
test("every screen draws something on an empty state", () => {
  for (const key of Object.keys(SCREENS)) {
    const [major, sub] = key.split("/");
    const drawn = render(major, sub, everything, null);
    assert.ok(Array.isArray(drawn), `${key} drew nothing`);
    const said = drawn.filter(Boolean).map((n) => n.textContent).join(" ");
    assert.ok(said.trim().length > 0, `${key} rendered an empty screen`);
  }
});

test("a major opens on its first visible tab", () => {
  assert.equal(routes.home("mail", everything), "/mail/inbox");
  assert.equal(routes.home("manage", everything), "/manage/fleet");
  assert.equal(routes.home("admin", everything), "/admin/identities");
});

// `--no-admin` empties two areas. A major that opened onto a tab the reader
// cannot see would be a tab that does nothing when pressed.
test("a major with hidden tabs opens on one that is shown", () => {
  const plain = { ...everything, adminEnabled: false };
  assert.equal(routes.home("mail", plain), "/mail/inbox");
  assert.equal(routes.home("admin", plain), routes.HOME, "admin has nothing to show");
  assert.deepEqual(routes.visible(routes.AREAS.find((a) => a.major === "admin"), plain), []);
});

test("resolve reads a route, and refuses one it does not know", () => {
  assert.deepEqual(
    { major: "project", sub: "code" },
    (({ major, sub }) => ({ major, sub }))(routes.resolve("/project/code")));

  // A query is not part of the route.
  assert.equal(routes.resolve("/mail/inbox?machine=studio").sub, "inbox");

  for (const bad of ["", "/", "/mail", "/mail/nope", "/nope/inbox", "/message/0"]) {
    assert.equal(routes.resolve(bad), null, `${bad} resolved to something`);
  }
});

// Every flat route this replaced was a link somebody could have bookmarked, and
// `#/admin` was the whole of four screens.
test("every route that moved lands somewhere that exists", () => {
  for (const [from, to] of Object.entries(routes.MOVED)) {
    assert.ok(routes.resolve(to), `${from} moved to ${to}, which is not a tab`);
    assert.equal(routes.resolve(from), null, `${from} is still a tab as well as a redirect`);
  }
});

// The two routes that did not move, and must not: a detail view is reached from
// a list, and its links are already out there.
test("detail routes are left alone", () => {
  for (const route of ["/message/0?machine=studio", "/tasks/the-parser"]) {
    assert.ok(routes.isDetail(route), `${route} is not treated as a detail view`);
    assert.equal(routes.MOVED[route.split("?")[0]], undefined);
  }
  assert.equal(routes.isDetail("/mail/inbox"), false);
});

test("an area's count is the sum of its tabs'", () => {
  const state = {
    ...everything,
    inbox: [{ read: false }, { read: false }, { read: true }],
    queue: [{ state: "failed", action: {} }],
  };
  const area = (name) => routes.AREAS.find((a) => a.major === name);

  assert.equal(routes.areaCount(area("mail"), state), 2);
  assert.equal(routes.areaCount(area("tooling"), state), 1);
  assert.equal(routes.areaCount(area("project"), state), 0);

  // A hidden tab's count does not reach the area above it, or the badge would
  // point at something the reader cannot open.
  assert.equal(routes.areaCount(area("admin"), { ...state, adminEnabled: false }), 0);
});
