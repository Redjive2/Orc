# Anno — Implementation Plan (Go)

Derived from [Vision.md](../../../Docs/Anno/Vision.md) and
[Reference.md](../../../Docs/Anno/Reference.md).

Guiding constraints, in priority order:

1. **Robust** — every error is handled and carries position; no panics, no silent
   truncation, no partial writes.
2. **Immutable** — parsed data is never mutated after construction. Mutation is
   confined to short, local builder scopes that yield frozen values.
3. **Simple** — small packages, one job each, no frameworks, stdlib only.
4. **Readable** — the parser reads like the spec; the spec's vocabulary
   (marker, kind, span, resolver) is the code's vocabulary.

---

## 1. Semantics recovered from the example

`Vision.md`'s worked example pins down behaviour the prose leaves implicit.
Reconstructing `example.go` (the `// ./example.go` line is doc framing, not file
content) gives a 32-line file, which matches the reported `32 lines < 1:32>`.

| Annotation | Marker line | Reported range | Reported count |
|---|---|---|---|
| `section data` | 3 | 4:7 | 3 |
| `symbol SampleOperation` (`@:;`) | 5 | 6:6 | 1 |
| `section types` | 9 | 10:19 | 8 |
| `symbol Pair` | 11 | 12:15 | 4 |
| `symbol Operation` (`@:;`) | 17 | 18:18 | 1 |
| `section code` | 21 | 23:32 | 8 |
| `symbol Operate` | 22 | 23:32 | 8 |
| `part declarations` | 24 (open), 29 (close) | 25:28 | 4 |

Three rules fall out, and they are the ones to implement:

- **Span** = the lines strictly after the opening marker, up to and excluding the
  terminator (a `@:<` close, the next same-or-shallower-kind open, or EOF).
- **Content range** = the span with leading *and* trailing lines dropped while
  they are blank or are themselves marker lines. This explains `section code`
  starting at 23 (line 22 is the nested `symbol` marker) and `section types`
  ending at 19 (line 20 is blank).
- **Line count** = `end - start + 1` minus the marker lines *inside* the content
  range. Interior blank lines still count (`symbol Operate` = 10 lines minus the
  two markers at 24 and 29 = 8).

**Status: implemented.** All eight rows, both separator rules, and the file row
reproduce byte-for-byte; `internal/render` holds the table as a golden constant.

Two documented inconsistencies, resolved rather than reproduced blindly:

- The root row reports `32 lines <1:32>` — a raw count, not marker-excluded.
  **Resolution:** the file root is a raw span, never a content range. Implement
  as documented.
- `anno index` reports `section code` at `23:32` but `anno read …@code` emits
  from line 22 (it includes the nested `symbol Operate` marker). **Resolution:**
  these are two different projections of one node — `read` emits the **span**
  verbatim, `index` displays the **content range**. Both doc outputs are then
  correct and consistent.

A third surfaced during implementation: Vision.md showed `read example.go^declarations`
dedented to column zero, but showed the same lines indented one tab inside its own
`read example.go:Operate` output. **Resolution:** spans are emitted verbatim.
Anno never dedents — dedenting would break the read/write round trip, and `write`
would then have to guess an indent to restore. Vision.md has since been corrected
to the indented form, so the doc and the implementation now agree; the resolution
stands recorded here because it is the reason the doc was changed rather than the
code.

## 2. Marker grammar

```
open  := "@:>" WS kind WS name [ WS "[" meta { WS meta } "]" ]
next  := "@:;" WS kind WS name [ WS "[" meta { WS meta } "]" ]
close := "@:<" WS name
kind  := "section" | "symbol" | "part"
name  := one or more non-space, non-"[" runes
meta  := one or more non-space, non-"]" runes
```

- A marker occupies the **tail** of its line: everything from the sigil to end of
  line is the annotation. Text before the sigil (comment leader, code,
  indentation) is ignored.
- Runs of whitespace collapse to one separator, so the doc's aligned form
  (`@:> symbol  Pair`) parses identically.
- **Trailing comment closers.** Block-comment languages force a closer after the
  annotation. A known-closer table (`*/`, `-->`, `--}}`, `#}`, `}}`, `*)`, `--`)
  is stripped from the tail before parsing. Anything else left over is a parse
  error with line and column — never silently swallowed.
- `@:;` binds to exactly the next line: span and content range are both
  `[L+1, L+1]`. If `L` is the last line, that is a parse error.
- Kind rank: `section` = 0, `symbol` = 1, `part` = 2. Rank drives nesting.

## 3. Tree construction

Single pass over classified lines, over a stack of open nodes:

- **open(kind k, line L)** — close every open node of rank ≥ rank(k), each
  terminating at `L-1`. Attach the new node to the deepest still-open node of
  rank < rank(k), else to the file root. Push it.
- **next(kind k, line L)** — same closing behaviour, then attach a node with span
  `[L+1, L+1]` already closed. Never pushed.
- **close(name n, line L)** — find the nearest open node named `n`. Not found →
  error (`unbalanced close`), listing the currently open names. Found → close it
  and, implicitly, every node above it on the stack, all terminating at `L-1`.
- **EOF** — close all open nodes at the last line.

Duplicate names are *not* a parse error (the same `part declarations` may
legitimately appear in several functions). They become an error only at
resolution time, reported as an ambiguity listing every candidate with its line.

## 4. Target syntax and resolution

A target is a path followed by a **chain** of resolver-qualified steps:

```
target := path { step }
step   := resolver name
```

| Resolver | Kind |
|---|---|
| `@` | section |
| `:` | symbol |
| `^` | part |

The chain may be fully qualified — `example.go@code:Operate^declarations` — or
partial — `example.go^declarations`. An empty chain addresses the file root.
Unqualified steps were dropped: the syntax has no way to write one, so the
matching code for them would have been unreachable.

**Matching.** A chain matches a node when its steps map onto a *subsequence* of
that node's ancestor path (root last), in order, with the final step matching the
node itself. Kind and name must both agree on every step. This is what makes `file^part` and
`file@section:symbol^part` both name the same node: the short form simply
constrains less.

Chains are resolved by walking the tree once and collecting **every** node that
matches — never by stopping at the first hit. That distinction is the whole
difference between a graceful ambiguity error and a silent wrong answer.

**Ambiguity is a first-class outcome, not a failure mode.** Zero matches →
`ErrNotFound`, with the nearest same-name candidates of other kinds listed if any
exist (the common case is a wrong resolver character). Two or more matches →
`ErrAmbiguous`, exit code 3, and stderr lists every candidate **fully qualified**
with its line range:

```
$ anno read example.go^declarations
anno: ambiguous target "example.go^declarations" — 2 matches:
  example.go@code:Operate^declarations      <25:28>
  example.go@code:Reduce^declarations       <41:44>
```

Each listed line is a valid target, so the fix is a copy-paste. This matters most
for `write`, where a guessed disambiguation would edit the wrong region: `write`
therefore **never** proceeds on an ambiguous target, even if the matches are
identical in content.

**Splitting path from chain.** A path may itself contain resolver characters — a
Windows drive letter, a directory named `a:b` — so the split cannot be decided by
syntax alone. The parser returns *every* valid reading, ordered most-path-first,
and the command layer takes the first whose path exists with the right kind. A
file literally named `example.go^declarations` therefore wins over reading it as
a chain, which is the conservative choice; when no reading exists on disk, the
error quotes the most chain-like one, since that is almost certainly what was
meant. Step names exclude whitespace, path separators, brackets and resolver
characters, so a chain, once started, has exactly one reading.

## 5. Commands

| Command | Behaviour |
|---|---|
| `anno index <file>` | Parse one file, render the tree table. |
| `anno overview <dir>` | Parse every regular file directly in `<dir>` (non-recursive — "package"), render each tree under its filename. Unreadable, binary, or non-UTF-8 files are skipped with a note on stderr, never a hard failure. Deterministic (lexicographic) order. |
| `anno read <target>` | Emit the node's span verbatim — no dedent, no trimming, original line endings — so that `read` and `write` are exact inverses. A final newline is supplied where the file lacks one. |
| `anno find <dir><resolver><name>` | Resolve across the directory; for each match print a `path<resolver>name` header, its index row, and its content. |
| `anno write <target> <content>` | Replace the node's span. `<content>` of `-` reads stdin, which is the practical path for multi-line content and the one the Claude hooks will use. |

Exit codes: `0` success · `1` usage error · `2` target not found · `3` ambiguous
target · `4` parse error · `5` I/O error. Diagnostics go to stderr; only command
output goes to stdout, so hooks can pipe cleanly.

### 5a. Planned: narrow `read` by default

`read` emits a node's whole span, so `anno read file.go@code` returns every
symbol in that section. [Dock](../Dock/Plan.md) §2 takes the opposite default —
a section's *own* content, up to its first child, with `--tree` for the whole
span — because the narrow form is what a caller usually wants and the frugal
path should be the one taken by forgetting a flag. **The same narrowing belongs
here**, and is recorded rather than done because it changes the behaviour of a
finished tool:

- `anno read <target>` returns the node's own lines, stopping at its first child
  annotation.
- `anno read <target> --tree` returns the span as it does today.
- `write` takes `--tree` identically, so the two always mean the same region and
  the round-trip property holds in both modes.

The cost is a breaking change to `read` and a re-pinning of the goldens that
quote its output — §1's worked example among them. The alternative, adding
`--own` and leaving the default wide, keeps every existing caller working but
leaves the two tools disagreeing about what a bare `read` means, which is worse
for an agent that uses both.

## 6. Write safety

`write` is the only mutating path, so it gets the most scrutiny:

1. Read the file, record its SHA-256 and the byte offsets of the target span.
2. Reject content that would corrupt structure: an unbalanced `@:<`, or an open
   marker of rank ≤ the target's own rank (which would silently terminate the
   node being written and swallow following code).
3. Splice content into the byte slice, preserving the file's trailing-newline
   state. Line endings adopt the file's style **only if the file is uniform**; a
   file that already mixes styles has none to impose, and rewriting its
   terminators would change lines the caller never addressed.
4. Re-parse the result in memory. If the target node no longer resolves to the
   same path in the tree, abort — nothing is written.
5. Re-hash the on-disk file and compare to step 1; abort on mismatch (concurrent
   modification).
6. Write to a temp file in the same directory, `fsync`, copy the original's mode,
   `rename` over the target, `fsync` the directory. Temp file removed on any
   failure path.

Nested annotations inside a replaced span are destroyed by design — the caller
asked to replace that region. Step 4 guarantees the *file* stays well-formed.

## 6a. Hook integration

Two hooks, both `PostToolUse`, both served by one binary `anno-hook` that routes
on `tool_name`. Wiring and rationale are in [Hooks.md](Hooks.md).

- **guard** (`Edit|Write|NotebookEdit|MultiEdit`) — rebuilds the edited file's
  tree and exits 2 if annotations that were there no longer parse, feeding the
  fault back to the agent. This is the "minimizing accidental scope leak" half of
  Vision.md's closing line.
- **index** (`Read`) — returns the file's index as `additionalContext`, so the
  agent can address regions by name rather than re-reading. This is the "saving
  tokens and time" half.

The governing constraint is that a hook fires on every matching tool call and
must never break a session, so **only a genuinely broken annotated file may
block**. Unparseable input, an unknown event, a missing path, a binary, a wrong
field type — all exit 0 in silence, asserted by `TestNothingUnexpectedEverBlocks`
and by `FuzzRun`, which holds that no input at all can produce another exit code.
Files with no annotations are checked for markers before anything else, so a
partly annotated project never hears about the rest of itself.

It is a Go binary rather than a shell script because the index hook must emit
JSON containing arbitrary paths and annotation names, which can carry quotes and
backslashes; escaping that in shell is harder to get right than it looks and
cannot be tested properly.

## 6b. Colour

`internal/style` is a value type: the zero `Palette` is plain, so every path that
has not asked for colour gets none and byte-exact tests need no opt-out. Colour
is decided per stream, so a piped index stays clean while errors beside it on a
terminal stay legible. `NO_COLOR` disables, `CLICOLOR_FORCE=1` forces through a
pipe, `TERM=dumb` is honoured.

The rule that makes it safe: **padding is computed from plain text, and colour is
applied only when writing.** The layout pass never sees an escape sequence, so a
coloured table is aligned identically to a plain one — asserted by stripping the
sequences and comparing byte for byte.

## 7. Package layout

Module at `Orc/Anno/go.mod`. Proposed module path `orc/anno` — **confirm**, this
is the one thing I could not infer (no VCS remote in the tree).

```
Anno/
  go.mod
  cmd/anno/main.go        thin: parse argv, dispatch, map error → exit code
  internal/source/        File: bytes, lines, line-ending style, trailing-newline
                          flag, hash. Load/validate (UTF-8, NUL, size cap).
  internal/marker/        Line → Marker classification. Pure, no I/O.
  internal/tree/          Marker stream → immutable Tree. Spans, content ranges,
                          counts. Resolution by target.
  internal/target/        Target parsing: path + resolver + name.
  internal/render/        Tree → the box-drawn table. Pure: layout pass computes
                          column widths, render pass emits strings.
  internal/edit/          Splice planning + atomic apply (§6).
  internal/cli/           One file per command, each a pure
                          (args, fs) → (stdout string, error).
  internal/style/         Palette: colour as a value; zero value is plain.
  internal/hook/          Claude Code PostToolUse handling (§6a).
  cmd/anno-hook/          thin: streams in, exit code out.
```

Nothing outside `source`, `edit`, and `cli` touches the filesystem. Everything in
`marker`, `tree`, `target`, and `render` is a pure function of its input, which is
what makes the test suite cheap.

## 8. Immutability in practice

- Domain types are value structs with unexported fields and accessor methods;
  every constructor is `New…(…) (T, error)` and validates. No zero value is ever
  meaningful — a zero `Node` cannot be produced outside the package.
- Child slices are returned via `iter.Seq[Node]` (Go 1.23+) for traversal, and
  via `slices.Clone` where a slice is genuinely needed. Trees are small; the copy
  is not worth optimising away.
- Construction uses a local `builder` with a mutable stack, confined to one
  function in `tree`, which returns a frozen `Tree`. This is the honest boundary:
  parsing *is* stateful, and pretending otherwise would cost readability.
- No package-level mutable state, no `init()`, no singletons. Sentinel errors and
  the kind/resolver/closer tables are the only package-level values, all
  constants or immutable maps built once and never exported for mutation.
- `render` and `edit` build output through `strings.Builder` / byte slices that
  never escape their function.

## 9. Error handling

- Typed errors carrying position: `ParseError{Path, Line, Col, Reason}`,
  `ResolveError{Target, Candidates}`, `WriteError{Path, Stage, Err}`. All support
  `errors.Is`/`As` against sentinels (`ErrNotFound`, `ErrAmbiguous`,
  `ErrUnbalanced`, `ErrConflict`).
- A file with several parse problems reports **all** of them via `errors.Join`,
  so an agent fixing annotations gets one round trip instead of five.
- Every `os` and `io` call's error is wrapped with `%w` and the path. No
  `_ = err`. No `panic` outside genuinely impossible states, and those use a
  helper that documents the invariant.

## 10. Milestones

| # | Deliverable | Done when |
|---|---|---|
| 1 | `source` + `marker` | Fuzz test on `marker.Classify` finds no panic; table tests cover every sigil, kind, malformed form, and comment closer. |
| 2 | `tree` | The `Vision.md` example is a golden test: every span, content range, and count in the §1 table reproduces exactly. |
| 3 | `target` + `render` + `anno index` | `anno index example.go` is byte-identical to the doc's output, as a golden file. Chain matching is tested on a fixture with a deliberately duplicated `part` name: fully-qualified chains resolve uniquely, the short form errors with both candidates listed. |
| 4 | `anno read` | All three doc `read` invocations reproduce byte-for-byte. |
| 5 | `anno overview`, `anno find` | Multi-file fixture dir; skip-and-note behaviour tested against a binary and a non-UTF-8 file. |
| 6 | `edit` + `anno write` | Property test: `write(target, read(target))` leaves the file byte-identical, over the whole fixture corpus. Each §6 abort path has a test. |
| 7 | Claude Code hook integration | Hook config documented, exercised end-to-end. |
| 8 | Colour | Kinds coloured, structure dimmed; stripping the escapes gives back the plain table byte for byte. |

All milestones are complete. Milestone 8 was added after `AGENTS.md` turned up a
standing preference for colour and box drawing in every Orc tool; the table
already had the alignment and box drawing, and now has the colour.

## 10a. What the build actually found

Four defects, each caught by a mechanism this plan called for rather than by
reading the code:

1. **`Fprintln` return values ignored** (§5). A broken stdout exited 0. Found by
   an integration test with a writer that fails after *n* writes.
2. **Content could close its own enclosing annotation** (§6). The check counted
   close markers without matching names, so `@:< outer` inside a replacement was
   "balanced" and silently truncated the enclosing annotation. The rule is now
   that content may only close what it opened.
3. **Names ending in a comment terminator** (§2). `stripCloser` removes one
   terminator and is therefore not idempotent, so such a name could not be
   addressed twice running. Found by `FuzzClassify` via a rendering-stability
   property. Rejected at parse time now.
4. **`@:;` claiming a marker line** (§3) and **two line-ending corruptions**
   (§6). Found by `FuzzPipeline`, which writes every annotation's own content
   back and asserts the file is unchanged. The first left a parent annotation
   ending *before* its own child — caught by the §8 output assertions, which is
   exactly what they are for.

Every one of these is now a named test; the two fuzz corpora are checked in as
permanent regression seeds.

## 10b. Coverage

97.1% of statements (1178/1213), with `fault`, `marker` and `style` at 100%. The
residue is 35 statements, all defensive:

- **29 unreachable-by-construction guards** — `if err != nil` on helpers that
  cannot fail given a validated tree, plus the `default:` arm over a closed set
  of marker ops. They are kept deliberately: they are the §8 output assertions,
  and removing them to chase a number would remove the thing that caught defect
  4 above. Where a guard was *redundant* rather than defensive — render
  re-checking a kind that `tree.Build` already validated — it was deleted
  instead.
- **2 TOCTOU / re-validation paths** in `source` that need a racing writer.
- **Two `func main` lines**, one per binary. Both are single `os.Exit(…)` calls
  over logic that lives in a testable package, and both are exercised by
  subprocess tests that build and run the real executables — but a statement
  profile cannot attribute work done in another process.

`edit.Commit` is at 100% on its failure paths: an injected-operations seam drives
every step — temp create, write, sync, chmod, close, rename, re-read — into
failure and asserts the original file is untouched and no debris is left.

## 10a. Colour

Anno draws through `internal/style`, which maps Anno's vocabulary — a section, a
symbol, a part, a name, a range — onto roles in `orc/theme`, the Catppuccin
scheme every Orc tool shares. The three annotation kinds take three visually
distinct roles, because telling them apart at a glance is the reason to colour
an index at all.

Nothing in Anno names a colour, and nothing in Anno decides colour policy: the
flavour is `ORC_THEME` (macchiato by default), and `ORC_AGENT` forces plain
output because an agent's output is another program's input. Colour is decided
per stream, so a piped index stays clean while the errors beside it on a
terminal stay legible. See [Theme.md](../Theme.md).

## 11. Decisions to confirm

1. **Module path** — `orc/anno` assumed and used. Trivial to change.
2. **`overview` recursion** — non-recursive assumed ("package"). A recursive
   variant would need a flag, which the CLI spec does not currently have.
3. **`write` content via `-`/stdin** — the spec shows `<content>` as an argv
   argument; stdin is added because multi-line content through argv is fragile.
   Argv form still works for single-line content.
4. **Comment-closer stripping** — the closer table in §2 is a judgement call. The
   strict alternative (error on any trailing text) is one line to switch to.
5. **`read` emits spans verbatim** and never dedents (see §1). This contradicted
   one line of Vision.md's worked example, which contradicted itself; the
   round-trip guarantee decided it, and Vision.md has since been corrected to
   match.
