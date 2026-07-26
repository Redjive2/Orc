// The smallest DOM the pure modules need. It is deliberately not a browser:
// these tests are about logic, and a stub makes it obvious that the renderer
// never reaches for anything but createElement, createTextNode, and append.
//
// It grew a body, parentage, and focus when the editor arrived — because the
// editor's whole point is *where it lives* relative to the redrawn view, and a
// stub with no notion of a tree cannot express that.

class Node {
  constructor() { this.childNodes = []; this.parentNode = null; }
  append(...children) {
    for (const child of children) {
      const node = typeof child === "string" ? new Text(child) : child;
      node.parentNode = this;
      this.childNodes.push(node);
    }
  }
  remove() { this.parentNode?.removeChild(this); }
  // Connected means reachable from the body, which is what tells an open editor
  // apart from one that has been torn out.
  get isConnected() {
    for (let n = this; n; n = n.parentNode) if (n === globalThis.document?.body) return true;
    return false;
  }
  get textContent() {
    return this.childNodes.map((c) => c.textContent).join("");
  }
}

class Text extends Node {
  constructor(data) { super(); this.data = String(data); this.nodeType = 3; }
  get textContent() { return this.data; }
}

class Element extends Node {
  constructor(tag) {
    super();
    this.tagName = tag.toUpperCase();
    this.nodeType = 1;
    this.attributes = new Map();
    this.className = "";
    this.listeners = {};
  }
  setAttribute(k, v) { this.attributes.set(k, String(v)); }
  getAttribute(k) { return this.attributes.has(k) ? this.attributes.get(k) : null; }
  removeAttribute(k) { this.attributes.delete(k); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn);
  }
  removeChild(child) {
    this.childNodes = this.childNodes.filter((c) => c !== child);
    child.parentNode = null;
    return child;
  }
  get firstChild() { return this.childNodes[0] ?? null; }
  // The two methods the editor asks a textarea for. They do nothing here; what
  // the tests check is the text, not the caret.
  focus() { this.focused = true; }
  setSelectionRange(start, end) { this.selection = [start, end]; }
}

class Fragment extends Node {
  constructor() { super(); this.nodeType = 11; }
}

export function installDOM() {
  globalThis.Node = Node;
  const body = new Element("body");
  const listeners = {};
  const byId = new Map();
  globalThis.document = {
    body,
    listeners,
    getElementById: (id) => {
      if (!byId.has(id)) byId.set(id, new Element("div"));
      return byId.get(id);
    },
    createElement: (tag) => new Element(tag),
    createTextNode: (data) => new Text(data),
    createDocumentFragment: () => new Fragment(),
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    removeEventListener(type, fn) {
      listeners[type] = (listeners[type] || []).filter((f) => f !== fn);
    },
  };
}
