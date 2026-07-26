// Package fixture holds the worked example from the Anno documentation.
//
// The example in Docs/Anno/Vision.md is the specification's only concrete
// statement of line-range semantics, so it is reproduced here verbatim and used
// as the golden case across the test suite. Keeping it in one place means a
// change to the documented behaviour breaks exactly one constant.
package fixture

// ExampleGo is the example file from Vision.md. The doc's leading
// "// ./example.go" line is documentation framing rather than file content:
// without it the file is 32 lines, which is what the documented index reports.
const ExampleGo = `package main

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
`

// ExampleReadSection is the documented output of `anno read example.go@code`:
// the raw span, which begins at the nested symbol marker on line 22.
const ExampleReadSection = `// @:> symbol Operate [Pair Operation ->String]
func Operate(p Pair, o Operation) string {
	// @:> part declarations
	var (
		l = p.L
		r = p.R
	)
	// @:< declarations

	return o(l, r)
}
`

// ExampleReadSymbol is the documented output of `anno read example.go:Operate`.
const ExampleReadSymbol = `func Operate(p Pair, o Operation) string {
	// @:> part declarations
	var (
		l = p.L
		r = p.R
	)
	// @:< declarations

	return o(l, r)
}
`

// ExampleReadPart is the output of `anno read example.go^declarations`.
//
// The indentation is load-bearing: a span is emitted verbatim, which is what
// makes read and write exact inverses of each other. Anno never dedents.
// Vision.md once showed this block dedented to column zero while showing the
// identical lines indented one tab in its own `read example.go:Operate` output;
// the documentation has since been corrected to the indented form, and this
// constant is what it was corrected against.
const ExampleReadPart = "\tvar (\n\t\tl = p.L\n\t\tr = p.R\n\t)\n"
