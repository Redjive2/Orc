// The rules these mirror live in Go, and the whole value of the checks is that
// they agree with them. So each group below names its source, and the cases are
// the ones those functions single out.
//
// The asymmetry worth remembering while reading: a check that is **stricter**
// than the tool is a real bug, because it refuses something that would have
// worked and a browser offers no way round it. A check that is **looser** costs
// only the old behaviour — the value goes and the queue says why. So the
// "accepts" cases matter more than the "refuses" ones.

import test from "node:test";
import assert from "node:assert/strict";

import * as check from "../check.js";

const ok = (got, what) => assert.equal(got, "", `${what} was refused: ${got}`);
const bad = (got, what) => assert.notEqual(got, "", `${what} was accepted`);

// --- mailbox names: Common/user.Parse --------------------------------------

test("an ordinary mailbox name is accepted", () => {
  for (const n of ["boss", "atlas", "a", "agent-7", "a.b_c-d", "x9"]) {
    ok(check.mailbox(n), n);
  }
});

test("a mailbox name is normalised before it is judged", () => {
  // user.Parse lower-cases and trims first, so these are the same name.
  ok(check.mailbox("BOSS"), "BOSS");
  ok(check.mailbox("  boss  "), "padded");
});

test("a mailbox name may not contain what the tool forbids", () => {
  for (const n of ["my agent", "agent!", "a/b", "señor", "a+b"]) {
    bad(check.mailbox(n), n);
  }
});

test("a mailbox name says where the bad character is", () => {
  const got = check.mailbox("my agent");
  assert.match(got, /position 3/, got);
  assert.match(got, /space/, got);
});

test("a mailbox name must start with a letter or digit", () => {
  for (const n of [".hidden", "_leading", "-dash"]) {
    bad(check.mailbox(n), n);
  }
});

// A leading dash is called out as an option rather than as a bad character,
// because that is the mistake being made — ParseName checks it first for the
// same reason.
test("a name that looks like a flag is told so", () => {
  assert.match(check.mailbox("--force"), /option/);
});

test("a mailbox name is bounded at 64 characters", () => {
  ok(check.mailbox("a".repeat(64)), "64 characters");
  bad(check.mailbox("a".repeat(65)), "65 characters");
});

// The two reserved sets differ, and getting them the wrong way round would
// refuse valid names — the failure this file exists to prevent.
test("the mailbox reserved words are Mailman's", () => {
  for (const n of ["all", "any", "none", "system", "mailman"]) {
    bad(check.mailbox(n), n);
  }
  // Orc's words are not reserved here: a mailbox called `role` is fine.
  for (const n of ["role", "identity", "permission", "authority"]) {
    ok(check.mailbox(n), n);
  }
});

// --- role and permission names: Orc/internal/model.ParseName ----------------

test("the label reserved words are Orc's", () => {
  for (const n of ["all", "any", "none", "identity", "role", "permission", "authority"]) {
    bad(check.label(n), n);
  }
  // And Mailman's are not: a role called `system` is fine.
  for (const n of ["system", "mailman"]) {
    ok(check.label(n), n);
  }
});

test("an ordinary role or permission name is accepted", () => {
  for (const n of ["reviewer", "write-docs", "orc-agents", "read-all", "shell.build"]) {
    ok(check.label(n), n);
  }
});

// --- durations: Common/clock.ParseSpan --------------------------------------

test("a duration is one number and one unit", () => {
  for (const d of ["30m", "2h", "1s", "7d", "4w", "0m"]) {
    ok(check.span(d), d);
  }
});

test("a duration must use a unit the tool knows", () => {
  for (const d of ["30", "5y", "2M", "10ms"]) {
    bad(check.span(d), d);
  }
});

// The one worth having. clock.ParseSpan is not time.ParseDuration, so anybody
// fluent in Go types this — and the generic message would send them hunting for
// a typo that is not there.
test("a compound duration is refused with the spelling that works", () => {
  const got = check.span("1h30m");
  bad(got, "1h30m");
  assert.match(got, /90m/, `it should suggest 90m: ${got}`);
});

test("a compound duration that does not reduce is still refused clearly", () => {
  const got = check.span("1h1s");
  bad(got, "1h1s");
  assert.match(got, /one number and one unit/, got);
});

test("a duration with no number is refused", () => {
  bad(check.span("h"), "h");
  bad(check.span(""), "empty");
});

test("an absurd duration is a typo, not a request", () => {
  bad(check.span("99999999h"), "99999999h");
});

// --- numbers ---------------------------------------------------------------

test("a bounded number takes its bounds", () => {
  ok(check.whole("50", { min: 1, max: 99 }), "50");
  ok(check.whole("1", { min: 1, max: 99 }), "1");
  ok(check.whole("99", { min: 1, max: 99 }), "99");
  bad(check.whole("0", { min: 1, max: 99 }), "0");
  bad(check.whole("100", { min: 1, max: 99 }), "100");
});

test("a number field says so when it was given a word", () => {
  const got = check.whole("high", { min: 1, max: 5, what: "priority" });
  assert.match(got, /whole number/, got);
  assert.match(got, /priority/, got);
});

test("an empty number is missing rather than wrong", () => {
  assert.match(check.whole("", { min: 1, max: 5, what: "priority" }), /cannot be empty/);
});

// --- paths ------------------------------------------------------------------

test("repository-relative paths are accepted", () => {
  ok(check.paths("Docs/Orc internal/cli"), "two relative paths");
  ok(check.paths("README.md"), "one file");
});

test("a path out of the repository is refused before it is queued", () => {
  bad(check.paths("/etc/passwd"), "absolute");
  bad(check.paths("../secrets"), "climbing out");
  bad(check.paths("Docs/../../etc"), "climbing out mid-path");
  bad(check.paths("C:\\Windows"), "a windows absolute path");
});

test("the path refusal names the offending one", () => {
  const got = check.paths("Docs/Orc /etc/passwd");
  assert.match(got, /\/etc\/passwd/, got);
});

// --- wiring -----------------------------------------------------------------

test("a field spec can name its check", () => {
  assert.equal(typeof check.of("mailbox"), "function");
  assert.equal(typeof check.of("span"), "function");
  const fn = () => "no";
  assert.equal(check.of(fn), fn);
});

// A misspelled check must not stop a dialog opening. Validating one field less is
// a small loss; a sheet that will not appear is the whole action lost.
test("a check that does not exist is nothing rather than a crash", () => {
  assert.equal(check.of("nosuchcheck"), null);
  assert.equal(check.of(undefined), null);
  assert.equal(check.of(null), null);
});

// "2 hours" ends in a valid unit and has letters before it, so the compound-
// duration heuristic fires on it — and there is no single-unit spelling to
// offer. Confidently suggesting one anyway is worse than a plain refusal.
test("a duration that is words is refused without a made-up suggestion", () => {
  const got = check.span("2 hours");
  bad(got, "2 hours");
  assert.doesNotMatch(got, /90m/, `it invented a suggestion: ${got}`);
  assert.match(got, /30m or 2h/, got);
});
