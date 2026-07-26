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
  assert.equal(got.arg, "Anno/internal/**");
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

test("orc and tool name exactly one thing", () => {
  assert.equal(clauses.read("orc(assign)").error, null);
  assert.ok(clauses.read("orc(assign role)").error);
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
