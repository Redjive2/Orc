import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const { h } = await import("../dom.js");

// The element builder, and one rule about styling that nothing here can see for
// itself.
//
// The site is served with `default-src 'self'` and no `unsafe-inline`, which is
// asserted by a Go test and is the policy the pages are written to satisfy rather
// than the other way round. The consequence is that a `style` **attribute** is
// discarded by the browser without a word: no console error worth the name, no
// exception, no failing test — the markup simply arrives without it.
//
// That is how the activity charts spent two rounds of fixes being invisible. Every
// bar carried `style="height:80%;background:…"`, every unit test passed because a
// stub DOM has no content policy, and every bar rendered at no height at all. The
// second attempt changed the *shape* of the style, which was the wrong half.
//
// So `h` applies styles through the CSSOM, which `style-src` does not govern, and it
// takes an object so a string cannot quietly get the attribute back.

test("a style is applied to the element, not written as an attribute", () => {
  const el = h("span", { style: { height: "40%" } });
  assert.equal(el.style.height, "40%");
  // The attribute is the thing a content policy throws away, so its absence is the
  // property worth asserting rather than the presence of the other.
  assert.equal(el.getAttribute("style"), null);
});

// A custom property is not a member of the style object and has to be set by name.
// `el.style["--fill"] = x` assigns a property nobody reads: it does not throw, and
// no CSS ever sees the value.
test("a custom property is set by name", () => {
  const el = h("span", { style: { "--fill": "62%", background: "red" } });
  assert.equal(el.style["--fill"], "62%");
  assert.equal(el.style.background, "red");
});

// Empty rather than absent has a meaning here — a bar with nothing to say takes its
// look from the sheet — and it must not become an attribute either.
test("nothing to style leaves the element alone", () => {
  for (const style of [null, undefined, false]) {
    const el = h("span", { style });
    assert.equal(el.getAttribute("style"), null);
  }
});

test("the other attributes still go on as attributes", () => {
  const el = h("a", { href: "/x", title: "t", class: "row" });
  assert.equal(el.getAttribute("href"), "/x");
  assert.equal(el.getAttribute("title"), "t");
  assert.equal(el.className, "row");
});
