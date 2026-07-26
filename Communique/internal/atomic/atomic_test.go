package atomic_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
)

func TestWriteFileReplacesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	if err := atomic.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := atomic.WriteFile(path, []byte("second"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", info.Mode().Perm())
	}
	assertNoDebris(t, dir, 1)
}

// TestReaderNeverSeesAPartialFile is the property the whole package exists for.
// A reader running throughout a burst of writes must only ever observe a value
// some writer actually wrote.
func TestReaderNeverSeesAPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	values := []string{
		strings.Repeat("a", 1<<16),
		strings.Repeat("b", 1<<16),
		strings.Repeat("c", 1<<16),
	}
	if err := atomic.WriteFile(path, []byte(values[0]), 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if err := atomic.WriteFile(path, []byte(values[i%len(values)]), 0o600); err != nil {
				t.Errorf("WriteFile: %v", err)
				break
			}
		}
		close(stop)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("ReadFile: %v", err)
				return
			}
			whole := false
			for _, v := range values {
				if string(got) == v {
					whole = true
					break
				}
			}
			if !whole {
				t.Errorf("reader saw a partial file of %d bytes", len(got))
				return
			}
		}
	}()

	wg.Wait()
	assertNoDebris(t, dir, 1)
}

func TestWriteFileRejectsAnEmptyPath(t *testing.T) {
	if err := atomic.WriteFile("", nil, 0o600); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
}

func TestWriteFileLeavesTheOriginalOnFailure(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := atomic.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := atomic.WriteFile(path, []byte("replacement"), 0o600); !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("the original was disturbed: %q", got)
	}
}

func TestWriteAndReadJSON(t *testing.T) {
	type record struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "r.json")

	if err := atomic.WriteJSON(path, record{Name: "a", Count: 2}, 0o600); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got record
	if err := atomic.ReadJSON(path, &got); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if got.Name != "a" || got.Count != 2 {
		t.Errorf("got %+v", got)
	}

	// Readable with ordinary tools: indented, newline-terminated.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") || !strings.Contains(string(raw), "\n  ") {
		t.Errorf("file is not human-readable:\n%s", raw)
	}
}

func TestReadJSONRefusesUnknownFieldsAndExtraDocuments(t *testing.T) {
	type record struct {
		Name string `json:"name"`
	}
	dir := t.TempDir()

	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"name":"a","invented":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var got record
	err := atomic.ReadJSON(unknown, &got)
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "invented") {
		t.Errorf("message %q should name the unknown field", err)
	}

	two := filepath.Join(dir, "two.json")
	if err := os.WriteFile(two, []byte(`{"name":"a"} {"name":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = atomic.ReadJSON(two, &got)
	if !errors.Is(err, fault.ErrParse) || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("error = %v, want a complaint about the second document", err)
	}
}

func TestCreateJSONIsExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "once.json")

	if err := atomic.CreateJSON(path, map[string]int{"n": 1}, 0o600); err != nil {
		t.Fatalf("CreateJSON: %v", err)
	}
	err := atomic.CreateJSON(path, map[string]int{"n": 2}, 0o600)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("error = %v, want a conflict", err)
	}

	var got map[string]int
	if err := atomic.ReadJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got["n"] != 1 {
		t.Errorf("the second write clobbered the first: %v", got)
	}
	assertNoDebris(t, dir, 1)
}

// TestCreateJSONRacesToOneWinner is the exclusivity the queue's sequence
// allocation relies on.
func TestCreateJSONRacesToOneWinner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contested.json")

	const racers = 16
	var wg sync.WaitGroup
	won := make([]bool, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won[i] = atomic.CreateJSON(path, map[string]int{"who": i}, 0o600) == nil
		}()
	}
	wg.Wait()

	winners := 0
	for _, w := range won {
		if w {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d racers won, want exactly 1", winners)
	}
	assertNoDebris(t, dir, 1)
}

func TestReadFileRejections(t *testing.T) {
	dir := t.TempDir()

	if _, err := atomic.ReadFile(filepath.Join(dir, "missing")); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("a missing file should be not-found, got %v", err)
	}
	if _, err := atomic.ReadFile(dir); !errors.Is(err, fault.ErrIO) {
		t.Errorf("a directory should be an i/o fault, got %v", err)
	}

	if runtime.GOOS != "windows" {
		fifo := filepath.Join(dir, "pipe")
		if err := makeFIFO(fifo); err == nil {
			if _, err := atomic.ReadFile(fifo); !errors.Is(err, fault.ErrIO) {
				t.Errorf("a FIFO should be an i/o fault, got %v", err)
			}
		}
	}

	// Not on Windows: a mode of zero there clears the write bit and nothing
	// else, so the file stays perfectly readable and there is no unreadable
	// file to make. And not as root, who is not refused anything.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		secret := filepath.Join(dir, "secret")
		if err := os.WriteFile(secret, []byte("x"), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := atomic.ReadFile(secret); !errors.Is(err, fault.ErrIO) {
			t.Errorf("an unreadable file should be an i/o fault, got %v", err)
		}
	}
}

func TestReadJSONReportsAMissingFileAsNotFound(t *testing.T) {
	var into map[string]any
	err := atomic.ReadJSON(filepath.Join(t.TempDir(), "missing.json"), &into)
	if !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("error = %v, want not-found", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := atomic.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomic.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := atomic.Remove(path); err != nil {
		t.Errorf("removing an absent file should succeed, got %v", err)
	}
}

func TestMkdirAll(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	if err := atomic.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory was not created: %v", err)
	}

	// A path blocked by a file is an i/o fault, not a panic.
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomic.MkdirAll(filepath.Join(blocked, "under"), 0o700); !errors.Is(err, fault.ErrIO) {
		t.Errorf("error = %v, want an i/o fault", err)
	}
}

func TestWriteJSONRejectsUnencodableValues(t *testing.T) {
	dir := t.TempDir()
	err := atomic.WriteJSON(filepath.Join(dir, "f.json"), make(chan int), 0o600)
	if !errors.Is(err, fault.ErrIO) {
		t.Errorf("error = %v, want an i/o fault", err)
	}
	err = atomic.CreateJSON(filepath.Join(dir, "g.json"), make(chan int), 0o600)
	if !errors.Is(err, fault.ErrIO) {
		t.Errorf("error = %v, want an i/o fault", err)
	}
	assertNoDebris(t, dir, 0)
}

// assertNoDebris checks a directory holds exactly the files it should, so a
// failed write cannot leave a temporary file behind.
func assertNoDebris(t *testing.T, dir string, want int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != want {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %d entries %v, want %d", len(entries), names, want)
	}
}

// modeBitsBite reports whether this machine can genuinely deny the process
// access to a file it owns.
//
// Two machines cannot, and the tests that chmod something all need one that
// can. Root is
// refused nothing. Windows has no mode bit for this at all: os.Chmod there
// toggles the read-only attribute and leaves reading alone, and does not make a
// directory unwritable — so the failure these tests provoke simply does not
// happen, and the assertion would fail for the wrong reason.
func modeBitsBite() bool {
	return os.Geteuid() != 0 && runtime.GOOS != "windows"
}

// TestAReplaceThatIsBusyIsRetried is for Windows, where renaming over a file
// fails outright while anything else has it open — a virus scanner, the search
// indexer, an editor mid-save. Those last milliseconds. Losing somebody's edit
// to one would be a failure about nothing.
func TestAReplaceThatIsBusyIsRetried(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	var tries int
	ops := atomic.FakeOps()
	ops.SetRename(func(from, to string) error {
		tries++
		if tries < 3 {
			return errors.New("the file is in use by another process")
		}
		return os.Rename(from, to)
	})

	if err := atomic.WriteFileWith(path, []byte("written\n"), 0o600, ops); err != nil {
		t.Fatalf("a transient busy file was not waited out: %v", err)
	}
	if tries != 3 {
		t.Errorf("renamed %d times, want 3", tries)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "written\n" {
		t.Errorf("contents = %q", got)
	}
}

// A file that is really held is reported rather than waited out forever, and
// the temporary is still cleaned up.
func TestAReplaceThatKeepsFailingIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	ops := atomic.FakeOps()
	ops.SetRename(func(string, string) error { return errors.New("still in use") })

	err := atomic.WriteFileWith(path, []byte("x"), 0o600, ops)
	if !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
	assertNoDebris(t, dir, 0)
}
