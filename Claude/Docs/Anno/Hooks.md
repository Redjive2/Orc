# Anno — Claude Code hook integration

Vision.md's closing line is the goal: Anno "is combined with Claude Code hooks to
let Claude agents use this functionality and write to specific annotations,
saving tokens and time while minimizing accidental scope leak in changes."

Two hooks deliver that, both on `PostToolUse`:

| Hook | Fires on | Does |
|---|---|---|
| **guard** | `Edit`, `Write`, `NotebookEdit`, `MultiEdit` | Blocks an edit that left the file's annotations unparseable, and says why. |
| **index** | `Read` | Hands back the file's annotation tree, so the agent can address regions by name instead of re-reading the file. |

Both are the single binary `anno-hook`, which reads the event on stdin and
decides which job applies.

---

## Installing

Build both binaries and put them on `PATH`:

```bash
cd Anno && go build -o ~/.local/bin/anno ./cmd/anno && go build -o ~/.local/bin/anno-hook ./cmd/anno-hook
```

Then add this to `.claude/settings.json` (project) or `~/.claude/settings.json`
(user). Both hooks are the same command; `anno-hook` routes on `tool_name`.

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|NotebookEdit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "anno-hook",
            "timeout": 10,
            "statusMessage": "checking annotations"
          }
        ]
      },
      {
        "matcher": "Read",
        "hooks": [
          {
            "type": "command",
            "command": "anno-hook",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
```

The two entries could be collapsed into one with matcher
`Edit|Write|NotebookEdit|MultiEdit|Read`, since the binary routes internally.
They are kept apart so either can be switched off on its own.

## What each hook does

### guard — after an edit

Loads the edited file and rebuilds its annotation tree. If the tree no longer
parses **and** the file still contains annotation markers, the hook exits 2. That
blocks the tool call and feeds stderr back to the agent:

```
anno: this edit left the annotations in /src/app.go unparseable, so they can no
longer be addressed.

/src/app.go:41: close of "declarations" matches no open annotation

Fix the markers, or run `anno index /src/app.go` to see what parses.
```

The agent sees the message and repairs the markers on its next turn.

### index — after a read

Emits `hookSpecificOutput.additionalContext` carrying the file's index table and
a reminder of how to address it:

```
/src/app.go carries anno annotations. Its structure is:

|----------:-------------|------|------------------|
[app.go]                  [      ] 210 lines < 1:210> |
|  section    handlers    [      ]  90 lines <12:104> |
|  |  symbol  ServeHTTP   [      ]  40 lines <14:54>  |
…

You can read or replace any one of these regions by name, instead of re-reading
or rewriting the whole file:
  anno read  /src/app.go@section       (also :symbol and ^part)
  anno write /src/app.go^part -        (content on stdin)
Chains may be partial or fully qualified; an ambiguous one fails and lists every
candidate.
```

## Design rules

A hook runs on every matching tool call, so the governing constraint is that it
must never break a session. Three rules follow, and each is a test:

1. **Only a genuinely broken annotated file may block.** Unparseable JSON on
   stdin, an event Anno does not handle, a tool it does not care about, a
   missing path, a deleted file, a binary, a wrong field type — every one of
   these exits 0 silently. `TestNothingUnexpectedEverBlocks` enumerates them,
   and `FuzzRun` asserts no input whatsoever can produce another exit code.
2. **Files with no annotations are never anyone's business.** The guard checks
   for annotation markers before blocking, so a project only partly annotated
   never sees a complaint about the rest of it.
3. **Silence is the default output.** The index hook says nothing for a file
   without annotations. A hook that fires on every read and spends tokens on
   nothing would defeat the point of the tool.

The index context is never coloured: it is read by a model, not a terminal.

## Why a binary rather than a shell script

The index hook must emit JSON containing an arbitrary file path and arbitrary
annotation names, which can carry quotes and backslashes. Escaping that reliably
in shell is harder to get right than it looks, and impossible to test properly.
The binary uses `encoding/json`, reuses the packages the rest of Anno is built
from, and is covered by unit tests, a subprocess test that runs the real
executable, and a fuzz target.

## Colour

`anno` colours its own terminal output — kinds carry a colour each, structure is
dimmed, ranges are blue — and detects per stream, so a piped index stays plain.
`NO_COLOR` disables it, `CLICOLOR_FORCE=1` forces it on through a pipe, and
`TERM=dumb` is honoured. Escape sequences are added only when writing, never
while measuring, so a coloured table is aligned identically to a plain one; that
is asserted by stripping the sequences and comparing byte for byte.
