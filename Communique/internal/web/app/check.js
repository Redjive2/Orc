// What a field will be refused for, said before it is sent.
//
// Every value typed into cq ends up at a tool that validates it properly —
// `user.Parse` for a mailbox, `model.ParseName` for a role, `clock.ParseSpan` for
// a duration. Those are the authority and this is not trying to replace them.
// What it replaces is the *delay*: an action queued from the browser is applied
// minutes later on a machine nobody is looking at, so a name with a space in it
// comes back as a refusal in the queue long after the person who typed it has
// moved on, with a message written for a terminal.
//
// So these mirror the rules rather than inventing them, and each one names the
// function it mirrors. Two consequences follow, and both matter:
//
//   - **A check that is stricter than the tool is a bug**, because it refuses
//     something that would have worked and there is no way around it from a
//     browser. When in doubt these accept.
//   - **A check that is looser is merely unhelpful.** The value goes, the tool
//     refuses it, and the queue says why — which is what happened before any of
//     this existed. So the failure mode of being wrong here is bounded.
//
// The rules are pinned by check.test.js against the Go source they come from. If
// one of those changes, that test is the thing that should fail.

// Names.
//
// Both tools spell the same rule: 1–64 characters, lower-cased, letters, digits,
// and `. _ -`, starting with a letter or digit. They differ only in what is
// reserved, which is why there are two functions and not one with a flag — the
// caller knows whether it is naming a mailbox or a role, and a boolean at the
// call site reads as neither.
const MAX_NAME = 64;
const SHAPE = /^[a-z0-9][a-z0-9._-]*$/;

// RESERVED_MAILBOX is user.Parse's set: names Mailman gives its own meaning.
const RESERVED_MAILBOX = new Set([".", "..", "all", "any", "none", "system", "mailman"]);

// RESERVED_LABEL is model.ParseName's set: the words Orc's own grammar uses, so
// a role called `role` cannot be told apart from the word in a command.
const RESERVED_LABEL = new Set([".", "..", "all", "any", "none",
  "identity", "role", "permission", "authority"]);

// name is the shared half of both rules. `what` names the kind of thing for the
// message, because "name is reserved" without saying what sort of name leaves
// somebody guessing which of the two sets they fell into.
function name(raw, what, reserved) {
  const s = String(raw ?? "").trim().toLowerCase();

  if (s === "") return `${what} cannot be empty`;
  // Checked before anything else and against the raw spelling, as ParseName does:
  // somebody who typed a flag by accident is better told that than told about
  // the character set.
  if (String(raw).trim().startsWith("-")) {
    return `${what} looks like an option; names start with a letter or digit`;
  }
  if (s.length > MAX_NAME) {
    return `${what} is longer than ${MAX_NAME} characters`;
  }
  if (reserved.has(s)) return `${what} “${s}” is reserved`;

  if (!SHAPE.test(s)) {
    // The offending character, at its position, because "invalid name" on a name
    // somebody has read four times is not a hint.
    const chars = [...s];
    for (let i = 0; i < chars.length; i++) {
      const c = chars[i];
      if (!/[a-z0-9._-]/.test(c)) {
        const shown = c === " " ? "a space" : `“${c}”`;
        return `${what} has ${shown} at position ${i + 1}; use letters, digits, and . _ -`;
      }
    }
    return `${what} must start with a letter or digit`;
  }
  return "";
}

// mailbox checks an identity or account name — mirrors Common/user.Parse.
export function mailbox(raw, what = "the name") {
  return name(raw, what, RESERVED_MAILBOX);
}

// label checks a role or permission name — mirrors Orc/internal/model.ParseName.
export function label(raw, what = "the name") {
  return name(raw, what, RESERVED_LABEL);
}

// span checks a duration — mirrors Common/clock.ParseSpan.
//
// Worth reading before assuming: this is **not** Go's `time.ParseDuration`. One
// whole number and one unit, and `s m h d w` only — so `1h30m` is refused, and
// `90m` is how that is spelled. Somebody who knows Go will type the first, which
// is exactly the person this message is for.
const SPAN_UNITS = { s: "seconds", m: "minutes", h: "hours", d: "days", w: "weeks" };
const SPAN_MAX = 1 << 20;

export function span(raw, what = "the duration") {
  const s = String(raw ?? "").trim();
  if (s === "") return `${what} cannot be empty`;

  const unit = s[s.length - 1];
  if (!(unit in SPAN_UNITS)) {
    return `${what} must end in s, m, h, d, or w — like 30m or 2h`;
  }
  const digits = s.slice(0, -1);
  if (digits === "") return `${what} has no number before the ${unit}`;
  if (!/^[0-9]+$/.test(digits)) {
    // The `1h30m` case, named, because it is the mistake somebody fluent in Go
    // makes and the generic message would send them looking for a typo that is
    // not there. Only when there is a single-unit spelling to offer, though:
    // "2 hours" also ends in a unit and has letters before it, and telling
    // somebody to write "90m" instead would be nonsense with a confident tone.
    const better = suggestSpan(s);
    if (better) {
      return `${what} takes one number and one unit; write ${better} rather than ${s}`;
    }
    return `${what} is not a whole number of ${SPAN_UNITS[unit]} — write it like 30m or 2h`;
  }
  if (Number(digits) > SPAN_MAX) return `${what} is too large`;
  return "";
}

// suggestSpan turns a compound duration into the single-unit one that means the
// same, when it can. `1h30m` is a very common thing to type and "90m" is a much
// better answer than "that is wrong".
function suggestSpan(s) {
  const parts = [...String(s).matchAll(/([0-9]+)([smhdw])/g)];
  if (parts.length < 2) return "";
  const seconds = { s: 1, m: 60, h: 3600, d: 86400, w: 604800 };
  let total = 0;
  let consumed = 0;
  for (const [whole, n, unit] of parts) {
    total += Number(n) * seconds[unit];
    consumed += whole.length;
  }
  if (consumed !== s.length || total === 0) return "";
  for (const unit of ["w", "d", "h", "m", "s"]) {
    if (total % seconds[unit] === 0) return `${total / seconds[unit]}${unit}`;
  }
  return "";
}

// whole checks a number a field is bounded to.
//
// Separate from the dialog's own numeric handling because the message wants the
// field's name in it, and because an empty box and a word typed into a number
// field are different mistakes with different fixes.
export function whole(raw, { min, max, what = "it" } = {}) {
  const s = String(raw ?? "").trim();
  if (s === "") return `${what} cannot be empty`;
  if (!/^-?[0-9]+$/.test(s)) return `${what} must be a whole number`;

  const n = Number.parseInt(s, 10);
  if (min != null && max != null && (n < min || n > max)) {
    return `${what} must be from ${min} to ${max}`;
  }
  if (min != null && n < min) return `${what} must be at least ${min}`;
  if (max != null && n > max) return `${what} must be at most ${max}`;
  return "";
}

// paths checks a space-separated list of repository-relative paths.
//
// The rule is the one the library verbs enforce: inside the checkout and nowhere
// else. An absolute path and a `..` are the two ways out of it, and both are
// refused by the agent with a message about containment that reads like an
// accusation when it was a slip.
export function paths(raw, what = "the paths") {
  const list = String(raw ?? "").trim().split(/\s+/).filter(Boolean);
  if (list.length === 0) return `${what} cannot be empty`;

  for (const p of list) {
    if (p.startsWith("/") || /^[a-zA-Z]:[\\/]/.test(p)) {
      return `${what} must be inside the repository; “${p}” is an absolute path`;
    }
    if (p.split(/[\\/]/).includes("..")) {
      return `${what} must stay inside the repository; “${p}” climbs out of it`;
    }
  }
  return "";
}

// nonempty is the check a field with no syntax of its own still wants: a subject,
// a description, a line of prose.
export function nonempty(raw, what = "it") {
  return String(raw ?? "").trim() === "" ? `${what} is needed` : "";
}

// CHECKS maps the name a field spec uses to the function, so a form can say
// `check: "mailbox"` in data rather than importing anything.
export const CHECKS = { mailbox, label, span, paths, nonempty };

// of returns the check a field asked for, or null.
//
// A spec naming a check that does not exist is a mistake in the caller, and it
// returns null rather than throwing: a dialog that would not open because a
// validator was misspelled is worse than one that validates one field less.
export function of(spec) {
  if (typeof spec === "function") return spec;
  if (typeof spec === "string") return CHECKS[spec] || null;
  return null;
}
