# Theme — the colour scheme Orc's tools share

Module `orc/theme`, at `Orc/Theme/`. Stdlib only, as every Orc tool is.

## Why it is a module

`anno` and `mailman` are separate modules with no dependency on each other, and
the tools still to come will be too. Left alone, each would grow its own idea of
what a heading or a warning looks like, and the set would stop looking like one
program.

So the scheme is one local module that every tool requires by path:

```
require orc/theme v0.0.0
replace orc/theme => ../Theme
```

The `replace` is what keeps each tool buildable on its own — no published
version, and no effect on any module that does not ask for it. A `go.work` at
the tree root lists every module as a convenience for editing across them, but
nothing requires it: `GOWORK=off go build ./...` succeeds in every module, and
that is the property to keep.

## The scheme

Catppuccin, all four flavours, transcribed unmodified from the published
palette so output sits correctly beside any other Catppuccin-themed window.
**Macchiato is the default.**

A tool never names a colour. It names a **role**, and the flavour in force
decides the colour:

| Role | Macchiato | Is for |
|---|---|---|
| `Text` | text | ordinary content |
| `Heading` | text, bold | a column label, a field name |
| `Title` | mauve, bold | what the whole thing is |
| `Muted` | overlay1 | counts, timestamps, things already read |
| `Subtle` | overlay0, italic | an aside the eye should skip |
| `Frame` | surface2 | box drawing, which should recede |
| `Primary` | blue | the thing being looked for |
| `Secondary` | mauve | a second category |
| `Tertiary` | teal | a third |
| `Accent` | peach | unusual, but not wrong |
| `Info` | sapphire | identifiers and references |
| `Success` | green | a satisfied condition |
| `Warning` | yellow | needs attention |
| `Danger` | red | a failure |

That indirection is the point. It is what lets one setting restyle every tool at
once, and what stops two tools disagreeing about what green means.

**Colour is a layer, never information.** Every use of it in every tool is
redundant with a glyph or a word, so a pipe through `grep`, a dumb terminal, and
`NO_COLOR` all lose the pleasure and nothing else.

## Configuration

Session-configurable: export it once, every tool follows.

| Variable | Effect |
|---|---|
| `ORC_THEME` | `macchiato` (default), `mocha`, `frappe`, `latte`, or `none` |
| `ORC_AGENT` | set to anything → output is always plain |
| `NO_COLOR` | set to anything → no colour, per no-color.org |
| `CLICOLOR_FORCE` | anything but `0` → colour even through a pipe |
| `TERM=dumb` | no colour |
| `COLORTERM` | `truecolor`/`24bit` → 24-bit; otherwise the 256-colour cube |

Resolution order — every step that can say *no* is checked before any step that
can say *yes*:

1. `ORC_AGENT`
2. `NO_COLOR`
3. `ORC_THEME=none`
4. `TERM=dumb`
5. `CLICOLOR_FORCE`
6. otherwise, only when the stream really is a terminal

A misspelled `ORC_THEME` is a **usage error**, not a silent fall back to the
default. A setting that quietly does nothing is one the operator concludes is
broken.

### Agents get no colour, ever

`ORC_AGENT` is first in that list and absolute — it beats `CLICOLOR_FORCE`, a
chosen flavour, and a real terminal. An agent's output is an input to another
program, and escape sequences in it are corruption rather than decoration.

**Orc must set `ORC_AGENT` when it spawns an agent.** Until it does, agents are
still covered by rule 6, because a spawned process has no terminal — but that is
an accident of how they are run, and this is the guarantee.

Mailman's suite asserts the guarantee end-to-end: an agent's output is
byte-identical to the plain rendering, so nothing downstream has to strip
anything.

## Colour depth

24-bit where `COLORTERM` says so, and the xterm 256-colour cube everywhere else
— the palette is approximated rather than abandoned. The approximation measures
both the colour cube and the grey ramp and takes the closer, because Catppuccin's
greys (which most of a frame is drawn in) otherwise collapse onto a handful of
muddy cube entries.

## What each tool maps to what

Tools own the mapping from *their* vocabulary to roles; the scheme owns what the
roles look like.

**Anno** — `Section`→Secondary, `Symbol`→Tertiary, `Part`→Success (three
distinct hues, because telling the kinds apart at a glance is why an index is
coloured at all); `Name`→Heading, `Meta`→Accent, `Span`→Info, `Quiet`→Muted,
`Good`→Success, `Alarm`→Danger.

**Mailman** — `Title`→Title, `Header`→Heading, `Frame`→Frame, `Muted`→Muted,
`Unread`→Warning (bold), `User`→Success, `Subject`→Primary, `Convo`→Secondary,
`ID`→Info, `Good`→Success, `Bad`→Danger, `Note`→Subtle.

## Adding a tool

1. `require orc/theme v0.0.0` + `replace orc/theme => ../Theme` in its `go.mod`.
2. `theme.ForStream(os.Stdout, os.LookupEnv)` per stream in `main`, reporting
   the error as a usage failure.
3. A local `style` package mapping the tool's own vocabulary to roles — so the
   drawing code names what things *are*, and only one file knows about the
   scheme.
4. Put `theme.Help()` in the help text, so every tool documents the settings in
   the same words.

## Status

Wired: `anno`, `mailman`, `muff` (and `muff-hook`, which is plain by
construction — a hook's output is read by a model), `cq`, `dock`, `orcprobe`.

Every tool in the workspace now resolves its scheme through this package, so
`ORC_THEME` restyles all of them at once and no two can disagree about what
green means. cq's *website* is separately Catppuccin Macchiato and dark-only:
CSS cannot import a Go package, so that one is a parallel implementation of the
same palette rather than a caller of it.

Not wired: `orc` itself, which does not exist yet. It is four small steps when
it does.
