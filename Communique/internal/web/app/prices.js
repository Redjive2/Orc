// What tokens cost, in money.
//
// Its own file because it is the one thing on this site that is a *fact about a
// vendor* rather than a fact about the fleet. Everything else the browser draws
// was measured on a machine somebody owns; this is a published price list that
// changes when Anthropic changes it, and it will go stale here without anything
// failing. So it is one small module with a date on it, rather than numbers
// scattered through the panels that read them.
//
// Three things it is careful about, because a money figure is believed harder
// than a token count:
//
//   - **A model with no price is not free.** Orc folds every model id to a tier
//     — see `alias` in internal/activity — and a tier this table has never heard
//     of returns null rather than zero. A row that quietly costs nothing is a
//     row that makes a total wrong.
//   - **List price, not a bill.** These are the public per-token rates. A fleet
//     running on a subscription is not billed this way at all, and one on a
//     negotiated contract pays something else. It is the standard way to value
//     usage and it is not an invoice; every screen that shows it says so.
//   - **Cache writes and reads are not input.** They are priced off it by a
//     multiplier, and folding them into one rate would misprice the largest
//     number in the whole series by an order of magnitude.

// PRICED is when these rates were taken, so a reader can tell how much to trust
// them. Shown wherever a total is.
export const PRICED = "2026-06-24";

// PRICES is dollars per million tokens, by the tier orc reports.
//
// Keyed on the tier rather than the model id because that is what arrives: orc
// folds `claude-opus-5`, `claude-opus-4-8` and everything else containing
// "opus" to `opus` before the reading is ever written. That works today because
// the current generation of each tier is priced uniformly, and it is a
// coincidence rather than a rule — the day two opus models cost different
// amounts, this table cannot tell them apart and will need the full id.
const PRICES = {
  opus: { input: 5, output: 25 },
  sonnet: { input: 3, output: 15 },
  haiku: { input: 1, output: 5 },
};

// Cache tokens are priced off the input rate rather than having one of their own.
//
// A write costs more than fresh input because it is stored; a read costs almost
// nothing, which is the whole reason a long session is affordable at all. The
// write multiplier is the five-minute one — the hour-long cache is double — and
// which of the two a session used is not in the reading, so this is an
// assumption rather than a measurement. It is the cheaper of the two, so a
// figure drawn from it is a floor.
const CACHE_WRITE = 1.25;
const CACHE_READ = 0.1;

// cost is what one set of token counters came to, in dollars, or null when the
// model is not one this table prices.
//
// Null rather than zero, and every caller has to decide what to do about it. An
// unpriced model folded in as nothing would make a fleet look cheaper the more
// it used the model nobody had a rate for.
export function cost(tokens, model) {
  const rate = PRICES[String(model || "").toLowerCase()];
  if (!rate) return null;

  const t = tokens || {};
  const per = (n) => (Number(n) || 0) / 1e6;
  return per(t.input) * rate.input
    + per(t.output) * rate.output
    + per(t.cache_create) * rate.input * CACHE_WRITE
    + per(t.cache_read) * rate.input * CACHE_READ;
}

// priced reports whether a tier has a rate at all, for a caller that wants to
// count what it could not price rather than add it in.
export function priced(model) {
  return Object.hasOwn(PRICES, String(model || "").toLowerCase());
}

// money renders dollars at a precision that suits the size.
//
// Sub-cent figures are real here — one short turn on haiku is a fraction of a
// cent — and rounding them all to "$0.00" would draw a busy fleet as free. Above
// a dollar the cents stop mattering and the magnitude starts to, so it goes the
// other way.
export function money(n) {
  if (n === null || n === undefined || !Number.isFinite(n)) return "—";
  if (n === 0) return "$0";
  if (n < 0.01) return "<$0.01";
  if (n < 10) return `$${n.toFixed(2)}`;
  if (n < 1000) return `$${n.toFixed(0)}`;
  return `$${(n / 1000).toFixed(1)}k`;
}
