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
  cc: (cuid, machine, user) =>
    request("POST", `/api/v1/convos/${encodeURIComponent(cuid)}/cc`, { machine, user }),
  send: (machine, to, subject, body) =>
    request("POST", "/api/v1/messages", { machine, to, subject, body }),
  retry: (id) => request("POST", `/api/v1/queue/${encodeURIComponent(id)}/retry`, {}),
  drop: (id) => request("DELETE", `/api/v1/queue/${encodeURIComponent(id)}`),
  // Editing the mirrored checkout. Every one queues and leaves on the next sync.
  writeFile: (machine, path, text, base) =>
    request("POST", "/api/v1/library/write", { machine, path, text, base }),
  createFile: (machine, path, text) =>
    request("POST", "/api/v1/library/create", { machine, path, text }),
  deleteFile: (machine, path, base) =>
    request("POST", "/api/v1/library/delete", { machine, path, base }),
  makeDir: (machine, path) => request("POST", "/api/v1/library/mkdir", { machine, path }),
  removeDir: (machine, path) => request("POST", "/api/v1/library/rmdir", { machine, path }),

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
  completeTask: (machine, name, sub, force) =>
    taskCall("complete", machine, name, { sub: sub || undefined, force: force || undefined }),
  // The fleet. Reading is one call because a fleet is one derived thing; the
  // verbs are one call each, and every one queues.
  fleet: () => request("GET", "/api/v1/fleet"),

  newIdentity: (machine, name) => request("POST", "/api/v1/fleet/identities", { machine, name }),
  newRole: (machine, name, authority, description) =>
    request("POST", "/api/v1/fleet/roles", { machine, name, authority, description }),
  newPermission: (machine, name, floor, patterns) =>
    request("POST", "/api/v1/fleet/permissions", { machine, name, floor, patterns }),

  assignRole: (machine, name, role) => fleetCall("identities", name, "role", machine, { role }),
  moveIdentity: (machine, name, boss) => fleetCall("identities", name, "move", machine, { boss }),
  employ: (machine, name, model, effort) =>
    fleetCall("identities", name, "employ", machine, { model: model || undefined, effort: effort || undefined }),
  fire: (machine, name) => fleetCall("identities", name, "fire", machine, {}),
  poke: (machine, name, message) =>
    fleetCall("identities", name, "poke", machine, { message: message || undefined }),
  refreshAgent: (machine, name) => fleetCall("identities", name, "refresh", machine, {}),
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
  // With a role it narrows that one role; without, it deletes the permission.
  removePermission: (machine, name, role) =>
    request("DELETE", `/api/v1/fleet/permissions/${encodeURIComponent(name)}`,
      { machine, role: role || undefined }),
  tend: (machine) => request("POST", "/api/v1/fleet/tend", { machine }),

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
