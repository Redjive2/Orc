Orc (just the tool, from here on out) is a minimal agentic orchestrator.

It can spawn, attach to, track, and manage Claude Code sessions. It also implicitly provides an 'identity' to each code session.

A given identity is unique, and keeps personal memories + authorization info + a workspace + identifying information all in one place. This lets several Code sessions each fill the role of a single, persistent agent.

It also means that commands like `mailman` and `muff` don't need to expose extra machinery for accounts. All the information is tracked correctly by Orc.

So, Orc has two purposes:
- Create and manage 'identities'
- Assign Claude Code instances to identities to keep each alive as need be

Beside that, it's a regular meta-harness. It just takes several identities, populates them with Code instances, and sends them on their merry way (while exposing a few primitives to make things easier)