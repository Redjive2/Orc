// Tests for the two pure modules. They run under `node --test`, with a minimal
// DOM: the point is the logic, and a real browser adds nothing a correctness
// test needs.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const { render } = await import("../markdown.js");
const { since, ellipsis, clock } = await import("../dom.js");

// text renders markdown and returns the resulting text content.
function text(md) {
  const frag = render(md);
  return frag.textContent;
}

// html renders markdown and returns a description of the tree, so a test can
// assert structure without an HTML serialiser.
function shape(md) {
  const frag = render(md);
  const walk = (node) =>
    node.nodeType === 3
      ? node.data
      : `${node.tagName.toLowerCase()}(${node.childNodes.map(walk).join("")})`;
  return frag.childNodes.map(walk).join("");
}

test("paragraphs", () => {
  assert.equal(shape("hello"), "p(hello)");
  assert.equal(shape("one\ntwo"), "p(one two)");
  assert.equal(shape("one\n\ntwo"), "p(one)p(two)");
});

test("emphasis and code", () => {
  assert.equal(shape("a **b** c"), "p(a strong(b) c)");
  assert.equal(shape("a *b* c"), "p(a em(b) c)");
  assert.equal(shape("a _b_ c"), "p(a em(b) c)");
  assert.equal(shape("a `b` c"), "p(a code(b) c)");
});

test("fenced code is literal", () => {
  assert.equal(shape("```\n**not bold**\n```"), "pre(code(**not bold**))");
  assert.equal(shape("```\n<script>x</script>\n```"), "pre(code(<script>x</script>))");
});

test("lists", () => {
  assert.equal(shape("- one\n- two"), "ul(li(one)li(two))");
  assert.equal(shape("* one\n+ two"), "ul(li(one)li(two))");
});

// The renderer builds nodes; it never parses a string. So a body containing a
// tag produces text containing a tag, and nothing else.
test("markup in a body stays text", () => {
  const md = "<script>alert(1)</script>";
  assert.equal(shape(md), "p(<script>alert(1)</script>)");
  assert.equal(text(md), md);
});

test("an image tag is not an element", () => {
  assert.equal(shape(`<img src=x onerror="alert(1)">`), `p(<img src=x onerror="alert(1)">)`);
});

test("links must be http or https", () => {
  assert.equal(shape("[a](https://example.com)"), "p(a(a))");
  assert.equal(shape("[a](http://example.com)"), "p(a(a))");
  assert.equal(shape("[a](javascript:alert(1))"), "p(a (javascript:alert(1)))");
  assert.equal(shape("[a](data:text/html,x)"), "p(a (data:text/html,x))");
  assert.equal(shape("[a](/relative)"), "p(a (/relative))");
});

test("links carry no-referrer", () => {
  const frag = render("[a](https://example.com)");
  const anchor = frag.childNodes[0].childNodes[0];
  assert.equal(anchor.getAttribute("rel"), "noreferrer noopener");
  assert.equal(anchor.getAttribute("href"), "https://example.com");
});

test("unterminated markers stay literal", () => {
  assert.equal(shape("a **b"), "p(a **b)");
  assert.equal(shape("a `b"), "p(a `b)");
  assert.equal(shape("[a](unclosed"), "p([a](unclosed)");
});

test("empty and odd input", () => {
  assert.equal(shape(""), "");
  assert.equal(shape(null), "");
  assert.equal(shape(undefined), "");
  assert.equal(shape("\n\n\n"), "");
});

// A pathological body must not take the page down with it.
test("adversarial input terminates quickly", () => {
  const started = Date.now();
  render("*".repeat(20000));
  render("`".repeat(20000));
  render("[".repeat(20000));
  render("- ".repeat(20000));
  assert.ok(Date.now() - started < 3000, "rendering took too long");
});

test("staleness reads at a glance", () => {
  const now = Date.parse("2026-07-24T18:31:04Z");
  const ago = (secs) => since(new Date(now - secs * 1000).toISOString(), now);
  assert.equal(ago(0), "0s ago");
  assert.equal(ago(12), "12s ago");
  assert.equal(ago(90), "2m ago");
  assert.equal(ago(3540), "59m ago");
  assert.equal(ago(3600), "1h ago");
  assert.equal(ago(7200), "2h ago");
  assert.equal(ago(60 * 60 * 72), "3d ago");
  assert.equal(since("not a date", now), "never");
});

test("ellipsis keeps columns honest", () => {
  assert.equal(ellipsis("abc", 5), "abc");
  assert.equal(ellipsis("abcdef", 5), "abcd…");
  assert.equal(ellipsis(null, 5), "");
});

test("a bad timestamp does not render as a real time", () => {
  assert.equal(clock("nonsense"), "--:--");
});

// --- the board's own pieces ---------------------------------------------

const { meter } = await import("../views.js");

test("the progress meter carries its own numbers", () => {
  // Colour and bar width can both be gone; the count still reads.
  assert.equal(meter(0, 8).textContent, "░░░░░░░ 0/8");
  assert.equal(meter(8, 8).textContent, "▓▓▓▓▓▓▓ 8/8");
  assert.equal(meter(4, 8).textContent, "▓▓▓▓░░░ 4/8");
});

test("a task with no subtasks shows a dash rather than an empty bar", () => {
  assert.equal(meter(0, 0).textContent, "—");
});

test("the meter never overflows its width", () => {
  for (const [done, total] of [[100, 8], [-1, 8], [3, 1]]) {
    const bars = meter(done, total).textContent.split(" ")[0];
    assert.ok(bars.length <= 7, `"${bars}" is ${bars.length} wide`);
  }
});
