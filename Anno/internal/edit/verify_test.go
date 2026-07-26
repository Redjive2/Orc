package edit

import (
	"errors"
	"strings"
	"testing"

	"orc/anno/internal/target"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
)

// setup resolves a chain and returns everything verify needs.
func setup(t *testing.T, text, addr string) (source.File, target.Match, []target.Step) {
	t.Helper()
	f, err := source.Parse("x.go", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := tree.Build(f)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := target.ParseOne(addr)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := target.Resolve(tr, tgt.Steps())
	if err != nil || len(matches) != 1 {
		t.Fatalf("Resolve: %v, %d matches", err, len(matches))
	}
	return f, matches[0], tgt.Steps()
}

// The guards below are unreachable through Prepare, whose content rules reject
// the inputs that would trigger them. They exist so that a defect in those
// rules cannot silently corrupt a file, and are therefore tested directly.

func TestVerifyAcceptsAnUndisturbedStructure(t *testing.T) {
	src := "// @:> section s\nold\n"
	f, m, steps := setup(t, src, "x.go@s")
	if err := Verify(f, []byte("// @:> section s\nnew\n"), m, steps); err != nil {
		t.Errorf("verify rejected a sound edit: %v", err)
	}
}

func TestVerifyRefusesAnEditThatRemovesTheAnnotation(t *testing.T) {
	src := "// @:> section s\nold\n"
	f, m, steps := setup(t, src, "x.go@s")
	err := Verify(f, []byte("nothing here\n"), m, steps)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "would remove") {
		t.Errorf("message %q should say the annotation would be removed", err)
	}
}

func TestVerifyRefusesAnEditThatMovesTheAnnotation(t *testing.T) {
	src := "// @:> section outer\n// @:> symbol s\nold\n"
	f, m, steps := setup(t, src, "x.go:s")
	// The symbol survives but is no longer inside the same section.
	err := Verify(f, []byte("// @:> section other\n// @:> symbol s\nold\n"), m, steps)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "would move") {
		t.Errorf("message %q should say the annotation would move", err)
	}
}

func TestVerifyRefusesAnEditThatCreatesAmbiguity(t *testing.T) {
	src := "// @:> section outer\n// @:> symbol s\nold\n"
	f, m, steps := setup(t, src, "x.go:s")
	err := Verify(f, []byte("// @:> section outer\n// @:> symbol s\na\n// @:> symbol s\nb\n"), m, steps)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("message %q should say the target would be ambiguous", err)
	}
}

func TestVerifyRefusesAnEditThatBreaksTheFile(t *testing.T) {
	src := "// @:> section s\nold\n"
	f, m, steps := setup(t, src, "x.go@s")

	if err := Verify(f, []byte("// @:> section s\n\x00\n"), m, steps); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a binary result should be refused as unreadable, got %v", err)
	}
	if err := Verify(f, []byte("// @:> section s\n// @:< ghost\n"), m, steps); !errors.Is(err, fault.ErrUnbalanced) {
		t.Errorf("a broken annotation should be refused, got %v", err)
	}
}

func TestVerifyReportsAnEmptyChain(t *testing.T) {
	src := "// @:> section s\nold\n"
	f, m, _ := setup(t, src, "x.go@s")
	if err := Verify(f, []byte(src), m, nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("an empty chain should be an internal fault, got %v", err)
	}
}
