// Tests for the highlighter.
//
// Almost all of these check one thing from different angles: the text survives.
// A highlighter is a layer over somebody's source, and one that dropped a
// backslash or swallowed a quote would be showing them a file that is not their
// file — a wrong fact, dressed up in colour. Which token got which colour
// matters much less, and is tested only where a mistake would tint the rest of
// the screen.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const hl = await import("../highlight.js");

const textOf = (node) => node.textContent;

// survives is the invariant, applied to whatever it is given.
function survives(source, path) {
  assert.equal(textOf(hl.highlight(source, path)), source,
    `highlighting ${path} changed the text`);
}

test("the text survives, whatever the language", () => {
  const cases = [
    [".go", `package main\n\nimport "fmt"\n\n// A comment with "quotes" in it.\nfunc main() {\n\tfmt.Println("hello\\n")\n}\n`],
    [".js", "const x = `a ${b} c`; // trailing\n/* block\n   comment */\nexport default 42;\n"],
    [".css", ".row { grid-template-columns: 2ch 1fr; } /* note */\n"],
    [".sh", "#!/bin/sh\nset -e  # strict\necho \"hi $USER\"\n"],
    [".json", `{"a": 1, "b": [true, null], "c": "x\\"y"}\n`],
    [".html", "<!-- hi --><p class=\"x\">text</p>\n"],
    [".txt", "no language for this, so it stays plain\n"],
    ["Makefile", "all:\n\techo hi\n"],
  ];
  for (const [ext, source] of cases) survives(source, `f${ext}`);
});

// The cases a naive scanner gets wrong, each of which would eat the rest of a
// file if it were mishandled.
test("the text survives the awkward cases", () => {
  const awkward = [
    `s := "a \\\\" // this quote closes, the backslash was escaped\nnext()\n`,
    `s := "unterminated\nnext()\n`,
    "s := `a raw string\nover two lines`\nnext()\n",
    "/* unterminated block comment\nstill going\n",
    "// comment at the very end with no newline",
    '"',
    "`",
    "\\",
    "",
    "\n\n\n",
    "0x1f 1_000 3.14 1e9",
  ];
  for (const source of awkward) survives(source, "f.go");
});

test("an empty file and a file of one character survive", () => {
  assert.equal(textOf(hl.highlight("", "f.go")), "");
  assert.equal(textOf(hl.highlight("x", "f.go")), "x");
  assert.equal(textOf(hl.highlight(null, "f.go")), "");
});

// A language nobody listed is drawn plain rather than guessed at.
test("an unknown language is drawn plain", () => {
  assert.equal(hl.language("a.rs"), null);
  assert.equal(hl.language("Makefile"), null);
  assert.equal(hl.language("noextension"), null);
  const out = hl.highlight("fn main() {}", "a.rs");
  assert.equal(textOf(out), "fn main() {}");
});

// Markdown is rendered, not highlighted: it has its own renderer, and two of
// them fighting over the same text would be one of them losing.
test("markdown is not highlighted", () => {
  assert.equal(hl.language("Vision.md"), null);
});

function classesIn(node, out = []) {
  if (node.className) out.push(node.className);
  for (const child of node.childNodes || []) classesIn(child, out);
  return out;
}

test("comments, strings, numbers and keywords are told apart", () => {
  const got = classesIn(hl.highlight(`func f() { // note\n  s := "x"\n  n := 42\n}`, "f.go"));
  for (const want of ["t-comment", "t-string", "t-number", "t-word"]) {
    assert.ok(got.includes(want), `nothing was marked ${want}: ${got.join(" ")}`);
  }
});

// A line comment that swallowed its newline would tint every following line, so
// the newline stays outside it.
test("a line comment ends at its line", () => {
  const out = hl.highlight("// note\ncode()\n", "f.go");
  const comment = [...out.childNodes].find((n) => n.className === "t-comment");
  assert.equal(comment.textContent, "// note");
  assert.doesNotMatch(comment.textContent, /\n/);
});

// A stray quote in one line must not paint the rest of the file as a string.
test("an unterminated string ends at its line", () => {
  const out = hl.highlight('s := "oops\nreal_code()\n', "f.go");
  const string = [...out.childNodes].find((n) => n.className === "t-string");
  assert.doesNotMatch(string.textContent, /real_code/);
});

test("a block renders as a code block, not a paragraph", () => {
  const el = hl.block("func main() {}", "f.go");
  assert.equal(el.tagName, "PRE");
  assert.equal(el.className, "code");
  assert.equal(el.childNodes[0].tagName, "CODE");
  assert.equal(el.textContent, "func main() {}");
});
