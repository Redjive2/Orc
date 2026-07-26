// Permission clauses: reading them, colouring them, and explaining them.
//
// A clause is `kind(argument)` — `read(Anno/** Dock/**)`, `write(** except Docs/**)`,
// `spawn(24)` — and it is the densest thing anybody types into cq. It is also the
// thing with the worst failure mode: a clause that does not parse is refused by
// Orc with a good message, but a clause that parses and says something *other*
// than what was meant is a permission that quietly works wrongly. `read(Ano/**)`
// is valid. So is `orc(policy)`, which controls nothing at all.
//
// So this module does three jobs, and all three are about making the wrong thing
// look wrong before it is queued: it colours the parts so an argument is visibly
// an argument and an exception visibly an exception, it says what it could not
// read, and it offers the words — the actual orc verbs and tool capabilities the
// fleet checks — rather than leaving somebody to guess them.
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

// EXCEPT separates what a clause allows from what it takes back out. A word
// rather than punctuation, for the reason Orc gives: a clause is read aloud more
// often than it is typed.
export const EXCEPT = "except";

// KINDS are Orc's five, with what each one takes and what it means.
//
// The wording is Orc's Reference, shortened: two descriptions of the same rule
// that drift apart are worse than one that is terse.
export const KINDS = [
  { kind: "read", takes: "path globs", example: "read(Anno/** Dock/**)",
    means: "may read these files" },
  { kind: "write", takes: "path globs", example: "write(** except Docs/**)",
    means: "may edit them — write implies read" },
  { kind: "spawn", takes: "one budget", example: "spawn(24)",
    means: "how much thinking it may employ at once" },
  { kind: "orc", takes: "verbs", example: "orc(new assign)",
    means: "narrows which orc verbs it may run" },
  { kind: "tool", takes: "capabilities", example: "tool(upgrade)",
    means: "a named capability in another orc tool" },
];

const NAMES = new Set(KINDS.map((k) => k.kind));

// MAX_LOAD is model.MaxLoad. A budget above it is a number somebody meant as
// something else.
export const MAX_LOAD = 4096;

// FALLBACK_WORDS is what the sheet offers when the fleet carried no vocabulary —
// an older Orc, or a machine that could not be reached.
//
// A short list rather than a stale copy of the full one: these are the words that
// have to exist for the *syntax* to be worth showing at all, and anything more
// would be a second privilege list quietly going out of date. When the fleet
// carries its own, this is not used.
const FALLBACK_WORDS = {
  verbs: [{ word: "new" }, { word: "assign" }, { word: "employ" }, { word: "remove" }],
  tools: [{ word: "upgrade", in: "cq" }],
};

// read splits one clause into its parts and says what is wrong with it.
//
// The return is always a whole clause: `kind`, `terms`, `excepts`, and the exact
// text, plus an `error` when it could not be read and `notes` for what parsed but
// probably does not mean what somebody hoped. Nothing is dropped, because the
// highlighter draws from this and the text has to survive.
export function read(text, words) {
  const raw = String(text ?? "");
  const trimmed = raw.trim();
  const empty = { raw, kind: "", terms: [], excepts: [], error: null, notes: [] };
  if (!trimmed) return empty;

  const open = trimmed.indexOf("(");
  if (open < 0 || !trimmed.endsWith(")")) {
    return { ...empty, kind: trimmed, error: "must be written kind(argument)" };
  }

  const kind = trimmed.slice(0, open).trim().toLowerCase();
  const body = trimmed.slice(open + 1, -1).trim();
  const got = { ...empty, kind };

  if (!NAMES.has(kind)) {
    return { ...got, error: `unknown kind — try ${[...NAMES].join(", ")}` };
  }
  if (!body) return { ...got, error: "has an empty argument" };

  // A budget is a number, so it is none of what follows: no list, no exception.
  if (kind === "spawn") {
    if (!/^\d+$/.test(body) || Number(body) > MAX_LOAD) {
      return { ...got, terms: [body],
        error: `spawn takes one load budget from 0 to ${MAX_LOAD}; a budget is a number, ` +
          `so it has no list and no ${EXCEPT}` };
    }
    return { ...got, terms: [body] };
  }

  const fields = body.split(/\s+/).filter(Boolean);
  const at = fields.findIndex((f) => f.toLowerCase() === EXCEPT);
  if (fields.filter((f) => f.toLowerCase() === EXCEPT).length > 1) {
    return { ...got, error: `says ${EXCEPT} twice; one list is taken out, so there is one ${EXCEPT}` };
  }
  const terms = at < 0 ? fields : fields.slice(0, at);
  const excepts = at < 0 ? [] : fields.slice(at + 1);
  got.terms = terms;
  got.excepts = excepts;

  if (at === 0) {
    return { ...got, error: `starts with ${EXCEPT}, so it allows nothing; say what it allows first` };
  }
  if (at >= 0 && excepts.length === 0) {
    return { ...got, error: `ends with ${EXCEPT} and takes nothing out` };
  }

  for (const term of [...terms, ...excepts]) {
    const wrong = termProblem(kind, term);
    if (wrong) return { ...got, error: `${term} — ${wrong}` };
  }

  // Notes, not errors. Orc parses a clause naming a verb it does not check and so
  // does this; what it does is nothing, and saying so is the entire reason the
  // vocabulary travels with the fleet.
  got.notes = [...terms, ...excepts]
    .map((term) => unknownWord(kind, term, words))
    .filter(Boolean);
  return got;
}

// termProblem returns what is wrong with one term, or null.
function termProblem(kind, term) {
  if (kind === "read" || kind === "write") {
    // Orc's cleanGlob, in the ways it refuses: a path that starts outside the
    // workspace, and one that climbs out of it.
    if (term.startsWith("/") || term.startsWith("~")) {
      return "a clause is relative to the workspace, so it cannot start at /";
    }
    if (term.split("/").includes("..")) {
      return "a clause cannot climb out of the workspace with ..";
    }
    if (term === "." || term === "./") return "selects nothing; use ** for everything";
    return null;
  }
  // orc and tool name words, not paths. Wildcards are fine — `orc(re*)` is a
  // pattern over verbs exactly as `read(Anno/*)` is one over paths.
  if (term.includes("/")) return `${kind}() names verbs, not paths, so a term cannot contain a slash`;
  if (/[()]/.test(term)) return "has a parenthesis in a term; clauses do not nest";
  return null;
}

// unknownWord returns a note when a verb or capability is one nothing checks.
function unknownWord(kind, term, words) {
  if (kind !== "orc" && kind !== "tool") return null;
  if (/[*?[\]]/.test(term)) return null; // a glob is not a word
  const known = vocabulary(words)[kind === "orc" ? "verbs" : "tools"];
  if (known.some((w) => w.word === term.toLowerCase())) return null;
  return `${term} is not a ${kind === "orc" ? "verb" : "capability"} anything checks — ` +
    "it parses, and it controls nothing";
}

// vocabulary normalises whatever the fleet carried, falling back when it carried
// nothing.
export function vocabulary(words) {
  const verbs = words && words.verbs && words.verbs.length ? words.verbs : FALLBACK_WORDS.verbs;
  const tools = words && words.tools && words.tools.length ? words.tools : FALLBACK_WORDS.tools;
  return { verbs, tools };
}

// split breaks a line into clauses.
//
// On parentheses rather than on whitespace, because a clause now contains spaces:
// `read(a/** b/**) spawn(2)` is two clauses and splitting it on spaces would make
// four fragments, none of which parses. Text outside any parentheses still splits
// on whitespace, so a half-typed `read` is one item and gets its own message.
export function split(text) {
  const out = [];
  let current = "";
  let depth = 0;
  for (const ch of String(text ?? "")) {
    if (ch === "(") depth++;
    if (ch === ")") depth = Math.max(0, depth - 1);
    if (/\s/.test(ch) && depth === 0) {
      if (current) out.push(current);
      current = "";
      continue;
    }
    current += ch;
  }
  if (current) out.push(current);
  return out;
}

// problems returns one message per unreadable clause, for a form to show before
// it queues anything.
export function problems(text, words) {
  return split(text)
    .map((c) => ({ clause: c, ...read(c, words) }))
    .filter((c) => c.error)
    .map((c) => `${c.clause} — ${c.error}`);
}

// notes returns what parsed and probably does not mean what somebody hoped.
export function notes(text, words) {
  return split(text).flatMap((c) => read(c, words).notes);
}

// highlight draws a line of clauses as coloured nodes.
//
// Every character of the input appears exactly once in the output, including the
// spaces between and inside clauses: a highlighter that tidied whitespace would be
// showing somebody a line they did not type.
export function highlight(text, words) {
  const raw = String(text ?? "");
  const out = [];
  let rest = raw;
  while (rest) {
    const lead = rest.match(/^\s+/);
    if (lead) {
      out.push(document.createTextNode(lead[0]));
      rest = rest.slice(lead[0].length);
      continue;
    }
    const piece = split(rest)[0] ?? rest;
    const at = rest.indexOf(piece);
    if (at > 0) {
      out.push(document.createTextNode(rest.slice(0, at)));
      rest = rest.slice(at);
    }
    out.push(...clauseNodes(piece, words));
    rest = rest.slice(piece.length);
  }
  return out;
}

// clauseNodes draws one clause, part by part.
function clauseNodes(text, words) {
  const got = read(text, words);
  if (got.error) {
    // Unreadable: drawn whole and marked, rather than guessed at. A half-coloured
    // broken clause reads as though the coloured half were fine.
    return [h("span", { class: "cl-bad", title: got.error }, text)];
  }

  const open = text.indexOf("(");
  const nodes = [
    h("span", { class: `cl-kind cl-${got.kind}` }, text.slice(0, open)),
    h("span", { class: "cl-paren" }, "("),
  ];
  // The body is walked rather than rebuilt from the parsed terms, so the exact
  // spacing somebody typed survives colouring.
  let seenExcept = false;
  for (const part of text.slice(open + 1, -1).split(/(\s+)/)) {
    if (!part) continue;
    if (/^\s+$/.test(part)) {
      nodes.push(document.createTextNode(part));
      continue;
    }
    if (part.toLowerCase() === EXCEPT) {
      seenExcept = true;
      nodes.push(h("span", { class: "cl-except" }, part));
      continue;
    }
    const note = got.notes.some((n) => n.startsWith(`${part} `));
    nodes.push(h("span", {
      class: `${seenExcept ? "cl-deny" : "cl-arg"}${note ? " cl-idle" : ""}`,
      title: note ? "nothing checks this word" : null,
    }, part));
  }
  nodes.push(h("span", { class: "cl-paren" }, ")"));
  return nodes;
}

// chip draws one clause as a standalone element, for a list rather than a line.
export function chip(text, words, extra = null) {
  return h("span", { class: "clause" }, ...highlight(text, words), extra);
}

// cheatsheet is the reference that sits under the box somebody is typing into.
//
// Three layers, because they answer different questions: the kinds, the shape of
// an argument, and — folded away until asked for — the actual words `orc()` and
// `tool()` accept on *this* fleet. The last is the one nobody can guess, and the
// one a browser is uniquely able to offer.
export function cheatsheet(words, open = true) {
  const known = vocabulary(words);
  return h("details", { class: "cheatsheet", open: open ? "" : null },
    h("summary", {}, "clause cheat sheet"),
    h("table", { class: "cheat" },
      h("tbody", {},
        ...KINDS.map((k) => h("tr", {},
          // The examples are about the *shape* of a clause, so they are drawn
          // without the fleet's vocabulary: a sheet that struck through its own
          // `orc(new assign)` because this fleet spells its verbs differently
          // would be arguing with itself.
          h("td", {}, ...highlight(k.example)),
          h("td", { class: "muted" }, k.takes),
          h("td", { class: "muted" }, k.means))))),
    h("p", { class: "muted hint" },
      "an argument is a list, and it can take things back out: ",
      h("code", {}, "read(Anno/** Dock/**)"), " and ",
      h("code", {}, `write(** ${EXCEPT} Docs/**)`), ". an exception always wins. ",
      h("code", {}, "**"), " crosses directories, ", h("code", {}, "Anno/"), " and ",
      h("code", {}, "Anno/**"), " mean the same thing, and ", h("code", {}, "write"),
      " implies ", h("code", {}, "read"), ". every kind but ", h("code", {}, "spawn"),
      " takes both halves."),
    wordSheet(known.verbs, "orc", "verbs orc checks — a clause naming anything else controls nothing", words),
    wordSheet(known.tools, "tool", "capabilities other tools ask about", words),
  );
}

// wordSheet renders one sub-sheet: the arguments one kind actually accepts.
function wordSheet(list, kind, note, words) {
  return h("details", { class: "cheatsheet inner" },
    h("summary", {}, `what ${kind}() takes`),
    h("p", { class: "muted hint" }, note),
    h("table", { class: "cheat" },
      h("tbody", {},
        ...list.map((w) => h("tr", {},
          // Highlighted with the same vocabulary, or the sheet would mark its own
          // words as ones nothing checks.
          h("td", {}, ...highlight(`${kind}(${w.word})`, words)),
          h("td", { class: "muted" }, w.does || ""),
          h("td", { class: "muted" }, w.in ? `checked by ${w.in}` : ""))))));
}
