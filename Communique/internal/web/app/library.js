// The library: the repository, folded.
//
// Two tabs over one tree. Documents are the files Dock found § sections in, and
// code is everything else — the same data, filtered, because "what is written
// about this" and "what does it do" are different questions and answering both
// in one list would serve neither.
//
// Nothing here fetches. The tree arrives as structure without text, so every
// file and every section can be listed and folded before anything is read; a
// file's contents are asked for when a reader opens it, and once. That is what
// makes folding worth having rather than a way of hiding what was already
// downloaded.

import { h, ellipsis } from "./dom.js";
import { render } from "./markdown.js";
import { block } from "./highlight.js";
import { digest } from "./digest.js";

// plural counts a thing without the "1 files" that reads as a bug in a tool
// that is otherwise careful about its words.
export function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

// tree turns the flat list of paths into nested directories.
//
// The server sends paths because a path is what addresses a file; the nesting is
// presentation, and rebuilding it here keeps the wire format flat and obvious.
export function tree(files) {
  const root = { dirs: new Map(), files: [] };
  for (const file of files) {
    const parts = file.path.split("/");
    let node = root;
    for (const dir of parts.slice(0, -1)) {
      if (!node.dirs.has(dir)) node.dirs.set(dir, { dirs: new Map(), files: [] });
      node = node.dirs.get(dir);
    }
    node.files.push(file);
  }
  return root;
}

// counts totals a directory, so a folded one still says how much is inside it.
export function counts(node) {
  let files = node.files.length;
  let lines = node.files.reduce((n, f) => n + (f.lines || 0), 0);
  for (const child of node.dirs.values()) {
    const inner = counts(child);
    files += inner.files;
    lines += inner.lines;
  }
  return { files, lines };
}

// emptyReason says why there is nothing to show, or nothing when there is.
//
// Three different things produce an empty tab and they have three different
// remedies, so they get three different sentences. Telling somebody that no
// machine mirrors a repository when the truth is that the request failed sends
// them to the wrong machine entirely.
export function emptyReason(state) {
  const lib = state.library;

  if (lib && lib.unreachable) {
    return [
      h("p", { class: "error" }, `the repository could not be fetched: ${lib.unreachable}`),
      h("p", { class: "muted" }, "an older cq on the server has no library to serve; rebuild and restart it"),
    ];
  }
  if (!lib) {
    return [h("p", { class: "muted" }, "no machine is mirroring a repository")];
  }
  if ((lib.files || []).length === 0) {
    return [
      h("p", { class: "muted" }, "nothing mirrored yet"),
      // The setting, and where it has to be set. It is read by `cq sync` on the
      // agent machine, which is not the machine serving this page — the single
      // most common way to set it and see no effect.
      h("p", { class: "muted" }, "set CQ_LIBRARY on the agent machine and sync; `cq status` there says whether it is set"),
    ];
  }
  return null;
}

// isDocument reports whether Dock found sections in a file. It is the split
// between the two tabs, and it is Dock's answer rather than a guess from the
// extension: a .md file with no § headings is not a document Dock can address.
export function isDocument(file) {
  return (file.sections || []).length > 0;
}

export function library(state, actions, { kind }) {
  const ctx = { state, actions };
  const empty = emptyReason(state);
  if (empty) return empty;

  const all = state.library.files || [];

  const files = all.filter((f) => (kind === "docs" ? isDocument(f) : !isDocument(f)));
  const notes = state.library.notes || [];

  if (files.length === 0) {
    // Why it is empty, when the collection knows why. Saying "no document
    // carries a § section" because `dock` was not installed is a confident
    // claim about the reader's files that happens to be false, and it sends
    // them to look in the wrong place.
    if (kind === "docs" && notes.length > 0) {
      return notes.map((n) => h("p", { class: "warn" }, n));
    }
    return [h("p", { class: "muted" }, kind === "docs"
      ? "no document carries a § section yet"
      : "every mirrored file is a document")];
  }

  const out = [];
  if (state.library.truncated) {
    // Said, not silently true: a reader who cannot find a file has to be able
    // to tell that from the file not existing.
    out.push(h("p", { class: "warn" }, state.library.truncated));
  }
  for (const note of notes) out.push(h("p", { class: "warn" }, note));
  const root = tree(files);
  out.push(...dirNodes(root, "", ctx, 0));
  // The root gets the same controls as any other directory. Without them the
  // one place you cannot make a file is the top of the tree, which is where a
  // new module starts.
  out.push(dirControls("", root, ctx));
  return out;
}

// fileKey identifies a file in the read-text cache.
//
// The machine is part of it because a path is only unique within one checkout.
// Two machines mirroring trees that both hold `Docs/Vision.md` would otherwise
// share one entry — showing one machine's text under the other's file, and
// handing an edit the wrong digest to claim it was made against.
export function fileKey(file) {
  return `${file.machine || ""}\u0000${file.path}`;
}

// dirNodes draws one directory: its subdirectories, then its files.
function dirNodes(node, prefix, ctx, depth) {
  const out = [];
  for (const [name, child] of [...node.dirs.entries()].sort()) {
    const path = prefix ? `${prefix}/${name}` : name;
    const inner = counts(child);
    out.push(fold({
      key: `dir:${path}`,
      ctx,
      depth,
      label: h("span", { class: "dir" }, `${name}/`),
      note: `${plural(inner.files, "file")} · ${plural(inner.lines, "line")}`,
      controls: () => dirControls(path, child, ctx),
      children: () => dirNodes(child, path, ctx, depth + 1),
    }));
  }
  for (const file of node.files.sort((a, b) => a.path.localeCompare(b.path))) {
    out.push(fileNode(file, ctx, depth));
  }
  return out;
}

// dirControls are what can be done to a directory.
//
// The machine comes from a file inside it, because a directory is not itself
// mirrored — it exists because things are in it, and an action has to name the
// machine that holds them.
function dirControls(path, node, ctx) {
  if (ctx.state.picked !== `dir:${path}`) return null;
  const machine = machineIn(node);

  return h("div", { class: "controls row" },
    h("button", { class: "quiet", onclick: () => ctx.actions.newFile(machine, path) }, "new file"),
    h("button", { class: "quiet", onclick: () => ctx.actions.newFolder(machine, path) }, "new folder"),
    // The root is the checkout itself. Removing it is not a folder operation,
    // and there is no `..` to be standing in afterwards.
    path === "" ? null : h("button", {
      class: "quiet danger",
      onclick: () => ctx.actions.removeFolder(machine, path, filesUnder(node), hollow(node)),
    }, "delete folder"),
  );
}

// filesUnder is every file the mirror is showing inside a directory, at any
// depth — the manifest a recursive removal carries, and the number the operator
// is asked to confirm.
//
// Skipped files are in it too. They are files the mirror knows about and named a
// reason for, so they are part of what the operator was shown, and leaving them
// out would make the agent refuse over a file that is on screen.
export function filesUnder(node) {
  const out = [];
  const walk = (n) => {
    for (const f of n.files) out.push(f.path);
    for (const child of n.dirs.values()) walk(child);
  };
  walk(node);
  return out.sort();
}

// hollow reports a directory with nothing in it at all — no files anywhere
// under it, and no subdirectories either.
//
// It picks the verb. A hollow one gets `rmdir`, which is the narrowest thing
// that does the job and gives the clearest refusal if it turns out not to be
// empty after all. Anything else gets the recursive verb and the manifest.
function hollow(node) {
  return node.files.length === 0 && node.dirs.size === 0;
}

// machineIn finds which machine holds a directory's contents.
function machineIn(node) {
  for (const f of node.files) if (f.machine) return f.machine;
  for (const child of node.dirs.values()) {
    const found = machineIn(child);
    if (found) return found;
  }
  return "";
}

function fileNode(file, ctx, depth) {
  const name = file.path.split("/").pop();
  const note = file.skipped
    ? file.skipped
    : `${plural(file.lines, "line")}${structureNote(file)}`;

  return fold({
    key: `file:${file.path}`,
    ctx,
    depth,
    label: h("span", { class: "file" }, name),
    note,
    // Opening a file is what asks for its text. Until then the row is drawn
    // from the tree alone, which is why the whole repository can be listed.
    onOpen: () => ctx.actions.openFile(file),
    // A file's controls need it to have been read: an edit and a delete each
    // carry the digest of what was on screen, and there is no digest of a file
    // nobody has opened. So unlike a folder's, these appear only once it is.
    controls: () => fileControls(file, ctx),
    children: () => fileBody(file, ctx, depth + 1),
  });
}

function structureNote(file) {
  const bits = [];
  const sections = (file.sections || []).length;
  const annotations = countAnnotations(file.annotations || []);
  if (sections > 0) bits.push(plural(sections, "section"));
  if (annotations > 0) bits.push(plural(annotations, "annotation"));
  return bits.length > 0 ? ` · ${bits.join(" · ")}` : "";
}

export function countAnnotations(nodes) {
  return nodes.reduce((n, a) => n + 1 + countAnnotations(a.children || []), 0);
}

// fileBody draws what is inside a file: its structure, and its text once read.
function fileBody(file, ctx, depth) {
  const state = ctx.state;
  const loaded = state.files ? state.files[fileKey(file)] : null;
  if (file.skipped) {
    return [h("p", { class: "muted" }, file.skipped)];
  }
  if (!loaded) return [h("p", { class: "muted" }, "reading…")];
  if (loaded.error) return [h("p", { class: "error" }, loaded.error)];

  const lines = (loaded.text || "").split("\n");
  const out = [];

  // Dock's sections and Anno's annotations are both foldable spans over the same
  // lines, so both are drawn the same way and a file may have either or both.
  for (const section of file.sections || []) {
    out.push(fold({
      key: `sec:${file.path}:${section.number}`,
      ctx,
      depth,
      label: h("span", { class: "sect" }, `§${section.number} ${section.name}`),
      note: plural(section.lines, "line"),
      children: () => [
        prose(file, slice(lines, section.start, section.end)),
        h("div", { class: "controls" },
          h("button", {
            class: "quiet",
            onclick: () => ctx.actions.editSpan(file, loaded.text || "",
              section.start, section.end, `§${section.number} ${section.name}`),
          }, "edit this section"),
        ),
      ],
    }));
  }
  for (const node of file.annotations || []) {
    out.push(annotationNode(file, node, lines, ctx, depth));
  }

  // A file with no structure is just its text, and hiding that behind another
  // fold would be a fold that always has exactly one thing in it.
  if (out.length === 0) {
    out.push(prose(file, loaded.text || ""));
  }
  return out;
}

// fileControls are what can be done to a whole file.
//
// They appear only once the file has been opened — the destructive one should be
// reached only by somebody who has looked at what they are deleting, and an edit
// carries the digest of text nobody has fetched yet otherwise.
function fileControls(file, ctx) {
  if (ctx.state.picked !== `file:${file.path}`) return null;
  const loaded = ctx.state.files ? ctx.state.files[fileKey(file)] : null;
  if (!loaded || loaded.error || file.skipped) return null;
  const text = loaded.text || "";
  return h("div", { class: "controls row" },
    h("button", {
      class: "quiet",
      onclick: () => ctx.actions.editFile(file, text),
    }, "edit"),
    h("button", {
      class: "quiet danger",
      onclick: () => ctx.actions.deleteFile(file),
    }, "delete"),
  );
}

function annotationNode(file, node, lines, ctx, depth) {
  return fold({
    key: `ann:${file.path}:${node.kind}:${node.name}:${node.start}`,
    ctx,
    depth,
    label: h("span", { class: "annot" }, `${node.kind} ${node.name}`),
    note: `${plural(node.lines, "line")}${node.meta && node.meta.length ? ` · ${node.meta.join(" ")}` : ""}`,
    children: () => {
      const inner = (node.children || []).map((c) => annotationNode(file, c, lines, ctx, depth + 1));
      // The annotation's own content, plus whatever is nested inside it. Both,
      // because a symbol's body is worth reading and so are its parts.
      return [
        block(slice(lines, node.content_start, node.content_end), file.path),
        h("div", { class: "controls" },
          h("button", {
            class: "quiet",
            onclick: () => ctx.actions.editSpan(file, wholeOf(ctx, file),
              node.content_start, node.content_end, `${node.kind} ${node.name}`),
          }, "edit this annotation"),
        ),
        ...inner,
      ];
    },
  });
}

// wholeOf is the file's whole text, which a span edit needs in order to splice
// its change back into it.
function wholeOf(ctx, file) {
  const loaded = ctx.state.files ? ctx.state.files[fileKey(file)] : null;
  return (loaded && loaded.text) || "";
}

function slice(lines, start, end) {
  // A span of 0:0 is a heading with nothing under it of its own, which Dock
  // reports for a document somebody has started and not written yet. Slicing it
  // as though it began at line 1 would show the reader the wrong lines.
  if (start === 0 && end === 0) return "";
  // Otherwise spans are one-based and inclusive, the way every Orc tool reports
  // them.
  return lines.slice(Math.max(start - 1, 0), end).join("\n");
}

// prose renders markdown for a document and highlighted source for anything
// else. Markdown has its own renderer and is not also highlighted: two of them
// over the same text would be one of them losing.
function prose(file, text) {
  if (file.path.endsWith(".md")) return h("div", { class: "body" }, render(text));
  return block(text, file.path);
}

// fold is one collapsible row.
//
// Openness lives in the application's state rather than in the DOM, so a redraw
// — which happens on every sync — does not collapse everything the reader had
// opened. That is the difference between a fold that works and one that fights.
function fold({ key, ctx, depth, label, note, children, controls, onOpen }) {
  const open = ctx.state.open ? ctx.state.open[key] : false;

  const head = h("button", {
    class: ctx.state.picked === key ? "fold picked" : "fold",
    "aria-expanded": open ? "true" : "false",
    "aria-current": ctx.state.picked === key ? "true" : null,
    // The depth is handed to CSS rather than turned into padding here: a phone
    // cannot afford a two-character indent five levels down a repository, and
    // that is a layout decision, which belongs in the stylesheet.
    //
    // An object, so it goes through the CSSOM. As a `style` attribute this never
    // reached the browser at all — the site's content policy has no
    // `unsafe-inline` — so every fold in the tree was drawn at depth zero. See
    // dom.js.
    style: { "--depth": depth },
    onclick: () => {
      if (!open && onOpen) onOpen();
      ctx.actions.toggle(key);
      // Picking is what puts this row's controls on screen. One row at a time,
      // because a tree with a button under every line is a tree nobody can read
      // — and the thing somebody is about to act on is the thing they just
      // touched.
      ctx.actions.pick(key);
    },
  },
    h("span", { class: "twist" }, open ? "▾" : "▸"),
    label,
    note ? h("span", { class: "muted note" }, note) : null,
  );

  // Controls sit on the row they belong to, at the right-hand end, and outside
  // the fold's contents. Outside, because a folder can be acted on without being
  // opened first — deleting one should not require expanding it — and because in
  // an open file they would otherwise be six hundred lines further down, past the
  // thing they act on.
  //
  // Beside the fold rather than inside it: the whole row is already a button, and
  // a button within a button is both invalid and a target nobody hits reliably.
  // So the two are siblings in a flex row, which is what puts them on one line
  // without either swallowing the other's clicks.
  const acts = controls ? controls() : null;
  const row = acts ? h("div", { class: "fold-row" }, head, acts) : head;

  if (!open) return row;
  return h("div", { class: "folded" }, row,
    // `tree`, because this one holds deeper folds rather than content. Each fold
    // carries its own indent, so the inset every other fold body takes would
    // compound once per level here and push a filename five levels down off the
    // side of a phone.
    h("div", { class: "inner tree" }, ...children()));
}

// crumbs is the header line: what is being read, and how much of it there is.
export function libraryHeader(state, kind) {
  const lib = state.library;
  if (!lib) return null;
  const files = (lib.files || []).filter((f) => (kind === "docs" ? isDocument(f) : !isDocument(f)));
  const lines = files.reduce((n, f) => n + (f.lines || 0), 0);
  return h("p", { class: "muted" },
    `${ellipsis(lib.root || "repository", 32)} · ${plural(files.length, "file")} · ${plural(lines, "line")}`);
}
