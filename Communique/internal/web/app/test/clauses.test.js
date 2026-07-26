// Tests for reading and colouring permission clauses.
//
// Two properties matter more than the rest, and they are the two a highlighter
// gets wrong quietly. First: the text survives — what comes out is exactly what
// went in, or somebody is being shown a permission that is not the one they typed.
// Second: this never claims authority. Orc validates, and a check here that
// refused something Orc would accept would be the wrong opinion on screen, so
// `problems` reports and nothing gates on it.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const clauses = await import("../clauses.js");

function text(nodes) {
  return nodes.map((n) => n.textContent).join("");
}

test("every kind orc knows parses", () => {
  for (const k of clauses.KINDS) {
    const got = clauses.read(k.example);
    assert.equal(got.error, null, `${k.example}: ${got.error}`);
    assert.equal(got.kind, k.kind);
  }
});

test("the parts come apart", () => {
  const got = clauses.read("read(Anno/internal/**)");
  assert.equal(got.kind, "read");
  assert.deepEqual(got.terms, ["Anno/internal/**"]);
  assert.deepEqual(got.excepts, []);
});

test("a clause without a shape is refused", () => {
  for (const bad of ["read", "read(", "read Anno", "(Anno)"]) {
    assert.ok(clauses.read(bad).error, `${bad} should not read`);
  }
});

test("an unknown kind names the ones that exist", () => {
  const got = clauses.read("delete(Anno/**)");
  assert.match(got.error, /read/);
  assert.match(got.error, /spawn/);
});

test("an empty argument is not a clause", () => {
  assert.match(clauses.read("read()").error, /empty/);
});

test("spawn takes a number, and not any number", () => {
  assert.equal(clauses.read("spawn(24)").error, null);
  assert.equal(clauses.read("spawn(0)").error, null);
  assert.ok(clauses.read("spawn(lots)").error);
  assert.ok(clauses.read("spawn(-1)").error);
  assert.ok(clauses.read(`spawn(${clauses.MAX_LOAD + 1})`).error);
  assert.equal(clauses.read(`spawn(${clauses.MAX_LOAD})`).error, null);
});

// Orc's cleanGlob refuses these with fault.Escape. The wording differs — this is
// a browser talking to a person, not a program reporting a code — but the two
// must agree about *which* clauses are refused, or the form accepts what the
// fleet will not.
test("a glob cannot leave the workspace", () => {
  assert.ok(clauses.read("read(/etc/passwd)").error);
  assert.ok(clauses.read("read(~/.ssh/**)").error);
  assert.ok(clauses.read("write(../../**)").error);
  assert.ok(clauses.read("read(Anno/../../**)").error);
  assert.equal(clauses.read("read(Anno/..hidden/**)").error, null);
});

test("orc and tool name words, not paths", () => {
  assert.equal(clauses.read("orc(assign)").error, null);
  assert.equal(clauses.read("orc(assign grant)").error, null);
  assert.ok(clauses.read("tool(anno/index)").error);
});

test("a kind is read case-insensitively", () => {
  assert.equal(clauses.read("READ(Anno/**)").kind, "read");
});

test("splitting drops nothing but whitespace", () => {
  assert.deepEqual(clauses.split("  read(a/**)   spawn(2) "), ["read(a/**)", "spawn(2)"]);
  assert.deepEqual(clauses.split(""), []);
});

test("problems names the clause it could not read, and only that one", () => {
  const bad = clauses.problems("read(Anno/**) nope spawn(4)");
  assert.equal(bad.length, 1);
  assert.match(bad[0], /^nope /);
});

// The property the whole module rests on.
test("highlighting keeps every character, spacing included", () => {
  for (const line of [
    "read(Anno/**) write(Orc/**) spawn(24)",
    "  read(a)   orc(assign)  ",
    "read(broken spawn(2)",
    "",
    "\tread(a/**)\n",
  ]) {
    assert.equal(text(clauses.highlight(line)), line);
  }
});

test("a kind is coloured for what it is", () => {
  const nodes = clauses.highlight("write(Orc/**)");
  const kind = nodes.find((n) => (n.className || "").includes("cl-kind"));
  assert.ok(kind);
  assert.match(kind.className, /cl-write/);
});

test("an unreadable clause is marked whole, not half-coloured", () => {
  const nodes = clauses.highlight("read(oops");
  assert.equal(nodes.length, 1);
  assert.match(nodes[0].className, /cl-bad/);
  assert.equal(nodes[0].textContent, "read(oops");
});

test("a chip carries the clause it was given", () => {
  assert.equal(clauses.chip("spawn(24)").textContent, "spawn(24)");
});

test("the cheat sheet shows every kind, and its examples are valid", () => {
  const sheet = clauses.cheatsheet();
  for (const k of clauses.KINDS) {
    assert.ok(sheet.textContent.includes(k.example), `${k.example} missing`);
    assert.equal(clauses.read(k.example).error, null);
  }
});

// --- lists, exceptions, and the words a clause may name ---------------------

test("a clause is a list", () => {
  const got = clauses.read("read(Anno/** Dock/**)");
  assert.equal(got.error, null);
  assert.deepEqual(got.terms, ["Anno/**", "Dock/**"]);
});

test("an exception is the second half of the same clause", () => {
  const got = clauses.read("write(** except Docs/** .git/**)");
  assert.equal(got.error, null);
  assert.deepEqual(got.terms, ["**"]);
  assert.deepEqual(got.excepts, ["Docs/**", ".git/**"]);
});

test("except is read whatever its case", () => {
  assert.deepEqual(clauses.read("read(** EXCEPT a/**)").excepts, ["a/**"]);
});

test("the shapes an except cannot take", () => {
  for (const bad of ["read(except a/**)", "read(a/** except)", "read(a/** except b/** except c/**)"]) {
    assert.ok(clauses.read(bad).error, `${bad} should not read`);
  }
});

// A budget is a number, and the message has to say so — "invalid" would leave
// somebody trying spawn(24 48) with nowhere to go.
test("a budget is not a list and says why", () => {
  const got = clauses.read("spawn(24 48)");
  assert.ok(got.error);
  assert.match(got.error, /number/);
  assert.ok(clauses.read("spawn(** except 4)").error);
});

test("verbs and capabilities glob too", () => {
  assert.equal(clauses.read("orc(** except remove)").error, null);
  assert.equal(clauses.read("tool(**)").error, null);
  assert.equal(clauses.read("orc(re*)").error, null);
});

// Splitting on parentheses rather than whitespace: a clause has spaces in it now,
// and splitting on those would make four unparseable fragments out of two clauses.
test("a line splits into clauses, not into words", () => {
  assert.deepEqual(
    clauses.split("read(a/** b/**) write(** except Docs/**)"),
    ["read(a/** b/**)", "write(** except Docs/**)"]);
  assert.deepEqual(clauses.split("  read(a)   spawn(2) "), ["read(a)", "spawn(2)"]);
  assert.deepEqual(clauses.split("read"), ["read"]);
});

test("highlighting still keeps every character, spaces inside a clause included", () => {
  for (const line of [
    "read(Anno/** Dock/**) write(** except Docs/**)",
    "  orc(new  assign)  ",
    "read(broken spawn(2)",
    "read(a/** except)",
  ]) {
    assert.equal(text(clauses.highlight(line)), line);
  }
});

test("an exception is drawn as the part that does not apply", () => {
  const nodes = clauses.highlight("write(** except Docs/**)");
  const classes = nodes.map((n) => n.className || "");
  assert.ok(classes.some((c) => c.includes("cl-except")), classes.join("|"));
  assert.ok(classes.some((c) => c.includes("cl-deny")), classes.join("|"));
});

const WORDS = {
  verbs: [{ word: "new", does: "create an identity" }, { word: "assign", does: "give a role" }],
  tools: [{ word: "upgrade", does: "rebuild everything", in: "cq" }],
};

// The difference between a refusal and a remark. Orc accepts `orc(policy)`; what
// it does with it is nothing, and that is worth saying without blocking the queue.
test("a word nothing checks is a note, not an error", () => {
  const got = clauses.read("orc(policy)", WORDS);
  assert.equal(got.error, null);
  assert.equal(got.notes.length, 1);
  assert.match(got.notes[0], /controls nothing/);
  assert.equal(clauses.read("orc(new)", WORDS).notes.length, 0);
});

test("a glob is not a word, so it is not reported as an unknown one", () => {
  assert.deepEqual(clauses.read("orc(** except new)", WORDS).notes, []);
});

test("notes and problems are counted separately across a line", () => {
  const line = "orc(policy) read(/etc/**)";
  assert.equal(clauses.problems(line, WORDS).length, 1);
  assert.equal(clauses.notes(line, WORDS).length, 1);
});

test("the cheat sheet offers the fleet's own words", () => {
  const sheet = clauses.cheatsheet(WORDS);
  for (const want of ["orc(new)", "orc(assign)", "tool(upgrade)", "checked by cq"]) {
    assert.ok(sheet.textContent.includes(want), `${want} missing from the sheet`);
  }
});

// A fleet from an older Orc carries no vocabulary. The sheet still has to draw.
test("a fleet with no vocabulary still gets a sheet", () => {
  const sheet = clauses.cheatsheet(undefined);
  assert.ok(sheet.textContent.includes("what orc() takes"));
  assert.ok(sheet.textContent.includes("upgrade"));
});

// --- the sheet is the whole list, and says whose ---------------------------

// The sheet is headed "what orc() takes" and read as the answer to that question.
// A partial one does not look partial, so a fallback that listed four of fifteen
// verbs was not a smaller answer — it was a wrong one.
test("the fallback offers every word, described", () => {
  const known = clauses.vocabulary(undefined);
  for (const [kind, list] of [["orc", known.verbs], ["tool", known.tools]]) {
    assert.ok(list.length > 0, `${kind}() has no words to offer at all`);
    for (const w of list) {
      assert.ok(w.does && w.does.trim(),
        `${kind}(${w.word}) is listed with no description; the sheet shows a bare ` +
        `word in a column headed by what it does`);
    }
  }
  // Pinned low rather than exactly: the count belongs to Orc, and vocabulary_test.go
  // is what holds the two lists equal. This only catches a fallback that shrank back
  // to a handful.
  assert.ok(known.verbs.length >= 15,
    `only ${known.verbs.length} verbs offered; orc checks more than that`);
});

// The list above is a copy of Orc's, and a copy is a thing that goes out of date.
// The sheet may be dated, but it must not be dated *silently*.
test("a sheet drawn from this build's own words says so", () => {
  const sheet = clauses.cheatsheet(undefined);
  assert.ok(sheet.textContent.includes("this build knows"),
    "the sheet passes off its own copy as the fleet's answer");

  const fleet = clauses.cheatsheet({
    verbs: [{ word: "new", does: "make one" }],
    tools: [{ word: "upgrade", does: "rebuild", in: "cq" }],
    innocuous: ["echo"],
  });
  assert.ok(!fleet.textContent.includes("this build knows"),
    "the fleet answered with its own vocabulary and the sheet still hedged");

  // An orc from before `shell` sends verbs and tools and no innocuous list. The
  // sheet still has to say what runs without a clause, and still has to admit
  // that part is its own.
  const older = clauses.cheatsheet({
    verbs: [{ word: "new", does: "make one" }],
    tools: [{ word: "upgrade", does: "rebuild", in: "cq" }],
  });
  assert.ok(older.textContent.includes("echo"), "the sheet dropped what runs with no clause");
  assert.ok(older.textContent.includes("this build knows"),
    "the sheet passed off its own innocuous list as the fleet's");
});

// The halves travel separately: an Orc that grows a verb but has only ever had one
// capability sends verbs and no tools, and hedging over the whole sheet then would
// be a warning about nothing.
test("only the part that was missing is called second-hand", () => {
  const partial = clauses.vocabulary({ verbs: [{ word: "new", does: "make one" }] });
  assert.deepEqual(partial.mine, { verbs: false, tools: true, innocuous: true });

  // What `shell` allows with no clause travels the same way and falls back the
  // same way — an older orc sends none, and the sheet still has to say something
  // true rather than nothing.
  const shellOnly = clauses.vocabulary({ innocuous: ["echo"] });
  assert.deepEqual(shellOnly.innocuous, ["echo"]);
  assert.equal(shellOnly.mine.innocuous, false);
  assert.ok(clauses.vocabulary({}).innocuous.includes("echo"));
});

// `unknownWord` reads the same list, which is the quieter half of the bug: with a
// short fallback, an unreachable fleet made the box tell somebody that a clause
// naming a real verb controlled nothing.
test("a real verb is not called meaningless when the fleet went quiet", () => {
  for (const verb of ["grant", "revoke", "workspace", "poke", "model"]) {
    assert.equal(clauses.read(`orc(${verb})`).error, null);
    assert.deepEqual(clauses.problems(`orc(${verb})`, undefined), [],
      `orc(${verb}) is reported as controlling nothing when the fleet sends no vocabulary`);
  }
});

test("every example in the sheet is a clause that parses", () => {
  for (const k of clauses.KINDS) {
    assert.equal(clauses.read(k.example).error, null, `${k.example} does not parse`);
  }
});

// A sheet that marked its own words as unknown would be advising against itself.
test("the sub-sheet does not mark its own words idle", () => {
  const only = { verbs: [{ word: "wake", does: "nudge quiet sessions" }], tools: [] };
  const sheet = clauses.cheatsheet(only);
  const idle = [];
  const walk = (n) => {
    if ((n.className || "").includes("cl-idle")) idle.push(n.textContent);
    for (const k of n.childNodes || []) walk(k);
  };
  walk(sheet);
  assert.deepEqual(idle, []);
});
