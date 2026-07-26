package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/macmuffin/internal/task"
)

// binding is the on-disk record tying a worktree to a task.
type binding struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
	Main    string `json:"main"`
	Task    string `json:"task"`
	At      string `json:"at"`
}

// Binding is a resolved worktree binding.
type Binding struct {
	Path string
	Main string
	Task task.Name
}

// bindingPath keys a binding by the hash of its resolved absolute path.
//
// Hashed rather than escaped, so the hook's lookup is one stat and one read
// rather than a scan — and so a path containing a separator, a newline, or
// anything else awkward cannot become a filename that means something different
// from the path it stands for.
func (s *Store) bindingPath(worktree string) string {
	sum := sha256.Sum256([]byte(canonical(worktree)))
	return filepath.Join(s.root, worktreesDir, hex.EncodeToString(sum[:])+".json")
}

// Canonical is canonical, for callers outside the store who have to compare a path
// against a binding's. Two spellings of one directory — /tmp and /private/tmp, most
// often — are the same binding, and a caller that compared them literally would
// rewrite every binding it looked at.
func Canonical(path string) string { return canonical(path) }

// canonical resolves a path to the one spelling every caller will agree on.
//
// Symlinks are followed, because the hook looks a directory up by the root
// `repo` resolved for it, and a binding stored under an unresolved spelling of
// the same directory would simply never be found — on macOS, where /tmp and
// /var are symlinks, that is every binding made through either of them.
//
// When the path is not there, the nearest existing ancestor is resolved and the
// rest appended. That keeps a lookup stable after the directory is deleted,
// which is exactly when `Unbind` needs to find the binding.
func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}

	rest := ""
	for at := abs; ; {
		if resolved, err := filepath.EvalSymlinks(at); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(at)
		if parent == at {
			return abs
		}
		rest = filepath.Join(filepath.Base(at), rest)
		at = parent
	}
}

// Bind ties a worktree to a task.
//
// It refuses a worktree already bound to a different *active* task: an
// ambiguous directory-to-task lookup would silently enforce the wrong scope,
// which is worse than refusing to bind. Rebinding a worktree whose task is
// finished is allowed, because that is how a worktree gets reused.
func (s *Store) Bind(name task.Name, worktree, main string) error {
	if name.Zero() {
		return fault.Internal{Where: "store.Bind", Detail: "no task named"}
	}
	if strings.TrimSpace(worktree) == "" {
		return fault.Internal{Where: "store.Bind", Detail: "no worktree given"}
	}

	existing, found, err := s.Bound(worktree)
	if err != nil {
		return err
	}
	if found && !existing.Task.Equal(name) {
		holder, err := s.Load(existing.Task)
		switch {
		case err == nil && !holder.Completed():
			return fault.Conflict{Path: worktree, Reason: fmt.Sprintf(
				"already bound to %s; unbind it or use another worktree", existing.Task)}
		case err != nil && !isNotFound(err):
			return err
		}
		// The holder is finished or gone, so the worktree is free again.
	}

	// The record stores the canonical spelling, because that is what it is
	// filed under and what every lookup will ask with. Keeping the caller's
	// spelling would make the file disagree with its own name.
	data, err := json.MarshalIndent(binding{
		Version: Version,
		Path:    canonical(worktree),
		Main:    canonical(main),
		Task:    name.String(),
		At:      clock.Format(s.clock.Now()),
	}, "", "  ")
	if err != nil {
		return fault.Internal{Where: "store.Bind", Detail: err.Error()}
	}
	return s.writeFile(s.bindingPath(worktree), append(data, '\n'))
}

// Bound reports which task a worktree is bound to, if any.
func (s *Store) Bound(worktree string) (Binding, bool, error) {
	if strings.TrimSpace(worktree) == "" {
		return Binding{}, false, fault.Internal{Where: "store.Bound", Detail: "no worktree given"}
	}
	path := s.bindingPath(worktree)

	got, err := s.readBinding(path)
	if err != nil {
		if isNotFound(err) {
			return Binding{}, false, nil
		}
		return Binding{}, false, err
	}
	// The file is keyed by the hash of its own path; a binding whose content
	// says otherwise was copied rather than written, and must not answer for a
	// worktree it does not describe.
	if canonical(got.Path) != canonical(worktree) {
		return Binding{}, false, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"binding is filed under %s but describes %s", worktree, got.Path)}
	}
	return got, true, nil
}

// readBinding decodes one binding file. A missing file is reported as NotFound
// so a caller can tell "no binding" from "a binding that will not read".
func (s *Store) readBinding(path string) (Binding, error) {
	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Binding{}, fault.NotFound{Target: path}
		}
		return Binding{}, fault.IO{Op: "read", Path: path, Err: err}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b binding
	if err := dec.Decode(&b); err != nil {
		return Binding{}, fault.Parse{Path: path, Reason: "worktree binding: " + err.Error()}
	}
	if dec.More() {
		return Binding{}, fault.Parse{Path: path, Reason: "worktree binding has trailing content"}
	}
	if b.Version != Version {
		return Binding{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"worktree binding is version %d, this macmuffin writes version %d", b.Version, Version)}
	}

	name, err := task.ParseName(b.Task)
	if err != nil {
		return Binding{}, fault.Parse{Path: path, Reason: "worktree binding names a bad task: " + err.Error()}
	}
	if strings.TrimSpace(b.Path) == "" {
		return Binding{}, fault.Parse{Path: path, Reason: "worktree binding names no path"}
	}

	return Binding{Path: b.Path, Main: b.Main, Task: name}, nil
}

// Unbind removes a worktree binding. It is not an error if there was none.
func (s *Store) Unbind(worktree string) error {
	if strings.TrimSpace(worktree) == "" {
		return fault.Internal{Where: "store.Unbind", Detail: "no worktree given"}
	}
	if err := s.ops.remove(s.bindingPath(worktree)); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove the binding for", Path: worktree, Err: err}
	}
	return nil
}

// Bindings lists every worktree binding, and names the files that would not
// decode.
//
// Unreadable entries are returned rather than raised, because the only caller is
// `verify`, whose whole job is to report damage rather than trip over it.
func (s *Store) Bindings() ([]Binding, []string, error) {
	dir := filepath.Join(s.root, worktreesDir)

	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	var (
		out     []Binding
		damaged []string
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		got, err := s.readBinding(filepath.Join(dir, e.Name()))
		if err != nil {
			damaged = append(damaged, e.Name())
			continue
		}
		out = append(out, got)
	}
	slices.SortFunc(out, func(a, b Binding) int { return strings.Compare(a.Path, b.Path) })
	return out, damaged, nil
}
