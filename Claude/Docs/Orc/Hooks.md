# Orc — Claude Code hook integration

`orc-hook` is two things at once, and they are separate jobs that happen to share
a binary because Claude fires both through the same mechanism.

| Hook | Fires on | Does |
|------|----------|------|
| **the permission boundary** | `PreToolUse` on `Read`, `Edit`, `Write`, `NotebookEdit`, `MultiEdit`, `Bash`, `Agent` | Blocks a tool call an identity's permissions do not allow, and says which clause would allow it. |
| **the event feed** | `UserPromptSubmit`, `PostToolUse`, `Notification`, `Stop`, `SubagentStop`, `SessionStart`, `SessionEnd` | Appends one line per firing to the session's `events.jsonl`, which is what `orc attach` draws. |

They are two entries in `settings.json` rather than one matcher over both,
because they answer different questions and a single entry would make the hook
guess which job it was doing.

It runs on `PreToolUse`, not `PostToolUse`: a permission violation has to be
prevented, not reported after the write.

---

## Installing

Nothing to install by hand. `orc employ` writes each identity's
`settings.json` into its own `CLAUDE_CONFIG_DIR` when it prepares the session, so
the wiring exists because the session exists.

What must be true is that the binaries are on the session's `PATH`:

```bash
cd Orc && go build -o ~/.local/bin/orc ./cmd/orc && go build -o ~/.local/bin/orc-hook ./cmd/orc-hook
```

`orc-hook` is named bare rather than by path, so a machine can install Orc
anywhere and an Orcprobe shim can stand in front of it. `orc doctor` is what says
whether it is actually findable.

The compiled file looks like this — it is written, not authored, and editing it
by hand lasts until the next `orc employ`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Read|Edit|Write|NotebookEdit|MultiEdit|Bash|Agent",
        "hooks": [{ "type": "command", "command": "orc-hook", "timeout": 10 }]
      }
    ]
  }
}
```

The hook's own deadline is 2 seconds, well inside the 10 above — the outer
timeout is a backstop, not the mechanism.

---

## Which identity is in force

The hook is never told on the command line whose session it is in. It reads
`$ORC_USER` from the session's environment, which `orc employ` set when it
started the session, and which the identity cannot change without losing its own
credential.

That is the whole of it. There is no search, no inference, and no fallback to a
"current" identity — a hook that guessed which agent it was guarding would guard
the wrong one exactly when it mattered.

---

## The ladder

This is the one hook in the tree that may not simply fail open. Anno's and
Macmuffin's are bystanders; this one is the boundary, so it degrades in three
rungs:

| What it can read | What it decides |
|------------------|-----------------|
| the live store | current permissions, grants included |
| only `authz.json` | the permissions as they were at populate, and it says so |
| neither | reads pass; writes and `Agent` block |

The third rung is the honest consequence of being the only brake. A stalled write
is recoverable and says what to do; an unbounded one is not. Reads still pass,
because a blocked read produces a confused agent and discloses nothing new — it
already has whatever the last successful read gave it.

---

## What a refusal looks like

Every refusal has three parts: what was refused, why, and the way forward. They
go to stderr, which Claude feeds back to the agent as the reason its tool call
failed.

The rule behind that shape is the one Macmuffin's hook doc states: **a refusal
that does not say how to proceed just gets worked around.** An agent told only
"no" will try the same thing another way. An agent told which permission it lacks
and which command grants it will ask for that instead.

Two refusals are worth knowing by sight:

- **A subagent.** `Agent` and `Task` are both denied, for reasons that have nothing
  to do with paths: the fleet's load accounting depends on an identity being one
  session, and a subagent is a session Orc did not start and cannot see. Both
  spellings, because Claude tests a tool call against both and which one a build
  uses varies — naming one of them left the other open on a fleet that had a rule
  in its settings, a refusal in its hook, and `orc doctor` reporting subagents were
  off. This denial comes before the store is even opened, because the accounting
  depends on it holding whether or not the store is readable.
- **A session started from a shell.** `claude` and `orc-session` in a command line
  are the same thing by another route, and they are refused the same way — before
  the clauses, because no clause should permit it. An identity trusted with a shell
  is not thereby trusted with a second fleet nobody can see. What remains is a
  command line orc cannot read, which needs `shell(**)` and is a decision somebody
  made rather than a gap.
- **The store itself.** Reaching into `~/.orc` is worded as a containment failure
  rather than a permission problem, because that is what it is — no clause can
  permit it, so there is nothing to ask for. The message names the parts of that
  directory that *are* the agent's own, because an agent that wandered in by
  accident, through a glob or a recursive search, needs to know that.

---

## What this does not cover

**`bypassPermissions` is unverified.** Whether a `deny` rule in `settings.json`
survives that mode has not been proven; the probe is `Claude/Mock/deny-probe.sh`
and it needs a credential. Everything is therefore built to the pessimistic
reading: the compiled rules are documentation, and every denial that matters is
*also* enforced by the hook. That is why the `Agent` denial appears in both
places, and why the keyring's is in the hook at all. A rule that might be ignored
cannot be the only thing standing in front of the fleet's credentials.

**Bash is guarded, not understood.** The hook sees a command line, not a syscall
trace. A shell command that writes through an interpreter, a symlink, or a
process it spawns is beyond what any `PreToolUse` hook can see. This is the same
limit Macmuffin's scope guard has and states.

**The settings are a snapshot.** They were true when the session started. A
`grant` or a `move` a minute later is not in them, and the hook is what reads live
state. This is not a defect in the compilation — it is why the hook exists.

---

## The rules that keep it safe

1. **Exit 0 or 2, never anything else.** The codes are Claude's, not Orc's: 0
   lets the call proceed, 2 blocks it and feeds stderr back. A hook that exited
   70 on a defect would turn a bug in Orc into a broken session, so a defect
   exits 0 and says so on stderr. `Claude/Docs/ExitCodes.md` deliberately does
   not apply here.
2. **A 2-second deadline on the whole check.** A store on a slow disk or behind a
   stalled lock must not freeze a session. Two seconds is far longer than a
   healthy check and far shorter than a human notices as a hang.
3. **Unparseable input and unknown events exit 0.** A hook that refused what it
   did not recognise would break every session on the day Claude adds an event.
4. **It never writes fleet state.** It appends to its own session's event feed —
   unlocked, outside the derivation — and nothing else. That is the one
   clarification to Plan.md §7.3's "never writes".
5. **The feed is never fatal.** Every error writing it is dropped. A feed that
   could not be written is a view with a gap in it; a tool call that failed
   because its logging failed would be the hook doing precisely what a bystander
   must not.

---

## Living with the other hooks

Anno's and Macmuffin's hooks run on the same tool calls. The order Claude runs
them in is not specified, and none of the three depends on it: each answers a
different question, and a block from any one is a block.

- **Macmuffin** asks whether the file is inside the scope of the task in force.
- **Anno** asks whether an annotated block is being edited coherently.
- **Orc** asks whether this identity may touch this path at all.

Orc's is the outermost of the three — a path an identity has no permission for is
refused whether or not a task claims it — but nothing in the code depends on that
being true, and an agent may see a refusal from any of them first.
