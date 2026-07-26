# Dock — Implementation Plan (Go)

This plan was written before `Docs/Dock/Vision.md` and `Docs/Dock/Reference.md`
existed, from the spoken brief and a round of decisions with the author, restated
in §1 so the inference stays visible and correctable. It is written to the
conventions [Anno](../Anno/Plan.md), [Mailman](../Mailman/Plan.md), and
[Macmuffin](../Macmuffin/Plan.md) establish for this tree.

Those two documents now exist, written from §2–4 and §5 once the decisions in §15
had settled. Where this plan and they disagree, they are the specification and
this is the record of how it was arrived at.

Guiding constraints, in priority order:

1. **Robust** — every error is handled and carries position; no panics, no silent
   truncation, no partial writes. Dock writes to documentation the same way Anno
   writes to code, and with the same care.
2. **Immutable** — parsed data is never mutated after construction. Mutation is
   confined to short builder scopes that yield frozen values.
3. **Simple** — small packages, one job each, no frameworks, stdlib only.
4. **Readable** — the spec's vocabulary (section, number, link, target, dangling)
   is the code's vocabulary.
5. **Frugal** — every command returns the least text that answers the question.
   Dock is measured in tokens an agent did *not* have to read. §6 is where this
   stops being a slogan and becomes specific.

The fifth is Dock's reason to exist, and one decision below makes it much easier
to honour than the first draft of this plan did: **Dock has no syntax of its
own.** A section is a markdown heading, and a link is a markdown link. There are
no new markers to add to a document, and therefore no tokens spent on them, no
marker grammar to learn, and nothing that looks like debris to a human reader.

---

## 1. The brief, restated

- **"Like Anno for documentation."** Same address discipline, same commands, same
  write safety. An agent that knows Anno knows Dock.
- **"Bases on the section symbol — the two ss stack thing."** The sigil is `§`,
  and it lives in the heading: `## §1.2 Section Name`.
- **"Mirror Anno, but flat: only one kind, the section."** One kind. See the note
  below on what "flat" survives as.
- **"Link individual sections to sections in other files or the same file."**
  Links are ordinary markdown links whose destination is a Dock target:
  `[the grammar](./grammar.md§2.1)`, `[Install](§1.1)`.
- **"Minimize effort and tokens by minimizing collateral information."** The
  frugality constraint; links are **not** followed unless asked.
- **"Linking to sections/symbols/parts from Anno should be possible."** An Anno
  chain is a legal link destination: `[how anno parses one](example.go@a:b^c)`.
  Code targets resolve by calling `anno` (§8).

**What "flat" survives as.** Headings nest, and the numbering encodes that
nesting, so a document is a tree. But there is exactly one **kind** of node, and
— more importantly — **addressing is flat**: `§1.2.1` and `§'Numbering'` each
name one section outright. Anno needs a chain like `@code:Operate^declarations`
because a bare name is ambiguous under nesting; Dock needs no chain, because the
number *is* the chain and a name is unique per file (§4). Everything Anno spends
on subsequence matching and ambiguity reporting, Dock does not spend at all.

---

## 2. Sections

A section is a markdown heading that carries a `§` number:

```markdown
# §1 Guide
## §1.1 Install
### §1.1.1 From source
## §1.2 Sections
```

**The depth rule.** The number of `#`s equals the number of dot-separated
components. `## §1.2` is well-formed; `## §1.2.3` and `### §1.2` are parse
errors naming the line and both counts. This single rule is what makes a document
self-checking: the structure is stated twice, in the heading level and in the
number, and Dock refuses to guess when they disagree.

**The parent rule.** `§1.2.1` must appear under an open `§1.2`. A number whose
parent has not been seen is a parse error naming the missing ancestor.

**The sequence rule.** Siblings run `1, 2, 3, …` in order, with no gaps and no
repeats. `§1.1` followed by `§1.3` is a parse error, and so is `§1.2` followed by
`§1.1`. The error names the number found and the number expected, so the fix is
mechanical.

Numbering is therefore exact: a document either is well-formed or says precisely
how it is not. The cost is real and worth naming — deleting or reordering a
section means renumbering the ones after it, and every link that named them by
number now points somewhere else. `check` (§5) is what finds those links, and
§10's refusal to rewrite them is what keeps the renumbering a decision rather
than a surprise.

**A heading without `§` is not a section.** It is ordinary prose inside whatever
section encloses it. Marking up a document is therefore incremental and
per-heading: a doc with three marked headings costs three `§`s and nothing else,
and a doc with none is invisible to Dock (which is what makes the hook in §9 safe
to run on every read).

**Two spans, and the narrow one is the default.** A section has:

- an **own span** — the lines after its heading, up to the first *section*
  heading of any depth, or to the end of the section. This is the section's own
  prose.
- a **tree span** — the lines after its heading, up to the next heading of depth
  ≤ its own, or end of file. This includes every subsection, exactly as Anno's
  `section` includes the symbols inside it.

Both spans end at a *section* heading, never at an unmarked one. An unmarked
heading is prose (above), so it stays inside the section that encloses it — and
it must, or the text beneath it would belong to no own span at all and be
unreachable by a default `read`. This was found while building `doc`: the
looser reading, "up to the first heading of any depth", puts a hole in any
document that marks only some of its headings.

`read` and `write` both operate on the **own span** by default and on the tree
span under `--tree`. Defaulting narrow is the frugality constraint applied to the
most common command: asking for `§1` in a chapter-sized document should not
return the chapter, and an agent should not be able to overrun its context by
forgetting a flag. The two commands always agree on which span they mean, so the
round trip is exact in both modes.

*(Anno's `read` has only the wide behaviour. The same narrowing belongs there —
`anno read @section` returning the section's own lines rather than every symbol
inside it, with `--tree` for the whole span. Noted in Anno's plan as a follow-up;
it is a behaviour change to a finished tool, so it is not folded in here.)*

**The heading is structure, not content, and Dock never writes it.** Excluding
the heading line from the span has three consequences worth stating: `read` does
not spend a line telling an agent the name of the thing it just asked for by
name; `write` cannot destroy a heading, renumber a document, or change its shape;
and the read/write round trip is exact without any special case.

**Content is emitted verbatim** — no dedent, ever — which is what makes `read`
and `write` inverses. Anno's `fixture.ExampleReadPart` documents the same rule
for the same reason.

**Names.** A section's name is the heading text after the number. Names are
matched case-insensitively with internal whitespace collapsed, and are **unique
per file**: a duplicate is a parse error at the second occurrence, naming the
first's line. Flatness leaves nothing to disambiguate with, and a document with
two sections called *Install* has a bug in it either way.

**Numbers are unique per file** by the same rule and the same error.

---

## 3. Links

A link is an ordinary markdown link whose destination is a Dock target. It may
appear anywhere a markdown link may appear — mid-sentence, in a list, in a table
cell — and it belongs to whichever section encloses it.

```markdown
## §1.2 Sections

A section is a heading carrying a `§` number. See [the grammar](./grammar.md§2.1)
and [how anno parses one](../../Anno/example.go:Operate).

## §1.3 Troubleshooting

Start with [Install](§1.1).
```

- **Paths are relative to the linking file**, as markdown links already are. A
  path escaping the doc root (§4) is an error.
- **A destination with no path is a link within the same file** — `(§1.1)`.
- **The link text is the label.** It is already there, it is already what a human
  reads, and Dock takes it rather than inventing a second labelling mechanism.
- **A destination Dock does not recognise is not a Dock link.** `(https://…)`,
  `(#anchor)`, `(./other.md)` with no `§` and no Anno resolver — all ordinary
  markdown, all ignored. Dock's graph is about sections, and a graph that fills
  with every URL in the corpus is a graph nobody reads.

**What this costs, stated plainly.** `§` is not a markdown anchor, so
`[x](./grammar.md§2.1)` does not navigate in a rendered markdown viewer the way
`#heading-anchor` would. That is the price of addressing a section by a stable
number instead of by a slug that changes whenever someone edits the heading text.
It is a deliberate trade and it is listed in §15.

**Extraction requires a little markdown awareness**, and only a little: Dock must
skip fenced code blocks (``` and `~~~`) and inline code spans, or every example
in this very document becomes an edge. It must also skip HTML comments. It parses
nothing else — not emphasis, not lists, not tables — which is why §14 can still
say Dock does not parse markdown in any meaningful sense.

Links are directed, are **not** followed by default (§6), and are never rewritten
by Dock (§10). A backlink is computed by scanning, never stored: a stored
backlink is a second source of truth that goes stale the moment someone edits a
file in an ordinary editor.

---

## 4. Targets

```
target  := [path] ("§" ref | anno-chain)
ref     := number | "'" name "'"
number  := digit { "." digit }
```

| Form | Names |
|---|---|
| `guide.md§1.2` | section `1.2` of `guide.md` |
| `guide.md§'Install'` | the section named *Install* in `guide.md` |
| `§1.2` | section `1.2` of *this* file |
| `§'Install'` | the section named *Install* in this file |
| `guide.md` | the whole file — not a Dock link (§3) |
| `example.go@code:Operate^declarations` | an Anno annotation, fully qualified |
| `example.go^declarations` | an Anno annotation, partially qualified |

Names are quoted because they contain spaces; numbers are not, because they
cannot. Both forms name exactly one section, since both are unique per file (§2).

The first resolver character decides who resolves it: `§` is Dock's, and `@`,
`:`, `^` are Anno's. An Anno chain passes through untouched, partial or whole, so
everything true of Anno chains — including that an ambiguous one fails and lists
its candidates — holds inside a Dock link with no restatement and no second
implementation.

**Splitting path from target** is Anno's problem solved Anno's way: the parser
returns every valid reading, most-path-first, and the command layer takes the
first whose path exists. When none does, the error quotes the most target-like
reading.

**The doc root** is the nearest ancestor directory containing a `.dock` file,
else the repository root, else the file's own directory. It bounds path escape
and gives `overview`, `links`, and `check` a default scope. It is not a store:
Dock keeps no state anywhere, and `.dock` may be empty.

---

## 5. Commands

Proposed `Reference.md`. Dock exposes the same CLI structure as every other Orc
sub-app:

```
dock <command> <args...>
```

| Command | Does |
|---|---|
| `dock index <file>` | Returns the numbered table of sections in the given file, with link counts |
| `dock overview <dir>` | Returns the sections of every document under the given directory |
| `dock read <target>` | Returns the specified section's own prose |
| `dock find <dir>§<ref>` | Returns the content and index of the specified section, across a tree |
| `dock write <target> <content>` | Writes `<content>` to the specified section's own prose |
| `dock links <target>` | Returns what this section links to, and what links to it |
| `dock check [<dir>]` | Reports every link that does not resolve, and every numbering fault |

Flags, all of them about frugality (§6):

| Flag | Does |
|---|---|
| `--tree` | On `read` and `write`: the section *and every subsection under it*, rather than its own prose |
| `--follow[=<n>]` | On `read`: also emit linked sections, to depth `n` (default 1) |
| `--budget=<lines>` | On `read --follow`: stop before exceeding a line budget, and say what was omitted |

`read` and `write` default to the own span and take `--tree` identically (§2), so
whatever `read` returned is what `write` replaces. A `read --tree` piped back
through a plain `write` is refused rather than silently truncating a subtree.

`write` takes `-` to read content from stdin, which is the practical path for
multi-line content and the one the Claude hooks use.

Exit codes come from the shared table in `common/fault` (Macmuffin's plan §5,
§10.1): `0` ok · `1` usage · `2` not found · `3` ambiguous · `4` parse ·
`5` i/o · `6` conflict · `70` internal. `check` exits `2` when anything dangles,
so a hook or a CI step can branch on it without parsing output.

---

## 6. Minimizing collateral information

The purpose, as mechanisms rather than as an aspiration. Each is testable, and
each is a place where the obvious implementation would have been wasteful.

1. **`read` returns one section** — not the file, not the neighbours, and not the
   heading the caller already named.
2. **`read` stops at the first subsection.** Reading `§1` in a chapter-sized
   document would otherwise mean reading the chapter. This is the difference
   between "what does this section say" and "what is in this part of the
   document", and an agent usually wants the first — so the first is the default
   and the second costs a `--tree`. The frugal path is the one taken by
   forgetting a flag, not the one taken by remembering one.
3. **Links are not followed by default.** Following is `--follow`, bounded and
   explicit, because a doc set is a graph and the transitive closure of a graph
   is the whole doc set.
4. **`--follow` deduplicates by section identity.** A diamond emits the shared
   section once, with a one-line note where the second copy would have gone.
5. **`--follow` respects `--budget`**, stopping before the line count is exceeded
   and naming exactly what it omitted and how to read it. An agent that overruns
   its context because a document was bigger than expected has been failed by the
   tool, not by the document.
6. **`index` returns structure, never content.** It is what an agent reads to
   decide what to read.

   Measured, and not what this plan assumed: over the fixture the index costs
   **671 bytes to describe a 557-byte document**. Its table is about a line per
   section plus a frame, so it is a saving only once a document is bigger than
   its own table, and on a small one it costs more than reading the whole thing.
   The mechanism that always pays is `read` — a leaf section of that same
   document is 31 bytes, an eighteenfold saving. The budget suite states both,
   so neither claim can quietly stop being true.
7. **Nothing that carries no information is printed.** No kind column when there
   is one kind, no indent gutter when the number already encodes depth, no
   decoration when the output is not a terminal.
8. **The hook hands back an index, not a file** (§9). This is where most of the
   saving actually lands, because it applies without the agent knowing Dock
   exists.

The test for all of this is a corpus measurement, not a vibe: `fixture` holds a
small doc set, and a test pins the byte count of `read`, `read --tree`,
`read --follow=1`, and `index` over it. A change that makes Dock chattier fails a
test that says so in bytes.

---

## 7. Presentation

`AGENTS.md` asks every Orc tool for colour, vertical alignment, tables, box
drawing, and diagrams; §6 asks for frugality. The resolution is the one Anno
already found: **the pretty layer is for terminals and absent everywhere else.**
Colour is on only when the stream is a terminal, and `NO_COLOR`, `TERM=dumb`, and
`--no-color` each turn it off. Hook output is never coloured, because it is read
by a model.

**`index` is a box table**, two passes as in Anno's `render` — measure every
cell, then draw fixed-width rows. The number carries the depth, so there is no
indent gutter; the name is indented instead, which keeps the numbers in one
scannable column:

```
$ dock index guide.md
|---------:-------------------|--------|-------------------|
[guide.md]                    [→3 ←1 ]  26 lines < 1:26> |
| §1       Guide              [→0 ←0 ]  24 lines < 3:26> |
| §1.1     Install            [→0 ←1 ]   3 lines < 7: 9> |
| §1.2     Sections           [→2 ←0 ]  10 lines <13:22> |
| §1.2.1     Numbering        [→0 ←0 ]   3 lines <18:20> |
| §1.3     Troubleshooting    [→1 ←0 ]   3 lines <24:26> |
|---------:-------------------|--------|-------------------|
```

**`links` is a diagram**, because a link list is a graph, and a graph drawn as a
list is harder to read than one drawn as a graph:

```
$ dock links guide.md§1.2

  guide.md §1.2 Sections
    │
    ├─→ grammar.md§2.1                   the grammar
    ├─→ ../Anno/example.go:Operate        how anno parses one      (anno)
    │
    └─← guide.md§1.3                      Install
```

**Colour is a layer, never information.** A dangling link is `✗` *and* red; an
Anno target is tagged `(anno)` *and* dimmed. Piping through `grep` loses nothing.
Golden tests run with colour off, and `TestColourStripsToPlain` runs every
rendering command twice and asserts the coloured output strips byte-for-byte to
the plain one — on both streams.

The scheme is `orc/theme`'s, resolved **per stream**, so a piped index stays
plain while the diagnostics beside it on a terminal stay legible. Colour is on by
default wherever the stream is a terminal, and the shared resolution order
applies unchanged: `ORC_AGENT` first and absolute, then `NO_COLOR`,
`ORC_THEME=none`, `TERM=dumb`, `CLICOLOR_FORCE`, and finally the terminal test. A
misspelled `ORC_THEME` is a usage error rather than a silent fallback.

`--no-color` and `--color` do the same for one command. They are taken off the
line before dispatch, so they work in any position and no command knows they
exist — muff's flags, with muff's names and muff's reasoning: a caller assembling
one command, which is what Orc will be doing, should not have to set an
environment variable. `--no-color` always wins, and `--color` re-resolves through
the scheme rather than forcing a palette, so `ORC_THEME` still chooses the
flavour and `ORC_AGENT` still overrides both.

**Two things are never coloured, structurally rather than by rule.** The hook
takes no palette at all, because its output is read by a model. And `read` emits
a section's bytes verbatim whatever the palette says: `read` and `write` are
inverses, so painting the content would mean `write` could never put it back.
The headers `--follow` draws *around* other sections are dock's own words and are
painted; the content between them is not.

**The help is data, not a wall of text**, as cq's and muff's are — a table of
commands, targets, and settings, each painted for what it *is* rather than
matched by a regular expression afterwards. Every pad measures the plain twin of
a line rather than the painted one, which is the bug that makes half the world's
CLIs wobble under `NO_COLOR`. It ends with `theme.Help()` — the shared package's
own words, so every Orc tool documents the settings identically — the colour
flags, and the exit codes dock can actually return.

---

## 8. Anno interop

Dock resolves code targets by executing `anno`, not by importing it: Anno's
packages are `internal/`, the two tools version independently, and Macmuffin's
plan already sets the precedent of one Orc tool driving another by exec. The
coupling is one package, `internal/anno`, with the exec behind an interface so
every test runs against a recorder rather than a real binary.

| Dock needs | Runs | Reads |
|---|---|---|
| content of a code target | `anno read <target>` | stdout |
| existence, for `check` | `anno read <target>` | exit code only |
| candidates, on ambiguity | `anno read <target>` | stderr, which lists them fully qualified |

**When `anno` is not on `PATH`**, code links are reported as *unchecked* rather
than broken, in their own row of `check`'s summary. Reporting a link as dangling
because the tool that resolves it is missing would send someone to fix a document
that is correct.

Anno's exit codes are the shared ones, so `2` and `3` from a subprocess map onto
Dock's own without translation — the payoff for Macmuffin's decision 13.

---

## 9. The Claude hook

One hook, `dock-hook`, on `PostToolUse` matching `Read`, mirroring `anno-hook`'s
index job. When an agent reads a document containing `§` headings, the hook
returns `hookSpecificOutput.additionalContext` with the file's index and one line
on how to address it:

```
guide.md carries dock sections. Its structure is:

  §1      Guide            24 lines < 3:26>   →0 ←0
  §1.1    Install           3 lines < 7: 9>   →0 ←1
  §1.2    Sections         10 lines <13:22>   →2 ←0
  §1.2.1    Numbering       3 lines <18:20>   →0 ←0
  §1.3    Troubleshooting   3 lines <24:26>   →1 ←0

Read one with `dock read guide.md§1.2`, or add `--tree` for its subsections too.
```

Anno's three hook rules hold verbatim, and each is a test:

1. **Silence is the default.** A document with no `§` headings produces nothing.
   A hook that spends tokens on every read of every file would invert the tool's
   purpose.
2. **Nothing unexpected ever misbehaves.** Unparseable JSON, an unhandled event,
   a missing path, a deleted file, a binary, a wrong field type — all exit 0
   silently. `TestNothingUnexpectedEverBlocks` enumerates them and `FuzzRun`
   asserts no input produces another exit code.
3. **Hook output is never coloured.** It is read by a model, not a terminal.

Dock's hook never blocks: it is `PostToolUse` on `Read`, and there is no such
thing as a read that should have been refused. That is a real difference from
Anno's guard hook and Macmuffin's scope hook, and it makes this the safest of the
three.

---

## 10. Write safety

`write` is the only mutating path, and it is Anno's §6 sequence unchanged,
because documentation deserves what code gets:

1. Read the file, record its SHA-256 and the byte offsets of the target span —
   own or tree, matching the flag (§5).
2. Reject content that would corrupt structure. Because the span excludes the
   heading (§2), this is one rule per mode:
   - **own span** — content may contain **no section heading**. Own prose is by
     definition the lines before the first subsection, so a `§` heading there is
     either a subsection the caller did not ask to create or a break in the
     section itself. An unmarked heading is prose and is permitted, which is
     what keeps `write(read(x))` exact for a section that contains one.
   - **`--tree`** — content may contain no heading of depth ≤ the target's own,
     which would end the section being written into. Deeper headings are
     permitted and must satisfy §2's three rules under the target: right depth,
     right parent, right sequence.
3. Splice into the byte slice, preserving the file's trailing-newline state. Line
   endings adopt the file's style only if the file is uniform.
4. Re-parse the result in memory. If the section tree is not identical — same
   numbers, same names, same nesting — abort; nothing is written.
5. Re-hash the on-disk file and compare to step 1; abort on mismatch.
6. Temp file in the same directory, `fsync`, copy mode, `rename`, `fsync` the
   directory. Temp removed on every failure path.

Step 4 also re-extracts the **links** in the replaced span. Writing content with
a malformed target would otherwise produce a file that indexes fine and whose
links have silently vanished.

Dock does **not** rewrite links when a section is renumbered or renamed, and does
not offer a rename at all. A tool that edits other people's files to keep its own
graph tidy is a tool nobody trusts with `write`. `check` reports what broke, and
a person or an agent fixes it deliberately.

---

## 11. Package layout

Module at `Orc/Dock/go.mod`, module path `orc/dock`.

Dock is the fourth tool, which Macmuffin's plan (§10.1) named as the moment to
look again at what is shared. Looking: Dock needs `source` (load and validate a
file — UTF-8, NUL, size cap, line-ending style, trailing-newline flag, hash) and
the atomic commit sequence byte-for-byte identically to Anno, and Macmuffin's
store needs the commit sequence too. So `Orc/Common` gains two packages, **and
they are extracted in Macmuffin's milestone 0 rather than here** — one retrofit
of Anno instead of two, done while that extraction is already open and its
"nothing changed" criterion is already being verified:

```
Common/
  fault/ clock/ user/ identity/ style/     (from Macmuffin's plan §10.1)
  source/                                  file load, validation, hashing
  commit/                                  the atomic write sequence, alone
```

`commit` is only the six-step file replacement — not Anno's splice planning,
which stays in Anno because it is about annotation structure. That split is what
keeps the extraction honest: three tools genuinely do the identical thing, and
the thing they do identically is small and finished.

```
Dock/
  go.mod
  cmd/dock/main.go        thin: parse argv, dispatch, map error → exit code
  cmd/dock-hook/main.go   the PostToolUse index hook (§9)
  internal/scan/          the markdown-lite line scanner: headings, fences,
                          code spans, comments, links. Pure, no I/O.
  internal/doc/           scanned lines → immutable Doc: the numbered section
                          tree, spans, content ranges, the depth and parent rules
  internal/link/          Link values, and the graph over a doc set
  internal/target/        target parsing: path + Dock ref or Anno chain (§4)
  internal/anno/          the anno subprocess boundary (§8)
  internal/root/          doc root discovery, path containment
  internal/render/        the table and the link diagram; pure, two-pass
  internal/edit/          splice planning; commit comes from common (§10)
  internal/cli/           one file per command, each (args, fs) → (stdout, error)
  internal/fixture/       the golden doc set (§12)
```

Nothing outside `cli`, `edit`, `root`, and `common/source` touches the
filesystem; only `internal/anno` starts a process. Everything in `scan`, `doc`,
`link`, `target`, and `render` is a pure function of its input.

---

## 12. Validation and testing

Validation discipline is Anno's and Macmuffin's, unchanged and not restated at
length: no meaningful zero values, a private `validate()` on every type with
invariants, bounds on everything from outside, `fault.Check` as the assertion
primitive returning rather than panicking, every `os`/`io`/`exec` error wrapped
with `%w` and the path, no bare `_ = err`, and multi-problem inputs reported
together via `errors.Join` — so a document with four numbering faults is fixed in
one round trip.

**Table tests** — the depth, parent, and sequence rules in every violated form
(wrong component count, missing ancestor, gap, repeat, out-of-order), duplicate
names, `§` in a heading that is not a heading, headings inside fences, links
inside fences and code spans and comments, every target form in §4, the
path/target split, and the exit-code mapping.

**Golden tests** — `fixture` holds a small doc set: three documents, a same-file
link, a cross-file link, a link into Anno's own `example.go`, a dangling link,
and a diamond. The rendered output of `index`, `overview`, `links`, `check`,
`read`, `read --tree`, and `read --follow=1` is pinned. A layout change breaks
exactly one constant, as `fixture.ExampleGo` does for Anno.

**Byte-budget tests** (§6) — the sizes of `read`, `read --tree`,
`read --follow=1`, and `index` over the fixture are pinned as numbers. This is
the only suite in the tree asserting an *upper bound on output*, and it is what
keeps the tool honest about its own purpose.

**Fuzz targets** — `scan.Line` (no panic; a fence never leaks), `doc.Build` (no
panic; rendering stability, the property that caught Anno's non-idempotent closer
strip), `target.Parse` (every returned reading re-parses to itself), and
`dock-hook`'s `Run` (no input produces an exit code other than 0).

**Property tests**
- `write(target, read(target))` leaves the file byte-identical, over the whole
  fixture corpus, **in both modes** — Anno's `FuzzPipeline` property, which found
  four real defects there and will find them here.
- The section tree is invariant under `write`, for any content that is accepted.
- A `read --tree` result written back through a plain `write` is always refused,
  never truncated: the mode mismatch is caught by step 2, not discovered later.
- Every line Dock prints that looks like a target *is* a target: rendered by
  `links` or `index`, pasted into `read`, it resolves.
- `--follow` never emits a section twice, at any depth, over generated link
  graphs including cycles.
- `--budget` output never exceeds the budget.
- A document with no `§` produces an empty index and no hook output, for any
  input — the frugality guarantee stated as a property.

**Cycle handling** — a link cycle between documents is legitimate and common. It
is tested explicitly: `--follow` terminates, `check` terminates, and neither
reports the cycle as a problem.

**Subprocess tests** — `internal/anno` against a recorder for unit tests, plus
one test that builds and runs the real `anno` binary against the real fixture, so
the interop contract is checked against the actual tool rather than against my
assumptions about it.

**Hygiene** — `go test -race ./...`, `-count=2`, fuzz corpora committed, no test
reading the real `$HOME`.

---

## 13. Milestones

| # | Deliverable | Done when |
|---|---|---|
| — | *Prerequisite:* `common/source` and `common/commit` exist | Delivered by [Macmuffin's milestone 0](../Macmuffin/Plan.md), not by Dock. If Dock starts first, that milestone comes with it. |
| 1 | `scan` + `doc` | Fuzz clean; depth, parent, and sequence rules table-tested in every violated form; duplicates rejected with both lines named; fences never leak. |
| 2 | `target` + `link` | Every §4 form parses; path/target split tested against a file literally named like a target; same-file refs resolve; links in fences and code spans ignored. |
| 3 | `render` + `index`, `read`, `--tree` | Table golden byte-exact; content emitted verbatim; heading never included; own and tree spans each pinned; byte budgets pinned. |
| 4 | `root` + `overview`, `find` | Recursive walk deterministic; unreadable, binary, and non-UTF-8 files skipped with a note, never a hard failure. |
| 5 | `link` graph + `links`, `check` | Diagram golden; backlinks correct; cycles terminate; dangling links and numbering faults reported with position. |
| 6 | `internal/anno` + code targets | Recorder tests plus one against the real binary; missing `anno` reported as unchecked, not broken. |
| 7 | `edit` + `write` | Round-trip property holds over the corpus; tree invariance holds; every abort path tested; malformed links in written content rejected. |
| 8 | `--follow`, `--budget` | Dedup and budget properties hold over generated graphs. |
| 9 | `dock-hook` | `FuzzRun` clean; `TestNothingUnexpectedEverBlocks` complete; silent on unmarked documents. |

Milestones 1–3 are one coherent sitting and produce something immediately
useful; 5 is where Dock stops being Anno-for-prose and becomes a doc graph.

**Status: milestones 1, 2, and 3 are complete.** `scan`, `doc`, `target`,
`link`, `style`, `render`, `fixture`, and `cli` are green under `-race -count=2`,
fuzz-clean, and build with `GOWORK=off`. `dock index` and `dock read` work
against real files. The index table is pinned as `fixture.GuideIndex`; a second
test strips the coloured rendering and compares it byte-for-byte against the
plain one; and the byte budgets of §6 are pinned.

The prerequisite arrived as part of this: `common/source` and `common/commit`
were extracted from Anno (§11), which is what unblocked `read`. Anno's suite,
its goldens, and its nine commit failure-injection tests are unchanged — the
seam in its `export_test.go` was reshaped to keep `commit_test.go` untouched, so
the tests that prove "a failed write leaves the original intact" still prove it.

**Milestone 4 is complete too.** `root` finds a doc root (`.dock`, else a
repository, else the document's own directory), bars a path that resolves
outside one — including through a symlink, which is why `fault.Escape` has its
own exit code — and walks a tree deterministically. `overview` and `find` are
built on it, and a file that will not load is skipped with a note on stderr
rather than failing the command.

**Milestone 5 is complete.** `link.Graph` resolves a whole corpus — paths
normalised so `./a.md` and `a.md` are one node, backlinks computed by scanning
and never stored, cycles terminating, and Anno targets held as *unchecked*
rather than reported broken. `dock links` draws the diagram and `dock check`
reports every dangling link with its position, exiting `2` so CI can branch on
it without parsing the report.

Two consequences worth recording:

- **`index` now counts backlinks** when the tree is small enough to read
  (`MaxBacklinkScan`, 250 documents), and still prints `←?` past that. The
  counts it produces by walking a corpus are byte-identical to the ones
  `fixture.GuideIndex` was pinned with by hand — which is what turns that golden
  from a statement about the test into a statement about Dock.
- **A backlink names its source, not its target.** The first draft rendered an
  arrow's target in both directions, so asking a section for its backlinks
  printed the section's own name back at it once per citation. The question a
  backlink answers is "who cites this".

**Milestone 6 is complete.** `internal/anno` runs the binary behind a `Runner`
interface, maps its shared exit codes onto verdicts, bounds every call with a
deadline, and answers *Unknown* — never *Missing* — when anno is absent, fails
for its own reasons, or times out. `Graph.Recheck` folds those answers back in
while leaving the graph a pure function of its input.

An ambiguous chain is **dangling, not unchecked**: a link naming more than one
annotation does not address one thing, which is as broken for a reader following
it as naming none. The candidates anno lists are carried into the report, and
each is a valid target.

The interop is checked against the real binary, not a stub — one test builds
`anno` from the sibling module and asks it about a target that exists, one that
does not, and an ambiguous one, feeding each candidate back to confirm it
resolves. That test is what a recorder cannot give: it pins the contract against
the actual tool rather than against assumptions about it.

**Milestone 7 is complete.** `dock write` replaces a section's content through
`common/commit`, with the two structural rules of §10 and the re-parse backstop
behind them. The round-trip property holds over the corpus in both spans, and
every abort path — stat, re-read, temp create, write, flush, chmod, close,
rename, and a file that changed underneath — is driven into failure and asserted
to leave the original untouched with no debris.

`FuzzWriteReadRoundTrip` earned its place immediately, finding two real defects
that the table tests had missed. Both corpus entries are checked in as permanent
regression seeds:

- **A no-op write invented a final newline.** Reading a section with no content
  gives `""`, and writing that back appended a terminator to a file that had
  none. Inserting nothing must change nothing.
- **A mixed-ending file had its endings rewritten.** `normalise` folded CRLF to
  LF and then only folded back for *uniform* files, so a file whose lines ended
  differently lost the caller's CRLFs — the worst of both rules. Content now
  passes through byte for byte when there is no dominant style, which is what
  §10 step 3 always meant.

**Milestone 8 is complete.** `read --follow[=n] --budget=<lines>` walks outward
breadth-first, so the nearest citations come first and a depth limit means what
it says. Both bounds are announced rather than silent: a section reached twice
prints `— … is shown above`, and one the budget stops prints what it was and how
to read it. A reader who cannot tell "there was nothing more" from "there was
more and you did not get it" has been misled by the tool.

Following also reaches into code: an Anno chain's content comes from `anno read`,
tagged `(anno)` so prose and code are tellable apart without reading them. That
is the last row of §8's table, and the last use of the boundary.

Two decisions the implementation forced:

- **The budget bounds content, not output.** Headers and notes are always
  printed, because suppressing the very lines that say what was omitted is what
  would make a budget silently lossy. The tests count marker lines rather than
  parsing the output, so the property is measured rather than inferred.
- **`--budget` without `--follow` is a usage error.** On its own it could only
  truncate the section that was asked for, which is not what a budget is for.

**Milestone 9 is complete, and with it the plan.** `dock-hook` fires on
`PostToolUse` over `Read` and hands back a document's index — structure, never
content. `CodeOK` is the only status it can produce, which `FuzzRun` asserts over
millions of inputs, and `TestNothingUnexpectedEverMisbehaves` enumerates twenty
things it might be handed and is silent about. Installation and the design rules
are in [Hooks.md](Hooks.md).

Two decisions the implementation added:

- **A broken document is not the hook's business.** One whose numbering does not
  parse produces nothing rather than a complaint: the agent just read the file
  and can see the headings, and a hook is not the place to report a fault nobody
  asked about. `dock check` is.
- **A large index is bounded** at `MaxSections`, announced rather than silent.
  The context is spent on every read, so a hundred-section reference would cost
  more than it saves.

---

## Status

All nine milestones are complete. `dock` and `dock-hook` build, and the suite is
green under `-race -count=2` with `GOWORK=off` as well as in the workspace.

| Package | Does |
|---|---|
| `scan` | markdown-lite: headings, fences, code spans, comments, links |
| `doc` | the numbered section tree; depth, parent, and sequence rules |
| `target` | the address grammar, Dock's and Anno's |
| `link` | edges, and the resolved graph over a corpus |
| `root` | doc-root discovery, containment, deterministic walking |
| `style` | Dock's vocabulary mapped to Theme roles; column measurement |
| `render` | the index table, the link diagram, the check report |
| `anno` | the subprocess boundary onto `anno` |
| `edit` | splice planning; `common/commit` applies it |
| `cli` | one method per command |
| `hook` | the `PostToolUse` index hook |

What is deliberately still open is in §14, and the two external contracts Dock
now rests on are `common/source` and `common/commit`, both extracted from Anno
during milestone 3 with its suite and goldens unchanged.

Two things the build found, both recorded above rather than left as differences
between the plan and the code: the own span ends at a *section* heading rather
than any heading (§2), and `target` needs a small table of non-hierarchical URI
schemes (§15.17).

---

## 14. What is deliberately not built

- **No syntax of Dock's own.** No markers, no sidecar files, no front matter. A
  document that Dock understands is a document that renders normally and reads
  normally to a human who has never heard of Dock.
- **No markdown parsing beyond what §3 needs** — headings, fences, code spans,
  comments, and inline links. Not emphasis, not lists, not tables. This is also
  what keeps Dock working on `.txt`, `.rst`, and `.adoc`, where `#` headings are
  the convention or nearby.
- **No renumbering, no rename, no link rewriting** (§10).
- **No stored index or cache.** Doc sets are small; a scan cannot disagree with
  the source of truth.
- **No transitive follow by default**, and no unbounded follow at all.
- **No link types or weights.** An edge is an edge, and the link text is its
  label.
- **No writing to Anno targets.** Dock reads code through `anno`; writing code is
  `anno write`'s job.

---

## 15. Decisions

Settled with the author:

1. **The sigil is `§`** (§1).
2. **Sections are headings**: `## §N.M Name`, with the number's depth matching
   the heading's depth (§2). No markers, no `§>`/`§<`/`§;`.
3. **Links are ordinary markdown links** whose destination is a Dock or Anno
   target (§3). No link marker.
4. **Duplicate names — and numbers — are a parse error** at the second
   occurrence, naming the first's line (§2).
5. **The heading is excluded from the span** (§2), so `read` returns content
   alone and `write` can never touch a heading, renumber a document, or change
   its shape.
6. **`read` and `write` default to the own span; `--tree` widens both** (§2, §5).
   The frugal path is the one taken by forgetting a flag. **Follow-up: the same
   narrowing belongs in Anno**, whose `read` has only the wide behaviour —
   recorded in [Anno's plan](../Anno/Plan.md) as a behaviour change to make
   deliberately, not folded in here.
7. **Numbering is exact** (§2): the sequence rule refuses gaps, repeats, and
   out-of-order siblings at parse time. A document either is well-formed or says
   precisely how it is not; the cost is that deleting a section means renumbering
   what follows.
8. **Only inline links are extracted** (§3). Reference-style
   (`[label][id]` … `[id]: guide.md§1.2`) is not.

9. **`§` destinations do not navigate in a rendered markdown viewer** (§3), and
   that is accepted. A number is stable under rewording; a heading anchor is not,
   and would leave `check` reporting breakage caused by ordinary editing.
10. **Heading metadata is dropped.** Anno's `[...]` has no use once the heading
    text is the name and the link text is the label.
11. **The doc root is `.dock`, else the repo root, else the file's directory**
    (§4). `.dock` may be empty — a marker, not a store.
12. **`overview` recurses**, where Anno's does not. Doc trees are the case where
    recursion is the point.
13. **Anno interop by subprocess** (§8), not a shared package.
14. **`common/source` and `common/commit` are extracted in Macmuffin's
    milestone 0** (§11), so Anno is retrofitted once rather than twice.
15. **`check` exits 2 on anything dangling**, so CI can branch on it.
16. **Module path** `orc/dock`.
17. **Containment is a reason, not an exit code.** `check` resolves every broken
    cross-file link against the doc root and says when one left the tree, which
    the graph cannot know — it has no filesystem and no notion of where a corpus
    begins. But the arrow stays *dangling* and `check` keeps exiting `2`: for a
    documentation tool a link out of the tree is a mistake in a document, not a
    containment breach, and leaving `11` to Orcprobe — where a path escaping the
    probe really is the thing to alarm on — is what keeps that code worth
    alarming on. So **dock never exits 11**, and §1.5 of its Reference does not
    list it.
18. **A walk reads documentation, not every file.** `overview`, `find`, and
    `check` consider only documentation extensions — `.md`, `.txt`, `.rst`,
    `.adoc` and their variants — while `index` and `read` still take any path a
    caller names. Naming a file is a decision; sweeping a tree is not.

    This was found by running `dock overview` on Dock's own repository. Dock's
    markers are ordinary markdown, so a source file holding documentation in a
    string literal — a fixture, a help text, a test — parses as a document and
    reports its examples as broken sections. The output was pages of numbering
    faults from Go files that are not documentation and were never meant to be
    read as it. §14's claim that Dock works on `.txt` and `.rst` is unaffected;
    what changed is that a sweep no longer reads `.go`.
19. **A table of non-hierarchical URI schemes** — `mailto:`, `tel:`, `data:`,
    `javascript:`, `sms:`, `urn:` — is excluded before a destination is read as
    an Anno chain. Found while building `target`: `mailto:someone@example.com`
    otherwise parses as a path and two steps, since `:` and `@` are Anno's
    resolvers and every character in it is a legal step name. `://` and a
    leading `#` cover the rest. Like Anno's comment-closer table this is a
    judgement call rather than a specification, and it is one line to extend.

Nothing is open, and nothing is outstanding. `common/source` and `common/commit`
were extracted during milestone 3; `Docs/Dock/Vision.md` and
`Docs/Dock/Reference.md` were written from §2–4 and §5 once the decisions above
had settled; and every heading under `Docs/` now carries a number, so Dock reads
the documentation it was built for.
