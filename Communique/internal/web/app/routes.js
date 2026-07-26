// What the site is made of: five areas, and what is in each.
//
// This is the one place that says a tab exists. The navigation draws itself from
// it and the router matches against it, so the two cannot come to disagree about
// what there is — which is the failure a hand-written `else if` chain beside a
// hand-written list of links eventually reaches.
//
// It holds no rendering and imports nothing. What each route *draws* is
// screens.js, kept apart so that this can be read, and tested, as a description
// of the site rather than as part of it.

// The areas, in the order they are shown. A major tab lands on its first
// sub-tab, so the order of `subs` decides what opening an area shows first —
// `manage` leads with the fleet because it is about what is running, and a
// repository survey would make the common case the second click.
export const AREAS = [
  {
    major: "mail",
    subs: [
      { sub: "inbox", count: unread },
      { sub: "compose" },
      { sub: "sent" },
      { sub: "archive" },
      // The whole-store view: who has an account, and who has read what. It is
      // about the mail, which is why it is here and not under admin.
      { sub: "store", needs: "adminEnabled" },
    ],
  },
  {
    major: "project",
    // `location` is where each agent works — the copy of the repository its hands
    // are on, which is what the two above are read relative to.
    subs: [{ sub: "code" }, { sub: "docs" }, { sub: "location" }],
  },
  {
    major: "manage",
    subs: [
      { sub: "fleet" },
      { sub: "tasks" },
      { sub: "tree" },
      { sub: "tokens" },
    ],
  },
  {
    // Who exists, what job they hold, and what that job may do — in that order,
    // because each answers the next one's "why".
    major: "admin",
    subs: [
      { sub: "identities", needs: "adminEnabled" },
      { sub: "roles", needs: "adminEnabled" },
      { sub: "perms", needs: "adminEnabled" },
      // And what they are told: the standing instructions each agent runs under.
      // Last, because it is the only one of the four that persuades rather than
      // permits — a prompt cannot stop anything the three above allow.
      { sub: "instruct", needs: "adminEnabled" },
    ],
  },
  {
    major: "tooling",
    subs: [{ sub: "queue", count: stuck }, { sub: "rebuild" }],
  },
];

// --- the counts ----------------------------------------------------------
//
// Only what needs a decision. Something on its way is not a problem, and a badge
// that counted it would never reach zero.

function unread(state) {
  return (state.inbox || []).filter((m) => !m.read).length;
}

function stuck(state) {
  return (state.queue || []).filter(
    (e) => e.state === "failed" || e.state === "in_doubt").length;
}

// --- reading a route -----------------------------------------------------

// Where the site opens.
export const HOME = "/mail/inbox";

// Routes that are not tabs: a detail view is reached *from* a list, and the
// links to them are already out there in bookmarks and in the message rows the
// mailbox draws. Re-nesting those would break them for nothing.
const DETAIL = ["/message/", "/tasks/"];

export function isDetail(route) {
  return DETAIL.some((prefix) => route.startsWith(prefix));
}

// Where the flat routes went.
//
// Every one of these was a link somebody could have bookmarked, and `#/admin`
// in particular was the whole of four screens. They are kept indefinitely: a
// redirect costs a line, and a dead bookmark costs somebody the trust that the
// address bar means anything.
export const MOVED = {
  "/inbox": "/mail/inbox",
  "/compose": "/mail/compose",
  "/sent": "/mail/sent",
  "/archive": "/mail/archive",
  "/code": "/project/code",
  "/docs": "/project/docs",
  "/tasks": "/manage/tasks",
  "/tree": "/manage/tree",
  "/queue": "/tooling/queue",
  // The fleet is what the old admin tab was opened for.
  "/admin": "/admin/identities",
};

// resolve turns a hash into the tab it names.
//
// It returns null for a detail route and for anything it does not recognise —
// the caller decides what to do about each, because "show the inbox" is right
// for a typo and wrong for a message link.
export function resolve(route) {
  const [path] = String(route || "").split("?");
  const parts = path.split("/").filter(Boolean);
  if (parts.length < 2) return null;

  const [major, sub] = parts;
  const area = AREAS.find((a) => a.major === major);
  if (!area) return null;
  const found = area.subs.find((s) => s.sub === sub);
  return found ? { major, sub, tab: found } : null;
}

// home is where a major tab goes when it is clicked: its first sub-tab that the
// reader can actually see.
export function home(major, state) {
  const area = AREAS.find((a) => a.major === major);
  if (!area) return HOME;
  const first = visible(area, state)[0];
  return first ? `/${major}/${first.sub}` : HOME;
}

// visible drops the sub-tabs this deployment has nothing behind.
//
// `--no-admin` is the case: the panel is not served, so the tabs that read it
// would be tabs that always say nothing. An area with no visible sub-tabs is
// dropped entirely by `nav`.
export function visible(area, state) {
  return area.subs.filter((s) => !s.needs || state[s.needs]);
}

// count is what a sub-tab's badge says, and a major's is the sum of its
// children's — a badge on a sub-tab is invisible whenever another area is open,
// which is most of the time and exactly when it matters.
export function count(tab, state) {
  return tab.count ? tab.count(state) : 0;
}

export function areaCount(area, state) {
  return visible(area, state).reduce((n, s) => n + count(s, state), 0);
}
