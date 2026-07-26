// Package anno is Dock's boundary onto the anno binary.
//
// Dock resolves code targets by running anno rather than by importing it:
// Anno's packages are internal, the two tools version independently, and one
// Orc tool driving another by exec is the pattern Macmuffin's plan already
// sets. The exec sits behind an interface so every test runs against a recorder
// rather than a real process, with one test against the real binary to check
// the contract against the actual tool instead of against assumptions about it.
//
// The rule that shapes everything here: **a target anno could not be asked
// about is unchecked, not broken.** Reporting a link as dangling because the
// tool that resolves it is missing would send someone to fix a document that is
// correct.
package anno

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"orc/common/fault"
)

// Deadline bounds one call. A subprocess that hangs must not hang a check over
// a whole corpus, and anno reads one file — if it has not answered in this long,
// something is wrong with the machine rather than with the link.
const Deadline = 10 * time.Second

// Verdict is what anno said about a target.
type Verdict int

const (
	// Exists means anno resolved the target.
	Exists Verdict = iota
	// Missing means anno is working and the target is not there.
	Missing
	// Ambiguous means the target names more than one annotation, which for a
	// link is as broken as naming none: it does not address one thing.
	Ambiguous
	// Unknown means anno could not be asked — it is absent, it failed for its
	// own reasons, or it timed out.
	Unknown
)

// String implements fmt.Stringer.
func (v Verdict) String() string {
	switch v {
	case Exists:
		return "exists"
	case Missing:
		return "missing"
	case Ambiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}

// Result is one answer from anno.
type Result struct {
	// Verdict is what anno said.
	Verdict Verdict
	// Why explains a Missing, Ambiguous, or Unknown verdict in one phrase.
	Why string
	// Content is the target's text, present only for Exists and only when it
	// was asked for.
	Content string
	// Candidates lists the fully qualified alternatives anno offered for an
	// ambiguous target. Each is a valid target, so a fix is a copy-paste.
	Candidates []string
}

// Runner executes the anno binary. It is the whole of the seam.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr string, code int, err error)
}

// Tool asks anno about targets. The zero Tool is unavailable and answers
// Unknown to everything, which is the safe default: it never calls a document
// broken.
type Tool struct {
	run  Runner
	name string
}

// New finds anno on PATH.
//
// A missing binary is not an error. Dock is useful without it — most links are
// between documents — and a corpus with code links simply reports them as
// unchecked until anno is installed.
func New() Tool {
	path, err := exec.LookPath("anno")
	if err != nil {
		return Tool{}
	}
	return Tool{run: command{path: path}, name: path}
}

// NewWith builds a tool over an injected runner, for tests.
func NewWith(r Runner) Tool { return Tool{run: r, name: "anno"} }

// Available reports whether anno can be asked at all.
func (t Tool) Available() bool { return t.run != nil }

// Name returns the binary the tool will run, for a diagnostic.
func (t Tool) Name() string { return t.name }

// Check asks whether a target resolves, reading only the status.
func (t Tool) Check(target string) Result {
	return t.ask(target, false)
}

// Read asks for a target's content.
func (t Tool) Read(target string) Result {
	return t.ask(target, true)
}

func (t Tool) ask(target string, wantContent bool) Result {
	if !t.Available() {
		return Result{Verdict: Unknown, Why: "anno is not on PATH"}
	}
	if strings.TrimSpace(target) == "" {
		return Result{Verdict: Unknown, Why: "empty target"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), Deadline)
	defer cancel()

	stdout, stderr, code, err := t.run.Run(ctx, "read", target)
	if err != nil {
		return Result{Verdict: Unknown, Why: "anno could not be run: " + err.Error()}
	}

	// anno's exit codes are the shared ones, so they map onto Dock's meanings
	// without translation — the payoff for one numbering across the tools.
	switch code {
	case fault.CodeOK:
		r := Result{Verdict: Exists}
		if wantContent {
			r.Content = stdout
		}
		return r
	case fault.CodeNotFound:
		return Result{Verdict: Missing, Why: firstLine(stderr, "anno found no such annotation")}
	case fault.CodeAmbiguous:
		return Result{
			Verdict:    Ambiguous,
			Why:        firstLine(stderr, "the target names more than one annotation"),
			Candidates: candidates(stderr),
		}
	case fault.CodeParse:
		// The *file* is malformed, not the link. That is a real problem, but it
		// is the code's problem and not this document's.
		return Result{Verdict: Unknown, Why: firstLine(stderr, "anno could not parse the file")}
	default:
		return Result{Verdict: Unknown, Why: firstLine(stderr, "anno exited "+itoa(code))}
	}
}

// firstLine takes the leading line of a diagnostic, so a report stays one line
// per fault, falling back when anno said nothing.
func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(strings.TrimSpace(s), "anno: ")
}

// candidates pulls the fully qualified alternatives out of an ambiguity
// diagnostic. anno lists them one per line, indented, and each is a valid
// target — which is what makes them worth carrying through.
func candidates(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(line, " ") {
			continue
		}
		// Drop anno's trailing line range, leaving the target itself.
		if i := strings.LastIndex(trimmed, " <"); i > 0 {
			trimmed = strings.TrimSpace(trimmed[:i])
		}
		out = append(out, trimmed)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// command runs the real binary.
type command struct{ path string }

// Run executes anno and separates its streams. A non-zero exit is not an error:
// it is anno's answer, and the codes are the contract.
func (c command) Run(ctx context.Context, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, c.path, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if ok := asExitError(err, &exit); ok {
			return stdout.String(), stderr.String(), exit.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), 0, fault.IO{Op: "run", Path: c.path, Err: err}
	}
	return stdout.String(), stderr.String(), 0, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
