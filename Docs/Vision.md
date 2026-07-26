# §1 Orc

Orc is a minimal AI orchestrator designed to work with Claude.

On its own, all it does is run Claude Code sessions and plug into the tooling
around it. It works with a series of other, small applications to be far more
effective:

| Tool       | Command   | Does                                                            |
|------------|-----------|-----------------------------------------------------------------|
| Anno       | `anno`    | Indexes and reads files in annotated blocks, to preserve tokens |
| Communiqué | `cq`      | Communicates with the user via a remote web server              |
| Dock       | `dock`    | Reads and links documentation efficiently, to preserve tokens   |
| Macmuffin  | `muff`    | Creates, tracks, and manages tasks                              |
| Mailman    | `mailman` | Sends inter-agent mail                                          |
| Orc        | `orc`     | The core user-facing tool; also spawns sub-agents               |

Each of these is integral to getting work done with Orc. Orc also works with Git
and hooks into Claude.

One tool sits outside that set. It is not part of getting work done, nothing
depends on it, and no agent ever runs it:

| Testing tool | Command    | Does                                                        |
|--------------|------------|-------------------------------------------------------------|
| Orcprobe     | `orcprobe` | Copies the whole Orc world into a sandbox I can break       |

Documentation on each lives in its own folder here. To start, read `Vision.md`
for a high-level overview of the tool, then `Reference.md` for the CLI shape, and
extend to other files as needed.
