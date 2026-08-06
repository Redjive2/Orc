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

// tidy is what a typed name becomes before anybody judges it.
//
// The rule underneath allows lower-case letters, digits, and `. _ -`, and that is
// not negotiable: these names are directory names, socket paths, and words in a
// command line on the agent's machine. What *is* negotiable is whether somebody
// has to type them that way. Two of the refusals were pure friction —
//
//   - a capital, which every tool downstream lower-cases anyway, so refusing it
//     was refusing something that would have worked;
//   - a space, which is how a person naturally writes "fix the parser" and which
//     `-` spells for a machine.
//
// — so both are absorbed here rather than reported, and the sheet shows what the
// name will be. Everything else is still refused, and named: turning `%` into
// something silently would be guessing at what somebody meant.
//
// Length is preserved deliberately. One space becomes one dash rather than a run
// of them collapsing, so the position in a message about the *next* character
// still counts to the same place in what was typed. `a  b` becoming `a--b` is
// valid and is what it looks like.
export function tidy(raw) {
  return String(raw ?? "").trim().toLowerCase().replace(/\s/g, "-");
}

// name is the shared half of both rules. `what` names the kind of thing for the
// message, because "name is reserved" without saying what sort of name leaves
// somebody guessing which of the two sets they fell into.
function name(raw, what, reserved) {
  const s = tidy(raw);

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
        return `${what} has “${c}” at position ${i + 1}; use letters, digits, and . _ -`;
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

// duration checks an interval — mirrors Go's time.ParseDuration, which is a
// different grammar from `span` above and the reason both exist.
//
// The distinction is not pedantry, it is the bug this was written for. `until` on
// a grant goes to clock.ParseSpan: one number, one unit, and `d` and `w` among
// them. Every *pace* — how long silence lasts, how often a cycle looks, how often
// a mirror syncs — goes to `orc pace` or to the sync endpoint, and both of those
// call time.ParseDuration. Putting `span` on those fields got it wrong in both
// directions at once: it accepted `1d` and `2w`, which time.ParseDuration refuses
// outright, and it refused `1h30m`, which time.ParseDuration is perfectly happy
// with. The second was the worse half — the sheet will not submit while a field
// is marked, so a valid interval could not be sent at all.
//
// So: compound is fine, the units are Go's, and `d` and `w` are named as the
// mistake they are rather than reported as bad syntax. Somebody typing `1d` has
// not made a typo; they have used a unit this particular parser does not have,
// and `24h` is the answer.
const GO_UNITS = ["ns", "us", "µs", "ms", "s", "m", "h"];
const DAYS = { d: ["24h", 24], w: ["168h", 168] };

export function duration(raw, what = "the duration", { min = 0, max = 0 } = {}) {
  const s = String(raw ?? "").trim();
  if (s === "") return `${what} cannot be empty`;

  // The unit Go does not have, caught first and answered with the spelling that
  // works. Reporting it as a parse failure would be true and useless.
  const bigger = s.match(/^([0-9]+)\s*([dw])$/i);
  if (bigger) {
    const [as, hours] = DAYS[bigger[2].toLowerCase()];
    const n = Number(bigger[1]);
    const same = n === 1 ? as : `${n * hours}h`;
    return `${what} has no ${bigger[2].toLowerCase()} unit here; write ${same} rather than ${s}`;
  }

  const parts = [...s.matchAll(/([0-9]+(?:\.[0-9]+)?)([a-zµ]+)/gi)];
  const consumed = parts.reduce((n, p) => n + p[0].length, 0);
  if (parts.length === 0 || consumed !== s.length) {
    return `${what} is not a duration — write it like 30s, 20m, or 1h30m`;
  }

  let total = 0;
  const seconds = { ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
  for (const [, n, unit] of parts) {
    if (!GO_UNITS.includes(unit)) {
      return `${what} does not know the unit “${unit}”; use ${GO_UNITS.join(", ")}`;
    }
    total += Number(n) * seconds[unit];
  }
  if (total <= 0) return `${what} must have something in it`;
  if (min && total < min) {
    return `${what} is under ${plainly(min)}, which is a busy-wait rather than a cycle`;
  }
  if (max && total > max) {
    return `${what} is longer than ${plainly(max)}, which is not a cycle`;
  }
  return "";
}

// plainly renders a bound in the spelling the tool would print it in, so the two
// halves of a message about `168h` do not disagree about what 168 hours is.
function plainly(secs) {
  for (const [unit, size] of [["h", 3600], ["m", 60], ["s", 1]]) {
    if (secs % size === 0) return `${secs / size}${unit}`;
  }
  return `${secs}s`;
}

// The floors and ceilings the tools apply, so a value that would be refused on
// the far side of a sync is refused in the sheet instead. Named after the
// constants they mirror: Orc/internal/cli MinQuiet and MinWatch, and cq's
// protocol.MinSyncPace and MaxPace.
export const PACE = {
  quiet: { min: 60, max: 7 * 24 * 3600 },   // --after: a wake's silence
  watch: { min: 5, max: 7 * 24 * 3600 },    // --every and --watch: a cycle
  sync: { min: 10, max: 7 * 24 * 3600 },    // the mirror's own interval
};

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

// segment checks one name in the library tree: a file or a folder, not a path.
//
// Mirrors protocol.checkPath, which the server applies to the whole path this
// ends up part of. Spaces and capitals are *not* touched here, unlike a name: a
// file is a file, `Reference.md` is its name, and there is no downstream tool
// that wants it lower-cased.
//
// What it refuses is the separator, because the box asks for a name and a value
// with a `/` in it silently makes directories, and `.` and `..`, which name
// somewhere other than a new file.
export function segment(raw, what = "the name") {
  const s = String(raw ?? "").trim();
  if (s === "") return `${what} cannot be empty`;
  if (s === "." || s === "..") return `${what} cannot be “${s}”`;
  if (s.includes("/") || s.includes("\\")) {
    return `${what} cannot hold a separator; it names one file in the folder you are in`;
  }
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f]/.test(s)) return `${what} has a control character in it`;
  return "";
}

// permissions checks a space-separated list of permission names against the ones a
// fleet actually has — mirrors what `orc assign permission` refuses.
//
// Every other check in this file measures a value's *shape*. This one measures it
// against the world, which is why it is built rather than called: the caller holds
// the fleet, and a check that had to fetch one would be a check that could not run
// while somebody was typing.
//
// Three ways a name is refused, and each says what to do about it:
//
//   - it is not a name at all — the shape rule, borrowed from `label`;
//   - no permission is called that, with the nearest one offered, because a typo
//     and a name somebody invented look identical in a refusal that only says "no";
//   - the role's authority is under the permission's floor, which orc refuses on the
//     machine. Checking it here turns a failure that arrives after the next sync
//     into one that arrives while the box is open.
//
// A name the role already holds is *not* refused. Asking for it again changes
// nothing, and a form that argued about it would be arguing about a request that
// does no harm.
export function permissions(known, { held = [], authority = null } = {}) {
  const byName = new Map((known || []).map((p) => [p.name, p]));
  const already = new Set(held || []);

  return (raw, what = "the permissions") => {
    const names = split(raw);
    if (names.length === 0) return `${what} cannot be empty`;

    // Every name, not up to the first bad one. The sheet already holds this rule
    // across fields — a form with three problems submitted three times, each
    // attempt revealing one more, reads as broken rather than as helpful — and a
    // field carrying a list of things has the same shape inside it.
    const seen = new Set();
    const wrong = [];
    for (const name of names) {
      const shape = label(name, `“${name}”`);
      if (shape) {
        wrong.push(shape);
        continue;
      }
      // Compared as the tools spell it. `EDIT` and `edit` are one permission —
      // orc lower-cases a name on the way in — so looking the typed spelling up
      // refused something that would have worked, and counted the two as
      // different when checking for a repeat.
      const key = tidy(name);
      if (seen.has(key)) {
        wrong.push(`“${key}” is named twice`);
        continue;
      }
      seen.add(key);

      const got = byName.get(key);
      if (!got) {
        const near = nearest(key, [...byName.keys()]);
        wrong.push(near
          ? `there is no permission called “${key}” — did you mean “${near}”?`
          : `there is no permission called “${key}” on this fleet`);
        continue;
      }
      if (authority != null && got.floor > authority) {
        wrong.push(`“${key}” needs authority ${got.floor}, and the role has ${authority}`);
      }
    }
    return wrong.join("; ");
  };
}

// oneOf checks a single name against the ones that exist — a role, an agent, a
// machine. The list half of `permissions`, for the fields that take one thing.
//
// The same three refusals, and the same reason for each: a name that is not a name,
// a name nothing is called with the nearest offered, and — where the caller passes
// one — a rule about the thing itself. What it saves is a queued action that fails
// on a machine nobody is watching, minutes later, over a typo.
export function oneOf(known, { what = "it", barred = null } = {}) {
  const names = (known || []).map((k) => (typeof k === "string" ? k : k.name));
  const byName = new Set(names.map(tidy));

  return (raw, label = what) => {
    const got = String(raw ?? "").trim();
    if (got === "") return `${label} cannot be empty`;
    // One name, said so rather than mangled. `tidy` turns a space into a dash, so a
    // field that took several names and was wired to this check silently asked for
    // a thing called `edit-upgrade` and reported that nothing was called that. The
    // list checks — `permissions` — are what a field taking several wants.
    if (/\s/.test(got)) {
      return `${label} takes one name; “${got}” is several`;
    }

    const shape = name(got, label, RESERVED_MAILBOX);
    if (shape && !byName.has(tidy(got))) return shape;

    const key = tidy(got);
    if (!byName.has(key)) {
      const near = nearest(key, [...byName]);
      return near
        ? `there is no ${label} called “${key}” — did you mean “${near}”?`
        : `there is no ${label} called “${key}”`;
    }
    return barred ? barred(key) || "" : "";
  };
}

// Names is the list a permissions field holds, as the tools spell them and with the
// repeats gone.
//
// Exported because the caller has to send exactly what was checked. A form that
// validated one spelling and queued another would be a form whose checks mean
// nothing, and the two are written apart here — the box holds what somebody typed
// and the queue takes what orc accepts.
export function names(raw, { without = [] } = {}) {
  const drop = new Set((without || []).map(tidy));
  const seen = new Set();
  const out = [];
  for (const name of split(raw)) {
    const key = tidy(name);
    if (key === "" || seen.has(key) || drop.has(key)) continue;
    seen.add(key);
    out.push(key);
  }
  return out;
}

// split is the one definition of how a list field is broken into words.
function split(raw) {
  return String(raw ?? "").trim().split(/\s+/).filter(Boolean);
}

// nearest is the closest of a set of words, or "" when none is close.
//
// Edit distance, capped at a third of the word's length so a wrong guess is not
// offered with a straight face: "did you mean X?" about something unrelated is worse
// than not guessing, because somebody follows it.
export function nearest(word, words) {
  let best = "";
  let score = Infinity;
  const limit = Math.max(1, Math.floor(word.length / 3));
  for (const candidate of words) {
    const d = distance(word, candidate);
    if (d < score) {
      best = candidate;
      score = d;
    }
  }
  return score <= limit ? best : "";
}

// distance is Levenshtein, over two rows rather than a matrix.
function distance(a, b) {
  if (a === b) return 0;
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    const row = [i];
    for (let j = 1; j <= b.length; j++) {
      row[j] = Math.min(
        prev[j] + 1,
        row[j - 1] + 1,
        prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1),
      );
    }
    prev = row;
  }
  return prev[b.length];
}

// nonempty is the check a field with no syntax of its own still wants: a subject,
// a description, a line of prose.
export function nonempty(raw, what = "it") {
  return String(raw ?? "").trim() === "" ? `${what} is needed` : "";
}

// CHECKS maps the name a field spec uses to the function, so a form can say
// `check: "mailbox"` in data rather than importing anything.
export const CHECKS = {
  mailbox, label, span, paths, segment, nonempty,
  duration,
  // The three paces, each carrying the bounds its own tool applies. Separate
  // entries rather than one with options because a field spec names a check by
  // string, and a floor that lived at the call site would be a number nobody
  // could trace back to the constant it mirrors.
  "pace.quiet": (raw, what) => duration(raw, what, PACE.quiet),
  "pace.watch": (raw, what) => duration(raw, what, PACE.watch),
  "pace.sync": (raw, what) => duration(raw, what, PACE.sync),
};

// TIDIED are the checks whose value is absorbed rather than refused, so a caller
// naming one gets the tidying with it. Keyed the same way, so a field says
// `check: "label"` and both halves follow.
const TIDIED = new Set(["mailbox", "label"]);

// tidierOf returns the function that turns what was typed into what will be sent,
// or null where a field sends exactly what it was given.
export function tidierOf(spec) {
  return typeof spec === "string" && TIDIED.has(spec) ? tidy : null;
}

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
