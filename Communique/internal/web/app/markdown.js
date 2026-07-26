// A deliberately small markdown renderer.
//
// This is the highest-risk code in the application: it turns other people's
// text into elements. So it builds nodes directly rather than producing an
// HTML string, and it understands only paragraphs, code, emphasis, lists and
// links. Anything it does not understand stays literal text — which is the
// safe direction to be wrong in.
//
// No innerHTML appears anywhere in this file, so there is no escaping to get
// right: the DOM does it.

import { h } from "./dom.js";

const SAFE_SCHEME = /^https?:\/\//i;

export function render(markdown) {
  const out = document.createDocumentFragment();
  const lines = String(markdown ?? "").split("\n");

  let i = 0;
  while (i < lines.length) {
    const line = lines[i];

    if (line.trim() === "") { i++; continue; }

    // Fenced code: everything inside is literal, including markers.
    if (line.startsWith("```")) {
      const body = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) body.push(lines[i++]);
      if (i < lines.length) i++; // closing fence
      out.append(h("pre", {}, h("code", {}, body.join("\n"))));
      continue;
    }

    // A heading. Six levels, and the hashes have to be followed by a space —
    // `#hashtag` is a word somebody wrote, not a heading they meant.
    //
    // It renders as an <h2> and below rather than an <h1>: this prose is *inside* a
    // page that already has a heading, and a document that started at h1 would put
    // two of them on one screen and read as two documents to anything that navigates
    // by structure.
    const heading = /^(#{1,6}) +(.*)$/.exec(line);
    if (heading) {
      const level = Math.min(heading[1].length + 1, 6);
      out.append(h(`h${level}`, {}, ...inline(heading[2].trim())));
      i++;
      continue;
    }

    if (/^[-*+] /.test(line)) {
      const items = [];
      while (i < lines.length && /^[-*+] /.test(lines[i])) {
        items.push(h("li", {}, ...inline(lines[i].slice(2))));
        i++;
      }
      out.append(h("ul", {}, ...items));
      continue;
    }

    // A paragraph runs to the next blank line.
    const para = [];
    while (i < lines.length && lines[i].trim() !== "" && !lines[i].startsWith("```")
           && !/^#{1,6} /.test(lines[i]) && !/^[-*+] /.test(lines[i])) {
      para.push(lines[i++]);
    }
    out.append(h("p", {}, ...inline(para.join(" "))));
  }
  return out;
}

// inline returns an array of nodes for one line's worth of text.
function inline(text) {
  const nodes = [];
  let rest = String(text ?? "");

  // Each pattern is tried at the earliest position; whatever precedes it is
  // literal text. Nothing is ever concatenated into markup.
  const patterns = [
    { re: /`([^`]+)`/, make: (m) => h("code", {}, m[1]) },
    { re: /\[([^\]]+)\]\(([^)\s]+)\)/, make: (m) => link(m[1], m[2]) },
    { re: /\*\*([^*]+)\*\*/, make: (m) => h("strong", {}, m[1]) },
    { re: /\*([^*]+)\*/, make: (m) => h("em", {}, m[1]) },
    { re: /_([^_]+)_/, make: (m) => h("em", {}, m[1]) },
  ];

  for (let guard = 0; rest.length > 0 && guard < 5000; guard++) {
    let best = null;
    for (const p of patterns) {
      const m = p.re.exec(rest);
      if (m && (best === null || m.index < best.match.index)) best = { match: m, make: p.make };
    }
    if (!best) break;

    if (best.match.index > 0) nodes.push(rest.slice(0, best.match.index));
    nodes.push(best.make(best.match));
    rest = rest.slice(best.match.index + best.match[0].length);
  }
  if (rest.length > 0) nodes.push(rest);
  return nodes;
}

// link refuses any scheme but http and https, so a body cannot carry a
// javascript: or data: URL into the page.
function link(label, href) {
  if (!SAFE_SCHEME.test(href)) return `${label} (${href})`;
  return h("a", { href, rel: "noreferrer noopener", target: "_blank" }, label);
}
