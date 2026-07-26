package protocol_test

import (
	"strings"
	"testing"
	"time"

	"orc/cq/internal/protocol"
)

// TestPathologicalInputsDecodeQuickly guards the server's one untrusted entry
// point. A body that is merely refused is not enough: it must be refused
// *promptly*, or the refusal is itself the denial of service.
func TestPathologicalInputsDecodeQuickly(t *testing.T) {
	const budget = 500 * time.Millisecond

	for _, tc := range []struct{ name, body string }{
		{"deep array nesting", strings.Repeat("[", 100_000)},
		{"deep object nesting", strings.Repeat(`{"a":`, 100_000)},
		{"one enormous string", `{"agent":"` + strings.Repeat("x", 8<<20) + `"}`},
		{"very many keys", "{" + strings.Repeat(`"a":1,`, 200_000) + `"b":2}`},
		{"very many values", `{"results":[` + strings.Repeat(`1,`, 200_000) + `1]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var into protocol.SyncRequest
			start := time.Now()
			err := protocol.Decode(strings.NewReader(tc.body), protocol.MaxSnapshotBytes, &into)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatalf("expected a refusal")
			}
			if elapsed > budget {
				t.Errorf("refusing took %v, budget is %v", elapsed, budget)
			}
		})
	}
}
