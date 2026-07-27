// The one place that talks to the server.
//
// Every call goes through `request`, so the CSRF token, the error shape, and
// the "your session ended" case are handled once rather than at each call site.

let csrf = "";

export function setCSRF(token) { csrf = token; }

export class ApiError extends Error {
  constructor(code, message, status) {
    super(message || code);
    this.code = code;
    this.status = status;
  }
}

async function request(method, path, body) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrf) headers["X-CSRF-Token"] = csrf;

  let response;
  try {
    response = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
    });
  } catch (cause) {
    throw new ApiError("unavailable", "the server could not be reached", 0);
  }

  if (response.status === 401) {
    // The session ended underneath us. Going to the login page is the honest
    // response; rendering an empty inbox would suggest there is no mail.
    window.location.assign("/login");
    throw new ApiError("unauthenticated", "not authenticated", 401);
  }

  const text = await response.text();
  let payload = null;
  if (text.length > 0) {
    try { payload = JSON.parse(text); } catch { payload = null; }
  }

  if (!response.ok) {
    const err = payload && payload.error ? payload.error : {};
    throw new ApiError(err.code || "internal", err.message || response.statusText, response.status);
  }
  return payload;
}

// instructPath is §10's table, in one place, so the routes cannot drift between the
// six calls that would otherwise each spell one out.
function instructPath(kind, name, wake) {
  const root = wake ? "/api/v1/instruct/wake" : "/api/v1/instruct";
  if (kind === "system") return wake ? root : `${root}/system`;
  const group = kind === "role" ? "roles" : "identities";
  return `${root}/${group}/${encodeURIComponent(name)}`;
}

export const api = {
  session: () => request("GET", "/api/v1/session"),
  inbox: (machine) => request("GET", withMachine("/api/v1/inbox", machine)),
  archive: (machine) => request("GET", withMachine("/api/v1/archive", machine)),
  sent: (machine) => request("GET", withMachine("/api/v1/sent", machine)),
  message: (puid, machine) => request("GET", withMachine(`/api/v1/messages/${encodeURIComponent(puid)}`, machine)),
  queue: () => request("GET", "/api/v1/queue"),
  tasks: () => request("GET", "/api/v1/tasks"),
  adminState: () => request("GET", "/api/v1/admin/state"),
  library: () => request("GET", "/api/v1/library"),
  libraryFile: (path, machine) =>
    request("GET", withMachine(`/api/v1/library/file?path=${encodeURIComponent(path)}`, machine)),

  reply: (puid, machine, subject, body) =>
    request("POST", withMachine(`/api/v1/messages/${encodeURIComponent(puid)}/reply`, machine),
      { subject, body }),
  markRead: (puid, machine) =>
    request("POST", withMachine(`/api/v1/messages/${encodeURIComponent(puid)}/read`, machine), {}),
  archiveMessage: (puid, machine) =>
    request("POST", withMachine(`/api/v1/messages/${encodeURIComponent(puid)}/archive`, machine), {}),
  // Mailman prunes the archive and nothing else, so deleting live mail is an
  // archive and then this. Both are queued from the browser rather than folded
  // into one call, because one operation is one Mailman command.
  pruneMessage: (puid, machine) =>
    request("POST", withMachine(`/api/v1/messages/${encodeURIComponent(puid)}/prune`, machine), {}),
  cc: (cuid, machine, user) =>
    request("POST", `/api/v1/convos/${encodeURIComponent(cuid)}/cc`, { machine, user }),
  send: (machine, to, subject, body) =>
    request("POST", "/api/v1/messages", { machine, to, subject, body }),
  retry: (id) => request("POST", `/api/v1/queue/${encodeURIComponent(id)}/retry`, {}),
  drop: (id) => request("DELETE", `/api/v1/queue/${encodeURIComponent(id)}`),
  clearQueue: (states) => request("POST", "/api/v1/queue/clear", states ? { states } : {}),
  // Editing the mirrored checkout. Every one queues and leaves on the next sync.
  writeFile: (machine, path, text, base) =>
    request("POST", "/api/v1/library/write", { machine, path, text, base }),
  createFile: (machine, path, text) =>
    request("POST", "/api/v1/library/create", { machine, path, text }),
  deleteFile: (machine, path, base) =>
    request("POST", "/api/v1/library/delete", { machine, path, base }),
  makeDir: (machine, path) => request("POST", "/api/v1/library/mkdir", { machine, path }),
  removeDir: (machine, path) => request("POST", "/api/v1/library/rmdir", { machine, path }),
  // The manifest travels with the request: it is what the agent checks the real
  // directory against, so a folder that gained work since the mirror was taken
  // is refused rather than swept up with the rest.
  removeTree: (machine, path, paths) =>
    request("POST", "/api/v1/library/rmtree", { machine, path, paths }),

  // The task verbs. One call per Macmuffin command that changes something; each
  // queues and leaves on the next sync, like everything else the browser does.
  createTask: (machine, name, priority, difficulty) =>
    request("POST", "/api/v1/tasks", { machine, name, priority, difficulty }),
  pushTask: (machine, name) => taskCall("push", machine, name, {}),
  claimTask: (machine, name) => taskCall("claim", machine, name, {}),
  leaveTask: (machine, name) => taskCall("leave", machine, name, {}),
  assignTask: (machine, name, user) => taskCall("assign", machine, name, { user }),
  inviteToTask: (machine, name, user) => taskCall("invite", machine, name, { user }),
  kickFromTask: (machine, name, user) => taskCall("kick", machine, name, { user }),
  scopeTask: (machine, name, paths) => taskCall("scope", machine, name, { paths }),
  worktreeTask: (machine, name, path) => taskCall("worktree", machine, name, { path }),
  statusTask: (machine, name, status) => taskCall("status", machine, name, { status }),
  addSubtask: (machine, name, sub) => taskCall("subtasks", machine, name, { sub }),
  // The description. PUT replaces the whole document, DELETE removes it — the two
  // are different intents and the queue says which happened.
  describeTask: (machine, name, text) =>
    request("PUT", `/api/v1/tasks/${encodeURIComponent(name)}/description`, { machine, text }),
  undescribeTask: (machine, name) =>
    request("DELETE", `/api/v1/tasks/${encodeURIComponent(name)}/description`, { machine }),
  completeTask: (machine, name, sub, force) =>
    taskCall("complete", machine, name, { sub: sub || undefined, force: force || undefined }),
  // The fleet. Reading is one call because a fleet is one derived thing; the
  // verbs are one call each, and every one queues.
  fleet: () => request("GET", "/api/v1/fleet"),
  // The series: what each agent has cost and touched, by the hour. Its own route
  // because it is the one thing the server keeps that a snapshot does not carry —
  // a snapshot is replaced whole and a rate needs history.
  activity: (since) => request("GET", `/api/v1/activity?since=${encodeURIComponent(since)}`),

  newIdentity: (machine, name) => request("POST", "/api/v1/fleet/identities", { machine, name }),
  newRole: (machine, name, authority, description) =>
    request("POST", "/api/v1/fleet/roles", { machine, name, authority, description }),
  newPermission: (machine, name, floor, patterns) =>
    request("POST", "/api/v1/fleet/permissions", { machine, name, floor, patterns }),

  // The toolkit, by name of the operator: the queued action says exactly what it
  // will run rather than depending on which user the sync happens to be.
  installToolkit: (machine, operator) =>
    request("POST", "/api/v1/fleet/toolkit", { machine, name: operator }),

  assignRole: (machine, name, role) => fleetCall("identities", name, "role", machine, { role }),
  moveIdentity: (machine, name, boss) => fleetCall("identities", name, "move", machine, { boss }),
  employ: (machine, name, model, effort) =>
    fleetCall("identities", name, "employ", machine, { model: model || undefined, effort: effort || undefined }),
  fire: (machine, name) => fleetCall("identities", name, "fire", machine, {}),
  poke: (machine, name, message) =>
    fleetCall("identities", name, "poke", machine, { message: message || undefined }),
  refreshAgent: (machine, name) => fleetCall("identities", name, "refresh", machine, {}),
  // `from` is where the browser saw it working. The server requires it: a snapshot
  // is minutes old by the time somebody clicks, and without it a move made here
  // would silently overturn one made on the machine in between.
  moveWorkspace: (machine, name, { workspace, from, adopt }) =>
    fleetCall("identities", name, "workspace", machine, { workspace, from, adopt }),
  // Moving the whole machine's checkout, rather than one agent's workspace. It
  // carries no `from`: the machine sets the root to what is named rather than
  // stepping it from where it was, so a stale view lands in the same place as a
  // fresh one.
  moveLibrary: (machine, workspace) =>
    request("POST", "/api/v1/library/root", { machine, workspace }),
  // The standing instructions. The layer is in the path, so a call cannot name one
  // layer and carry another; `name` is the role's or the agent's, and empty for the
  // fleet's own.
  setInstruct: (machine, { kind, name, wake }, text) =>
    request("PUT", instructPath(kind, name, wake), { machine, text }),
  clearInstruct: (machine, { kind, name, wake }) =>
    request("DELETE", instructPath(kind, name, wake), { machine }),

  grant: (machine, name, permission, until) =>
    fleetCall("identities", name, "grant", machine, { permission, until: until || undefined }),
  revoke: (machine, name, permission) =>
    fleetCall("identities", name, "revoke", machine, { permission }),
  removeIdentity: (machine, name) =>
    request("DELETE", `/api/v1/fleet/identities/${encodeURIComponent(name)}`, { machine }),

  setAuthority: (machine, name, authority) =>
    fleetCall("roles", name, "authority", machine, { authority }),
  addPermission: (machine, name, permission) =>
    fleetCall("roles", name, "permissions", machine, { permission }),
  setBudget: (machine, name, load) => fleetCall("roles", name, "budget", machine, { load }),
  removeRole: (machine, name) =>
    request("DELETE", `/api/v1/fleet/roles/${encodeURIComponent(name)}`, { machine }),
  // A permission's floor and clauses, together: Orc's `edit permission` keeps
  // whichever half it is not given, so both are always sent from here — a form
  // that showed both and changed one would otherwise leave the other to a default.
  editPermission: (machine, name, floor, patterns) =>
    request("PATCH", `/api/v1/fleet/permissions/${encodeURIComponent(name)}`,
      { machine, floor, patterns }),
  // With a role it narrows that one role; without, it deletes the permission.
  removePermission: (machine, name, role) =>
    request("DELETE", `/api/v1/fleet/permissions/${encodeURIComponent(name)}`,
      { machine, role: role || undefined }),
  tend: (machine) => request("POST", "/api/v1/fleet/tend", { machine }),
  // One route for both cycles and all three layers: the body says which cycle and
  // whose layer, and neither `identity` nor `role` means the fleet's own.
  pace: (machine, body) => request("POST", "/api/v1/fleet/pace", { machine, ...body }),
  // Sync's own interval, which is the server's rather than a fleet's: it is about
  // the link between the two machines, so it is not queued to either.
  // One setting per call, because that is how orc changes it: a whole-list write
  // from a stale form would revert whatever somebody else set in between.
  tariff: (machine, setting, load) =>
    request("POST", "/api/v1/fleet/tariff", { machine, setting, load }),
  syncPace: () => request("GET", "/api/v1/sync/pace"),
  setSyncPace: (watch) => request("POST", "/api/v1/sync/pace", { watch }),

  // Rebuild and restart everything. One request: the server upgrades itself, and
  // every agent machine gets a queued action it applies on its next sync.
  upgrade: (body) => request("POST", "/api/v1/upgrade", body || {}),

  deleteTask: (machine, name, sub) =>
    request("DELETE", `/api/v1/tasks/${encodeURIComponent(name)}`,
      { machine, sub: sub || undefined }),
  logout: () => request("POST", "/api/v1/logout", {}),
};

// taskCall is the shape every task verb but two shares: the task in the path, the
// operands in the body. Create has no task yet and delete is a DELETE, so those
// two are written out above.
function taskCall(verb, machine, name, body) {
  return request("POST", `/api/v1/tasks/${encodeURIComponent(name)}/${verb}`,
    { machine, ...body });
}

// fleetCall is the shape every fleet verb but the creates and deletes shares: the
// subject in the path, the operands in the body.
function fleetCall(kind, name, verb, machine, body) {
  return request("POST",
    `/api/v1/fleet/${kind}/${encodeURIComponent(name)}/${verb}`, { machine, ...body });
}

function withMachine(path, machine) {
  if (!machine) return path;
  const sep = path.includes("?") ? "&" : "?";
  return `${path}${sep}machine=${encodeURIComponent(machine)}`;
}
