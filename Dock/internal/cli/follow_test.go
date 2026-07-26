package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/anno"
	"orc/dock/internal/cli"
	"orc/dock/internal/style"
)

// marked writes a corpus whose every section's content is a known number of
// identical marker lines, so what --budget let through can be counted exactly
// rather than inferred from the shape of the output.
const marker = "xxxx"

// countMarkers counts content lines, ignoring the headers and notes that report
// what was and was not emitted.
func countMarkers(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if line == marker {
			n++
		}
	}
	return n
}

// diamond: §1 cites both a and b, and both cite c. Following from §1 must emit
// c once, however many paths reach it.
func diamond(t *testing.T, linesPer int) string {
	t.Helper()
	dir := t.TempDir()
	body := strings.Repeat(marker+"\n", linesPer)
	for name, text := range map[string]string{
		"top.md": "# §1 Top\n\n" + body + "\nSee [a](./a.md§1) and [b](./b.md§1).\n",
		"a.md":   "# §1 A\n\n" + body + "\nAlso [c](./c.md§1).\n",
		"b.md":   "# §1 B\n\n" + body + "\nAlso [c](./c.md§1).\n",
		"c.md":   "# §1 C\n\n" + body,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFollowIsOffByDefault(t *testing.T) {
	dir := diamond(t, 1)
	out, _, code := run(t, "read", filepath.Join(dir, "top.md")+"§1")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	// The prose itself contains "[a](./a.md§1)", so a substring check would
	// prove nothing. What proves it is the content line count: one section's
	// worth, and no other section's.
	if got := countMarkers(out); got != 1 {
		t.Errorf("emitted %d content lines, want just this section's 1:\n%s", got, out)
	}
	if strings.Contains(out, "is shown above") || strings.Contains(out, "omitted") {
		t.Errorf("read reported on links it was not asked to follow:\n%s", out)
	}
}

// TestFollowEmitsEachSectionOnce is §6's dedup mechanism. Without it, following
// depth 2 in a well-cross-referenced doc set costs more than reading the files.
func TestFollowEmitsEachSectionOnce(t *testing.T) {
	dir := diamond(t, 1)
	out, _, code := run(t, "read", filepath.Join(dir, "top.md")+"§1", "--follow=3")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if got := strings.Count(out, "c.md§1"); got == 0 {
		t.Fatalf("the shared section was never reached:\n%s", out)
	}
	// Four sections, one content line each: top, a, b, c — and c exactly once.
	if got := countMarkers(out); got != 4 {
		t.Errorf("emitted %d content lines, want 4 (c once):\n%s", got, out)
	}
	// The second path to c says so rather than repeating it.
	if !strings.Contains(out, "is shown above") {
		t.Errorf("the duplicate was dropped silently:\n%s", out)
	}
}

// TestFollowTerminatesOnACycle. A link cycle between documents is legitimate and
// common; --follow must not loop on it.
func TestFollowTerminatesOnACycle(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"a.md": "# §1 A\n\n" + marker + "\n\n[b](./b.md§1)\n",
		"b.md": "# §1 B\n\n" + marker + "\n\n[c](./c.md§1)\n",
		"c.md": "# §1 C\n\n" + marker + "\n\n[a](./a.md§1)\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan string, 1)
	go func() {
		out, _, _ := run(t, "read", filepath.Join(dir, "a.md")+"§1", "--follow=8")
		done <- out
	}()
	out := <-done

	if got := countMarkers(out); got != 3 {
		t.Errorf("emitted %d content lines, want 3 — the cycle repeated a section:\n%s", got, out)
	}
	if !strings.Contains(out, "is shown above") {
		t.Errorf("the cycle's return edge was not reported:\n%s", out)
	}
}

func TestFollowDepthIsRespected(t *testing.T) {
	dir := diamond(t, 1)
	top := filepath.Join(dir, "top.md") + "§1"
	for _, tc := range []struct{ depth, want int }{
		{1, 3}, // top, a, b
		{2, 4}, // and c
		{3, 4}, // nothing further to reach
	} {
		t.Run(fmt.Sprint(tc.depth), func(t *testing.T) {
			out, _, code := run(t, "read", top, fmt.Sprintf("--follow=%d", tc.depth))
			if code != fault.CodeOK {
				t.Fatalf("code = %d", code)
			}
			if got := countMarkers(out); got != tc.want {
				t.Errorf("depth %d emitted %d content lines, want %d:\n%s", tc.depth, got, tc.want, out)
			}
		})
	}
}

// TestBudgetIsNeverExceeded is §6's other bound. An agent that overran its
// context because a document was bigger than expected was failed by the tool.
//
// The budget bounds *content*; the headers and notes that say what was and was
// not emitted are always printed, because suppressing them to fit a budget is
// what would make the budget silently lossy.
func TestBudgetIsNeverExceeded(t *testing.T) {
	for _, linesPer := range []int{1, 3, 7} {
		dir := diamond(t, linesPer)
		top := filepath.Join(dir, "top.md") + "§1"
		for budget := 1; budget <= 4*linesPer+3; budget++ {
			out, _, code := run(t, "read", top, "--follow=8", fmt.Sprintf("--budget=%d", budget))
			if code != fault.CodeOK {
				t.Fatalf("budget %d: code = %d", budget, code)
			}
			got := countMarkers(out)
			// The section asked for is always emitted, so the floor is its own
			// size; beyond that nothing may push the total past the budget.
			ceiling := budget
			if linesPer > ceiling {
				ceiling = linesPer
			}
			if got > ceiling {
				t.Errorf("budget %d (sections of %d lines) emitted %d content lines:\n%s",
					budget, linesPer, got, out)
			}
		}
	}
}

// TestBudgetSaysWhatItOmitted: a reader who cannot tell "there was nothing
// more" from "there was more and you did not get it" has been misled.
func TestBudgetSaysWhatItOmitted(t *testing.T) {
	dir := diamond(t, 5)
	out, _, code := run(t, "read", filepath.Join(dir, "top.md")+"§1", "--follow=2", "--budget=6")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "omitted") {
		t.Fatalf("the budget stopped silently:\n%s", out)
	}
	// The note names the section and how to read it, so the next step is a
	// copy-paste rather than a guess.
	if !strings.Contains(out, "dock read") {
		t.Errorf("the note does not say how to read what was omitted:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "dock read ") {
			continue
		}
		ref := strings.Trim(strings.Fields(strings.SplitN(line, "dock read ", 2)[1])[0], "`")
		if _, _, code := run(t, "read", filepath.Join(dir, ref)); code != fault.CodeOK {
			t.Errorf("the suggested command %q does not work", ref)
		}
	}
}

func TestFollowFlagValidation(t *testing.T) {
	dir := diamond(t, 1)
	top := filepath.Join(dir, "top.md") + "§1"
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"budget without follow", []string{"read", top, "--budget=5"}, fault.CodeUsage},
		{"follow zero", []string{"read", top, "--follow=0"}, fault.CodeUsage},
		{"follow too deep", []string{"read", top, "--follow=99"}, fault.CodeUsage},
		{"follow not a number", []string{"read", top, "--follow=lots"}, fault.CodeUsage},
		{"budget zero", []string{"read", top, "--follow", "--budget=0"}, fault.CodeUsage},
		{"budget not a number", []string{"read", top, "--follow", "--budget=big"}, fault.CodeUsage},
		{"unknown flag", []string{"read", top, "--depth=2"}, fault.CodeUsage},
		{"follow on write", []string{"write", top, "text", "--follow"}, fault.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, code := run(t, tc.args...); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

// annoContent answers with a fixed annotation body.
type annoContent struct{ body string }

func (a annoContent) Run(_ context.Context, args ...string) (string, string, int, error) {
	return a.body, "", fault.CodeOK, nil
}

// TestFollowReadsCodeThroughAnno is the last use of the anno boundary: a link
// into code is followed by the only tool that can read it.
func TestFollowReadsCodeThroughAnno(t *testing.T) {
	dir := t.TempDir()
	body := "# §1 Guide\n\n" + marker + "\n\nSee [Operate](../code/x.go@code:Operate).\n"
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Plain())
	app.Anno = anno.NewWith(annoContent{body: "func Operate() {}\n"})
	if code := app.Main([]string{"read", filepath.Join(dir, "guide.md") + "§1", "--follow"}); code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs.String())
	}

	got := out.String()
	if !strings.Contains(got, "func Operate()") {
		t.Errorf("the annotation's content was not followed:\n%s", got)
	}
	// It is tagged, so a reader can tell prose from code without reading it.
	if !strings.Contains(got, "(anno)") {
		t.Errorf("the code section is not marked as anno's:\n%s", got)
	}
}

// TestFollowWithoutAnnoSkipsCodeQuietly: a code link is not a broken link, and
// read is not the command that reports on link health.
func TestFollowWithoutAnnoSkipsCodeQuietly(t *testing.T) {
	dir := t.TempDir()
	body := "# §1 Guide\n\n" + marker + "\n\nSee [Operate](../code/x.go@code:Operate).\n"
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Plain())
	app.Anno = anno.Tool{}
	if code := app.Main([]string{"read", filepath.Join(dir, "guide.md") + "§1", "--follow"}); code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if got := countMarkers(out.String()); got != 1 {
		t.Errorf("emitted %d content lines, want 1:\n%s", got, out.String())
	}
}
