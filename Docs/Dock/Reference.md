# §1 Dock — CLI

Dock exposes the same CLI structure as every other Orc sub-app:

```
dock <command> <args...>
```

| Command                    | Does                                        |
|----------------------------|---------------------------------------------|
| `index <file>`             | The sections in a document, and their sizes |
| `overview <dir>`           | The same, for every document under a tree   |
| `read <target>`            | One section's own prose                     |
| `write <target> <content>` | Replace it; `<content>` of `-` reads stdin  |
| `find <dir>§<ref>`         | A section by number or name, across a tree  |
| `links <target>`           | What a section cites, and what cites it     |
| `check [<dir>]`            | Every link in a tree that does not resolve  |

A walk — `overview`, `find`, `check` — reads documentation files only. `index`
and `read` take any path you name: naming a file is a decision, sweeping a tree
is not.

## §1.1 Flags

| Flag               | Does                                                           |
|--------------------|----------------------------------------------------------------|
| `--tree`           | On `read` and `write`: the section *and* everything under it   |
| `--follow[=<n>]`   | On `read`: also the sections it links to, `n` deep (default 1) |
| `--budget=<lines>` | On `read --follow`: stop before that many content lines        |

`read` and `write` mean the same span by `--tree`, so whatever `read` returned is
what `write` replaces.

`--follow` emits a section at most once and says so where a repeat would have
gone; `--budget` names what it omitted and how to read it.

## §1.2 Targets

| Form                      | Names                                 |
|---------------------------|---------------------------------------|
| `guide.md§1.2`            | section 1.2 of `guide.md`             |
| `guide.md§'Install'`      | the section named *Install*           |
| `§1.2`                    | section 1.2 of *this* file, in a link |
| `example.go@code:Operate` | an Anno annotation, resolved by Anno  |

A path may itself contain a `§` or a resolver character, so a target has more
than one reading. Dock tries them longest-path-first and takes the first whose
path exists — a file genuinely named `guide§1.md` therefore wins over reading it
as an address.

## §1.3 Sections

A section is a markdown heading carrying a `§` number, and the number's depth
matches the heading's:

```markdown
## §1.2 Sections
```

`read` returns the lines after the heading, verbatim, up to the first subsection.
The heading itself is never part of a span, so `write` cannot renumber a document
or change its shape.

## §1.4 Anno

A target using `@`, `:`, or `^` is Anno's, and Dock runs `anno` to resolve it.

When `anno` is not on `PATH` those links are reported as **unchecked**, never as
broken: a link is not wrong because the tool that resolves it is missing.
`check`'s summary says how many it left to `anno`.

## §1.5 Exit codes

| Code | Means                                               |
|------|-----------------------------------------------------|
| `0`  | ok                                                  |
| `1`  | usage                                               |
| `2`  | not found — including a dangling link, from `check` |
| `4`  | parse — a malformed number or target                |
| `5`  | i/o                                                 |
| `6`  | conflict — a document changed under a `write`       |
| `70` | internal                                            |

The numbers are the shared ones every Orc tool uses, so a script can branch on a
status without knowing which binary it called.

## §1.6 Colour

Colour follows `orc/theme`, the scheme every Orc tool shares, and is decided per
stream: a piped index stays plain while the diagnostics beside it stay legible.

| Setting           | Effect                                                    |
|-------------------|-----------------------------------------------------------|
| `$ORC_THEME`      | `macchiato` (default), `mocha`, `frappe`, `latte`, `none` |
| `$ORC_AGENT`      | set to anything → output is always plain                  |
| `$NO_COLOR`       | set to anything → no colour                               |
| `$CLICOLOR_FORCE` | anything but `0` → colour even through a pipe             |
| `--no-color`      | the same, for one command                                 |
| `--color`         | force it on for one command                               |

The flags work before or after the command. `--no-color` always wins, and
`$ORC_AGENT` outranks everything: an agent's output is another program's input,
and escape sequences in it are corruption rather than decoration.

Two things are never coloured. `read` emits a document's bytes verbatim, because
`read` and `write` are inverses — painting the content would mean `write` could
never put it back. And the hook's output is read by a model, not a terminal.

## §1.7 The hook

`dock-hook` runs on `PostToolUse` over `Read`: when an agent reads a marked
document, it hands back that document's index. It never blocks, says nothing
about a file carrying no `§`, and exits `0` whatever it is given.

Installation is in `Claude/Docs/Dock/Hooks.md`.

## JSON

`index --json` and `overview --json` print a document's sections as JSON, for
another program to read. It is the contract Communiqué mirrors through, so it is
a stable shape, not a rendering. A section's inbound count is omitted rather than
zeroed when it was not computed — "none" and "not counted" are different answers.
