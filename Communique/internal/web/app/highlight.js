// Syntax highlighting, deliberately shallow.
//
// It is a colouriser, not a parser. It knows comments, strings, numbers and a
// list of words per language, and nothing about structure — which is enough to
// read code by and cheap enough to run on a phone.
//
// One rule governs everything here: **the text must survive**. Highlighting is a
// layer over the source the way colour is a layer over a terminal, so the
// characters that come out are exactly the characters that went in, in order.
// A highlighter that dropped a backslash or swallowed a quote would be showing
// somebody a file that is not their file, which is worse than no colour at all.
// `highlight.test.js` asserts that on every fixture it has.
//
// Nodes are built directly, never HTML — the same discipline as markdown.js, and
// for the same reason: there is no escaping to get wrong if the DOM does it.

import { h } from "./dom.js";

// The languages, by the extension that names them. A language nobody listed is
// not an error: it is drawn plain, which is what an unknown file deserves.
const LANGUAGES = {
  ".go": {
    line: "//", block: ["/*", "*/"], quotes: `"'\``,
    words: `break case chan const continue default defer else fallthrough for func go goto
      if import interface map package range return select struct switch type var
      bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64
      rune string uint uint8 uint16 uint32 uint64 uintptr any
      true false nil iota make new len cap append copy delete panic recover`,
  },
  ".js": {
    line: "//", block: ["/*", "*/"], quotes: `"'\``,
    words: `async await break case catch class const continue debugger default delete do
      else export extends finally for from function if import in instanceof let new of
      return static super switch this throw try typeof var void while yield
      true false null undefined NaN Infinity`,
  },
  ".css": {
    line: null, block: ["/*", "*/"], quotes: `"'`,
    words: `important media supports keyframes import charset font-face
      grid flex block inline none auto inherit initial unset var calc`,
  },
  ".html": { line: null, block: ["<!--", "-->"], quotes: `"'`, words: "" },
  ".json": { line: null, block: null, quotes: `"`, words: "true false null" },
  ".sh": {
    line: "#", block: null, quotes: `"'`,
    words: `if then else elif fi for while do done case esac function return exit
      export local readonly set unset shift source echo cd test`,
  },
  ".md": null, // markdown is rendered, not highlighted
};

// language returns the rules for a path, or null for "draw it plain".
export function language(path) {
  const name = String(path || "");
  const dot = name.lastIndexOf(".");
  if (dot < 0) return null;
  const rules = LANGUAGES[name.slice(dot).toLowerCase()];
  if (!rules) return null;
  return { ...rules, words: new Set(String(rules.words).split(/\s+/).filter(Boolean)) };
}

const WORD = /[A-Za-z_$][A-Za-z0-9_$-]*/y;
const NUMBER = /[0-9][0-9_.xXa-fA-F]*/y;

// highlight turns source into painted nodes.
//
// It walks the text once, and every branch either emits the characters it
// consumed or falls through to emitting one character. That is what makes the
// output identical to the input whatever the file contains.
export function highlight(text, path) {
  const src = String(text ?? "");
  const rules = language(path);
  const out = document.createDocumentFragment();
  if (!rules) {
    out.append(document.createTextNode(src));
    return out;
  }

  let plain = "";
  const flush = () => {
    if (plain) {
      out.append(document.createTextNode(plain));
      plain = "";
    }
  };
  const paint = (cls, s) => {
    flush();
    out.append(h("span", { class: cls }, s));
  };

  let i = 0;
  while (i < src.length) {
    // A line comment runs to the end of the line, and the newline is not part
    // of it: keeping it plain is what stops one comment tinting the next line.
    if (rules.line && src.startsWith(rules.line, i)) {
      const end = src.indexOf("\n", i);
      const stop = end < 0 ? src.length : end;
      paint("t-comment", src.slice(i, stop));
      i = stop;
      continue;
    }

    if (rules.block && src.startsWith(rules.block[0], i)) {
      const close = src.indexOf(rules.block[1], i + rules.block[0].length);
      // An unterminated block comment runs to the end of the file, which is what
      // an editor shows and what the file actually means.
      const stop = close < 0 ? src.length : close + rules.block[1].length;
      paint("t-comment", src.slice(i, stop));
      i = stop;
      continue;
    }

    if (rules.quotes.includes(src[i])) {
      const quote = src[i];
      let j = i + 1;
      while (j < src.length) {
        // A backslash escapes the next character, whatever it is — including
        // another backslash, which is the case a naive scanner gets wrong.
        if (src[j] === "\\" && quote !== "`") { j += 2; continue; }
        if (src[j] === quote) { j++; break; }
        // Only a backtick string may hold a newline; anything else ends there,
        // so a stray quote cannot tint the rest of the file.
        if (src[j] === "\n" && quote !== "`") break;
        j++;
      }
      paint("t-string", src.slice(i, Math.min(j, src.length)));
      i = Math.min(j, src.length);
      continue;
    }

    WORD.lastIndex = i;
    const word = WORD.exec(src);
    if (word) {
      paint(rules.words.has(word[0]) ? "t-word" : "t-plain", word[0]);
      i += word[0].length;
      continue;
    }

    NUMBER.lastIndex = i;
    const number = NUMBER.exec(src);
    if (number) {
      paint("t-number", number[0]);
      i += number[0].length;
      continue;
    }

    plain += src[i];
    i++;
  }
  flush();
  return out;
}

// block renders source as a code block: highlighted, monospaced, and scrolling
// inside itself rather than wrapping. A wrapped line of code is a line whose
// indentation lies about its depth.
export function block(text, path) {
  return h("pre", { class: "code" }, h("code", {}, highlight(text, path)));
}
