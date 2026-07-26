// The fleet, in the admin panel.
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
function running(id) {
  if (id.populated) {
    const label = id.restarts ? `live · restarted ${id.restarts}×` : "live";
    return h("span", { class: "ok" }, label);
  }
  if (id.employed) return h("span", { class: "failed" }, "employed, not running");
  return h("span", { class: "muted" }, "—");
}

export function fleet(state, actions) {
  const fleets = state.fleet || [];
  if (state.fleetError) {
    return [h("p", { class: "warn" }, `the fleet could not be read: ${state.fleetError}`)];
  }
  if (fleets.length === 0) {
    return [h("p", { class: "muted" },
      "no machine mirrors an orc fleet — a machine that runs agents will carry one")];
  }
  return fleets.flatMap((f) => machineFleet(f, state, actions));
}

function machineFleet(f, state, actions) {
  if (f.unreachable) {
    return [h("article", { class: "card" },
      h("h2", {}, `${f.machine} — fleet`),
      h("div", { class: "body" }, h("p", { class: "warn" }, f.unreachable)))];
  }

  return [
    h("article", { class: "card" },
      h("h2", {}, `${f.machine} — fleet`),
      h("div", { class: "meta" },
        `operator ${f.operator}`,
        f.identities ? ` · ${f.identities.length} identities` : "",
        f.roles ? ` · ${f.roles.length} roles` : "",
        f.permissions ? ` · ${f.permissions.length} permissions` : ""),
      h("div", { class: "body" },
        ...(f.problems || []).map((p) => h("p", { class: "warn" }, p)),
        actions ? h("div", { class: "controls" },
          h("button", { onclick: () => actions.newIdentity(f) }, "hire…"),
          h("button", { onclick: () => actions.newRole(f) }, "new role…"),
          h("button", { onclick: () => actions.newPermission(f) }, "new permission…"),
          h("button", { class: "quiet", onclick: () => actions.tend(f) }, "tend"),
        ) : null,
        h("h3", {}, "identities"),
        ...identities(f, state, actions),
        h("h3", {}, "roles"),
        ...roles(f, actions),
        h("h3", {}, "permissions"),
        ...permissions(f, actions),
      )),
  ];
}

// identities are drawn in tree order with an indent, because who works for whom
// is the whole of what an Orc fleet is — a flat list of the same names says
// nothing about why one of them may not do something.
function identities(f, state, actions) {
  const list = f.identities || [];
  if (list.length === 0) return [h("p", { class: "muted" }, "nobody yet")];

  const depth = (id) => (id.chain ? Math.max(id.chain.length - 1, 0) : 0);
  return list.map((id) => h("div", { class: "agent" },
    h("div", { class: "agent-head" },
      h("span", { class: "name" },
        "  ".repeat(depth(id)),
        h("span", { class: id.operator ? "ok" : "" }, id.name)),
      level(id),
      h("span", { class: "muted" }, id.role || "no role"),
      running(id),
      h("span", { class: "muted" },
        id.employed ? `${id.model || "?"}/${id.effort || "?"} · load ${id.load}` : ""),
      h("span", { class: "muted" },
        id.has_spawn_budget ? `may employ ${id.spawn_budget}` : "no budget"),
    ),
    clauses(id),
    grants(id, f, actions),
    actions ? h("div", { class: "controls" },
      h("button", { class: "quiet", onclick: () => actions.assignRole(f, id) }, "role…"),
      id.operator ? null : h("button", { class: "quiet", onclick: () => actions.moveIdentity(f, id) }, "move…"),
      h("button", { class: "quiet", onclick: () => actions.grant(f, id) }, "grant…"),
      id.employed
        ? h("button", { class: "quiet", onclick: () => actions.fire(f, id) }, "fire")
        : h("button", { class: "quiet", onclick: () => actions.employ(f, id) }, "employ…"),
      id.populated ? h("button", { class: "quiet", onclick: () => actions.poke(f, id) }, "poke…") : null,
      id.populated ? h("button", { class: "quiet", onclick: () => actions.refreshAgent(f, id) }, "refresh") : null,
      id.operator ? null : h("button", { class: "quiet danger", onclick: () => actions.removeIdentity(f, id) }, "remove"),
    ) : null,
  ));
}

// The clauses an identity actually has, which is the answer to "why can it not
// edit that file". A capped one shows what it asked for beside what it got.
function clauses(id) {
  const list = id.clauses || [];
  if (list.length === 0) {
    return h("div", { class: "clauses" }, h("span", { class: "muted" }, "no clauses"));
  }
  // Coloured the same way as the ones in the permission editor, so the clause an
  // agent turned out to have looks like the clause somebody wrote.
  return h("div", { class: "clauses" }, ...list.map((c) => h("span", { class: "clause" },
    ...clause.highlight(`${c.kind}(${c.arg})`),
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

function roles(f, actions) {
  const list = f.roles || [];
  if (list.length === 0) return [h("p", { class: "muted" }, "no roles yet")];

  return list.map((r) => h("div", { class: "agent" },
    h("div", { class: "agent-head" },
      h("span", { class: "name" }, r.name),
      h("span", {}, String(r.authority)),
      h("span", { class: "muted" }, r.description || ""),
      h("span", { class: "muted" }, budgetLabel(roleBudget(f, r))),
      h("span", { class: "muted" },
        r.held_by && r.held_by.length ? `held by ${r.held_by.join(", ")}` : "held by nobody"),
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

  return list.map((p) => {
    const held = holders.get(p.name) || [];
    return h("div", { class: "agent" },
      h("div", { class: "agent-head" },
        h("span", { class: "name" }, p.name),
        h("span", { class: "muted" }, `floor ${p.floor}`),
        h("span", { class: held.length ? "muted" : "pending" },
          held.length ? `held by ${held.join(", ")}` : "held by nothing"),
      ),
      h("div", { class: "clauses" },
        ...(p.patterns || []).map((c) => clause.chip(c))),
      actions ? h("div", { class: "controls" },
        h("button", { class: "quiet", onclick: () => actions.editPermission(f, p) }, "edit…"),
        h("button", { class: "quiet danger", onclick: () => actions.removePermission(f, p) }, "delete"),
      ) : null,
    );
  });
}
