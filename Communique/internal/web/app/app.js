// The application: a router, one immutable state object, and a render pass.
//
// State is replaced rather than mutated, and every view is a function of it, so
// what is on screen cannot drift from what was fetched. It is the discipline
// the Go side follows, for the same reason.

import { api, setCSRF, ApiError } from "./api.js";
import { mount, h } from "./dom.js";
import * as focus from "./focus.js";
import * as views from "./views.js";
import * as routes from "./routes.js";
import * as screens from "./screens.js";
import * as library from "./library.js";
import { digest } from "./digest.js";
import { endingOf, toLF, fromLF, LF } from "./eol.js";
import * as editor from "./editor.js";
import * as dialog from "./dialog.js";
import * as fleetView from "./fleet.js";
import * as clauses from "./clauses.js";

const nav = document.getElementById("nav");
const view = document.getElementById("view");
const statusBar = document.getElementById("status");

// `adminEnabled` is whether the panel is served at all; `admin` is its
// contents. Keeping them apart means "the panel is off" and "the panel has not
// been fetched yet" are different states, which they are.
let state = {
  session: null, adminEnabled: false, machines: [], fleet: [], fleetError: "",
  upgrading: null,
  inbox: [], archive: [], sent: [], queue: [], tasks: [],
  // The library's structure, the file texts read so far, and which folds are
  // open. Openness is state rather than DOM, so a redraw on sync does not
  // collapse everything the reader had opened.
  // `picked` is the one row whose controls are on screen. A tree with a button
  // under every line is a tree nobody can read.
  library: null, files: {}, open: {}, picked: null,
  // What somebody has typed and not yet queued, keyed by which form it is in.
  //
  // Drafts are state for the same reason open folds are: the view is redrawn on
  // every sync, and a sync happens while somebody is typing. Text that lives
  // only in the DOM is gone when that happens — the failure the editor avoids
  // by living outside the redraw, which a form in the page flow cannot.
  drafts: {},
  detail: null, admin: null, error: null,
};

function set(patch) {
  state = Object.freeze({ ...state, ...patch });
  draw();
}

// stash records state without redrawing.
//
// It exists for one caller: a keystroke. Drafts have to survive a sync, so they
// belong in state — but redrawing on every letter would replace the field under
// the cursor as it was being typed into, which is the bug this is fixing rather
// than a different shape of it. So the draft is kept, and the screen is left
// exactly as the writer has it.
function stash(patch) {
  state = Object.freeze({ ...state, ...patch });
}

// current is the route being shown, with the flat ones this replaced sent on to
// where they went.
//
// The hash is rewritten rather than merely mapped, so the address bar agrees
// with the screen and a bookmark taken from it is a bookmark of the new shape.
function current() {
  const raw = location.hash.slice(1) || routes.HOME;
  const [path, query] = raw.split("?");
  if (routes.isDetail(path)) return raw;

  // Where it went, or home if it is nothing this site has ever had. Both are
  // rewritten rather than merely mapped, so the address bar agrees with the
  // screen — a page showing the inbox under a hash that says something else is
  // the disagreement this is here to stop.
  const moved = routes.MOVED[path] || (routes.resolve(path) ? null : routes.HOME);
  if (moved) {
    const next = moved + (query ? `?${query}` : "");
    // replace, not assign: the route somebody typed should not be a place the
    // back button returns them to, or leaving is two presses.
    location.replace(`#${next}`);
    return next;
  }
  return raw;
}

// draw renders the route.
//
// Focus and the caret are carried across the re-mount. Restoring a draft's text
// without them would still lose somebody's place: a sync landing mid-sentence
// would leave the words on screen and the cursor at the end of them, or gone
// altogether. What is remembered is the field's name, which is unique within a
// view, so the reader lands back where they were.
function draw() {
  const place = focus.remember(typeof document !== "undefined" ? document.activeElement : null);
  const route = current();
  mount(nav, views.nav(state, route));
  mount(statusBar, views.status(state));

  if (state.error) { mount(view, views.error(state.error)); return; }

  // A detail view is reached from a list rather than from the navigation, and
  // its links are already out there — so those keep the shape they had.
  if (route.startsWith("/message/")) {
    mount(view, views.message(state, state.detail, actions));
  } else if (route.startsWith("/tasks/")) {
    mount(view, views.task(state, decodeURIComponent(route.slice("/tasks/".length)), actions));
  } else {
    const here = routes.resolve(route);
    const drawn = here && screens.render(here.major, here.sub, state, actions);
    // Nothing matched, and the redirect in `route()` has already had its turn —
    // so this is a hash nobody wrote on purpose. Home, rather than a blank page
    // that looks like a broken site.
    mount(view, drawn || screens.render("mail", "inbox", state, actions));
  }

  focus.restore(view, place);
}

// --- actions ------------------------------------------------------------
//
// Every action refetches rather than guessing what the server now holds. A
// queued reply appears because the queue says so, not because the browser
// assumed it would.

const actions = {
  // draft records a keystroke. It does not redraw: see stash.
  draft(key, field, value) {
    const kept = state.drafts[key] || {};
    stash({ drafts: { ...state.drafts, [key]: { ...kept, [field]: value } } });
  },
  // forget drops a draft once what it held has been queued. It does not redraw
  // either — the action that queued it refetches, and that redraw is what
  // empties the form.
  forget(key) {
    const { [key]: gone, ...rest } = state.drafts;
    if (gone === undefined) return;
    stash({ drafts: rest });
  },
  async reply(m, subject, body) {
    if (!body.trim()) return;
    await run(() => api.reply(m.puid, m.machine, subject, body));
  },
  // Answering from the list, without opening the message first.
  //
  // The same verb as the composer on the message itself — Mailman roots a
  // conversation when the parent is standalone, so a quick reply starts the
  // thread exactly as a considered one does. What is saved is the navigation,
  // which on a phone is the whole cost of answering a one-line question.
  async quickReply(m) {
    const got = await dialog.ask({
      title: `reply to ${m.from}`,
      note: m.subject,
      fields: [
        { name: "subject", label: "subject", value: views.reSubject(m.subject) },
        { name: "body", label: "reply", kind: "lines", placeholder: "…" },
      ],
      submit: "queue reply",
    });
    if (!got) return;
    await actions.reply(m, got.subject, got.body);
  },
  // send reports whether the message was queued, so the form knows whether it
  // may clear itself. Every other action is fire-and-redraw.
  async send(machine, to, subject, body) {
    return run(() => api.send(machine, to, subject, body));
  },
  async cc(m, user) {
    return run(() => api.cc(m.convo.uid, m.machine, user));
  },
  // toggle and openFile do not go through `run`: they change what is on
  // screen, not what the server holds, and a redraw-and-refetch for opening a
  // fold would make the interface feel broken.
  toggle(key) {
    set({ open: { ...state.open, [key]: !state.open[key] } });
  },
  pick(key) {
    set({ picked: key });
  },
  async openFile(file) {
    const key = library.fileKey(file);
    if (state.files[key]) return; // read once
    set({ files: { ...state.files, [key]: { loading: true } } });
    try {
      const got = await api.libraryFile(file.path, file.machine);
      const raw = got.text || "";
      // The digest is of the bytes as they are on disk — taken before the text
      // is normalised, and taken once. An edit has to say what it was made
      // against, and that is the real file, not the browser's version of it.
      originals.set(key, { digest: await digest(raw), ending: endingOf(raw) });
      set({ files: { ...state.files, [key]: { ...got, text: toLF(raw) } } });
    } catch (err) {
      set({ files: { ...state.files, [key]: { error: String(err.message || err) } } });
    }
  },
  // Editing the checkout. Every one of these queues and leaves on the next
  // sync, and every one that expects to find something carries the digest of
  // what was on screen — so an edit made against a file an agent has since
  // changed is refused rather than silently overwriting it.
  async editFile(file, text) {
    const edited = await ask(`editing ${file.path}`, text);
    if (edited === null || edited === text) return;
    await run(() => api.writeFile(file.machine, file.path, outgoing(file, edited), hold(file)));
  },
  // A span edit splices back into the whole file, so there is one write path
  // rather than one per kind of structure — and the precondition still covers
  // the whole file, which is what actually gets replaced.
  async editSpan(file, whole, start, end, what) {
    const lines = whole.split("\n");
    const before = lines.slice(0, Math.max(start - 1, 0));
    const after = lines.slice(end);
    const edited = await ask(`editing ${what}`, lines.slice(Math.max(start - 1, 0), end).join("\n"));
    if (edited === null) return;
    const next = [...before, ...edited.split("\n"), ...after].join("\n");
    if (next === whole) return;
    await run(() => api.writeFile(file.machine, file.path, outgoing(file, next), hold(file)));
  },
  async deleteFile(file) {
    if (!await dialog.confirm({
      title: `delete ${file.path}?`,
      body: "it leaves on the next sync, and is refused if the file changed meanwhile. this cannot be undone.",
      submit: "delete it",
    })) return;
    await run(() => api.deleteFile(file.machine, file.path, hold(file)));
  },
  async newFile(machine, dir) {
    const name = await dialog.one({
      title: "a new file", label: "name", hint: `in ${dir || "the root"}/`,
      note: "it is created empty; open it to write something",
    });
    if (!name) return;
    await run(() => api.createFile(machine, under(dir, name), ""));
  },
  async newFolder(machine, dir) {
    const name = await dialog.one({
      title: "a new folder", label: "name", hint: `in ${dir || "the root"}/`,
    });
    if (!name) return;
    await run(() => api.makeDir(machine, under(dir, name)));
  },
  // Deleting a folder that has things in it, which is the ordinary thing to
  // want and was previously impossible without emptying it a file at a time.
  //
  // What makes it safe from a mirror minutes old is the manifest: the agent
  // walks the real directory and refuses if it holds a file this list does not
  // name, so work filed in there since the snapshot is not swept up with the
  // rest. The count is said before anything is queued, because this is the one
  // action that cannot be undone and cannot be checked afterwards, and a number
  // is the only preview a phone can give.
  async removeFolder(machine, dir, paths, empty) {
    if (!await dialog.confirm({
      title: `delete the folder ${dir}?`,
      body: empty
        ? "there is nothing in it. this cannot be undone."
        : `it takes the ${library.plural(paths.length, "file")} in it. this cannot be undone, ` +
          "and it is refused if anything was added since you last synced.",
      submit: empty ? "delete it" : `delete ${library.plural(paths.length, "file")}`,
    })) return;
    await run(() => empty
      ? api.removeDir(machine, dir)
      : api.removeTree(machine, dir, paths));
  },
  async retry(entry) { await run(() => api.retry(entry.action.id)); },
  async drop(entry) {
    // A settled action is a record of something that already happened, so
    // dropping one loses nothing but the record. The two that carry a *reason*
    // are worth a moment's pause; a done one is not, and asking would make
    // tidying up feel dangerous.
    if (entry.state === "failed" || entry.state === "in_doubt") {
      if (!await dialog.confirm({
        title: "forget this?",
        body: "it leaves the queue, and the reason it failed goes with it. nothing is retried.",
        submit: "forget it",
      })) return;
    }
    await run(() => api.drop(entry.action.id));
  },
  // Sweeping up. The queue is a log as much as a queue, and after a busy
  // afternoon the rows worth reading are below fifty that are done.
  async clearDone(count) {
    if (!await dialog.confirm({
      title: `clear ${count} finished action${count === 1 ? "" : "s"}?`,
      body: "they have all been applied; this removes the record, and nothing else. "
        + "anything refused or in doubt stays.",
      submit: "clear them", danger: false,
    })) return;
    await run(() => api.clearQueue(["done"]));
  },
  async markRead(m) { await run(() => api.markRead(m.puid, m.machine)); },
  async archive(m) { await run(() => api.archiveMessage(m.puid, m.machine)); },

  // --- the fleet ---------------------------------------------------------
  //
  // One action per Orc verb, named as Orc names them. Somebody who knows `orc`
  // should not have to learn a second vocabulary to drive the same fleet from a
  // phone — and the refusals that come back are Orc's own words.

  async newIdentity(f) {
    const name = await dialog.one({
      title: "hire an agent", label: "name",
      hint: "letters, digits, and dashes",
      note: "it is created under you, with no role and no permissions until you give it one",
    });
    if (!name) return;
    await run(() => api.newIdentity(f.machine, name));
  },
  async newRole(f) {
    const got = await dialog.ask({
      title: "a new role",
      note: "authority runs 1 to 99; the operator is 100 and that is a position, not a level",
      fields: [
        { name: "name", label: "name", hint: "letters, digits, and dashes" },
        { name: "authority", label: "authority", kind: "number", value: 50, min: 1, max: 99 },
        { name: "description", label: "what it is", hint: "one line; it shows in every listing" },
      ],
    });
    if (!got) return;
    await run(() => api.newRole(f.machine, got.name, got.authority, got.description));
  },
  async newPermission(f) {
    const got = await dialog.ask({
      title: "a new permission",
      note: "a named set of clauses with a floor: only an identity at or above it can hold this",
      fields: [
        { name: "name", label: "name" },
        { name: "floor", label: "floor", kind: "number", value: 1, min: 1, max: 100,
          hint: "the least authority that may hold it" },
        { name: "patterns", label: "clauses", kind: "clauses", words: f.vocabulary,
          hint: "one clause per set of parentheses",
          placeholder: "read(Anno/** Dock/**) write(** except Docs/**)" },
      ],
    });
    if (!got) return;
    const patterns = clauses.split(got.patterns);
    if (patterns.length === 0) return;
    await run(() => api.newPermission(f.machine, got.name, got.floor, patterns));
  },
  // editPermission changes a permission that already exists, rather than deleting
  // and remaking it: a role holding it keeps holding it, and the journal keeps the
  // history of what it used to say.
  async editPermission(f, permission) {
    const got = await dialog.ask({
      title: `edit ${permission.name}`,
      note: "every holder of this permission is affected the moment it lands",
      submit: "queue the change",
      fields: [
        { name: "floor", label: "floor", kind: "number", value: permission.floor,
          min: 1, max: 100, hint: "the least authority that may hold it" },
        { name: "patterns", label: "clauses", kind: "clauses", words: f.vocabulary,
          value: (permission.patterns || []).join(" "),
          hint: "one clause per set of parentheses", cheatsheet: false },
      ],
    });
    if (!got) return;
    const patterns = clauses.split(got.patterns);
    if (patterns.length === 0) return;
    await run(() => api.editPermission(f.machine, permission.name, got.floor, patterns));
  },

  async assignRole(f, id) {
    const role = await dialog.one({
      title: `the job for ${id.name}`, label: "role", value: id.role || "",
      note: "an identity holds exactly one role; this replaces whatever it had",
    });
    if (!role) return;
    await run(() => api.assignRole(f.machine, id.name, role));
  },
  async moveIdentity(f, id) {
    const boss = await dialog.one({
      title: `who ${id.name} works for`, label: "boss", value: id.boss || "",
      note: "the whole subtree is re-capped: nobody under it can exceed the new boss",
    });
    if (!boss) return;
    await run(() => api.moveIdentity(f.machine, id.name, boss));
  },
  async employ(f, id) {
    const got = await dialog.ask({
      title: `employ ${id.name}`,
      note: "it goes on the work list and a session starts. load is model × effort, "
        + "and a fleet is charged for being a fleet.",
      submit: "employ",
      fields: [
        { name: "model", label: "model", kind: "choice", value: id.model || "sonnet",
          options: [{ value: "haiku", label: "haiku · 1" }, { value: "sonnet", label: "sonnet · 2" },
            { value: "opus", label: "opus · 3" }] },
        { name: "effort", label: "effort", kind: "choice", value: id.effort || "medium",
          options: [{ value: "low", label: "low · 1" }, { value: "medium", label: "medium · 2" },
            { value: "high", label: "high · 3" }, { value: "xhigh", label: "xhigh · 4" },
            { value: "max", label: "max · 6" }] },
      ],
    });
    if (!got) return;
    await run(() => api.employ(f.machine, id.name, got.model, got.effort));
  },
  async fire(f, id) {
    if (!await dialog.confirm({
      title: `fire ${id.name}?`,
      body: id.populated
        ? "its session is stopped and it comes off the work list. the identity, its memories and its workspace stay."
        : "it comes off the work list. the identity, its memories and its workspace stay.",
      submit: "fire it", danger: false,
    })) return;
    await run(() => api.fire(f.machine, id.name));
  },
  async poke(f, id) {
    const message = await dialog.one({
      title: `poke ${id.name}`, label: "message", value: "continue",
      note: "typed into its session without attaching",
      submit: "send it",
    });
    if (message === null) return;
    await run(() => api.poke(f.machine, id.name, message));
  },
  async refreshAgent(f, id) {
    if (!await dialog.confirm({
      title: `refresh ${id.name}?`,
      body: "a new session with a fresh context. the identity keeps its memories, mailbox, tasks and workspace — the conversation is what goes.",
      submit: "refresh it",
    })) return;
    await run(() => api.refreshAgent(f.machine, id.name));
  },
  // moveWorkspace changes where an agent works.
  //
  // The form carries `from` — the path this browser was showing — because the
  // agent machine refuses a move made against a stale view. That is the same
  // protection the library's writes get from a digest, and it matters here for the
  // same reason: what the operator is looking at is minutes old, and the old
  // directory still exists on disk afterwards, so a silent overwrite would look
  // exactly like success.
  async moveWorkspace(f, id) {
    const got = await dialog.ask({
      title: `move ${id.name}`,
      note: id.employed
        ? "its running session keeps the old directory until it is refreshed."
        : "it will start there the next time it is employed.",
      submit: "queue the move",
      fields: [
        { name: "workspace", label: "new directory", value: id.workspace || "" },
        // Two operations rather than a toggle, because that is what they are: one
        // copies an agent's files to a new path, the other leaves a checkout
        // exactly as it is and points the agent at it. A checkbox would make the
        // more destructive of the two the unlabelled default.
        {
          name: "how", label: "and", kind: "choice",
          value: "adopt",
          options: [
            { value: "adopt", label: "work in what is already there" },
            { value: "move", label: "move its files to the new directory" },
          ],
        },
      ],
    });
    if (got === null) return;

    const workspace = (got.workspace || "").trim();
    if (!workspace || workspace === id.workspace) return;

    await run(() => api.moveWorkspace(f.machine, id.name, {
      workspace,
      from: id.workspace || "",
      adopt: got.how !== "move",
    }));
  },
  // editInstruct writes one layer.
  //
  // Two things it has to say, because neither is visible from the screen: a prompt
  // persuades where a permission enforces, and an edit changes nothing about the
  // sessions already running — they keep the instructions they started with until
  // somebody refreshes them. The note names those agents rather than describing
  // them, since "some sessions" is not something an operator can act on.
  async editInstruct(f, p) {
    const text = await editor.open({
      title: `${p.wake ? "wake message" : "instructions"} — ${label(p)}`,
      text: p.text || "",
      note: p.wake
        ? "sent to an agent that has gone quiet. the most specific one wins; the others are not sent."
        : `added to what ${affects(f, p)} already gets. ${stale(f, p)}`,
    });
    if (text === null) return;

    const body = text.trim();
    if (body === (p.text || "").trim()) return;
    // An emptied editor is a clear rather than an empty file: a layer that says
    // nothing and no layer at all compose identically, and two spellings of one
    // state is a state somebody eventually disagrees about.
    if (body === "") {
      await run(() => api.clearInstruct(f.machine, p));
      return;
    }
    await run(() => api.setInstruct(f.machine, p, text));
  },

  async clearInstruct(f, p) {
    if (!await dialog.confirm({
      title: `clear the ${p.wake ? "wake message" : "instructions"} for ${label(p)}`,
      body: p.wake
        ? "the next wake falls through to the role's, then the fleet's, then “continue”."
        : `${affects(f, p)} stops getting this layer. ${stale(f, p)}`,
      submit: "clear it",
    })) return;
    await run(() => api.clearInstruct(f.machine, p));
  },

  // showInstruct is the composition, laid out in the order it composes.
  //
  // It is assembled here from the layers the mirror already carries rather than
  // fetched: the server cannot reach the agent machine, so a round trip would
  // return this same data with a delay on it. `orc instruct show <agent>` on the
  // machine is the authority; this is what the mirror says it will be.
  async showInstruct(f, p) {
    const role = roleOf(f, p.name);
    const layers = [
      ["the fleet", pick(f, "system", "")],
      role ? [`the ${role} role`, pick(f, "role", role)] : null,
      [p.name, pick(f, "identity", p.name)],
    ].filter((l) => l && l[1]);

    await dialog.show({
      title: `what ${p.name} is told`,
      note: "as the mirror last saw it; `orc instruct show` on the machine is the authority",
      text: layers.length === 0
        ? `${p.name} runs on claude's own instructions; nothing is set for it.`
        : layers.map(([heading, text]) => `# ${heading}\n\n${text}`).join("\n\n"),
    });
  },
  // installToolkit adds the permissions every fleet is made with.
  //
  // The confirmation names them, because the operator is agreeing to a set they
  // cannot otherwise see the whole of — and says what it will not do, since "this
  // installs permissions" reads as though it might overwrite the ones already here.
  async installToolkit(f, absent) {
    if (!await dialog.confirm({
      title: `add ${absent.length === 1 ? "a permission" : `${absent.length} permissions`} to ${f.machine}`,
      body: `${absent.map((t) => t.name).join(", ")} — nothing holds them until you assign them to a role, ` +
        "and permissions already in this fleet are left exactly as they are.",
      submit: "install",
      danger: false,
    })) return;
    await run(() => api.installToolkit(f.machine, f.operator));
  },
  async grant(f, id) {
    const got = await dialog.ask({
      title: `grant to ${id.name}`,
      note: "every grant lapses. without an expiry it lasts the current session.",
      submit: "grant it",
      fields: [
        { name: "permission", label: "permission" },
        { name: "until", label: "until", required: false,
          hint: "a duration like 2h or 30m — blank ties it to the session" },
      ],
    });
    if (!got) return;
    await run(() => api.grant(f.machine, id.name, got.permission, got.until));
  },
  async revoke(f, id, permission) {
    if (!await dialog.confirm({
      title: `revoke ${permission} from ${id.name}?`,
      body: "the grant ends early. what its role gives it is unaffected.",
      submit: "revoke it", danger: false,
    })) return;
    await run(() => api.revoke(f.machine, id.name, permission));
  },
  async removeIdentity(f, id) {
    if (!await dialog.confirm({
      title: `remove ${id.name}?`,
      body: "the identity, its key, its memories and its workspace all go. this cannot be undone, and orc refuses if a session is still running.",
      submit: "remove it",
    })) return;
    await run(() => api.removeIdentity(f.machine, id.name));
  },

  async setAuthority(f, role) {
    const got = await dialog.ask({
      title: `what ${role.name} asks for`,
      note: "what an identity actually gets is the lower of this and its boss's",
      fields: [{ name: "authority", label: "authority", kind: "number",
        value: role.authority, min: 1, max: 99 }],
    });
    if (!got) return;
    await run(() => api.setAuthority(f.machine, role.name, got.authority));
  },
  async addPermission(f, role) {
    const permission = await dialog.one({
      title: `give ${role.name} a permission`, label: "permission",
      note: "orc refuses if the role's authority is below the permission's floor",
    });
    if (!permission) return;
    await run(() => api.addPermission(f.machine, role.name, permission));
  },
  async setBudget(f, role) {
    const got = await dialog.ask({
      title: `what ${role.name} may employ`,
      note: "in units of thinking: sonnet/medium is 4, opus/max is 18, and four "
        + "sonnet/medium agents cost 21 rather than 16. 0 refuses every employ.",
      fields: [{ name: "load", label: "load", kind: "number",
        value: fleetView.roleBudget(f, role) ?? 0, min: 0, max: 4096 }],
    });
    if (!got) return;
    await run(() => api.setBudget(f.machine, role.name, got.load));
  },
  async removeRole(f, role) {
    if (!await dialog.confirm({
      title: `delete the role ${role.name}?`,
      body: "orc refuses while anybody holds it. this cannot be undone.",
      submit: "delete it",
    })) return;
    await run(() => api.removeRole(f.machine, role.name));
  },
  async removePermission(f, permission) {
    if (!await dialog.confirm({
      title: `delete the permission ${permission.name}?`,
      body: "orc refuses while any role holds it — take it off those first. this cannot be undone.",
      submit: "delete it",
    })) return;
    await run(() => api.removePermission(f.machine, permission.name, ""));
  },
  async takePermissionFrom(f, role, permission) {
    if (!await dialog.confirm({
      title: `take ${permission} off ${role.name}?`,
      body: "the permission itself stays; this role stops holding it.",
      submit: "take it off", danger: false,
    })) return;
    await run(() => api.removePermission(f.machine, permission, role.name));
  },
  async tend(f) { await run(() => api.tend(f.machine)); },

  // Rebuild and restart the whole fleet.
  //
  // The confirmation says what will happen rather than asking whether you are
  // sure, and it says the part people forget: this takes the site down for as
  // long as a build takes, and the page you are reading it on goes with it.
  async upgradeEverything(state) {
    const machines = (state.machines || []).map((m) => m.machine);
    if (!await dialog.confirm({
      title: "rebuild and restart everything?",
      body: `this pulls the tree and rebuilds every orc tool on this server and on `
        + `${machines.length} agent machine${machines.length === 1 ? "" : "s"}`
        + `${machines.length ? ` (${machines.join(", ")})` : ""}. `
        + `the site restarts, so this page will be unreachable for a minute; the agents `
        + `rebuild on their next sync. nothing queued is lost.`,
      submit: "rebuild everything",
    })) return;
    // Not through `run`: it refetches, and what it would be refetching from is a
    // server that is about to go away. The reply is the last thing this page
    // hears, so it is what gets shown.
    try {
      const got = await api.upgrade({});
      set({ upgrading: got, error: null });
    } catch (err) {
      set({ error: err });
    }
  },

  // --- tasks -------------------------------------------------------------
  //
  // One action per Macmuffin verb. They read like the CLI does on purpose:
  // somebody who knows `muff` should not have to learn a second vocabulary to
  // drive the same pool from a phone.
  //
  // Short operands are asked for with `prompt`, the way the library's new-file
  // and new-folder actions already do. The editor is for prose; a task name is
  // one line and deserves one line of interface.
  async createTask(machine) {
    // One sheet, not three prompts. A task is a name and two numbers, and asking
    // for them one at a time makes somebody answer the first before they can see
    // what the third even is.
    const got = await dialog.ask({
      title: "a new task",
      note: `it will be created on ${machine}, as a draft, on the next sync`,
      submit: "queue it",
      fields: [
        { name: "name", label: "name", value: "", hint: "letters, digits, and dashes" },
        { name: "priority", label: "priority", kind: "number", value: 3, min: 1, max: 5,
          hint: "1 low → 5 high" },
        { name: "difficulty", label: "difficulty", kind: "number", value: 3, min: 1, max: 5,
          hint: "1 easy → 5 hard" },
      ],
    });
    if (!got) return;
    await run(() => api.createTask(machine, got.name, got.priority, got.difficulty));
  },
  async pushTask(t) { await run(() => api.pushTask(t.machine, t.name)); },
  async claimTask(t) { await run(() => api.claimTask(t.machine, t.name)); },
  async leaveTask(t) {
    if (!await dialog.confirm({
      title: `leave ${t.name}?`,
      body: "you stop collaborating on it. it leaves on the next sync.",
      submit: "leave it", danger: false,
    })) return;
    await run(() => api.leaveTask(t.machine, t.name));
  },
  async assignTask(t) {
    const user = await dialog.one({
      title: `assign ${t.name}`, label: "to",
      value: t.owner || "", hint: "an agent you control",
      note: "macmuffin tells them, and refuses if you do not control them",
    });
    if (!user) return;
    await run(() => api.assignTask(t.machine, t.name, user));
  },
  async inviteToTask(t) {
    const user = await dialog.one({
      title: `invite to ${t.name}`, label: "agent",
      note: "a collaborator can work on it; the owner is unchanged",
    });
    if (!user) return;
    await run(() => api.inviteToTask(t.machine, t.name, user));
  },
  async kickFromTask(t, user) {
    if (!await dialog.confirm({
      title: `remove ${user} from ${t.name}?`,
      body: "they stop being a collaborator, and macmuffin tells them.",
      submit: `remove ${user}`,
    })) return;
    await run(() => api.kickFromTask(t.machine, t.name, user));
  },
  async scopeTask(t) {
    // Space-separated, as `muff scope` takes them, and a whole replacement rather
    // than an addition — which is also what `muff scope` does, so the current
    // scope is what the box starts with rather than an empty one.
    const typed = await dialog.one({
      title: `scope for ${t.name}`, label: "paths",
      value: (t.scope || []).join(" "),
      hint: "separated by spaces, relative to the repository",
      note: "this replaces the whole scope; a task cannot be edited or completed without one",
    });
    if (typed === null) return;
    const paths = typed.split(/\s+/).filter(Boolean);
    if (paths.length === 0) return;
    await run(() => api.scopeTask(t.machine, t.name, paths));
  },
  async worktreeTask(t) {
    const path = await dialog.one({
      title: `worktree for ${t.name}`, label: "path",
      value: t.worktree || "", hint: `on ${t.machine}`,
      note: "it must be the root of a git worktree",
    });
    if (!path) return;
    await run(() => api.worktreeTask(t.machine, t.name, path));
  },
  async setStatus(t, status) { await run(() => api.statusTask(t.machine, t.name, status)); },
  async addSubtask(t) {
    const sub = await dialog.one({
      title: `a step of ${t.name}`, label: "name",
      hint: "letters, digits, and dashes", submit: "add it",
    });
    if (!sub) return;
    await run(() => api.addSubtask(t.machine, t.name, sub));
  },
  async completeTask(t) {
    // Macmuffin refuses to complete a task with unfinished steps unless it is
    // forced. Asking is better than sending --force always: "finish it anyway"
    // is a decision, and the CLI makes you say it out loud too.
    const outstanding = t.total - t.done;
    const force = outstanding > 0;
    if (!await dialog.confirm({
      title: `complete ${t.name}?`,
      body: force
        ? `${outstanding} step${outstanding === 1 ? "" : "s"} of it ${outstanding === 1 ? "is" : "are"} not done. completing it now finishes the task anyway.`
        : "it is marked done and leaves the active pool.",
      submit: force ? "complete it anyway" : "complete it",
      danger: force,
    })) return;
    await run(() => api.completeTask(t.machine, t.name, "", force));
  },
  async completeSubtask(t, sub) { await run(() => api.completeTask(t.machine, t.name, sub, false)); },
  async deleteTask(t) {
    if (!await dialog.confirm({
      title: `delete ${t.name}?`,
      body: "the task and its journal go. this leaves on the next sync and cannot be undone.",
      submit: "delete it",
    })) return;
    await run(() => api.deleteTask(t.machine, t.name, ""));
  },
  async deleteSubtask(t, sub) {
    if (!await dialog.confirm({
      title: `delete the step ${sub}?`,
      body: `it is removed from ${t.name}. this cannot be undone.`,
      submit: "delete it",
    })) return;
    await run(() => api.deleteTask(t.machine, t.name, sub));
  },
};


// run performs one action and redraws from what the server then reports, and
// says whether it worked.
//
// The caller usually ignores the answer — the queue on screen is the truth, not
// anything the browser assumed. Compose is the exception: it may only clear the
// writer's words once they are safely queued somewhere else.
// originals holds what each file was when it was read: the hash, so an edit can
// say what it was made against without rehashing a megabyte on every keystroke,
// and the line ending, so it can be written back the way it was found.
const originals = new Map();

function hold(file) { return originals.get(library.fileKey(file))?.digest || ""; }

// outgoing is the last thing done to text before it leaves.
function outgoing(file, text) {
  return fromLF(text, originals.get(library.fileKey(file))?.ending || LF);
}

// under joins a name onto a directory. The root has no name, and `"" + "/" + x`
// is an absolute path — which the agent refuses, correctly and unhelpfully.
function under(dir, name) {
  return dir ? `${dir}/${name}` : name;
}

// --- the standing instructions ------------------------------------------

// label names a layer in a sentence: the fleet's has no name of its own.
function label(p) {
  if (p.kind === "system") return "the fleet";
  return p.kind === "role" ? `the ${p.name} role` : p.name;
}

// affects is who a layer reaches, in words, because the blast radius of the
// fleet's layer is every agent there is and the row it sits on does not say so.
function affects(f, p) {
  if (p.kind === "system") return "every agent on this machine";
  if (p.kind === "role") return `every agent holding ${p.name}`;
  return p.name;
}

// stale is the sentence about when it applies, naming the sessions that will not
// see it until they restart.
function stale(f, p) {
  const running = (f.identities || [])
    .filter((id) => id.employed && reached(f, p, id))
    .map((id) => id.name);
  if (running.length === 0) return "nothing is running, so the next session started uses it.";
  return `${running.join(", ")} ${running.length === 1 ? "keeps the instructions it" : "keep the instructions they"} started with until refreshed.`;
}

function reached(f, p, id) {
  if (p.kind === "system") return true;
  if (p.kind === "role") return id.role === p.name;
  return id.name === p.name;
}

function roleOf(f, name) {
  const id = (f.identities || []).find((i) => i.name === name);
  return id ? id.role || "" : "";
}

function pick(f, kind, name) {
  const got = (f.prompts || []).find((p) =>
    p.kind === kind && (p.name || "") === (name || "") && !p.wake);
  return got ? got.text : "";
}

// ask puts the text in front of the reader to change.
//
// The editor lives outside #view, so the redraw that happens on every sync
// cannot replace the textarea somebody is typing into.
async function ask(what, text) {
  return editor.open({
    title: what,
    text,
    note: "queued here; it leaves on the next sync, and is refused if the file changed meanwhile",
  });
}

async function run(fn) {
  try {
    await fn();
    await refresh();
    return true;
  } catch (err) {
    if (err instanceof ApiError && err.code === "unauthenticated") return false;
    set({ error: err });
    return false;
  }
}

// --- loading ------------------------------------------------------------

async function refresh() {
  const route = location.hash.slice(1) || "/inbox";
  try {
    const [session, inbox, archive, sent, queue, tasks, lib, fleet] = await Promise.all([
      api.session(), api.inbox(), api.archive(), api.sent(), api.queue(), api.tasks(),
      // The tree is structure without text, so fetching it every refresh costs
      // kilobytes rather than the megabytes the repository actually is.
      //
      // A failure is kept rather than dropped. Swallowing it turned an older
      // server with no such route — and a network that was simply down — into a
      // tab confidently reporting that no machine mirrors a repository, which is
      // a claim about the fleet made from a fact about the request.
      api.library().catch((err) => ({ unreachable: String(err.message || err) })),
      // Same treatment as the library: a request that failed is a fact about the
      // request, not about whether this machine runs a fleet.
      api.fleet().catch((err) => ({ unreachable: String(err.message || err) })),
    ]);
    setCSRF(session.csrf);

    // The admin view can be large, so it is fetched only while it is on
    // screen. Everything else is small enough to keep current always.
    let admin = state.admin;
    if (route.startsWith("/admin") && session.admin) {
      admin = await api.adminState();
    }

    let detail = state.detail;
    const match = /^\/message\/([^?]+)(?:\?machine=(.*))?$/.exec(route);
    if (match) {
      detail = await api.message(decodeURIComponent(match[1]),
        match[2] ? decodeURIComponent(match[2]) : "");
    } else {
      detail = null;
    }

    set({
      session, adminEnabled: session.admin, machines: session.machines || [],
      inbox: inbox.messages || [], archive: archive.messages || [],
      sent: sent.messages || [],
      library: lib,
      fleet: (fleet && fleet.fleets) || [], fleetError: fleet && fleet.unreachable,
      queue: queue.queue || [], tasks: tasks.tasks || [],
      detail, admin, error: null,
    });
  } catch (err) {
    if (err instanceof ApiError && err.code === "unauthenticated") return;
    set({ error: err });
  }
}

// --- live updates -------------------------------------------------------

function listen() {
  const stream = new EventSource("/api/v1/events");
  stream.addEventListener("change", () => { refresh(); });
  stream.addEventListener("error", () => {
    // The stream drops; EventSource reconnects on its own. Polling is the
    // fallback so the page degrades rather than freezing.
    stream.close();
    setTimeout(listen, 5000);
  });
}

window.addEventListener("hashchange", () => { refresh(); });

// The staleness clock has to keep counting even when nothing arrives.
setInterval(() => { mount(statusBar, views.status(state)); }, 1000);
setInterval(() => { refresh(); }, 60000);

refresh().then(listen);
