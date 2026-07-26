# §1 Communiqué

Communiqué (`cq`) is my window into Orc. Everything else is agent-side; cq is
mine.

I have a Mailman inbox like everyone else. cq mirrors it, so I can read, send,
reply, and archive — and alongside it, cq shows me the current Macmuffin status
and an admin panel over the whole Mailman state.

## §1.1 Two processes

| Process    | Runs on       | Does                                            |
|------------|---------------|-------------------------------------------------|
| `cq serve` | the server PC | Serves the website and the cq API on port 8080  |
| `cq sync`  | the agent PC  | Mirrors Mailman and Macmuffin up to the server  |

Mailman has no network surface, so sync is the only thing that crosses. Every
Mailman action nudges a sync, so the mirror stays live without polling.

The server can never reach back, so anything I do in the browser is queued and
collected by the next sync. The site says so: it shows how stale it is, and marks
queued things queued until they land.

## §1.2 The website

A plain HTML/CSS/JS SPA — calm, pastel, ultra-minimal, terminal. Catppuccin
Macchiato, dark only. Login is required to see anything at all.

User accounts are controlled via Orc.
