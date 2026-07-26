// Permission clauses: reading them, colouring them, and explaining them.
//
// A clause is `kind(argument)` — `read(Anno/**)`, `spawn(24)`, `orc(assign)` —
// and it is the densest thing anybody types into cq. It is also the thing with
// the worst failure mode: a clause that does not parse is refused by Orc with a
// good message, but a clause that parses and says something *other* than what was
// meant is a permission that quietly works wrongly. `read(Ano/**)` is valid.
//
// So this module does two jobs, and both are about making the wrong thing look
// wrong before it is queued: it colours the parts so an argument is visibly an
// argument, and it says what it could not read, in Orc's own words.
//
// It is a reader, not an authority. Orc validates on the way in and its answer is
// the one that counts — a check here that disagreed would be a second opinion
// about who may do what, and the wrong one would be the one on screen. What this
// catches is the typo, before a round trip. What it never does is *accept*: a
// clause this module cannot read is still sent, because Orc knows patterns this
// does not and being unable to explain something is not grounds for refusing it.
//
// The highlighter follows highlight.js's one rule: **the text must survive**. The
// characters that come out are exactly the characters that went in, in order, so
// nobody is ever shown a clause that is not the clause they typed.

import { h } from "./dom.js";

// KINDS are Orc's five, with what each one takes and what it means.
//
// The wording is Orc's Reference, shortened: two descriptions of the same rule
// that drift apart are worse than one that is terse.
export const KINDS = [
  { kind: "read", takes: "a path glob", example: "read(Anno/**)",
    means: "may read these files" },
  { kind: "write", takes: "a path glob", example: "write(Anno/internal/**)",
    means: "may edit them — write implies read" },
  { kind: "spawn", takes: "a load budget", example: "spawn(24)",
    means: "how much thinking it may employ at once" },
  { kind: "orc", takes: "one verb", example: "orc(assign)",
    means: "narrows which orc verbs it may run" },
  { kind: "tool", takes: "one name", example: "tool(anno)",
    means: "a named capability in another orc tool" },
];

const NAMES = new Set(KINDS.map((k) => k.kind));

// MAX_LOAD is model.MaxLoad. A budget above it is a number somebody meant as
// something else.
export const MAX_LOAD = 4096;

// read splits one clause into its parts and says what is wrong with it.
//
// The return is always a whole clause: `kind`, `arg`, and the exact text, plus an
// `error` when it could not be read. Nothing is dropped, because the highlighter
// draws from this and the text has to survive.
export function read(text) {
  const raw = String(text ?? "");
  const trimmed = raw.trim();
  if (!trimmed) return { raw, kind: "", arg: "", error: null };

  const open = trimmed.indexOf("(");
  if (open < 0 || !trimmed.endsWith(")")) {
    return { raw, kind: trimmed, arg: "", error: "must be written kind(argument)" };
  }

  const kind = trimmed.slice(0, open).trim().toLowerCase();
  const arg = trimmed.slice(open + 1, -1).trim();

  if (!NAMES.has(kind)) {
    return { raw, kind, arg, error: `unknown kind — try ${[...NAMES].join(", ")}` };
  }
  if (!arg) return { raw, kind, arg, error: "has an empty argument" };

  if (kind === "spawn") {
    if (!/^\d+$/.test(arg) || Number(arg) > MAX_LOAD) {
      return { raw, kind, arg, error: `spawn takes a load budget from 0 to ${MAX_LOAD}` };
    }
    return { raw, kind, arg, error: null };
  }

  if (kind === "read" || kind === "write") {
    // Orc's cleanGlob, in the two ways it refuses: a path that starts outside the
    // workspace, and one that climbs out of it.
    if (arg.startsWith("/") || arg.startsWith("~")) {
      return { raw, kind, arg, error: "a clause is relative to the workspace, so it cannot start at /" };
    }
    if (arg.split("/").includes("..")) {
      return { raw, kind, arg, error: "a clause cannot climb out of the workspace with .." };
    }
    if (arg === "." || arg === "./") {
      return { raw, kind, arg, error: "selects nothing; use ** for everything" };
    }
    return { raw, kind, arg, error: null };
  }

  // orc and tool name one thing.
  if (/[/\s]/.test(arg)) {
    return { raw, kind, arg, error: `${kind}() names one thing, so no slash or space` };
  }
  return { raw, kind, arg, error: null };
}

// split breaks a line of clauses on whitespace, keeping nothing empty.
export function split(text) {
  return String(text ?? "").split(/\s+/).filter(Boolean);
}

// problems returns one message per unreadable clause, for a form to show before
// it queues anything.
export function problems(text) {
  return split(text)
    .map((c) => ({ clause: c, ...read(c) }))
    .filter((c) => c.error)
    .map((c) => `${c.clause} — ${c.error}`);
}

// highlight draws a line of clauses as coloured nodes.
//
// Every character of the input appears exactly once in the output, including the
// spaces between clauses: a highlighter that tidied whitespace would be showing
// somebody a line they did not type.
export function highlight(text) {
  const raw = String(text ?? "");
  const out = [];
  // Split on whitespace but keep it, so the gaps survive.
  const parts = raw.split(/(\s+)/);
  for (const part of parts) {
    if (!part) continue;
    if (/^\s+$/.test(part)) {
      out.push(document.createTextNode(part));
      continue;
    }
    out.push(...clauseNodes(part));
  }
  return out;
}

// clauseNodes draws one clause, part by part.
function clauseNodes(text) {
  const got = read(text);
  if (got.error) {
    // Unreadable: drawn whole and marked, rather than guessed at. A half-coloured
    // broken clause reads as though the coloured half were fine.
    return [h("span", { class: "cl-bad", title: got.error }, text)];
  }

  const open = text.indexOf("(");
  return [
    h("span", { class: `cl-kind cl-${got.kind}` }, text.slice(0, open)),
    h("span", { class: "cl-paren" }, "("),
    h("span", { class: "cl-arg" }, text.slice(open + 1, -1)),
    h("span", { class: "cl-paren" }, ")"),
  ];
}

// chip draws one clause as a standalone element, for a list rather than a line.
export function chip(text, extra = null) {
  return h("span", { class: "clause" }, ...highlight(text), extra);
}

// cheatsheet is the reference that sits under the box somebody is typing into.
//
// It is open by default the first time and remembers being closed, because the
// person who needs it needs it once and the person who does not should not have
// to dismiss it on every edit.
export function cheatsheet(open = true) {
  return h("details", { class: "cheatsheet", open: open ? "" : null },
    h("summary", {}, "clause cheat sheet"),
    h("table", { class: "cheat" },
      h("tbody", {},
        ...KINDS.map((k) => h("tr", {},
          h("td", {}, ...highlight(k.example)),
          h("td", { class: "muted" }, k.takes),
          h("td", { class: "muted" }, k.means))))),
    h("p", { class: "muted hint" },
      "globs are relative to the workspace: ", h("code", {}, "**"),
      " crosses directories, ", h("code", {}, "Anno/"), " and ", h("code", {}, "Anno/**"),
      " mean the same thing, and ", h("code", {}, "write"), " implies ", h("code", {}, "read"),
      ". separate clauses with spaces."),
  );
}
