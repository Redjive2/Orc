package cli

import (
	"strings"
	"testing"

	"orc/anno/internal/style"
)

// wantHelp is the help exactly as the constant printed it, kept here so the move to
// data stays checkable after the constant is gone. It is a golden: if a line of it
// has to change, that is a decision worth seeing in a diff.
const wantHelp = `anno — a minimal file annotation manager

usage:
  anno index    <file>                     tree of annotations in a file
  anno overview <dir>                      trees for every annotated file in a tree
  anno read     <file><chain>              content of an annotation
  anno find     <dir><chain>               content and index of matches in a directory
  anno write    <file><chain> <content>    replace an annotation's content ("-" reads stdin)

a chain addresses an annotation by kind and name:
  @name   section      :name   symbol      ^name   part

chains may be fully qualified or partial:
  anno read app.go@types:Pair^fields
  anno read app.go^fields

a partial chain that matches more than once fails, listing every candidate
fully qualified so it can be pasted back.

exit codes: 0 ok · 1 usage · 2 not found · 3 ambiguous · 4 parse · 5 i/o · 6 conflict · 9 out of scope`

// The help became data so it could be painted. This checks the move cost nothing:
// every line the old constant printed is still printed, in the same shape. The new
// colour section is the one addition, so the check is containment rather than
// equality — "nothing was lost" is the property that matters, and it is the one an
// exact match would stop expressing the moment anything was added on purpose.
func TestHelpKeepsEveryLineOfTheConstant(t *testing.T) {
	got := usage(style.Plain())

	for _, line := range strings.Split(wantHelp, "\n") {
		if !strings.Contains(got, line) {
			t.Errorf("the rendered help lost a line:\n%q\n\nrendered:\n%s", line, got)
		}
	}
}

// And it documents what the constant did not.
func TestHelpDocumentsColour(t *testing.T) {
	got := usage(style.Plain())

	for _, want := range []string{"ORC_THEME", "NO_COLOR", "ORC_AGENT", FlagNoColour, FlagColour} {
		if !strings.Contains(got, want) {
			t.Errorf("the help does not mention %q", want)
		}
	}
}
