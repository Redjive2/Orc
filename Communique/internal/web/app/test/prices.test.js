// What tokens cost, in money.
//
// The rates themselves are a fact about a vendor and will go stale; what is
// tested here is the arithmetic around them, and above all that an unpriced
// model is never silently free.

import test from "node:test";
import assert from "node:assert/strict";

const { cost, priced, money, PRICED } = await import("../prices.js");

test("a tier's rate is applied per million tokens", () => {
  // opus: $5/M in, $25/M out.
  assert.equal(cost({ input: 1e6, output: 0 }, "opus"), 5);
  assert.equal(cost({ input: 0, output: 1e6 }, "opus"), 25);
  assert.equal(cost({ input: 0, output: 2e5 }, "sonnet"), 3); // 0.2M × $15
});

// Cache tokens are priced off the input rate, not as input. They run orders of
// magnitude larger than everything else, so charging them at the input rate
// would make the total a reading of context size.
test("cache writes and reads are priced off input, not as input", () => {
  // 1M cache writes on opus: $5 × 1.25 = $6.25
  assert.equal(cost({ cache_create: 1e6 }, "opus"), 6.25);
  // 1M cache reads on opus: $5 × 0.1 = $0.50
  assert.equal(cost({ cache_read: 1e6 }, "opus"), 0.5);
  // And a realistic session is mostly cache reads — 10M of them costs less than
  // 1M of output, which is the whole reason long sessions are affordable.
  assert.ok(cost({ cache_read: 1e7 }, "opus") < cost({ output: 1e6 }, "opus"));
});

// The rule this module exists to enforce.
test("a model with no published rate is unpriced, not free", () => {
  assert.equal(cost({ input: 1e6, output: 1e6 }, "fable"), null);
  assert.equal(cost({ input: 1e6 }, ""), null);
  assert.equal(cost({ input: 1e6 }, undefined), null);
  assert.equal(priced("opus"), true);
  assert.equal(priced("fable"), false);
});

// Orc folds every model id to a tier before the reading is written, but the
// value still arrives as a string of whatever case.
test("the tier is matched without regard to case", () => {
  assert.equal(cost({ output: 1e6 }, "OPUS"), 25);
});

test("a missing counter is zero, not NaN", () => {
  // A bucket from an orc that predates a counter has the field absent. One
  // undefined must not poison the whole figure.
  assert.equal(cost({ output: 1e6 }, "opus"), 25);
  assert.ok(Number.isFinite(cost({}, "opus")));
});

// Sub-cent figures are real — one short haiku turn is a fraction of a cent — and
// rounding them all to $0.00 would draw a busy fleet as free.
test("money keeps small figures visible and large ones readable", () => {
  assert.equal(money(0), "$0");
  assert.equal(money(0.0004), "<$0.01");
  assert.equal(money(1.5), "$1.50");
  assert.equal(money(250), "$250");
  assert.equal(money(4200), "$4.2k");
  // Unpriced reaches here as null and must not render as a number.
  assert.equal(money(null), "—");
  assert.equal(money(undefined), "—");
});

test("the rates carry the date they were taken", () => {
  assert.match(PRICED, /^\d{4}-\d{2}-\d{2}$/);
});
