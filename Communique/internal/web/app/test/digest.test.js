// Tests for the edit precondition's digest.
//
// It has to be the same number the agent computes, or every edit is refused as
// stale and the feature simply does not work. So this checks the published
// vectors, the encoding, and — the part that would otherwise drift — the exact
// strings the Go side is tested against.

import test from "node:test";
import assert from "node:assert/strict";

const { sha256, digest } = await import("../digest.js");

const bytes = (s) => new TextEncoder().encode(s);

// The published SHA-256 vectors. If the hand-written implementation is wrong,
// it is wrong here first.
test("the published vectors", async () => {
  const cases = [
    ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
    ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
    ["hello", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"],
    ["abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
      "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"],
  ];
  for (const [input, want] of cases) {
    assert.equal(sha256(bytes(input)), want, `sha256(${JSON.stringify(input)})`);
    assert.equal(await digest(input), want, `digest(${JSON.stringify(input)})`);
  }
});

// A message exactly on a block boundary is where a padding mistake shows up:
// 55 bytes fits with its length, 56 forces a second block.
test("the padding is right at the block boundaries", async () => {
  for (const n of [54, 55, 56, 57, 63, 64, 65, 119, 120, 127, 128, 129]) {
    const input = "a".repeat(n);
    assert.equal(sha256(bytes(input)), await digest(input), `${n} bytes`);
  }
});

// The agent hashes the UTF-8 bytes of the file. A digest taken over anything
// else — code units, a normalised form — would disagree on every file with a
// character outside ASCII, and every § in the documentation is one.
test("the encoding is UTF-8", async () => {
  for (const input of ["§1.2 Sections", "café", "→ ← ▓░", "🙂", "a b"]) {
    assert.equal(sha256(bytes(input)), await digest(input));
    // And the bytes really are UTF-8 rather than one byte per character.
    if (input === "§1.2 Sections") {
      assert.equal(bytes(input).length, input.length + 1, "§ is two bytes");
    }
  }
});

// WebCrypto and the fallback must agree, because which one runs depends on
// whether the site is served over TLS — and an edit made on one connection must
// be applicable from the other.
test("WebCrypto and the fallback agree", async (t) => {
  if (!globalThis.crypto || !globalThis.crypto.subtle) {
    t.skip("no WebCrypto here to compare against");
    return;
  }
  for (const input of ["", "hello", "§1 Thing\n\nprose\n", "a".repeat(1000)]) {
    const web = await digest(input);
    assert.equal(web, sha256(bytes(input)), `they disagree on ${JSON.stringify(input.slice(0, 20))}`);
  }
});

// The exact strings write_test.go checks, so the two ends cannot drift apart
// without one of these files failing.
test("it agrees with the Go implementation's fixtures", async () => {
  assert.equal(await digest("hello"),
    "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824");
  assert.equal((await digest("")).length, 64);
});

test("nothing and undefined hash as the empty string", async () => {
  const empty = await digest("");
  assert.equal(await digest(null), empty);
  assert.equal(await digest(undefined), empty);
});
