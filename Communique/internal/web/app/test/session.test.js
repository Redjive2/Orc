import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const view = await import("../session.js");
const routes = await import("../routes.js");

// The session screen is the first thing in cq that reads like attaching to an
// agent, and the whole risk in it is that it reads like attaching *too* well.
// What is pinned here is the honesty: it says how old it is, it says what happens
// to what you type, and it never draws a blank pane where a real state belongs.
//
// The rest is address arithmetic — a machine and a name in a hash — which is worth
// tests because three files have to agree on the shape and only one of them takes
// it apart again.

function all(node, ok, out = []) {
  if (ok(node)) out.push(node);
  for (const child of node.childNodes || []) all(child, ok, out);
  return out;
}

function text(nodes) {
  return nodes.map((n) => n.textContent).join("\n");
}

// The submit handler is registered with addEventListener rather than assigned, so
// it is reached the way the browser reaches it. Its work is a promise chain it does
// not return — the form has nobody to hand one to — so the test lets the loop turn
// once rather than reaching inside for it.
function submit(form) {
  form.dispatchEvent({ type: "submit", preventDefault() {} });
  return new Promise((r) => setTimeout(r, 0));
}

function drawn(state, rest, actions = null, now = Date.parse("2026-08-06T12:00:00Z")) {
  const out = view.screen(state, rest, actions, now);
  return text(out.flatMap((n) => all(n, () => true)));
}

const NOW = Date.parse("2026-08-06T12:00:00Z");

function state(patch = {}, id = {}, session = undefined) {
  return {
    machines: [{ machine: "sandy", last_sync: "2026-08-06T11:59:30Z" }],
    fleet: [{
      machine: "sandy",
      operator: "rdm",
      identities: [
        { name: "rdm", operator: true },
        {
          name: "ember", role: "builder", employed: true, populated: true,
          model: "opus", effort: "high", ...id,
        },
      ],
      sessions: session === undefined
        ? [{ identity: "ember", live: true, waiting: true, turn: 24, prose: [{ who: "assistant", text: "done with the parser" }] }]
        : session,
    }],
    ...patch,
  };
}

test("the address carries the machine as well as the name", () => {
  assert.equal(routes.sessionAt("sandy", "ember"), "#/session/sandy/ember");
  assert.deepEqual(view.where("sandy/ember"), { machine: "sandy", name: "ember" });
});

test("a name with a slash or a space survives the round trip", () => {
  // Names are checked elsewhere and this is not the place that enforces them —
  // but an address that mangles one silently would send somebody to a screen
  // about a different agent, which is worse than a refusal.
  const at = routes.sessionAt("box one", "a/b");
  assert.deepEqual(view.where(at.slice("#/session/".length)), { machine: "box one", name: "a/b" });
});

test("a half-address is nothing rather than a guess", () => {
  // Without this, an empty machine finds the first fleet and shows whoever is in
  // it — a screen that is confidently about the wrong agent.
  assert.equal(view.where("ember"), null);
  assert.equal(view.where("/ember"), null);
  assert.equal(view.where("sandy/"), null);
});

test("the screen is a detail route, so the router does not rewrite it", () => {
  assert.equal(routes.isDetail("/session/sandy/ember"), true);
});

test("it says how old the mirror is, and what that means", () => {
  const out = drawn(state(), "sandy/ember");
  assert.match(out, /mirrored/);
  assert.match(out, /mirror, not a terminal/);
  assert.match(out, /leaves on the next sync/);
});

test("a machine that has never synced says so instead of showing an age", () => {
  const out = drawn(state({ machines: [] }), "sandy/ember");
  assert.match(out, /never synced/);
  assert.doesNotMatch(out, /mirrored/);
});

test("what the agent said is on the screen without opening anything", () => {
  const out = drawn(state(), "sandy/ember");
  assert.match(out, /done with the parser/);
});

test("an employed agent with nothing running is told apart from an idle one", () => {
  // The two look identical in a list and want opposite things done about them:
  // one needs `tend`, the other needs employing.
  const stopped = drawn(state({}, { employed: true }, []), "sandy/ember");
  assert.match(stopped, /tend/);

  const idle = drawn(state({}, { employed: false, populated: false }, []), "sandy/ember");
  assert.match(idle, /not employed/);
  assert.doesNotMatch(idle, /tend/);
});

test("an agent this machine does not have is a refusal naming it", () => {
  const out = drawn(state(), "sandy/nobody");
  assert.match(out, /sandy has no agent called nobody/);
});

test("a machine that carries no fleet says why rather than drawing empty", () => {
  const out = drawn(state(), "elsewhere/ember");
  assert.match(out, /no machine called elsewhere/);
});

test("the operator is not one of the agents a session can be opened for", () => {
  // Consistent with every other list: the operator is a person, runs no session,
  // and a screen offering to poke one would be a screen about nothing.
  const out = drawn(state(), "sandy/rdm");
  assert.match(out, /has no agent called rdm/);
});

test("a poke that has not landed yet is shown as waiting", () => {
  const queued = {
    queue: [{
      state: "queued",
      action: { op: "orc.poke", machine: "sandy", args: { identity: "ember", message: "carry on" } },
    }],
  };
  const out = drawn(state(queued), "sandy/ember", {});
  assert.match(out, /orc\.poke — queued/);
  assert.match(out, /carry on/);
});

test("another agent's queued poke is not shown on this one's screen", () => {
  const queued = {
    queue: [{
      state: "queued",
      action: { op: "orc.poke", machine: "sandy", args: { identity: "atlas", message: "not yours" } },
    }],
  };
  assert.doesNotMatch(drawn(state(queued), "sandy/ember", {}), /not yours/);
});

test("the compose box keeps the words when the send fails", async () => {
  const out = view.screen(state(), "sandy/ember", {
    draft() {}, forget() { throw new Error("cleared a draft that never queued"); },
    async sendPoke() { return false; },
  }, NOW);

  const forms = out.flatMap((n) => all(n, (x) => x.tagName === "FORM"));
  assert.equal(forms.length, 1);
  const box = all(forms[0], (x) => x.tagName === "TEXTAREA")[0];
  box.value = "keep me";

  await submit(forms[0]);
  assert.equal(box.value, "keep me");
});

test("the compose box clears only once the poke is queued", async () => {
  let forgot = false;
  const out = view.screen(state(), "sandy/ember", {
    draft() {}, forget() { forgot = true; },
    async sendPoke() { return true; },
  }, NOW);

  const form = out.flatMap((n) => all(n, (x) => x.tagName === "FORM"))[0];
  const box = all(form, (x) => x.tagName === "TEXTAREA")[0];
  box.value = "carry on";

  await submit(form);
  assert.equal(box.value, "");
  assert.equal(forgot, true);
});

test("an empty box is refused without a round trip", async () => {
  let sent = false;
  const out = view.screen(state(), "sandy/ember", {
    draft() {}, forget() {},
    async sendPoke() { sent = true; return true; },
  }, NOW);

  const form = out.flatMap((n) => all(n, (x) => x.tagName === "FORM"))[0];
  const box = all(form, (x) => x.tagName === "TEXTAREA")[0];
  box.value = "   ";

  await submit(form);
  assert.equal(sent, false);
});

test("a reader with no controls still gets the transcript", () => {
  // `actions` is absent on a view somebody may only read. It must lose the form
  // and the buttons and keep everything that says what is happening.
  const out = drawn(state(), "sandy/ember", null);
  assert.match(out, /done with the parser/);
  assert.match(out, /mirrored/);
});
