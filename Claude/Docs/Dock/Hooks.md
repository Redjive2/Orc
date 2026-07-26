# Dock — Claude Code hook integration

One hook, on `PostToolUse` over `Read`: when an agent reads a document carrying
`§` headings, `dock-hook` hands back that document's index, so the next thing the
agent does can be `dock read guide.md§1.2` instead of re-reading the whole
document.

This is where most of Dock's saving lands, because it applies without the agent
knowing Dock exists.

## Installing

Build both binaries and put them on `PATH`:

```bash
cd Dock && go build -o ~/.local/bin/dock ./cmd/dock && go build -o ~/.local/bin/dock-hook ./cmd/dock-hook
```

Then add this to `.claude/settings.json` (project) or `~/.claude/settings.json`
(user):

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Read",
        "hooks": [
          {
            "type": "command",
            "command": "dock-hook",
            "timeout": 10,
            "statusMessage": "indexing sections"
          }
        ]
      }
    ]
  }
}
```

Dock's hook and [Anno's](../Anno/Hooks.md) can share the `Read` matcher or sit in
separate entries. They do not interact: Anno reports on annotated *code* and Dock
on `§`-marked *documentation*, and each is silent about files that are not its
business.

## What it emits

For a document with sections, `hookSpecificOutput.additionalContext`:

```
guide.md carries dock sections. Its structure is:

  §1     20 lines   Guide
  §1.1   1 line     Install
  §1.2   6 lines    Sections
  §1.2.1 1 line     Numbering
  §1.3   2 lines    Troubleshooting

Read one instead of the whole document:
  dock read guide.md§1.2            its own prose
  dock read guide.md§1.2 --tree     and everything under it
  dock links guide.md§1.2           what it cites, and what cites it
```

Structure, never content — that is the whole saving. The sizes are what an agent
needs to decide what is worth reading.

## Design rules

A hook runs on every matching tool call, so the governing constraint is that it
must never break a session. Four rules follow, and each is a test.

1. **It never blocks.** The hook is `PostToolUse` on `Read`, and there is no such
   thing as a read that should have been refused. Unlike Anno's guard and
   Macmuffin's scope hook, this one has no failure mode that stops work: `CodeOK`
   is the only status it can produce, and `FuzzRun` asserts that over millions of
   inputs.
2. **Silence is the default.** A document with no `§` headings produces nothing,
   and the cheap test — does the file contain a `§` at all — runs first, because
   most files in a project are not documents and that is the path taken on every
   read.
3. **Nothing unexpected ever misbehaves.** Unparseable JSON, an unhandled event,
   a tool Dock does not care about, a missing path, a directory, a binary, a
   non-UTF-8 file, a wrong field type, a NUL in a path: all silent success.
   `TestNothingUnexpectedEverMisbehaves` enumerates twenty of them.
4. **Output is never coloured.** It is read by a model, not a terminal.

Two smaller decisions worth naming:

**A broken document is not the hook's business.** A document whose numbering does
not parse produces nothing rather than a complaint. The agent has just read the
file and can see the headings; a hook is not the place to report a fault nobody
asked about. `dock check` is.

**A large index is bounded.** Past `MaxSections` the hook lists the first forty
and says how many more there are and how to see them. The context is spent on
every read, so a hundred-section reference would cost more than it saves — and
the bound is announced rather than silent.

## Why a binary rather than a shell script

The emitted JSON carries an arbitrary file path and arbitrary section names,
which can hold quotes and backslashes. Escaping that reliably in shell is harder
than it looks and impossible to test properly. The binary uses `encoding/json`,
reuses the packages the rest of Dock is built from, and is covered by unit tests,
a fuzz target, and a subprocess test that builds and runs the real executable.
