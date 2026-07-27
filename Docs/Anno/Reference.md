# §1 Anno — CLI

The CLI follows this structure:

```
anno <command> <args...>
```

| Command                                   | Does                                                      |
|-------------------------------------------|-----------------------------------------------------------|
| `anno index <file path>`                  | Returns a tree of annotations at the given file           |
| `anno overview <folder path>`             | Returns a tree of annotations in the given package        |
| `anno read <file path><chain>`            | Returns the content of the specified annotation           |
| `anno find <folder path><chain>`          | Returns the content and index of the specified annotation |
| `anno write <file path><chain> <content>` | Writes `<content>` to the specified annotation            |

`overview` reads one directory, not a tree, and ends with the folders directly
inside it and what each holds — a count of files and folders, or **empty**, or
**cannot be read**. Without this a folder you have just made appears nowhere,
since an overview is a tree per annotated file and a new folder has none, and
nothing distinguishes it from a folder that was never created. A directory of
folders and nothing annotated therefore exits 0 with them listed rather than
"not found"; one with neither is still not found. `--json` is unchanged: it is
an array of trees, and the folders are for the person who asked.

## §1.1 Chains

Annotations are addressed by a chain of resolvers:

| Resolver | Selects |
|----------|---------|
| `@`      | section |
| `:`      | symbol  |
| `^`      | part    |

A chain may be fully qualified, naming every ancestor:

```
$ anno read example.go@code:Operate^declarations
<part content>
```

Or partial, naming only as much as is needed:

```
$ anno read example.go:Operate^declarations
$ anno read example.go^declarations
```

A partial chain that matches more than one annotation fails as ambiguous. Anno
lists every candidate, fully qualified, so the chain can be narrowed.

Claude is able to hook into all of these.

The project is stored at `Orc/Anno/go.mod`.

## Mentions

A sigil inside `"double quotes"` or `` `backticks` `` is a mention and is
ignored, so a file may discuss the syntax without being unreadable. A bare sigil
in prose is indistinguishable from a marker and is treated as one.

## Colour

Catppuccin, Macchiato by default, shared with every Orc tool — the index, the
help, and diagnostics are painted from the same roles `muff`, `cq`, and `orc`
use, so no two tools disagree about what a command name looks like.

`ORC_THEME=macchiato|mocha|frappe|latte|none`; `NO_COLOR` disables it, and
`ORC_AGENT` forces plain output for agents. `--no-color` and `--color` are the
same controls for a caller assembling one command — which is what Orc does when
it runs Anno inside a session. They are global: either may appear before or
after the command word, and no command sees them as arguments. `ORC_AGENT` wins
over `--color`: turning colour off for every tool at once must not be
defeatable per command.

The two streams are decided separately, so a piped index stays clean while the
errors beside it on a terminal stay legible.

Colour is a layer and never information — every colour is redundant with a glyph
or a word, and a test asserts that every screen, stripped of its escape
sequences, is byte-for-byte the plain rendering.

**`read` is the exception, deliberately.** It emits the span verbatim — no
dedent, no trimming, original line endings — which is what makes `read` and
`write` inverses of each other. Painting somebody's file content would break
that, so `read` is plain whatever the setting says.

## JSON

`index --json` and `overview --json` print the annotation tree as JSON, for
another program to read. It is the contract Communiqué mirrors through, so it is
a stable shape, not a rendering. Nesting is kept as nesting: a section holds its
symbols, a symbol holds its parts.
