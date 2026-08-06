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

// The two restrictions that were friction rather than rule.
//
// A capital was always accepted, because every tool downstream lower-cases. A
// space was not, and a space is how a person writes a name — so it is folded to
// the dash it would have been written as, and the sheet shows the result. This
// is looser than the tool, which is the safe direction only because what gets
// sent is `tidy`'s answer and not what was typed; dialog.test.js pins that half.
test("a name may be written with capitals and spaces", () => {
  ok(check.mailbox("My Agent"), "My Agent");
  ok(check.label("Fix The Parser"), "Fix The Parser");
  assert.equal(check.tidy("Fix The Parser"), "fix-the-parser");
  assert.equal(check.tidy("  Padded Name  "), "padded-name");
});

// Length is preserved, so a message about the character *after* a space still
// counts to the same place in what was typed.
test("each space becomes one dash, and runs are not collapsed", () => {
  assert.equal(check.tidy("a  b"), "a--b");
  assert.equal(check.mailbox("a  b!"), check.mailbox("a--b!"));
  assert.match(check.mailbox("a  b!"), /position 5/);
});

test("a mailbox name may not contain what the tool forbids", () => {
  // Everything else is still refused rather than guessed at: turning `%` into
  // something would be inventing what somebody meant.
  for (const n of ["agent!", "a/b", "señor", "a+b", "%parser"]) {
    bad(check.mailbox(n), n);
  }
});

test("a mailbox name says where the bad character is", () => {
  const got = check.mailbox("my/agent");
  assert.match(got, /position 3/, got);
  assert.match(got, /\//, got);
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

// --- paces: Go's time.ParseDuration, not clock.ParseSpan --------------------

// Two duration grammars live in this tree and they are not the same one. The
// bug that made these tests: every pace field was given `span`, which mirrors
// clock.ParseSpan, while `orc pace` and cq's own sync endpoint both call
// time.ParseDuration. It was wrong in both directions at once, and because the
// sheet will not submit while a field is marked, the second direction meant a
// perfectly good interval could not be sent at all.
test("a pace takes a compound duration, which the tool accepts", () => {
  for (const d of ["1h30m", "90m", "30s", "2h", "1h0m0s"]) {
    ok(check.duration(d), d);
  }
});

test("a pace refuses the units time.ParseDuration does not have", () => {
  // And says what to write instead. `1d` is not a typo — it is a unit this
  // parser has never had, and "not a duration" would send somebody hunting for
  // a mistake that is not there.
  assert.match(check.duration("1d"), /24h/);
  assert.match(check.duration("2w"), /336h/);
  assert.match(check.duration("7d"), /168h/);
});

test("a pace is bounded where its own tool bounds it", () => {
  // Orc/internal/cli MinQuiet (--after) and MinWatch (--every, --watch), and
  // cq's protocol.MinSyncPace and MaxPace.
  bad(check.CHECKS["pace.quiet"]("30s"), "30s against the wake floor");
  ok(check.CHECKS["pace.watch"]("30s"), "30s against the cycle floor");
  bad(check.CHECKS["pace.sync"]("5s"), "5s against the sync floor");
  ok(check.CHECKS["pace.sync"]("10s"), "10s, the sync floor exactly");
  bad(check.CHECKS["pace.sync"]("200h"), "longer than a week");
  ok(check.CHECKS["pace.sync"]("168h"), "a week exactly");
});

test("a pace with nothing in it is refused, as the tool refuses it", () => {
  // `orc pace` wants "a duration with something in it": zero is not a cycle.
  bad(check.duration("0s"), "0s");
  bad(check.duration("0h0m"), "0h0m");
  bad(check.duration("soon"), "soon");
  bad(check.duration("2 hours"), "2 hours");
});

// The other grammar, unchanged, on the one field that really uses it.
test("span is still clock.ParseSpan, for the field that goes there", () => {
  ok(check.span("2h"), "2h");
  ok(check.span("7d"), "7d");
  bad(check.span("1h30m"), "1h30m");
});

// --- permissions: measured against the fleet, not against a shape ------------

// Every other check here measures a value's shape. This one measures it against the
// world, which is why it is built rather than called — and it is the difference
// between "that is not a name" and "no permission is called that".
const fleetPerms = [
  { name: "edit", floor: 20, patterns: ["edit(**)"] },
  { name: "upgrade", floor: 60, patterns: ["orc(upgrade)"] },
  { name: "orc-agents", floor: 60, patterns: ["orc(new employ)"] },
];

test("a permission that does not exist is refused, with the nearest offered", () => {
  const at = check.permissions(fleetPerms, { authority: 99 });
  assert.match(at("editt"), /no permission called “editt”/);
  assert.match(at("editt"), /did you mean “edit”/);
  // And no guess where none is close: "did you mean X?" about something unrelated
  // is worse than not guessing, because somebody follows it.
  assert.doesNotMatch(at("zzzzzzzz"), /did you mean/);
});

test("a permission the role cannot reach is refused before it is queued", () => {
  const at = check.permissions(fleetPerms, { authority: 50 });
  ok(at("edit"), "edit at authority 50");
  assert.match(at("upgrade"), /needs authority 60, and the role has 50/);
});

test("several names are checked, and each one names itself when it fails", () => {
  const at = check.permissions(fleetPerms, { authority: 99 });
  ok(at("edit upgrade orc-agents"), "three good names");
  assert.match(at("edit nope"), /“nope”/);
  assert.match(at("edit edit"), /twice/);
});

// A name the role already holds is not refused. Asking again changes nothing on the
// machine, and a form that argued about it would be arguing about a harmless request.
test("a permission the role already holds is not an error", () => {
  const at = check.permissions(fleetPerms, { held: ["edit"], authority: 99 });
  ok(at("edit"), "one it already has");
  ok(at("edit upgrade"), "one it has and one it does not");
});

test("an empty list is refused like every other empty field", () => {
  bad(check.permissions(fleetPerms, {})(""), "nothing typed");
});

// Capitals work everywhere else a name is typed, and orc lower-cases one on the way
// in, so `EDIT` and `edit` are one permission. Looking the typed spelling up refused
// something that would have worked, and counted the two as different when checking
// for a repeat.
test("a permission may be typed with capitals", () => {
  const at = check.permissions(fleetPerms, { authority: 99 });
  ok(at("EDIT"), "EDIT");
  ok(at("Edit Upgrade"), "mixed case");
  assert.match(at("edit EDIT"), /twice/);
});

// Every bad name, not up to the first. A field submitted three times, each attempt
// revealing one more problem, reads as broken rather than as helpful.
test("every bad name is reported at once", () => {
  const got = check.permissions(fleetPerms, { authority: 30 })("nope upgrade alsonope");
  for (const want of ["nope", "upgrade", "alsonope"]) {
    assert.ok(got.includes(want), `${want} is missing from: ${got}`);
  }
});

// What is queued has to be what was checked. A form that validated one spelling and
// sent another is a form whose checks mean nothing.
test("names returns what the tools accept, deduplicated and without the held ones", () => {
  assert.deepEqual(check.names("EDIT  Upgrade edit"), ["edit", "upgrade"]);
  assert.deepEqual(check.names("edit upgrade", { without: ["EDIT"] }), ["upgrade"]);
  assert.deepEqual(check.names("   "), []);
});
