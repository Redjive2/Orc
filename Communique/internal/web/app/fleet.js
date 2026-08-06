// The fleet: what is running, and who may do what.
//
// Orc is the tool with no other remote face: mail has a mailbox, tasks have a
// board, and who-may-what had a terminal on the agent machine and nothing else.
// This is that, from a phone.
//
// It draws only what Orc derived. Authority here is *effective* — already the
// lower of a role's and a boss's — and the clause list is already intersected
// down the chain. Nothing is recomputed in the browser: a second derivation would
// be a second opinion about who may do what, and the wrong one would be the one
// on screen. Where the two numbers differ, both are shown, because an agent told
// it may write one directory while its role says another will otherwise file a
// bug against the wrong thing.

import { h } from "./dom.js";
import { sessionAt } from "./routes.js";
import { plural } from "./library.js";
import * as clause from "./clauses.js";

// remaining renders a wall-clock expiry as time left rather than as an instant.
//
// Orc's JSON carries the absolute moment, which is the right thing on the wire and
// the wrong thing on a screen: "until 2026-07-25T23:59:14.523Z" makes a reader do
// arithmetic against a clock they cannot see, when the only question they have is
// whether it is still good.
export function remaining(iso, now = Date.now()) {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return iso;
  const secs = Math.round((then - now) / 1000);
  if (secs <= 0) return "expired";
  if (secs < 60) return `${secs}s left`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m left`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `${hours}h left`;
  return `${Math.round(hours / 24)}d left`;
}

// roleBudget is the spawn load a role's permissions come to.
//
// Orc's role JSON does not carry one — a budget is a `spawn(n)` clause on a
// permission, not a field on a role — so it is worked out here from the two halves
// the fleet already carries. Largest wins, which is the rule Orc's own derivation
// uses.
//
// This is a *form default* and nothing else. Nothing branches on it, and what an
// identity may actually employ is `spawn_budget`, which Orc derived. It exists
// because the alternative was a sheet that offered 0 for a role already set to 24,
// where pressing enter would quietly wipe the budget.
export function roleBudget(f, role) {
  const byName = new Map((f.permissions || []).map((p) => [p.name, p]));
  let best = null;
  for (const name of role.permissions || []) {
    for (const clause of (byName.get(name) || {}).patterns || []) {
      const got = /^spawn\((\d+)\)$/.exec(clause);
      if (got) best = Math.max(best ?? 0, Number(got[1]));
    }
  }
  return best;
}

// A budget of nothing and no budget both refuse every employ, and they are
// different mistakes: one wants a bigger number, the other wants the permission.
function budgetLabel(load) {
  return load === null ? "no budget" : `may employ ${load}`;
}

// The scale Orc paints, with a glyph as well as a colour: colour is never the
// only signal, here or anywhere else in this interface.
function level(id) {
  if (id.operator) return h("span", { class: "ok" }, `${id.authority} operator`);
  if (id.capped) {
    // `‡` is Orc's own mark for a capped value, and it means the same thing here.
    return h("span", { class: "pending", title: "the boss chain caps it" },
      `${id.authority}/${id.asked_for}‡`);
  }
  return h("span", {}, String(id.authority));
}

// running says what is *actually* happening, which is a different question from
// what the work list says should be. The states where they disagree are the ones
// worth a word: employed with no session is what `tend` fixes.
function sessionState(id) {
  if (id.populated) {
    const label = id.restarts ? `live · restarted ${id.restarts}×` : "live";
    return h("span", { class: "ok" }, label);
  }
  if (id.employed) return h("span", { class: "failed" }, "employed, not running");
  return h("span", { class: "muted" }, "—");
}

// The fleet answers two different questions, and they became two tabs.
//
//   - *What is running right now* — `manage › fleet`. Load, sessions, employ and
//     fire. Something you look at because an agent is or is not working.
//   - *Who exists, and what may they do* — `admin › identities`, `roles`,
//     `perms`. The structure underneath, which changes rarely and deliberately.
//
// One screen answering both is the screen this was, and it was long enough that
// the running agents were below the permission editor.
//
// Each tab is built the same way: `perMachine` handles the cases every one of
// them shares — the fleet could not be read, no machine carries one, this
// machine is unreachable — so that no tab has to invent its own words for them.
export function perMachine(state, title, body) {
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
        h("h2", {}, `${f.machine} — ${title}`),
        h("div", { class: "body" }, h("p", { class: "warn" }, f.unreachable)))];
    }
    return [h("article", { class: "card" },
      h("h2", {}, `${f.machine} — ${title}`),
      ...body(f))];
  });
}

// agents is everybody in a fleet except the operator.
//
// The operator is a person, not an agent: it is never employed, runs no session,
// holds no standing instructions and works in whatever directory it likes. So
// every list *about agents* leaves it out — a row that can never be acted on is a
// row somebody reads once and then learns to skip past.
//
// It is not hidden. The card above each list names it, because who the operator
// is is worth knowing; it is just not one of the things being listed.
export function agents(f) {
  return (f.identities || []).filter((id) => !id.operator);
}

// running is `manage › fleet`: what is employed, and what to do about it.
//
// Everybody is listed, not only the employed — an agent that is *not* working is
// as often the thing somebody came to fix. The idle ones are dimmed rather than
// hidden, so the list reads as a fleet with three of eight busy instead of as a
// list of three.
export function running(state, actions) {
  return perMachine(state, "fleet", (f) => {
    const list = agents(f);
    const employed = list.filter((id) => id.employed).length;
    return [
      h("div", { class: "meta" },
        `operator ${f.operator} · ${employed} of ${plural(list.length, "agent")} employed`),
      h("div", { class: "body" },
        ...(f.problems || []).map((p) => h("p", { class: "warn" }, p)),
        actions ? h("div", { class: "controls" },
          h("button", { class: "quiet", onclick: () => actions.tend(f) }, "tend"),
          h("span", { class: "muted" }, "start what should be running and stop what should not"),
        ) : null,
        ...work(f, actions)),
    ];
  });
}

// work is one row per identity, ordered so the busy ones come first.
function work(f, actions) {
  const list = agents(f);
  if (list.length === 0) return [h("p", { class: "muted" }, "nobody yet")];

  const order = [...list].sort((a, b) => Number(Boolean(b.employed)) - Number(Boolean(a.employed)));
  return order.map((id) => h("div", { class: id.employed ? "agent" : "agent idle" },
    h("div", { class: "agent-head" },
      h("span", { class: "name" }, h("span", { class: id.operator ? "ok" : "" }, id.name)),
      h("span", { class: "muted" }, id.role || "no role"),
      sessionState(id),
      h("span", { class: "muted" },
        id.employed ? `${id.model || "?"}/${id.effort || "?"} · load ${id.load}` : ""),
      h("span", { class: "muted" },
        id.has_spawn_budget ? `may employ ${id.spawn_budget}` : "no budget"),
    ),
    actions ? h("div", { class: "controls row" },
      id.employed
        ? h("button", { class: "quiet", onclick: () => actions.fire(f, id) }, "fire")
        : h("button", { class: "quiet", onclick: () => actions.employ(f, id) }, "employ…"),
      h("a", { class: "quiet button", href: sessionAt(f.machine, id.name) }, "attach"),
      id.populated ? h("button", { class: "quiet", onclick: () => actions.poke(f, id) }, "poke…") : null,
      id.populated ? h("button", { class: "quiet", onclick: () => actions.refreshAgent(f, id) }, "refresh") : null,
    ) : null,
  ));
}

// tree is `admin › identities`: who exists and under whom.
export function tree(state, actions) {
  return perMachine(state, "identities", (f) => [
    h("div", { class: "meta" },
      `operator ${f.operator} · ${plural(agents(f).length, "agent")}`),
    h("div", { class: "body" },
      ...(f.problems || []).map((p) => h("p", { class: "warn" }, p)),
      actions ? h("div", { class: "controls" },
        h("button", { onclick: () => actions.newIdentity(f) }, "hire…")) : null,
      ...identities(f, state, actions)),
  ]);
}

export function roleList(state, actions) {
  return perMachine(state, "roles", (f) => [
    h("div", { class: "meta" }, `${(f.roles || []).length} roles`),
    h("div", { class: "body" },
      actions ? h("div", { class: "controls" },
        h("button", { onclick: () => actions.newRole(f) }, "new role…")) : null,
      ...roles(f, actions)),
  ]);
}

export function permissionList(state, actions) {
  return perMachine(state, "permissions", (f) => [
    h("div", { class: "meta" }, `${(f.permissions || []).length} permissions`),
    h("div", { class: "body" },
      actions ? h("div", { class: "controls" },
        h("button", { onclick: () => actions.newPermission(f) }, "new permission…")) : null,
      ...missingToolkit(f, actions),
      ...permissions(f, actions)),
  ]);
}

// missingToolkit is the permissions every fleet is made with that this one has not
// got.
//
// It is here because their absence is otherwise invisible. `orc bootstrap` installs
// the toolkit and is safe to run again, so a fleet made before one of them existed
// simply does not have it — and the only symptom is a list missing rows nobody knew
// to expect. A screen that shows what exists can never show that.
//
// Each is named with what it would be, because "you are missing eleven permissions"
// is not something anybody can act on, and because the decision to install them is
// one somebody should make having read what they are.
function missingToolkit(f, actions) {
  const absent = (f.toolkit || []).filter((t) => !t.have);
  if (absent.length === 0) return [];

  return [
    h("div", { class: "agent pending" },
      h("div", { class: "agent-head" },
        h("strong", {}, `${absent.length} of the toolkit ${absent.length === 1 ? "is" : "are"} not in this fleet`),
        h("span", { class: "muted" }, "every fleet is made with these"),
      ),
      h("p", { class: "muted" },
        "a fleet made before one of them existed simply does not have it; installing them adds what is missing and changes nothing else"),
      ...absent.map((t) => h("div", { class: "agent" },
        h("div", { class: "agent-head" },
          h("span", { class: "name" }, t.name),
          h("span", { class: "muted" }, `floor ${t.floor}`),
          h("span", { class: "muted" }, t.why || ""),
        ),
        h("div", { class: "clauses" },
          ...(t.patterns || []).map((c) => clause.chip(c, f.vocabulary))),
      )),
      actions ? h("div", { class: "controls" },
        h("button", { onclick: () => actions.installToolkit(f, absent) },
          `install the ${absent.length === 1 ? "missing one" : "missing ones"}`)) : null,
    ),
  ];
}

// identities are drawn in tree order with an indent, because who works for whom
// is the whole of what an Orc fleet is — a flat list of the same names says
// nothing about why one of them may not do something.
function identities(f, state, actions) {
  const list = agents(f);
  if (list.length === 0) return [h("p", { class: "muted" }, "nobody yet")];

  // Depth is rebased past the operator. Everybody reports to it, so leaving the
  // chain as Orc counts it would indent the whole fleet one step under a row that
  // is not on screen — a tree hanging from nothing.
  const depth = (id) => (id.chain ? Math.max(id.chain.length - 2, 0) : 0);
  return list.map((id) => h("div", { class: "agent" },
    h("div", { class: "agent-head" },
      h("span", { class: "name" },
        "  ".repeat(depth(id)),
        h("span", { class: id.operator ? "ok" : "" }, id.name)),
      level(id),
      h("span", { class: "muted" }, id.role || "no role"),
      h("span", { class: "muted" },
        id.has_spawn_budget ? `may employ ${id.spawn_budget}` : "no budget"),
    ),
    clauses(id, f.vocabulary),
    grants(id, f, actions),
    // What it has been doing, when there is a live session to say. Folded, so the
    // tree stays a tree.
    session(f, id, state, actions),
    // Structural only. Employing and firing live in `manage › fleet`, because
    // starting an agent is a thing you do to a running fleet, and giving it a
    // role is a thing you do to the shape of one.
    actions ? h("div", { class: "controls row" },
      h("button", { class: "quiet", onclick: () => actions.assignRole(f, id) }, "role…"),
      id.operator ? null : h("button", { class: "quiet", onclick: () => actions.moveIdentity(f, id) }, "move…"),
      h("button", { class: "quiet", onclick: () => actions.grant(f, id) }, "grant…"),
      id.operator ? null : h("button", { class: "quiet danger", onclick: () => actions.removeIdentity(f, id) }, "remove"),
    ) : null,
  ));
}

// The clauses an identity actually has, which is the answer to "why can it not
// edit that file". A capped one shows what it asked for beside what it got.
function clauses(id, words) {
  const list = id.clauses || [];
  if (list.length === 0) {
    return h("div", { class: "clauses" }, h("span", { class: "muted" }, "no clauses"));
  }
  // Coloured the same way as the ones in the permission editor, so the clause an
  // agent turned out to have looks like the clause somebody wrote.
  return h("div", { class: "clauses" }, ...list.map((c) => h("span", { class: "clause" },
    ...clause.highlight(`${c.kind}(${c.arg})`, words),
    c.capped ? h("span", { class: "pending" }, ` ‡ asked ${c.asked}`) : null,
    c.source === "grant" ? h("span", { class: "muted" }, ` · granted${c.lapses ? `, ${c.lapses}` : ""}`) : null,
  )));
}

// Grants are shown live *and* lapsed: "I granted that and it stopped working" is
// a question this should answer, and a row that has vanished answers nothing.
function grants(id, f, actions) {
  const list = id.grants || [];
  if (list.length === 0) return null;
  return h("div", { class: "grants" },
    ...list.map((g) => h("div", { class: "grant" },
      h("span", { class: g.live ? "ok" : "muted" }, g.live ? "live" : "lapsed"),
      h("span", {}, g.permission),
      h("span", { class: "muted" },
        g.until ? remaining(g.until) : (g.session ? "this session" : "")),
      actions && g.live
        ? h("button", { class: "quiet", onclick: () => actions.revoke(f, id, g.permission) }, "revoke")
        : null,
    )));
}

// holders names the agents in a role, and not the operator.
//
// It would only ever be there if somebody assigned the operator a role, which is
// legal and says nothing — its authority is 100 whatever role it holds. Listing
// it among the agents doing a job would be the one place this rule leaked.
function holders(f, r) {
  const who = (r.held_by || []).filter((name) => name !== f.operator);
  return who.length ? `held by ${who.join(", ")}` : "held by nobody";
}

function roles(f, actions) {
  const list = f.roles || [];
  if (list.length === 0) return [h("p", { class: "muted" }, "no roles yet")];

  return list.map((r) => h("div", { class: "agent" },
    h("div", { class: "agent-head" },
      h("span", { class: "name" }, r.name),
      h("span", {}, String(r.authority)),
      h("span", { class: "muted" }, r.description || ""),
      h("span", { class: "muted" }, budgetLabel(roleBudget(f, r))),
      h("span", { class: "muted" }, holders(f, r)),
    ),
    (r.permissions || []).length
      ? h("div", { class: "clauses" }, ...r.permissions.map((p) => h("span", { class: "clause" },
          p,
          actions
            ? h("button", {
                class: "quiet",
                title: `take ${p} off ${r.name}`,
                onclick: () => actions.takePermissionFrom(f, r, p),
              }, "×")
            : null)))
      : h("div", { class: "clauses" }, h("span", { class: "muted" }, "no permissions")),
    actions ? h("div", { class: "controls" },
      h("button", { class: "quiet", onclick: () => actions.setAuthority(f, r) }, "authority…"),
      h("button", { class: "quiet", onclick: () => actions.addPermission(f, r) }, "add permission…"),
      h("button", { class: "quiet", onclick: () => actions.setBudget(f, r) }, "budget…"),
      h("button", { class: "quiet danger", onclick: () => actions.removeRole(f, r) }, "delete"),
    ) : null,
  ));
}

function permissions(f, actions) {
  const list = f.permissions || [];
  if (list.length === 0) return [h("p", { class: "muted" }, "no permissions yet")];

  // Which roles hold each one, worked out here rather than asked for: the fleet
  // already carries both halves, and a permission nothing holds is the thing
  // worth noticing.
  const holders = new Map();
  for (const r of f.roles || []) {
    for (const p of r.permissions || []) {
      holders.set(p, [...(holders.get(p) || []), r.name]);
    }
  }

  // Which names belong to the toolkit. A row is marked by name alone, because that
  // is what orc treats as "the fleet has this one" — it never rewrites a permission,
  // so a fleet that redefined `upgrade` keeps its own clauses under the same name,
  // and the clauses beside the mark are the fleet's real ones either way.
  const builtin = new Set((f.toolkit || []).map((t) => t.name));

  return list.map((p) => {
    const held = holders.get(p.name) || [];
    return h("div", { class: "agent" },
      h("div", { class: "agent-head" },
        h("span", { class: "name" }, p.name),
        h("span", { class: "muted" }, builtin.has(p.name) ? "toolkit" : "yours"),
        h("span", { class: "muted" }, `floor ${p.floor}`),
        h("span", { class: held.length ? "muted" : "pending" },
          held.length ? `held by ${held.join(", ")}` : "held by nothing"),
      ),
      h("div", { class: "clauses" },
        ...(p.patterns || []).map((c) => clause.chip(c, f.vocabulary))),
      actions ? h("div", { class: "controls" },
        h("button", { class: "quiet", onclick: () => actions.editPermission(f, p) }, "edit…"),
        h("button", { class: "quiet danger", onclick: () => actions.removePermission(f, p) }, "delete"),
      ) : null,
    );
  });
}

// --- what an agent has been doing ------------------------------------------
//
// `orc view` for each live agent, carried by the snapshot. It is here rather than
// behind a button because there is nowhere to fetch it from: the server can never
// reach an agent machine, so this is as live as the last sync and no fresher. The
// panel says so rather than implying otherwise — a pane that looked live and was
// twenty minutes old would be worse than one that admits its age.

// sessionOf finds the carried session for an identity.
export function sessionOf(f, name) {
  return (f.sessions || []).find((s) => s.identity === name) || null;
}

// session is the pane under an identity: what it said, then what it did.
//
// Folded shut by default. A fleet of eight agents each showing a dozen rows is a
// page nobody can find an identity in, and the question this panel usually answers
// — who exists, under whom — is not the one the pane answers.
export function session(f, id, state, actions) {
  const got = sessionOf(f, id.name);
  if (!got) return null;

  const key = `session:${f.machine || ""}:${id.name}`;
  const open = state && state.open ? Boolean(state.open[key]) : false;

  const summary = h("button", {
    class: open ? "fold picked" : "fold",
    "aria-expanded": open ? "true" : "false",
    onclick: () => actions && actions.toggle(key),
  },
    h("span", { class: "twist" }, open ? "▾" : "▸"),
    h("span", { class: "sect" }, "session"),
    h("span", { class: "muted note" }, sessionSummary(got)));

  // The fold stays: it is the glance, and most of the time a glance is what a
  // list is for. The link is what it grew a floor under — somewhere to read the
  // whole thing and answer it without losing the words to a dialog.
  //
  // "attach", because that is the word orc uses for joining a session and this is
  // the same act through a different window. It is not the same thing as
  // `orc attach` and does not pretend to be — what it opens says so in its own
  // first line — but a fleet with two names for joining an agent would be a fleet
  // where somebody has to learn which is which.
  const whole = h("p", { class: "muted" },
    h("a", { href: sessionAt(f.machine || "", id.name) }, "attach to this session"));

  if (!open) return summary;
  // `session` as well as `inner`, because this fold holds prose and rows rather
  // than more tree. A library fold's inner is a container for deeper folds, which
  // carry their own indent and must not be pushed twice; this one is the bottom of
  // its tree and its contents were sitting flat against the rule that marks it.
  return h("div", { class: "folded" }, summary,
    h("div", { class: "inner session" }, ...sessionBody(got), whole));
}

// sessionSummary is the one line worth reading without opening anything: what it
// is doing, and how much there is to look at.
export function sessionSummary(s) {
  const bits = [];
  if (!s.live) bits.push("no session");
  else bits.push(s.waiting ? "waiting" : "working");
  if (s.turn) bits.push(`turn ${s.turn}`);
  if (s.rows && s.rows.length) bits.push(plural(s.rows.length, "event"));
  // A refusal is the thing somebody scanning this column is looking for, so it is
  // on the closed row rather than inside the fold.
  const blocked = (s.rows || []).filter((r) => r.blocked).length;
  if (blocked > 0) bits.push(`${blocked} blocked`);
  return bits.join(" · ");
}

export function sessionBody(s) {
  const out = [];
  if (s.note) out.push(h("p", { class: "warn" }, s.note));

  if (s.prose && s.prose.length > 0) {
    out.push(h("p", { class: "muted" }, "said"));
    out.push(h("div", { class: "said" },
      ...s.prose.map((p) => h("p", { class: p.who === "assistant" ? "from-agent" : "from-human" },
        h("span", { class: "muted" }, p.who === "assistant" ? "» " : "· "), p.text))));
  } else if (!s.prose_available && s.live) {
    // Told apart from "said nothing": one is an agent that has not spoken, the
    // other is a transcript that could not be read, and they send somebody to
    // different places.
    out.push(h("p", { class: "muted" }, "no transcript to read — `orc attach --direct` shows the session itself"));
  }

  if (s.rows && s.rows.length > 0) {
    out.push(h("p", { class: "muted" }, "did"));
    out.push(h("div", { class: "rows" }, ...s.rows.map(sessionRow)));
  }
  if (out.length === 0) out.push(h("p", { class: "muted" }, "nothing recorded yet"));
  return out;
}

export function sessionRow(r) {
  return h("div", { class: r.blocked ? "event blocked" : "event" },
    h("span", { class: "when" }, (r.at || "").slice(11, 19)),
    // The verdict as a word, never only as a colour: this is the column that says
    // an agent was refused, and a reader who cannot tell red from green would
    // otherwise see an ordinary row.
    h("span", { class: "verdict" }, r.blocked ? "blocked" : (r.verdict || "")),
    h("span", { class: "tool" }, r.tool || r.kind || ""),
    h("span", { class: "detail" }, r.detail || ""),
    // On its own line under what it explains: "blocked" without the reason sends
    // the reader to the permissions table to find out what they already needed.
    r.reason ? h("span", { class: "muted why" }, r.reason) : null);
}
