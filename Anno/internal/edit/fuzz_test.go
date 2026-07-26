package edit_test

import (
	"errors"
	"testing"

	"orc/anno/internal/edit"
	"orc/anno/internal/fixture"
	"orc/anno/internal/target"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
)

// FuzzPipeline drives arbitrary bytes through the whole read path and, for every
// annotation it finds, writes that annotation's own content straight back.
//
// It asserts the two properties the tool rests on: nothing panics on any input,
// and writing back what was read never changes a byte. A failure here is either
// a crash or silent corruption, which are the only two outcomes that would make
// anno unsafe to point at real source files.
func FuzzPipeline(f *testing.F) {
	for _, seed := range []string{
		fixture.ExampleGo,
		"// @:> section s\nx\n",
		"// @:> section s\r\nx\r\n",
		"// @:> section s\nx",
		"// @:; symbol s\nx\n",
		"// @:> section s\n// @:< s\n",
		"// @:> part p\n// @:> part p\n",
		"",
		"\n\n\n",
		"// @:> symbol a [x y]\n// @:> symbol a [x y]\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, text string) {
		file, err := source.Parse("fuzz.go", []byte(text))
		if err != nil {
			classified(t, err)
			return
		}
		if got := string(file.Bytes()); got != text {
			t.Fatalf("source did not round trip: %q became %q", text, got)
		}

		tr, err := tree.Build(file)
		if err != nil {
			classified(t, err)
			return
		}

		var walk func(nodes []tree.Node, chain []target.Step)
		walk = func(nodes []tree.Node, chain []target.Step) {
			for _, n := range nodes {
				step, err := target.NewStep(n.Kind(), n.Name())
				if err != nil {
					t.Fatalf("a tree node yielded an unusable step: %v", err)
				}
				here := append(append([]target.Step{}, chain...), step)

				matches, err := target.Resolve(tr, here)
				if err != nil {
					t.Fatalf("resolving a chain built from the tree failed: %v", err)
				}
				// A fully qualified chain may still be ambiguous when two
				// siblings share a name; only unique matches can be written.
				if len(matches) == 1 {
					span, err := file.Slice(n.Span().Start(), n.Span().End())
					if err != nil {
						t.Fatalf("slicing a node's own span failed: %v", err)
					}
					plan, err := edit.Prepare(file, matches[0], here, string(span))
					if err != nil {
						classified(t, err)
					} else if got := string(plan.Result()); got != text {
						t.Fatalf("writing back its own content changed the file:\n%q\nbecame\n%q", text, got)
					}
				}
				walk(n.Children(), here)
			}
		}
		walk(tr.Children(), nil)
	})
}

// classified asserts that a failure is one anno knows how to report, so that no
// input can produce a bare, unclassified error.
func classified(t *testing.T, err error) {
	t.Helper()
	for _, sentinel := range []error{
		fault.ErrParse, fault.ErrUnbalanced, fault.ErrUsage,
		fault.ErrConflict, fault.ErrNotFound, fault.ErrAmbiguous, fault.ErrIO,
	} {
		if errors.Is(err, sentinel) {
			return
		}
	}
	t.Fatalf("unclassified error: %v", err)
}
