# Macmuffin — Claude Code hook integration

`Docs/Macmuffin/Vision.md` asks that a task's scope "enforces editing (even via
Anno) only on files in scope via Claude Hook". `muff-hook` is that enforcement.

| Hook | Fires on | Does |
|------|----------|------|
| **scope guard** | `Edit`, `Write`, `NotebookEdit`, `MultiEdit`, `Bash` | Blocks a write to a file outside the scope of the task in force, and says how to proceed. |

It runs on `PreToolUse`, not `PostToolUse`: a scope violation has to be
prevented, not reported after the write.

---

## Installing

Build both binaries and put them on `PATH`:

```bash
cd Macmuffin && go build -o ~/.local/bin/muff ./cmd/muff && go build -o ~/.local/bin/muff-hook ./cmd/muff-hook
```

Then add this to `.claude/settings.json` (project) or `~/.claude/settings.json`
(user):

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write|NotebookEdit|MultiEdit|Bash",
        "hooks": [
          {
            "type": "command",
            "command": "muff-hook",
            "timeout": 10,
            "statusMessage": "checking scope"
          }
        ]
      }
    ]
  }
}
```

The hook's own deadline is 2 seconds, well inside the 10 above — the outer
timeout is a backstop, not the mechanism.

---

## Which task is in force

The hook is never told on the command line which task an agent is working on. It
works it out, in this order:

1. **`$MUFF_TASK`**, if set. An agent that says what it is working on is believed.
2. **The worktree binding.** The session's working directory is resolved to its
   git worktree root, and that root is looked up in the store. `muff worktree
   <task> <path>` is what creates the binding.
3. **Nothing.** No task is in force, and nothing is enforced.

Case 3 is the common one and the safe one: an agent that never opted in is never
blocked. So is a task with no scope — scope is opt-in per task.

---

## What a refusal looks like

Exit code 2 is Claude's block code; stderr is fed back to the agent.

```
muff: internal/render/render.go is outside the scope of fix-the-parser.

  in scope:  internal/tree/  internal/marker/  cmd/anno/main.go

Add it with `muff scope fix-the-parser internal/render/render.go`, or work on a
task that covers it.
```

Both ways forward are named, because a refusal that does not say how to proceed
just gets worked around.

A path that resolves *outside* the worktree gets a different message. An
out-of-scope edit is routine; a path escaping the tree is a containment failure,
and it says so.

---

## Bash, and what this does not cover

Deciding what an arbitrary shell command will write is undecidable, and the hook
does not pretend otherwise. It recognises exactly one shape — `anno write
<path>`, including after a `cd … &&` — because that is how Anno reaches the
filesystem.

That is belt-and-braces. The real mechanism is **`muff check-scope <paths…>`**,
which exits 0 or 9 and prints its reasoning on stderr. `anno write` calls it
before it reads the file, and relays the answer — Anno holds no opinion about
tasks, scopes, or which is in force. This is the "even via Anno" the vision asks
for, and it works because on Anno's side the question is decidable: Anno knows
exactly which file it is about to change.

Everything except a definite exit 9 is a yes there too. Macmuffin missing,
broken, slow, or unauthenticated must not stop somebody editing their own files.

Everything else through `Bash` — a heredoc, a `sed -i`, a script that writes ten
files — is out of reach. Saying so plainly is better than implying a guarantee
that does not hold.

---

## The rules that keep it safe

A hook fires on every matching tool call in somebody's live session, so one rule
outranks everything else: **only a genuine violation may block; everything
unexpected exits 0 silently.** Each rule below is a test in
`internal/hook/hook_test.go`.

1. **Only a genuine violation blocks.** Unparseable input, an unknown event, a
   tool Macmuffin does not care about, a missing path, no task in force, a
   missing store, an unreadable one — all pass. `FuzzRun` is the general form:
   no input whatsoever produces an exit code other than 0 or 2.
2. **A scopeless task never blocks.**
3. **The hook never writes.** It opens the store read-only, through a door that
   creates nothing and refuses every write path. A hook that journalled on every
   tool call would turn the journal into a log of keystrokes and put a lock in
   the path of every edit. The test fingerprints the whole store before and
   after rather than reading the code.
4. **A slow or broken store must not stall a session.** The check is bounded by a
   2-second deadline; on timeout it exits 0 with a note on stderr. The test
   stalls the store for real — the version file is a FIFO with no writer.

Rule 4 has a cost, and it is stated rather than hidden: **while the store is
broken, a violation gets through.** Failing open is right for a hook and failing
closed is right for `muff`'s own permission checks. The difference is that a
permission check is asked a question it can always answer, and a hook is a
bystander in somebody else's session.

---

## Living with Anno's hook

Both hooks can be installed at once. Anno's runs on `PostToolUse` and checks that
annotations still parse; Macmuffin's runs on `PreToolUse` and checks scope. They
never see the same event, and neither knows about the other.
