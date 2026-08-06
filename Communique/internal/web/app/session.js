// One agent's session, on a screen of its own.
//
// Everything here already crossed the wire before this file existed: the fleet
// snapshot carries what each live agent has said and done, `orc.poke` has been a
// queued action from the start, and refresh and fire are ordinary fleet calls.
// What was missing was a place where they are all the same thing. Reading a
// session meant opening a fold in a list, and answering it meant a modal that
// closed over the words being answered.
//
// **This is not a terminal, and it must not pretend to be one.** The server can
// never reach an agent machine — the whole of cq is built on that — so what is on
// screen is as old as the last sync, and what is typed here leaves on the next
// one. Both numbers are on the page. A pane that looked live while showing a
// five-minute-old transcript would be worse than the fold it replaces, because
// the fold at least sat under a staleness clock nobody could miss.
//
// The route carries the machine as well as the name. Identities are unique within
// a fleet and not across them, and the fold this grew out of already keyed on both
// — two machines with an `ember` each would otherwise share one screen.

import { h, since } from "./dom.js";
import { sessionBody, sessionSummary, sessionOf, agents } from "./fleet.js";
import { draftKey, drafted } from "./views.js";

// where splits `<machine>/<identity>` out of the route.
//
// Both halves are decoded, and a route missing either is nothing rather than a
// half-answer: a screen built from an empty machine name would find the first
// fleet and show somebody else's agent.
export function where(rest) {
  const cut = rest.indexOf("/");
  if (cut <= 0 || cut === rest.length - 1) return null;
  return {
    machine: decodeURIComponent(rest.slice(0, cut)),
    name: decodeURIComponent(rest.slice(cut + 1)),
  };
}

// screen is the whole route.
export function screen(state, rest, actions, now = Date.now()) {
  const at = where(rest || "");
  if (!at) return [h("p", { class: "muted" }, "no session named")];

  const back = h("p", {}, h("a", { href: "#/manage/fleet", class: "muted" }, "← fleet"));

  const f = (state.fleet || []).find((x) => x.machine === at.machine);
  if (!f) {
    return [back, h("p", { class: "warn" },
      `no machine called ${at.machine} carries a fleet — it may have stopped syncing`)];
  }
  if (f.unreachable) return [back, h("p", { class: "warn" }, f.unreachable)];

  const id = agents(f).find((x) => x.name === at.name);
  if (!id) {
    return [back, h("p", { class: "warn" },
      `${at.machine} has no agent called ${at.name}`)];
  }

  return [
    back,
    head(state, f, id, now),
    transcript(f, id),
    ...(actions ? [compose(state, f, id, actions), controls(f, id, actions)] : []),
    ...pending(state, f, id),
  ];
}

// head is who this is and how old the answer is.
//
// The staleness line is part of the card rather than left to the status bar at the
// foot of the page. On a phone the bar is off the bottom of a long transcript, and
// this is the one screen where the age of the mirror changes what the words mean:
// "waiting to be spoken to" four minutes ago is not the same claim as now.
function head(state, f, id, now) {
  const got = sessionOf(f, id.name);
  const machine = (state.machines || []).find((m) => m.machine === f.machine);

  const facts = [
    id.role || "no role",
    id.employed ? `${id.model || "?"}/${id.effort || "?"}` : "not employed",
  ];
  if (got && got.turn) facts.push(`turn ${got.turn}`);
  if (id.workspace) facts.push(id.workspace);

  return h("article", { class: "card" },
    h("h2", {}, id.name, " ", h("span", { class: "muted" }, f.machine)),
    h("div", { class: "meta" }, facts.join(" · ")),
    h("div", { class: "body" },
      h("p", {}, got ? sessionSummary(got) : "no session — nothing to watch until it is employed"),
      staleness(got, machine, now)));
}

// staleness says how old this is.
//
// **Two clocks, because there are two facts.** While this pane is open the machine
// sends *this session* every few seconds and mirrors everything else at its
// ordinary pace, so the transcript and the mailbox are genuinely different ages.
// One number would have to lie about one of them: the session's age claimed for
// the whole mirror, or the mirror's age claimed for a transcript that is seconds
// old. The status bar keeps saying how old the mirror is; this says how old the
// conversation is.
//
// An agent from before narrow rounds existed carries no session timestamp, which
// reads as "the mirror's age is all there is to go on" — the honest answer, and
// the one that was true for everybody until now.
function staleness(got, machine, now) {
  if (got && got.at) {
    const age = now - new Date(got.at).getTime();
    // Beyond about ten rounds, the thing that is supposed to be refreshing this
    // is not. A machine that stopped syncing, a lease another operator took, a
    // watcher that died — the reader cannot tell which and does not need to, but
    // they must not be told it is live while they read a transcript that has
    // stopped. Saying "and refreshes while you are reading it" over a five-minute
    // -old pane is the one lie this screen exists to not tell.
    if (age > 30 * 1000) {
      return h("p", { class: "warn" },
        `this conversation is ${since(got.at, now)} old and has stopped refreshing. `,
        "the machine may not be syncing, or somebody else may have opened another session on it.");
    }
    const cls = age > 15 * 1000 ? "muted stale" : "muted";
    return h("p", { class: cls },
      `this conversation is ${since(got.at, now)} old, and refreshes while you are reading it. `,
      "what you send still leaves on the next sync, so an answer takes a few seconds to come back.");
  }
  if (!machine || !machine.last_sync) {
    return h("p", { class: "warn" }, "this machine has never synced, so nothing here is current");
  }
  const age = now - new Date(machine.last_sync).getTime();
  const cls = age > 10 * 60 * 1000 ? "warn" : age > 2 * 60 * 1000 ? "muted stale" : "muted";
  return h("p", { class: cls },
    `mirrored ${since(machine.last_sync, now)} — this is a mirror, not a terminal, and what you send leaves on the next sync`);
}

// transcript is what it said and what it did, in the shape the fold already used.
//
// Unfolded, because opening a fold is what somebody came here to stop doing.
function transcript(f, id) {
  const got = sessionOf(f, id.name);
  return h("article", { class: "card" },
    h("h2", {}, "session"),
    h("div", { class: "body session" },
      ...(got
        ? sessionBody(got)
        : [h("p", { class: "muted" },
            id.employed
              ? "employed, but nothing is running — `tend` starts what should be"
              : "not employed, so there is no session to read")])));
}

// compose is a box rather than a modal.
//
// A modal was right for `poke…` from a list, where the message is the whole
// interaction. It is wrong here: the reason to answer an agent on this screen is
// that its words are on it, and a dialog covering them means writing a reply to a
// transcript from memory.
//
// Drafted through the same store as every other form, so a sync landing mid-word
// does not take the word — see stash in app.js.
function compose(state, f, id, actions) {
  const key = draftKey("poke", `${f.machine}:${id.name}`);
  const draft = drafted(state, key);

  const body = h("textarea", {
    name: "poke", placeholder: "…",
    oninput: (e) => actions.draft(key, "poke", e.target.value),
  });
  body.value = draft.poke ?? "";

  const button = h("button", { type: "submit" }, "queue it");
  const problem = h("p", { class: "error" });

  const form = h("form", {
    class: "compose",
    onsubmit: (e) => {
      e.preventDefault();
      const text = body.value.trim();
      problem.textContent = text ? "" : "nothing to send";
      if (!text) return;

      button.disabled = true;
      actions.sendPoke(f.machine, id.name, text)
        .then((ok) => {
          // Cleared only when it queued. A poke that failed to leave is still
          // what somebody wrote, and the queue below is about to say why.
          if (!ok) return;
          body.value = "";
          actions.forget(key);
          problem.textContent = "";
        })
        .finally(() => { button.disabled = false; });
    },
  }, h("label", { for: "poke" }, "say something"), body, problem, button);

  return h("article", { class: "card" },
    h("h2", {}, "say something"),
    h("div", { class: "meta" },
      id.populated
        ? "typed into its session, exactly as if you had attached to it"
        : "nothing is running to type into — this will not be sent"),
    h("div", { class: "body" }, form));
}

// controls are the verbs that change what is running.
//
// Deliberately few. Everything else about an agent — its role, its permissions,
// where it works, what it is paid to think with — belongs to the screens that own
// those, and duplicating them here would make two places to change one fact.
// What is here is what somebody watching a session reaches for: start it, stop it,
// or give it a clean context.
function controls(f, id, actions) {
  return h("article", { class: "card" },
    h("h2", {}, "the session itself"),
    h("div", { class: "body" },
      h("div", { class: "controls row" },
        id.employed
          ? h("button", { class: "quiet", onclick: () => actions.fire(f, id) }, "fire")
          : h("button", { class: "quiet", onclick: () => actions.employ(f, id) }, "employ…"),
        id.populated
          ? h("button", { class: "quiet", onclick: () => actions.refreshAgent(f, id) }, "refresh")
          : null),
      h("p", { class: "muted" },
        "refresh gives it a new session with a fresh context. it keeps its memories, mailbox, tasks and workspace — the conversation is what goes.")));
}

// pending is what has been said to this agent and has not landed yet.
//
// The round trip is two syncs — one to carry the poke out, one to carry back what
// it did — so between pressing the button and seeing an answer there is a gap with
// nothing in it. This is what goes in the gap. Without it the only evidence a
// message was ever sent is that the box emptied.
function pending(state, f, id) {
  const mine = (state.queue || []).filter((e) =>
    e.action && e.action.machine === f.machine &&
    e.action.args && e.action.args.identity === id.name &&
    (e.state === "queued" || e.state === "sent" || e.state === "failed"));
  if (mine.length === 0) return [];

  return [h("article", { class: "card" },
    h("h2", {}, "waiting"),
    h("div", { class: "body" },
      ...mine.map((e) => h("div", { class: e.state === "failed" ? "failed" : "pending" },
        `${e.action.op} — ${e.state}`,
        e.action.args.message ? h("span", { class: "muted" }, ` “${e.action.args.message}”`) : null,
        e.error ? h("span", { class: "muted" }, ` ${e.error}`) : null))))];
}
