// Whether the button can be pressed, above the button.
//
// `tooling › rebuild` is the one control in cq that takes the site down, and every
// way it fails is knowable beforehand — a detached head, a branch with no upstream,
// a checkout that has diverged, no toolchain on the supervisor's PATH. None of that
// was reachable from a browser, so the only way to find out was to press it and read
// the wreckage ten minutes later.
//
// The server decides the verdict (see upgrade/status.go). Nothing here re-derives it
// from the numbers: a browser deciding that four uncommitted files means "probably
// not" would be a second opinion about whether a build will work, and the two would
// disagree the first time either learned a new condition.

import { h } from "./dom.js";

// The three lights, and what each is for. A word beside every one of them, always:
// this whole panel is a colour telling somebody whether to press a destructive
// button, and a reader who cannot tell red from green needs the same answer.
const LIGHTS = {
  go: { dot: "●", word: "clear to rebuild", look: "go" },
  caution: { dot: "◐", word: "you probably should not", look: "caution" },
  stop: { dot: "○", word: "cannot rebuild", look: "stop" },
};

export function checkout(got) {
  if (!got) return [];
  if (got.unreachable) {
    // The route would not answer. Said rather than left blank: an absent panel over
    // a button that takes the site down reads as "nothing to report", which is the
    // one thing it does not mean.
    return [h("div", { class: "tree-state caution" },
      h("span", { class: "dot" }, "◌"),
      h("span", { class: "word" }, "cannot tell"),
      h("span", { class: "where muted" }, got.unreachable))];
  }

  // A check that was cut short is not a judgement. `caution` reads as "you
  // probably should not", which is a claim about the checkout — and the panel was
  // making it because a probe ran out of time, over a build that then worked.
  // Saying so is the difference between advice and a guess wearing its clothes.
  const light = (got.unknown && got.verdict === "caution")
    ? { dot: "◌", word: "cannot tell for certain", look: "caution" }
    : LIGHTS[got.verdict] || {
    // A verdict from a newer server reads as unfamiliar rather than as absent. The
    // one thing that must not happen is an unknown word drawing as green.
      dot: "◌", word: got.verdict || "unknown", look: "caution",
    };

  return [
    h("div", { class: `tree-state ${light.look}` },
      h("span", { class: "dot" }, light.dot),
      h("span", { class: "word" }, light.word),
      h("span", { class: "where muted" }, place(got))),
    ...reasons(got).map((r) => h("div", { class: `tree-why ${level(r)}` },
      h("span", { class: "text" }, r.text),
      // The fix beside the reason, because a screen that says what is wrong and
      // not what to do about it sends somebody to a terminal to work out a command
      // this already knows.
      r.fix ? h("span", { class: "fix muted" }, r.fix) : null)),
    // Whose checkout this is. The button also queues every agent machine and the
    // server cannot reach one to ask — that is the architecture — so a green light
    // here must not be read as a green light for the fleet.
    h("p", { class: "muted hint" },
      "this is the machine serving the site; each agent machine checks its own when " +
      "the queued rebuild reaches it. ahead and behind are as of its last fetch."),
  ];
}

// reasons is the list, whatever arrived in its place.
//
// A field that should be an array and is not would throw on `.map` and take the tab
// down — over a panel whose entire job is to be the calm thing on a page about a
// destructive button.
function reasons(got) {
  return Array.isArray(got.reasons) ? got.reasons.filter((r) => r && r.text) : [];
}

// level is a reason's level, bounded to the three the sheet styles.
//
// It goes into a class list, so an unbounded string is a reason that arrives styled
// as whatever it happens to spell — and a level this build has never heard of
// picking up no colour at all is worse than it reading as the caution it is.
function level(r) {
  return LIGHTS[r.level] ? r.level : "caution";
}

// place is the one line of git that is worth reading whatever the verdict: where the
// checkout is, and how far it is from where it is going.
function place(got) {
  const bits = [];
  if (got.detached) bits.push("not on a branch");
  else if (got.branch) bits.push(got.upstream ? `${got.branch} → ${got.upstream}` : got.branch);
  if (got.head) bits.push(`at ${got.head}`);
  if (got.behind) bits.push(`${got.behind} behind`);
  if (got.ahead) bits.push(`${got.ahead} ahead`);
  if (got.dirty) bits.push(`${got.dirty} changed`);
  return bits.join(" · ");
}
