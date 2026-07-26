// Package fixture holds the golden doc set every Dock test measures against.
//
// Keeping it in one place means a change to documented behaviour breaks exactly
// one constant, and that the byte budgets in §6 of the plan are measured against
// a corpus that does not drift.
//
// The set is deliberately small and deliberately awkward: three documents, a
// same-file link, a cross-file link, a link into Anno's own example.go, a
// dangling link, and a diamond — two sections citing the same target, which is
// what --follow has to deduplicate.
package fixture

// Guide is the primary document. It carries every kind of link Dock recognises,
// and one it does not: an ordinary URL, which must stay invisible.
const Guide = `# §1 Guide

Dock reads documentation without reading all of it.
See [the grammar](./grammar.md§2) before starting.

## §1.1 Install

Run ` + "`go install ./cmd/dock`" + `.

## §1.2 Sections

A section is a heading carrying a number, as [the grammar](./grammar.md§2)
explains. Anno does the same job for code: [Operate](../code/example.go@code:Operate).

### §1.2.1 Numbering

The number's depth matches the heading's depth.

## §1.3 Troubleshooting

Start with [Install](§1.1), then read [the site](https://example.com).
Something [went missing](§9.9) here.
`

// Grammar is the document Guide cites twice — the diamond's shared target.
const Grammar = `# §1 Preface

This document states the grammar.

# §2 Grammar

A target is a path and an address.

## §2.1 Targets

See [Numbering](./guide.md§1.2.1) for what a number means.
`

// Trouble links back into Guide, which is what gives Guide's sections their
// inbound counts.
const Trouble = `# §1 Symptoms

## §1.1 Nothing resolves

Check [Install](./guide.md§1.1) first.
`

// GuideIndex is the output of "dock index guide.md" over Guide, with the link
// counts the whole corpus implies. A change to the table's layout breaks this
// constant and nothing else.
//
// Read it as the specification it is: §1.3 declares three links but only two
// are targets, because one is an ordinary URL; §1.1 is cited twice, once from
// this document and once from Trouble.
const GuideIndex = `|--------------------------|-------|------------------|
| [guide.md]               | →5 ←3 | 22 lines  <1:22> |
| §1     Guide             | →1 ←0 | 20 lines  <3:22> |
| §1.1     Install         | →0 ←2 |   1 line   <8:8> |
| §1.2     Sections        | →2 ←0 |  6 lines <12:17> |
| §1.2.1     Numbering     | →0 ←1 |   1 line <17:17> |
| §1.3     Troubleshooting | →2 ←0 |  2 lines <21:22> |
|--------------------------|-------|------------------|
`
