// Tests for the library view.
//
// The interesting behaviour is what is *not* fetched: the whole repository can
// be listed and folded from structure alone, and a file's text is asked for once,
// when it is opened. Those are the properties that make the tabs usable on a
// repository of any size, and they are what these check.

import test from "node:test";
import assert from "node:assert/strict";

import { installDOM } from "./dom-stub.js";
installDOM();

const lib = await import("../library.js");

function text(nodes) {
  return nodes.filter(Boolean).map((n) => n.textContent).join(" ");
}

const doc = {
  path: "Docs/Dock/Vision.md", lines: 83, bytes: 2400, machine: "studio",
  sections: [
    { number: "1", name: "Dock", depth: 1, start: 3, end: 83, lines: 81, out: 0 },
    { number: "1.1", name: "Sections", depth: 2, start: 13, end: 36, lines: 24, out: 0 },
  ],
};
const source = {
  path: "Anno/internal/fixture/fixture.go", lines: 82, bytes: 1800, machine: "studio",
  annotations: [{
    kind: "section", name: "types", start: 21, end: 30, lines: 8,
    content_start: 21, content_end: 30,
    children: [{
      kind: "symbol", name: "Pair", meta: ["struct"], start: 23, end: 26, lines: 4,
      content_start: 23, content_end: 26, children: [],
    }],
  }],
};
const plain = { path: "Communique/internal/cli/cli.go", lines: 600, bytes: 20000, machine: "studio" };

function stateWith(files, extra = {}) {
  return { library: { root: "Orc", files }, files: {}, open: {}, ...extra };
}

const noop = { toggle() {}, openFile() {}, pick() {} };

// everything opens every directory, so a test can see the filenames rather than
// the one folded row the reader starts with.
function everything(files) {
  const open = {};
  for (const f of files) {
    const parts = f.path.split("/").slice(0, -1);
    for (let i = 1; i <= parts.length; i++) open[`dir:${parts.slice(0, i).join("/")}`] = true;
  }
  return open;
}

test("the two tabs split on whether Dock found sections", () => {
  const files = [doc, source, plain];
  const state = stateWith(files, { open: everything(files) });

  const docs = text(lib.library(state, noop, { kind: "docs" }));
  assert.match(docs, /Vision\.md/);
  assert.doesNotMatch(docs, /fixture\.go/);

  const code = text(lib.library(state, noop, { kind: "code" }));
  assert.match(code, /fixture\.go/);
  assert.match(code, /cli\.go/);
  assert.doesNotMatch(code, /Vision\.md/);
});

// Everything starts folded, which is the whole request: a big repository must
// not arrive as a wall of filenames.
test("a repository starts folded to its top-level directories", () => {
  const out = text(lib.library(stateWith([doc, source, plain]), noop, { kind: "code" }));
  assert.doesNotMatch(out, /fixture\.go/, `nothing should be unfolded yet:\n${out}`);
  assert.match(out, /Anno/);
});

// The whole point of the split: a .md file with no § headings is not a document
// Dock can address, so the classification is Dock's answer rather than a guess
// from the extension.
test("a markdown file with no sections is not a document", () => {
  const readme = { path: "README.md", lines: 10, bytes: 200, machine: "studio" };
  const state = stateWith([readme]);
  assert.doesNotMatch(text(lib.library(state, noop, { kind: "docs" })), /README/);
  assert.match(text(lib.library(state, noop, { kind: "code" })), /README/);
});

test("paths become a directory tree", () => {
  const root = lib.tree([doc, source, plain]);
  assert.deepEqual([...root.dirs.keys()].sort(), ["Anno", "Communique", "Docs"]);
  const counted = lib.counts(root);
  assert.equal(counted.files, 3);
  assert.equal(counted.lines, 83 + 82 + 600);
});

// A folded directory still has to say how much is inside it, or folding would
// hide the thing the reader needs in order to decide whether to unfold.
test("a folded directory reports what it contains", () => {
  const two = [plain, { ...plain, path: "Communique/internal/cli/help.go", lines: 100 }];
  const out = text(lib.library(stateWith(two), noop, { kind: "code" }));
  assert.match(out, /2 files/, `directory totals missing:\n${out}`);
  assert.match(out, /700 lines/, "a folded directory should total its lines");
});

// "1 files" reads as a bug in a tool that is otherwise careful about its words.
test("counts are pluralised", () => {
  assert.equal(lib.plural(1, "file"), "1 file");
  assert.equal(lib.plural(0, "file"), "0 files");
  assert.equal(lib.plural(2, "line"), "2 lines");

  const out = text(lib.library(stateWith([plain]), noop, { kind: "code" }));
  assert.match(out, /1 file\b/);
  assert.doesNotMatch(out, /1 files/);
});

test("nothing is fetched to draw the tree", () => {
  let fetched = 0;
  const actions = { ...noop, openFile() { fetched++; } };
  lib.library(stateWith([doc, source, plain]), actions, { kind: "code" });
  assert.equal(fetched, 0, "listing the repository must not read any file");
});

// Opening is what asks for the text. This is the property that lets the tab list
// megabytes of repository over a slow link.
test("opening a file asks for it once", () => {
  const asked = [];
  const actions = { ...noop, openFile: (f) => asked.push(f.path) };
  const open = { [`file:${plain.path}`]: false };
  const state = stateWith([plain], { open });

  const nodes = lib.library(state, actions, { kind: "code" });
  // Unfold every directory down to the file, then click it.
  clickAll(nodes, /cli\.go/);
  assert.deepEqual(asked, []);

  // With the directories open, the file row is reachable and clicking it reads.
  const opened = stateWith([plain], {
    open: { "dir:Communique": true, "dir:Communique/internal": true, "dir:Communique/internal/cli": true },
  });
  clickAll(lib.library(opened, actions, { kind: "code" }), /cli\.go/);
  assert.deepEqual(asked, [plain.path]);
});

// clickAll finds the fold whose label matches and clicks it.
function clickAll(nodes, pattern) {
  for (const node of nodes.filter(Boolean)) {
    const buttons = collectButtons(node);
    for (const b of buttons) {
      if (pattern.test(b.textContent)) b.listeners.click?.forEach((fn) => fn({ target: b }));
    }
  }
}

function collectButtons(node, out = []) {
  if (node.tagName === "BUTTON") out.push(node);
  for (const child of node.childNodes || []) collectButtons(child, out);
  return out;
}

test("an opened file shows its sections, and each holds only its own lines", () => {
  const lines = Array.from({ length: 83 }, (_, i) => `line ${i + 1}`).join("\n");
  const state = stateWith([doc], {
    files: { [lib.fileKey(doc)]: { text: lines } },
    open: {
      "dir:Docs": true, "dir:Docs/Dock": true,
      [`file:${doc.path}`]: true,
      [`sec:${doc.path}:1.1`]: true,
    },
  });

  const out = text(lib.library(state, noop, { kind: "docs" }));
  assert.match(out, /§1\.1 Sections/);
  // §1.1 is lines 13:36, so it holds those and not the ones around them.
  assert.match(out, /line 13/);
  assert.match(out, /line 36/);
  assert.doesNotMatch(out, /line 12\b/);
  assert.doesNotMatch(out, /line 37\b/);
});

// A heading with nothing under it yet reports a span of 0:0. Slicing that as if
// it began at line 1 would show the reader lines belonging to something else.
test("a section with no body of its own shows nothing, not the wrong lines", () => {
  const started = {
    path: "Docs/Notes.md", lines: 1, bytes: 12, machine: "studio",
    sections: [{ number: "1", name: "Notes", depth: 1, start: 0, end: 0, lines: 0, out: 0 }],
  };
  const state = stateWith([started], {
    files: { [lib.fileKey(started)]: { text: "# §1 Notes\n" } },
    open: {
      "dir:Docs": true, [`file:${started.path}`]: true,
      [`sec:${started.path}:1`]: true,
    },
  });
  const out = text(lib.library(state, noop, { kind: "docs" }));
  assert.match(out, /§1 Notes/, "the section is still listed");
  assert.doesNotMatch(out, /# §1 Notes/, "it should not show the heading as its own body");
});

test("an annotation shows its own content and nests its children", () => {
  const lines = Array.from({ length: 82 }, (_, i) => `line ${i + 1}`).join("\n");
  const state = stateWith([source], {
    files: { [lib.fileKey(source)]: { text: lines } },
    open: {
      "dir:Anno": true, "dir:Anno/internal": true, "dir:Anno/internal/fixture": true,
      [`file:${source.path}`]: true,
      [`ann:${source.path}:section:types:21`]: true,
    },
  });

  const out = text(lib.library(state, noop, { kind: "code" }));
  assert.match(out, /section types/);
  assert.match(out, /symbol Pair/, "a nested annotation should appear inside its parent");
  assert.match(out, /line 21/);
  assert.doesNotMatch(out, /line 20\b/);
});

test("a file that could not be carried says why instead of showing nothing", () => {
  const huge = {
    path: "big/generated.go", lines: 0, bytes: 5_000_000, machine: "studio",
    skipped: "it is larger than the 1024K limit for one file",
  };
  const state = stateWith([huge], { open: { "dir:big": true } });
  assert.match(text(lib.library(state, noop, { kind: "code" })), /larger than/);
});

test("a truncated library says so", () => {
  const state = stateWith([plain]);
  state.library.truncated = "40 files past the limit were left out entirely";
  assert.match(text(lib.library(state, noop, { kind: "code" })), /left out entirely/);
});

// TestAnEmptyDocsTabSaysWhy: without this it claimed "no document carries a §
// section yet" — a confident statement about the reader's own files — when the
// truth was that `dock` is not installed on the agent machine. A wrong
// diagnosis sends somebody to look in the wrong place.
test("an empty docs tab blames the missing tool, not the documents", () => {
  const state = stateWith([plain]);
  state.library.notes = ["`dock` could not be run on the agent machine, so no document has sections; install it and sync again"];

  const out = text(lib.library(state, noop, { kind: "docs" }));
  assert.match(out, /dock/);
  assert.match(out, /install it and sync again/);
  assert.doesNotMatch(out, /no document carries/,
    "it should not claim something about the documents that is not true");
});

// With no note, the honest answer really is that nothing carries a section.
test("an empty docs tab with nothing wrong says the plain thing", () => {
  const out = text(lib.library(stateWith([plain]), noop, { kind: "docs" }));
  assert.match(out, /no document carries a § section yet/);
});

// A note is worth seeing even when documents did arrive: one lens can fail while
// the other works.
test("a note is shown alongside the documents it did find", () => {
  const state = stateWith([doc], { open: everything([doc]) });
  state.library.notes = ["something went wrong"];
  assert.match(text(lib.library(state, noop, { kind: "docs" })), /something went wrong/);
});

test("an unmirrored machine says so rather than showing an empty tree", () => {
  const out = text(lib.library({ library: null, files: {}, open: {} }, noop, { kind: "docs" }));
  assert.match(out, /no machine is mirroring/);
});

// Three different things make a tab empty and they have three different
// remedies, so they get three different sentences. Telling somebody no machine
// mirrors a repository when the request simply failed sends them to the wrong
// machine entirely.
test("an unreachable library blames the request, not the fleet", () => {
  const state = { library: { unreachable: "not found" }, files: {}, open: {} };
  const out = text(lib.library(state, noop, { kind: "code" }));

  assert.match(out, /could not be fetched/);
  assert.match(out, /not found/);
  assert.match(out, /rebuild and restart/, "an older server is the usual cause and has a remedy");
  assert.doesNotMatch(out, /no machine is mirroring/);
});

// A library that arrived and is empty is a setting that was never made, and the
// message says which setting and — the part that matters — on which machine.
test("an empty library names the setting and the machine it belongs on", () => {
  const state = { library: { root: "Orc", files: [] }, files: {}, open: {} };
  const out = text(lib.library(state, noop, { kind: "code" }));

  assert.match(out, /nothing mirrored yet/);
  assert.match(out, /CQ_LIBRARY/);
  assert.match(out, /agent machine/, "it is set where sync runs, not where the page is served");
  assert.match(out, /cq status/, "and there is one command that says whether it took");
});

// The same three answers, in the survey.
test("the survey gives the same reasons as the tabs", () => {
  const unreachable = text(lib.library({ library: { unreachable: "nope" }, files: {}, open: {} }, noop, { kind: "code" }));
  assert.match(unreachable, /could not be fetched/);
});

// The indent is handed to CSS as a custom property rather than baked into a
// padding here. A phone cannot afford a two-character indent five levels down a
// repository, and how deep a row sits is a layout decision — which belongs in
// the stylesheet, where a media query can reach it.
test("a fold states its depth rather than its padding", () => {
  const state = stateWith([plain], { open: { "dir:Communique": true } });
  const nodes = lib.library(state, noop, { kind: "code" });

  // Folds only. The edit and delete controls are buttons too, and they sit at a
  // fixed place under a file rather than at a depth in the tree.
  const styles = [];
  const collect = (node) => {
    if (node.tagName === "BUTTON" && String(node.className).includes("fold")) {
      styles.push(node.attributes.get("style") || "");
    }
    for (const child of node.childNodes || []) collect(child);
  };
  for (const n of nodes.filter(Boolean)) collect(n);

  assert.ok(styles.length > 0, "no folds were drawn");
  for (const style of styles) {
    assert.match(style, /--depth:\s*\d+/, `a fold should state its depth: ${style}`);
    assert.doesNotMatch(style, /padding/, `a fold should not set its own padding: ${style}`);
  }
  // And the depth really is the nesting, not a constant.
  assert.ok(styles.some((s) => /--depth:\s*0/.test(s)), "the top level should be depth 0");
  assert.ok(styles.some((s) => /--depth:\s*1/.test(s)), "a child should be deeper than its parent");
});

// --- editing -------------------------------------------------------------

function buttons(nodes) {
  const found = [];
  const walk = (n) => {
    if (n.tagName === "BUTTON") found.push(n);
    for (const c of n.childNodes || []) walk(c);
  };
  for (const n of nodes.filter(Boolean)) walk(n);
  return found;
}

const labels = (nodes) => buttons(nodes).map((b) => b.textContent);

// A fold row is a button too, so its twist is what tells the tree apart from the
// controls hanging off whichever row is picked.
const controlLabels = (nodes) =>
  labels(nodes).filter((text) => !/^[▸▾]/.test(text));

test("the picked file offers edit and delete", () => {
  const state = stateWith([plain], {
    files: { [lib.fileKey(plain)]: { text: "package cli\n" } },
    picked: `file:${plain.path}`,
    open: {
      "dir:Communique": true, "dir:Communique/internal": true,
      "dir:Communique/internal/cli": true, [`file:${plain.path}`]: true,
    },
  });
  const got = labels(lib.library(state, noop, { kind: "code" }));
  assert.ok(got.includes("edit"), got.join(" "));
  assert.ok(got.includes("delete"), got.join(" "));
});

// The controls sit under the contents rather than beside the name, because a
// fold's row is a link and a button inside a link is a target nobody can hit.
test("a closed file offers nothing to do to it", () => {
  const state = stateWith([plain], {
    open: { "dir:Communique": true, "dir:Communique/internal": true, "dir:Communique/internal/cli": true },
  });
  const got = labels(lib.library(state, noop, { kind: "code" }));
  assert.ok(!got.includes("edit"), `a closed file offered ${got.join(" ")}`);
});

test("the picked directory offers new file and new folder", () => {
  const state = stateWith([plain], {
    open: { "dir:Communique": true }, picked: "dir:Communique",
  });
  const got = labels(lib.library(state, noop, { kind: "code" }));
  assert.ok(got.includes("new file"), got.join(" "));
  assert.ok(got.includes("new folder"), got.join(" "));
});

// A directory with files in it can be deleted. It could not before, which made
// removing a real folder a matter of deleting every file inside it one at a
// time and then the folder — so, in practice, not possible at all.
test("a directory with things in it can still be deleted", () => {
  const full = stateWith([plain], {
    open: { "dir:Communique": true }, picked: "dir:Communique",
  });
  assert.ok(labels(lib.library(full, noop, { kind: "code" })).includes("delete folder"));
});

// TestTheManifestIsEveryFileUnderneath: it is what the agent checks the real
// directory against, so a file left out of it is a file that makes the removal
// refuse, and one invented is a file the agent will not find.
test("the manifest is every file underneath, at any depth", () => {
  const state = stateWith([plain, doc], {
    open: { "dir:Communique": true }, picked: "dir:Communique",
  });
  const asked = [];
  const actions = {
    ...noop,
    removeFolder: (m, dir, paths, empty) => asked.push({ dir, paths, empty }),
  };
  for (const b of buttons(lib.library(state, actions, { kind: "code" }))) {
    if (b.textContent === "delete folder") b.listeners.click.forEach((fn) => fn({ target: b }));
  }
  assert.deepEqual(asked, [{ dir: "Communique", paths: [plain.path], empty: false }]);
});

// An edit has to say which machine holds the file, and a directory is not itself
// mirrored — it exists because things are in it.
//
// The root is in this list too: it is a directory like any other, and the top of
// the tree is exactly where a new module starts. Its path is empty rather than
// "/" — an absolute path is the one thing the agent refuses outright.
test("a directory action names the machine holding its contents", () => {
  const asked = [];
  const actions = { ...noop, newFile: (m, p) => asked.push([m, p]) };
  const state = stateWith([plain], {
    open: { "dir:Communique": true }, picked: "dir:Communique",
  });

  for (const b of buttons(lib.library(state, actions, { kind: "code" }))) {
    if (b.textContent === "new file") b.listeners.click.forEach((fn) => fn({ target: b }));
  }
  assert.deepEqual(asked, [["studio", "Communique"]]);
});

// The whole point of picking: one row's worth of controls on screen, not one
// under every line of the tree.
test("only the picked row carries controls", () => {
  const open = { "dir:Communique": true, "dir:Communique/internal": true };

  const none = controlLabels(lib.library(stateWith([plain], { open }), noop, { kind: "code" }));
  assert.deepEqual(none, [], `nothing is picked, so nothing should be offered: ${none.join(" ")}`);

  const one = stateWith([plain], { open, picked: "dir:Communique/internal" });
  const got = controlLabels(lib.library(one, noop, { kind: "code" }));
  assert.deepEqual(got, ["new file", "new folder", "delete folder"], got.join(" "));
});

// The root is a directory like any other and is where a new module starts, so it
// offers the first two — but it is the checkout itself, and there is no `..` to
// be standing in afterwards.
test("the picked root offers to be filled but never deleted", () => {
  const state = stateWith([plain], { picked: "dir:" });
  const got = controlLabels(lib.library(state, noop, { kind: "code" }));
  assert.deepEqual(got, ["new file", "new folder"], got.join(" "));
});

// The root holds the whole checkout, so "delete folder" there is not a folder
// operation — there would be nothing left to be standing in.
test("annotations are counted through their nesting", () => {
  assert.equal(lib.countAnnotations(source.annotations), 2);
  assert.equal(lib.countAnnotations([]), 0);
});

// A folder can be acted on without being opened first. Deleting one should not
// require expanding it, and the controls belong under the row they act on rather
// than at the bottom of whatever it contains.
test("a picked folder offers its controls while still closed", () => {
  const state = stateWith([plain], { picked: "dir:Communique" });
  const got = controlLabels(lib.library(state, noop, { kind: "code" }));
  assert.deepEqual(got, ["new file", "new folder", "delete folder"], got.join(" "));
});

// A file is different, and the difference is not cosmetic: an edit and a delete
// each carry the digest of what was on screen, and there is no digest of a file
// nobody has opened.
test("a picked file offers nothing until it has been read", () => {
  const open = {
    "dir:Communique": true, "dir:Communique/internal": true,
    "dir:Communique/internal/cli": true,
  };
  const unread = stateWith([plain], { open, picked: `file:${plain.path}` });
  assert.deepEqual(controlLabels(lib.library(unread, noop, { kind: "code" })), []);

  const read = stateWith([plain], {
    open: { ...open, [`file:${plain.path}`]: true },
    picked: `file:${plain.path}`,
    files: { [lib.fileKey(plain)]: { text: "package cli\n" } },
  });
  assert.deepEqual(controlLabels(lib.library(read, noop, { kind: "code" })), ["edit", "delete"]);
});

// A file the mirror could not carry has no text to edit and no digest to delete
// against, so it says why it was skipped and offers nothing.
test("a skipped file offers nothing to do to it", () => {
  const skipped = { ...plain, path: "Communique/big.bin", skipped: "it is not text" };
  const state = stateWith([skipped], {
    open: { "dir:Communique": true, [`file:${skipped.path}`]: true },
    picked: `file:${skipped.path}`,
    files: { [lib.fileKey(skipped)]: { text: "" } },
  });
  assert.deepEqual(controlLabels(lib.library(state, noop, { kind: "code" })), []);
});
