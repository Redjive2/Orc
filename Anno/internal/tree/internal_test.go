package tree

import (
	"errors"
	"strings"
	"testing"

	"orc/anno/internal/marker"
	"orc/common/fault"
)

// The guards below cannot be reached through Build, which is the point: they
// check that Build's own output is well formed. Reaching them requires
// constructing the corrupt values Build is supposed to never produce.

func TestFreezeRejectsCorruptBuilders(t *testing.T) {
	lines := []string{"a", "b", "c"}
	marks := map[int]bool{}

	for _, tc := range []struct {
		name string
		b    *builder
		want string
	}{
		{"nil", nil, "nil builder"},
		{"never closed", &builder{name: "x", markerLine: 1, spanStart: 2, spanEnd: 0}, "never closed"},
		{"non-positive start", &builder{name: "x", markerLine: 1, spanStart: 0, spanEnd: 2}, "not positive"},
		{"span past end of file", &builder{name: "x", markerLine: 1, spanStart: 2, spanEnd: 99}, "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := freeze(tc.b, lines, marks)
			if !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("error = %v, want an internal fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestFreezePropagatesChildFailures(t *testing.T) {
	lines := []string{"a", "b", "c"}
	parent := &builder{
		name: "ok", markerLine: 1, spanStart: 2, spanEnd: 3,
		children: []*builder{{name: "bad", markerLine: 2, spanStart: 3, spanEnd: 0}},
	}
	if _, err := freeze(parent, lines, map[int]bool{}); !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want the child's fault to propagate", err)
	}
	if _, err := freezeAll([]*builder{parent}, lines, map[int]bool{}); !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want the child's fault to propagate through freezeAll", err)
	}
}

func TestMeasureRejectsSpansPastEndOfFile(t *testing.T) {
	span, err := NewRange(1, 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := measure(span, []string{"a"}, map[int]bool{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
}

func TestValidateRejectsANegativeLineCount(t *testing.T) {
	bad := Tree{path: "x.go", name: "x.go", count: -1}
	if err := bad.validate(); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
}

// node is a shorthand for building the corrupt nodes validateNodes must reject.
func node(kind marker.Kind, name string, markerLine, start, end, lines int) Node {
	span := Range{start: start, end: end}
	return Node{
		kind: kind, name: name, markerLine: markerLine,
		span: span, content: span, lines: lines,
	}
}

func TestValidateNodesRejectsEveryStructuralDefect(t *testing.T) {
	full := Range{start: 1, end: 10}

	for _, tc := range []struct {
		name  string
		nodes []Node
		want  string
	}{
		{
			"invalid kind",
			[]Node{node(marker.Kind(9), "a", 1, 2, 3, 2)},
			"invalid kind",
		},
		{
			"empty name",
			[]Node{node(marker.Section, "", 1, 2, 3, 2)},
			"empty name",
		},
		{
			"marker line outside the file",
			[]Node{node(marker.Section, "a", 99, 2, 3, 2)},
			"outside 1..10",
		},
		{
			"span outside the parent",
			[]Node{node(marker.Section, "a", 1, 2, 99, 2)},
			"outside its parent",
		},
		{
			"overlapping siblings",
			[]Node{node(marker.Section, "a", 1, 2, 5, 4), node(marker.Section, "b", 2, 3, 6, 4)},
			"overlapping a sibling",
		},
		{
			"line count exceeds the span",
			[]Node{node(marker.Section, "a", 1, 2, 3, 99)},
			"reports 99 lines",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNodes(tc.nodes, full, -1, 10)
			if !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("error = %v, want an internal fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateNodesRejectsContentOutsideItsSpan(t *testing.T) {
	n := node(marker.Section, "a", 1, 2, 3, 2)
	n.content = Range{start: 5, end: 9}
	err := validateNodes([]Node{n}, Range{start: 1, end: 10}, -1, 10)
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want an internal fault", err)
	}
	if !strings.Contains(err.Error(), "outside its span") {
		t.Errorf("message %q should name the defect", err)
	}
}

func TestValidateNodesRejectsMisrankedNesting(t *testing.T) {
	parent := node(marker.Part, "p", 1, 2, 8, 6)
	parent.children = []Node{node(marker.Section, "s", 3, 4, 5, 2)}
	err := validateNodes([]Node{parent}, Range{start: 1, end: 10}, -1, 10)
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want an internal fault", err)
	}
	if !strings.Contains(err.Error(), "rank") {
		t.Errorf("message %q should name the rank violation", err)
	}
}
