// The survey: the whole repository as shape and size, never as content.
//
// The code and docs tabs are for reading one thing. This is the other question —
// how big is it, where does the weight sit, what is this made of — and it is an
// admin view because that is the panel about the state of things rather than
// about any one message or file.
//
// It carries no text, and not only because the tree endpoint sends none: a
// survey that could show a file's contents would be a second, worse code tab,
// and the two would drift. Counts here, contents there.

import { h, ellipsis } from "./dom.js";
import { tree, counts, plural, emptyReason } from "./library.js";

// BAR is the width of a share bar, in characters. Small enough to sit in a
// column beside a name, wide enough that a tenth is visible.
const BAR = 12;

// bar draws a share of a total the way the task board draws progress, because a
// reader who has learned one of them has learned the other.
export function bar(part, whole, width = BAR) {
  if (whole <= 0) return h("span", { class: "muted" }, "—");
  const filled = Math.max(0, Math.min(Math.round((part / whole) * width), width));
  return h("span", { class: "meter" },
    h("span", { class: "filled" }, "▓".repeat(filled)),
    h("span", { class: "empty" }, "░".repeat(width - filled)));
}

// share renders a percentage the way a reader reads one: whole numbers, and
// never "0%" for something that is present.
export function share(part, whole) {
  if (whole <= 0) return "—";
  const pct = (part / whole) * 100;
  if (pct > 0 && pct < 1) return "<1%";
  return `${Math.round(pct)}%`;
}

// extension is what a file is, for the purpose of counting kinds of thing. A
// file with no dot in its name is its own name: `Makefile` is a kind.
export function extension(path) {
  const name = path.split("/").pop();
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot) : name;
}

// byExtension totals the tree by kind, largest first.
export function byExtension(files) {
  const totals = new Map();
  for (const f of files) {
    const key = extension(f.path);
    const got = totals.get(key) || { ext: key, files: 0, lines: 0 };
    got.files++;
    got.lines += f.lines || 0;
    totals.set(key, got);
  }
  return [...totals.values()].sort((a, b) => b.lines - a.lines || a.ext.localeCompare(b.ext));
}

// widest returns the files carrying the most lines, largest first.
export function widest(files, n = 8) {
  return [...files]
    .filter((f) => (f.lines || 0) > 0)
    .sort((a, b) => b.lines - a.lines || a.path.localeCompare(b.path))
    .slice(0, n);
}

// spread describes how the lines are distributed across the files.
//
// The median is reported beside the mean because they answer different
// questions and disagree in the interesting case: a tree of small files with
// three enormous ones has a mean nothing resembles and a median that does.
export function spread(files) {
  const lines = files.map((f) => f.lines || 0).filter((n) => n > 0).sort((a, b) => a - b);
  if (lines.length === 0) return { files: 0, lines: 0, mean: 0, median: 0, largest: 0 };

  const total = lines.reduce((a, b) => a + b, 0);
  const mid = Math.floor(lines.length / 2);
  return {
    files: lines.length,
    lines: total,
    mean: Math.round(total / lines.length),
    median: lines.length % 2 ? lines[mid] : Math.round((lines[mid - 1] + lines[mid]) / 2),
    largest: lines[lines.length - 1],
  };
}

// survey draws the whole thing.
export function survey(state) {
  const lib = state.library;
  const notes = (lib && lib.notes) || [];
  if (!lib || !(lib.files || []).length) {
    // A lens that could not be run is a different answer from an empty
    // repository, and only one of them is about the files.
    if (notes.length > 0) return notes.map((n) => h("p", { class: "warn" }, n));
    return emptyReason(state) || [];
  }

  const files = lib.files;
  const all = spread(files);

  return [
    h("article", { class: "card" },
      h("h2", {}, `${ellipsis(lib.root || "repository", 32)} — survey`),
      h("div", { class: "meta" },
        `${plural(all.files, "file")} · ${plural(all.lines, "line")} · `,
        `${all.median} median · ${all.mean} mean · ${all.largest} largest`),
      h("div", { class: "body" },
        h("h3", {}, "by directory"),
        directories(files, all.lines),
        h("h3", {}, "by kind"),
        kinds(files, all.lines),
        h("h3", {}, "the largest files"),
        largest(files),
      )),
  ];
}

// directories totals each top-level directory. One level, not the whole tree:
// this answers "where is the weight", and a fully expanded tree answers "what
// is in here", which is what the code tab is for.
function directories(files, total) {
  const root = tree(files);
  const rows = [];

  for (const [name, child] of [...root.dirs.entries()].sort()) {
    rows.push({ name: `${name}/`, ...counts(child) });
  }
  if (root.files.length > 0) {
    rows.push({
      name: "(root)",
      files: root.files.length,
      lines: root.files.reduce((n, f) => n + (f.lines || 0), 0),
    });
  }
  rows.sort((a, b) => b.lines - a.lines || a.name.localeCompare(b.name));

  return h("div", { class: "survey" }, ...rows.flatMap((r) => [
    h("span", { class: "dir" }, r.name),
    h("span", { class: "num" }, String(r.files)),
    h("span", { class: "num" }, String(r.lines)),
    bar(r.lines, total),
    h("span", { class: "num muted" }, share(r.lines, total)),
  ]));
}

function kinds(files, total) {
  return h("div", { class: "survey" }, ...byExtension(files).flatMap((k) => [
    h("span", { class: "file" }, k.ext),
    h("span", { class: "num" }, String(k.files)),
    h("span", { class: "num" }, String(k.lines)),
    bar(k.lines, total),
    h("span", { class: "num muted" }, share(k.lines, total)),
  ]));
}

// largest compares the files with each other, not with the repository.
//
// Drawn against the total, every one of these is a slice of a percent and the
// bars are indistinguishable — which is the one thing this section exists to
// show. So the biggest file is the full bar and the rest are read against it,
// and the share of the whole is dropped rather than shown as "<1%" eight times.
function largest(files) {
  const rows = widest(files);
  const top = rows.length > 0 ? rows[0].lines : 0;

  return h("div", { class: "survey" }, ...rows.flatMap((f) => [
    h("span", { class: "file", title: f.path }, ellipsis(f.path, 44)),
    h("span", { class: "num" }, ""),
    h("span", { class: "num" }, String(f.lines)),
    bar(f.lines, top),
    h("span", { class: "num muted" }, ""),
  ]));
}
