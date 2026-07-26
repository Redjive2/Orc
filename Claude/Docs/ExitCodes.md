# Exit codes

Every Orc tool exits with the same numbers meaning the same things. A hook or a
shell script that branches on a status must not have to know which binary it
called.

The table is code, not prose: `orc/common/fault` defines the constants and the
`Code(err)` function that maps a sentinel to one of them. This document is
written from that table, and the two are changed in one commit or not at all.

| Code | Name | Sentinel | Means |
|---:|---|---|---|
| `0` | ok | — | It worked. |
| `1` | usage | `ErrUsage` | The command line was malformed. |
| `2` | not found | `ErrNotFound` | The target does not exist. |
| `3` | ambiguous | `ErrAmbiguous` | The target matched more than one thing. |
| `4` | parse | `ErrParse`, `ErrUnbalanced` | Input or stored data is malformed. |
| `5` | i/o | `ErrIO` | A filesystem operation failed. |
| `6` | conflict | `ErrConflict` | Something changed underneath the operation, or a write would overwrite what must not be. |
| `7` | auth | `ErrAuth` | Authentication failed. |
| `8` | denied | `ErrDenied` | Authenticated, but not permitted. |
| `9` | out of scope | `ErrScope` | Permitted, but not for that path. |
| `10` | unavailable | `ErrUnavailable` | A peer could not be reached. |
| `11` | escape | `ErrEscape` | A path resolved outside the root it was measured against. |
| `70` | internal | `ErrInternal` | A bug in the tool. |

## Why some of the choices

**`8` and `9` are separate.** "You may not do this at all" and "you may do this,
but not to that file" are different problems with different fixes: the first
needs a different actor, the second needs a different path or a wider scope.
Collapsing them would make a scope violation indistinguishable from a permission
failure to anything reading the status.

**`9` and `11` are separate too.** A scope violation is a path inside the root
but outside what a task declared — routine, and fixed by widening the scope or
editing something else. An escape is a path that resolves *outside the root
altogether*, which is a containment failure and the one thing a monitor should
alarm on. Sharing a code would make them indistinguishable to the hook that has
to tell them apart.

**`10` is not `5`.** A dead network and a bad disk are fixed by different people,
and `cq` is the tool that has to say which it hit.

**`70`, not `12`.** A defect must never be mistakable for a documented outcome,
and the gap leaves room to add outcomes without renumbering. An error outside the
vocabulary maps here too, rather than to `1` — a tool that returned an
unclassified error has a hole in it, and reporting that as a user mistake would
hide it.

**Internal is tested first.** A bug wrapped in something friendlier still reports
as a bug.

## Hook codes are not these

Claude Code hooks have their own contract: `0` proceeds, `2` blocks. A hook
binary (`anno-hook`, `muff-hook`) exits with *those*, not with the table above,
and the rule that only a genuine violation may block outranks reporting — an
unexpected input exits `0` silently rather than surfacing a fault. See
`Hooks.md`.

`muff check-scope` is the bridge between the two worlds: it is an ordinary
command, so it uses this table (`0` in scope, `9` outside), and the hook
translates.

## Adding a code

1. Add the constant and the `Code` case in `Common/fault/codes.go`.
2. Add it to `Sentinels()`, which is what the totality test walks.
3. Add a row here.

One commit, all tools at once. The totality test fails if a sentinel maps
nowhere, and the stability test fails if an existing number moves — which is a
breaking change to every script downstream and should have to break a test first.
