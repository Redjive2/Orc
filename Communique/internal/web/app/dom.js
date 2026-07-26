// Element building. Everything the application renders goes through `h`, and
// `h` sets text through `textContent` — never innerHTML. A subject line that
// contains a tag is a subject line that contains a tag.

export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "class") el.className = value;
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

export function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
  return el;
}

export function mount(el, ...children) {
  clear(el);
  for (const child of children.flat()) if (child) el.append(child);
  return el;
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
