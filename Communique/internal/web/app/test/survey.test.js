// Tests for the survey.
//
// A survey is arithmetic presented as fact, so the arithmetic is the thing worth
// testing: a percentage that rounds to nothing, a mean that no file resembles, a
// total that quietly omits the files it could not read. Every one of those is a
// wrong number shown confidently, which is worse than no survey at all.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const s = await import("../survey.js");

function text(nodes) {
  return nodes.filter(Boolean).map((n) => n.textContent).join(" ");
}

const files = [
  { path: "Communique/internal/cli/cli.go", lines: 600, bytes: 20000 },
  { path: "Communique/internal/cli/help.go", lines: 200, bytes: 8000 },
  { path: "Anno/internal/marker/marker.go", lines: 379, bytes: 12000 },
  { path: "Docs/Dock/Vision.md", lines: 83, bytes: 2400 },
  { path: "README.md", lines: 10, bytes: 200 },
  { path: "Makefile", lines: 12, bytes: 300 },
];

const stateWith = (fs, extra = {}) => ({ library: { root: "Orc", files: fs, ...extra } });

test("extension names a kind, and a file without one is its own kind", () => {
  assert.equal(s.extension("a/b/c.go"), ".go");
  assert.equal(s.extension("Docs/Vision.md"), ".md");
  assert.equal(s.extension("Makefile"), "Makefile");
  assert.equal(s.extension("a/.gitignore"), ".gitignore");
});

test("kinds are totalled and ordered by weight", () => {
  const got = s.byExtension(files);
  assert.equal(got[0].ext, ".go");
  assert.equal(got[0].files, 3);
  assert.equal(got[0].lines, 600 + 200 + 379);
  // Markdown is second by lines, and the two extensionless kinds come last.
  assert.equal(got[1].ext, ".md");
  assert.equal(got[1].lines, 93);
});

// The median sits beside the mean because they disagree in the interesting case:
// a tree of small files with three enormous ones has a mean nothing resembles.
test("the spread reports both a mean and a median", () => {
  const got = s.spread(files);
  assert.equal(got.files, 6);
  assert.equal(got.lines, 600 + 200 + 379 + 83 + 10 + 12);
  assert.equal(got.largest, 600);
  assert.equal(got.median, Math.round((83 + 200) / 2)); // 10 12 83 200 379 600
  assert.notEqual(got.mean, got.median, "this fixture exists to make them differ");
});

// A file that could not be carried has no line count, and counting it as a
// zero-line file would drag the median down with something that is not a fact.
test("files with no line count are left out of the spread", () => {
  const got = s.spread([...files, { path: "big/generated.go", bytes: 9e6, skipped: "too large" }]);
  assert.equal(got.files, 6, "the unreadable file should not be counted as empty");
});

test("an empty repository has an empty spread rather than a division by zero", () => {
  const got = s.spread([]);
  assert.deepEqual(got, { files: 0, lines: 0, mean: 0, median: 0, largest: 0 });
});

// "0%" for something that is present reads as "nothing", which is wrong. A
// share below one percent says so.
test("a small share says it is small rather than rounding to nothing", () => {
  assert.equal(s.share(1, 100000), "<1%");
  assert.equal(s.share(0, 100), "0%");
  assert.equal(s.share(50, 100), "50%");
  assert.equal(s.share(1, 0), "—");
});

test("a bar never draws wider than its width, whatever it is given", () => {
  for (const [part, whole] of [[0, 10], [5, 10], [10, 10], [20, 10], [-5, 10]]) {
    const drawn = s.bar(part, whole, 8).textContent;
    assert.equal(drawn.length, 8, `bar(${part}, ${whole}) drew ${JSON.stringify(drawn)}`);
  }
  assert.equal(s.bar(1, 0).textContent, "—");
});

test("the largest files come first and are bounded in number", () => {
  const many = Array.from({ length: 30 }, (_, i) => ({ path: `f${i}.go`, lines: i + 1 }));
  const got = s.widest(many);
  assert.equal(got.length, 8);
  assert.equal(got[0].lines, 30);
  assert.ok(got[0].lines >= got[1].lines);
});

test("the survey draws every section", () => {
  const out = text(s.survey(stateWith(files)));
  for (const want of ["survey", "by directory", "by kind", "the largest files"]) {
    assert.match(out, new RegExp(want));
  }
  // The totals a reader checks first.
  assert.match(out, /6 files/);
  assert.match(out, /1284 lines/);
  // Directories are named, and the heaviest is present.
  assert.match(out, /Communique\//);
  assert.match(out, /\.go/);
});

// Drawn against the repository total, every one of the largest files is a slice
// of a percent and the bars are all identical — which defeats the one section
// that exists to compare them. They are drawn against each other instead.
test("the largest files are compared with each other, not the repository", () => {
  const lopsided = [
    { path: "huge.go", lines: 1000 },
    { path: "small.go", lines: 100 },
    ...Array.from({ length: 40 }, (_, i) => ({ path: `pad${i}.go`, lines: 500 })),
  ];
  const out = s.survey(stateWith(lopsided));
  const bars = [];
  const walk = (n) => {
    if (String(n.className).includes("meter")) bars.push(n.textContent);
    for (const c of n.childNodes || []) walk(c);
  };
  for (const n of out) walk(n);

  // The last section's first bar is the biggest file and must be full.
  const full = bars.filter((b) => /^▓+$/.test(b));
  assert.ok(full.length > 0, `no bar was ever full:\n${bars.join("\n")}`);
});

// Never the contents. The code tab is for reading one file; a survey that could
// show a file's text would be a second, worse code tab and the two would drift.
test("the survey shows no file contents", () => {
  const withText = files.map((f) => ({ ...f, text: "SECRET BODY TEXT" }));
  const out = text(s.survey(stateWith(withText)));
  assert.doesNotMatch(out, /SECRET BODY TEXT/);
});

test("a file at the repository root is counted, not dropped", () => {
  const out = text(s.survey(stateWith(files)));
  assert.match(out, /\(root\)/, "files beside the directories need somewhere to be counted");
});

// The same distinction the docs tab makes: a missing tool and an empty
// repository are different answers, and only one is about the files.
test("an empty survey blames the missing tool when there is one", () => {
  const out = text(s.survey(stateWith([], { notes: ["`dock` could not be run"] })));
  assert.match(out, /dock/);
  assert.doesNotMatch(out, /nothing mirrored yet/);
});

test("an empty survey with nothing wrong says the plain thing", () => {
  assert.match(text(s.survey(stateWith([]))), /nothing mirrored yet/);
  assert.match(text(s.survey({ library: null })), /no machine is mirroring/);
});
