package cli

import "testing"

// TestScanDetach: the detach sequence is recognised, and every other keystroke
// reaches the session unchanged — including the prefix itself when it turns out not
// to have been a detach.
//
// This is an internal test because the function is the whole of what makes `^\`
// safe to reserve: a caller that dropped a held prefix would make one key silently
// unusable inside somebody's session, and that is not a thing an end-to-end test
// would notice.
func TestScanDetach(t *testing.T) {
	cases := []struct {
		name     string
		in       []string // successive reads, so a sequence split across two is covered
		want     string
		detach   bool
		armedEnd bool
	}{
		{name: "ordinary keys pass through", in: []string{"hello"}, want: "hello"},
		{name: "the sequence detaches", in: []string{"\x1cd"}, want: "", detach: true},
		{name: "split across reads", in: []string{"\x1c", "d"}, want: "", detach: true},
		{name: "text before the sequence still arrives", in: []string{"ls\x1cd"}, want: "ls", detach: true},
		{
			// The prefix followed by anything else is not a detach, so both bytes go
			// to the session: ^\ is a key a program may legitimately want.
			name: "prefix and another key are forwarded",
			in:   []string{"\x1cx"}, want: "\x1cx",
		},
		{name: "prefix at the end is held", in: []string{"a\x1c"}, want: "a", armedEnd: true},
		{name: "two prefixes forward one", in: []string{"\x1c\x1c"}, want: "\x1c\x1c"},
		{name: "control keys the session owns are untouched", in: []string{"\x03\x04\x1d"}, want: "\x03\x04\x1d"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var armed bool
			var got string
			detached := false
			for _, chunk := range c.in {
				out, detach := scanDetach([]byte(chunk), &armed)
				got += string(out)
				if detach {
					detached = true
					break
				}
			}
			if got != c.want {
				t.Errorf("forwarded %q, want %q", got, c.want)
			}
			if detached != c.detach {
				t.Errorf("detached = %v, want %v", detached, c.detach)
			}
			if !detached && armed != c.armedEnd {
				t.Errorf("armed = %v at the end, want %v", armed, c.armedEnd)
			}
		})
	}
}
