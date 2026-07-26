# §1 Anno

Anno (`anno`) is a minimal file annotation manager.

## §1.1 Markers

Annotations are created with a marker at the end of any line:

| Marker | Form                                 | Means                       |
|--------|--------------------------------------|-----------------------------|
| `@:>`  | `@:> <kind> <name> [<metadata...>]?` | Open an annotation          |
| `@:;`  | `@:; <kind> <name> [<metadata...>]?` | Annotate only the next line |
| `@:<`  | `@:< <name>`                         | Close an annotation         |

The following kinds are supported, and nest outermost first:

| Kind      | Holds                                     |
|-----------|-------------------------------------------|
| `section` | A section of code (types, handlers, etc.) |
| `symbol`  | A single declaration                      |
| `part`    | A piece of code (a few lines, max)        |

An annotation of kind `k` ends automatically when the next annotation of kind
`k` is opened or the file ends.

You can attach metadata to an annotation with a space-separated list in square
brackets after the name. By convention, starting a metadata object with `->`
means 'returns'.

**A sigil inside a string is a mention, not a marker.** Anno annotates any text
file and has no idea what language it is reading, so it knows only the two
quoting characters that mean *literal* almost everywhere: the double quote, with
backslash escapes, and the backtick. Without this a program that talks about
`@:>` — Anno's own source, its tests, anything documenting the syntax — is a
program Anno refuses to read.

The single quote is not one of them: it is an apostrophe far more often than a
delimiter.

A sigil in bare prose is still a marker, because nothing distinguishes it from
one. Write it quoted or in backticks when discussing it.

## §1.2 Example

A simple annotated go snippet:

```go
// ./example.go
package main

// @:> section data
var (
	// @:; symbol SampleOperation [Operation]
	SampleOperation Operation = func(x, y string) string { return x + y }
)

// @:> section types
type (
	// @:> symbol Pair [struct L R]
	Pair struct {
		L string
		R string
	}

	// @:; symbol Operation
	Operation func(string, string) string
)

// @:> section code
// @:> symbol Operate [Pair Operation ->String]
func Operate(p Pair, o Operation) string {
	// @:> part declarations
	var (
		l = p.L
		r = p.R
	)
	// @:< declarations

	return o(l, r)
}
```

You can search the tree this describes with `anno index <file path>`:

```
$ anno index example.go
|----------:-------------------|----------:---------:-------|------------------|
[example.go]                   [                            ] 32 lines < 1:32> |
|  section    data             [                            ]  3 lines < 4: 7> |
|  |  symbol  SampleOperation  [Operation                   ]  1 line  < 6: 6> |
|  section    types            [                            ]  8 lines <10:19> |
|  |  symbol  Pair             [struct    L         R       ]  4 lines <12:15> |
|  |  symbol  Operation        [                            ]  1 line  <18:18> |
|  section    code             [                            ]  8 lines <23:32> |
|  |  symbol  Operate          [Pair      Operation ->String]  8 lines <23:32> |
|  |  |  part declarations     [                            ]  4 lines <25:28> |
|--:--:------------------------|----------:---------:-------|------------------|
```

Or, read the content inside a specific annotation. Content is emitted verbatim,
so `read` and `write` are exact inverses:

```
$ anno read example.go@code
// @:> symbol Operate [Pair Operation ->String]
func Operate(p Pair, o Operation) string {
	// @:> part declarations
	var (
		l = p.L
		r = p.R
	)
	// @:< declarations

	return o(l, r)
}

$ anno read example.go:Operate
func Operate(p Pair, o Operation) string {
	// @:> part declarations
	var (
		l = p.L
		r = p.R
	)
	// @:< declarations

	return o(l, r)
}

$ anno read example.go^declarations
	var (
		l = p.L
		r = p.R
	)
```

This is combined with Claude Code hooks to let Claude agents use this
functionality and write to specific annotations, saving tokens and time while
minimizing accidental scope leak in changes.
