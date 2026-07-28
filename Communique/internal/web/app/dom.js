// Element building. Everything the application renders goes through `h`, and
// `h` sets text through `textContent` — never innerHTML. A subject line that
// contains a tag is a subject line that contains a tag.

export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "class") el.className = value;
    else if (key === "style") style(el, value);
    else if (key.startsWith("on") && typeof value === "function") {
      el.addEventListener(key.slice(2).toLowerCase(), value);
    } else el.setAttribute(key, value === true ? "" : String(value));
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    el.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return el;
}

// style applies per-element style through the CSSOM, and takes an object rather than
// a string on purpose.
//
// The site's content policy is `default-src 'self'` with no `unsafe-inline`, so a
// `style` attribute is **discarded by the browser without a word**. That is not a
// theoretical constraint: the activity charts were written with
// `style: "height:80%;background:…"`, every test passed because the stub DOM has no
// content policy, and every bar arrived in a real browser with no height at all.
//
// Writing through `el.style` is not a way around the policy — `style-src` governs
// attributes and stylesheets, not the CSSOM, which is exactly the seam this needs. An
// object is required so nothing can pass a string here and quietly get the attribute
// back, which is the bug this exists to make unwritable.
//
// Almost nothing should use it. A style set here is invisible to the stylesheet, so
// it is for values that are *computed* — a bar's height, a colour taken from a
// reading — and never for anything that could be a class.
function style(el, styles) {
  if (!styles || typeof styles !== "object") return;
  for (const [property, value] of Object.entries(styles)) {
    if (value === null || value === undefined || value === false) continue;
    // A custom property is not a member of the style object and has to be set by
    // name. Getting this wrong is silent: `el.style["--fill"] = x` assigns a
    // property nobody reads and no CSS ever sees it.
    if (property.startsWith("--")) el.style.setProperty(property, String(value));
    else el.style[property] = String(value);
  }
}

export function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
  return el;
}

export function mount(el, ...children) {
  clear(el);
  for (const child of children.flat()) if (child) el.append(child);
  return el;
}

// findByName walks a tree for the first field with this name.
//
// Written by hand rather than with querySelector because it has to work in the
// tests' stub DOM as well as a browser — and because the thing it looks for is
// one attribute, which is less code than escaping a selector.
export function findByName(root, name) {
  if (!root || !name) return null;
  if (root.getAttribute && root.getAttribute("name") === name) return root;
  for (const child of root.childNodes || []) {
    const found = findByName(child, name);
    if (found) return found;
  }
  return null;
}

// A box-drawn rule, so the page frames things the way the CLI does.
export function rule(label = "", width = 78) {
  const text = label ? `╭─ ${label} ` : "╭";
  return "─".repeat(Math.max(width - text.length, 0));
}

// --- formatting ---------------------------------------------------------

const pad = (n) => String(n).padStart(2, "0");

export function clock(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "--:--";
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// A duration in the shortest honest form. The staleness clock is on screen at
// all times, so it has to read at a glance.
export function since(iso, now = Date.now()) {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "never";
  const secs = Math.max(0, Math.round((now - then) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

export function ellipsis(text, width) {
  const s = String(text ?? "");
  return s.length <= width ? s : s.slice(0, Math.max(width - 1, 0)) + "…";
}
