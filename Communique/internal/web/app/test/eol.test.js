// Tests for line endings.
//
// The property that matters is the round trip: text taken out of a file and put
// back unchanged has to be byte-identical, or every edit made from a phone
// against a Windows checkout arrives as a whole-file rewrite.

import test from "node:test";
import assert from "node:assert/strict";

const { endingOf, toLF, fromLF, CRLF, LF } = await import("../eol.js");

const trip = (text) => fromLF(toLF(text), endingOf(text));

test("text that was not edited comes back exactly as it went in", () => {
  for (const text of [
    "package main\r\n\r\nfunc main() {}\r\n",
    "package main\n\nfunc main() {}\n",
    "no trailing newline\r\n\r\nat all",
    "",
    "\r\n",
    "\n",
    "one line",
  ]) {
    assert.equal(trip(text), text, JSON.stringify(text));
  }
});

test("the ending is whichever the file actually uses", () => {
  assert.equal(endingOf("a\r\nb\r\n"), CRLF);
  assert.equal(endingOf("a\nb\n"), LF);
  assert.equal(endingOf("no newlines here"), LF);
  assert.equal(endingOf(""), LF);
});

// A file with both has to be written back one way or another, so the majority
// wins — it is the choice that leaves the smaller diff.
test("a mixed file follows its majority", () => {
  assert.equal(endingOf("a\r\nb\r\nc\r\nd\n"), CRLF);
  assert.equal(endingOf("a\nb\nc\nd\r\n"), LF);
  // A tie is not CRLF: turning half a file into carriage returns to settle a
  // draw is a bigger change than leaving it alone.
  assert.equal(endingOf("a\r\nb\n"), LF);
});

test("the browser only ever sees LF", () => {
  assert.ok(!toLF("a\r\nb\r\nc").includes("\r"));
  assert.equal(toLF("a\r\nb"), "a\nb");
  assert.equal(toLF("a\nb"), "a\nb");
});

// This is what the DOM does to a textarea's value, and the reason any of this
// exists: what comes back out of an editor is LF whatever went in.
test("what the editor returns is restored to the file's own ending", () => {
  const onDisk = "first\r\nsecond\r\nthird\r\n";
  const shown = toLF(onDisk);
  const edited = shown.replace("second", "changed");

  const written = fromLF(edited, endingOf(onDisk));
  assert.equal(written, "first\r\nchanged\r\nthird\r\n");
  assert.ok(!written.includes("\n\n"), "a bare LF was left among the CRLFs");
});

test("a file that was LF stays LF", () => {
  const onDisk = "first\nsecond\n";
  const written = fromLF(toLF(onDisk).replace("second", "changed"), endingOf(onDisk));
  assert.equal(written, "first\nchanged\n");
  assert.ok(!written.includes("\r"));
});

test("nothing and undefined are the empty string, not a crash", () => {
  assert.equal(toLF(null), "");
  assert.equal(toLF(undefined), "");
  assert.equal(fromLF(null, CRLF), "");
  assert.equal(endingOf(null), LF);
});
