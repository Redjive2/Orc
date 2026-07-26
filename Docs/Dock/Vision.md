# §1 Dock

Dock (`dock`) is a minimal documentation reader. It exists to answer a question
about a document without reading the document.

It is Anno for prose, with one difference that shapes everything: **Dock has no
syntax of its own.** A section is a markdown heading and a link is a markdown
link, so a document Dock understands renders normally, reads normally, and costs
nothing extra to a human who has never heard of Dock.

## §1.1 Sections

A section is a heading carrying a `§` number:

```markdown
# §1 Guide
## §1.1 Install
### §1.1.1 From source
## §1.2 Sections
```

Three rules make a document self-checking, and Dock refuses to guess when any of
them is broken:

| Rule     | Says                                                       |
|----------|------------------------------------------------------------|
| depth    | the number of `#`s equals the number of dotted components  |
| parent   | `§1.2.1` appears under an open `§1.2`                      |
| sequence | siblings run `1, 2, 3 …` in order, with no gaps or repeats |

The structure is stated twice — in the heading level and in the number — so a
document either is well formed or says precisely how it is not.

**A heading without a `§` is not a section.** It is ordinary prose inside
whatever section encloses it, so marking up a document is incremental, one
heading at a time, and a document with no `§` at all is invisible to Dock.

## §1.2 Links

A link is an ordinary markdown link whose destination is a target:

```markdown
See [the grammar](./grammar.md§2.1) and [Install](§1.1).
Anno does the same for code: [Operate](../code/example.go@code:Operate).
```

A destination Dock does not recognise — a URL, an anchor, a plain path — is
ordinary markdown and is ignored. Dock's graph is about sections.

`§` is not a markdown anchor, so these links do not navigate in a rendered
viewer. That is the price of addressing a section by a stable number instead of
by a slug that changes whenever someone edits a heading.

## §1.3 Targets

| Form                      | Names                                |
|---------------------------|--------------------------------------|
| `guide.md§1.2`            | section 1.2 of `guide.md`            |
| `guide.md§'Install'`      | the section named *Install*          |
| `§1.2`                    | section 1.2 of *this* file           |
| `example.go@code:Operate` | an Anno annotation, resolved by Anno |

Numbers are unique per file and so are names, so both forms name exactly one
section. Anno's chains pass through untouched: `@`, `:`, and `^` are Anno's
resolvers, and Dock hands those targets to `anno` rather than resolving them
itself.

## §1.4 Minimizing collateral information

Everything above serves this. Reading a document to answer one question spends
the whole document; Dock spends the part that answers it.

- `read` returns one section's own prose — not the file, not the neighbours, and
  not the heading you just named.
- Links are followed only when asked, to a depth you give, and a section is
  emitted at most once however many paths reach it.
- `--budget` stops before a line count and says exactly what it left out.
- `index` returns structure, never content: it is what you read to decide what
  to read.

Claude Code hooks carry most of this: after an agent reads a marked document,
the hook hands back its index, so the next thing the agent does can address a
section by name instead of re-reading the whole thing.
