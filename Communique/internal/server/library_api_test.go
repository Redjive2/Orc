package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"orc/cq/internal/protocol"
)

// The library is served in two pieces: structure without text, then one file at
// a time. That split is what lets a browser list a repository of any size, so
// most of what is worth testing is that the pieces stay separate.

func libSnapshot(machine protocol.MachineID) protocol.Snapshot {
	snap := sampleSnapshot(machine)
	snap.Library = &protocol.Library{
		Root: "Orc",
		Files: []protocol.File{
			{
				Path: "Docs/Vision.md", Lines: 3, Bytes: 19, Text: "# §1 Thing\n\nprose\n",
				Sections: []protocol.Section{
					{Number: "1", Name: "Thing", Depth: 1, Start: 1, End: 3, Lines: 3},
				},
			},
			{
				Path: "internal/app.go", Lines: 3, Bytes: 28, Text: "package app\n\nfunc Main() {}\n",
				Annotations: []protocol.Annotation{
					{Kind: "section", Name: "code", Start: 1, End: 3, Lines: 3, ContentStart: 1, ContentEnd: 3},
				},
			},
			{Path: "big/generated.go", Bytes: 9_000_000, Skipped: "it is larger than the limit"},
		},
	}
	return snap
}

func putLibrary(t *testing.T, h *harness, machine protocol.MachineID) {
	t.Helper()
	req := protocol.SyncRequest{
		Protocol: protocol.Version, Agent: "cq/test", SentAt: at,
		Snapshot: libSnapshot(machine),
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if w := h.do(t, "POST", "/api/v1/sync", string(body), h.withToken()); w.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
}

// TestTheTreeCarriesNoText is the property the whole design rests on. A tree
// that included file contents would make the first page load the size of the
// repository, which is the thing this exists to avoid.
func TestTheTreeCarriesNoText(t *testing.T) {
	h := newHarness(t)
	putLibrary(t, h, "studio")
	cookie, _ := h.login(t)

	w := h.do(t, "GET", "/api/v1/library", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "package app") || strings.Contains(body, "prose") {
		t.Errorf("the tree leaked file text:\n%s", body)
	}

	var view struct {
		Root  string `json:"root"`
		Files []struct {
			Path        string             `json:"path"`
			Sections    []protocol.Section `json:"sections"`
			Annotations []json.RawMessage  `json:"annotations"`
			Skipped     string             `json:"skipped"`
		} `json:"files"`
	}
	decodeInto(t, w.Body.Bytes(), &view)
	if view.Root != "Orc" || len(view.Files) != 3 {
		t.Fatalf("view = %+v", view)
	}
	// Structure is what the tree is *for*, so it must be there.
	var sections, annotations, skipped int
	for _, f := range view.Files {
		sections += len(f.Sections)
		annotations += len(f.Annotations)
		if f.Skipped != "" {
			skipped++
		}
	}
	if sections != 1 || annotations != 1 || skipped != 1 {
		t.Errorf("structure missing: %d sections, %d annotations, %d skipped", sections, annotations, skipped)
	}
}

func TestOneFileComesWithItsText(t *testing.T) {
	h := newHarness(t)
	putLibrary(t, h, "studio")
	cookie, _ := h.login(t)

	w := h.do(t, "GET", "/api/v1/library/file?path=internal%2Fapp.go", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var file struct {
		Path        string                `json:"path"`
		Text        string                `json:"text"`
		Annotations []protocol.Annotation `json:"annotations"`
	}
	decodeInto(t, w.Body.Bytes(), &file)
	if !strings.Contains(file.Text, "package app") {
		t.Errorf("text = %q", file.Text)
	}
	if len(file.Annotations) != 1 {
		t.Errorf("annotations = %+v", file.Annotations)
	}
}

// TestAPathMayNotClimbOut: the server has no repository of its own and only
// matches against what a snapshot carried, but a path that climbs out of a tree
// is a bug or an attempt and neither should be answered.
func TestAPathMayNotClimbOut(t *testing.T) {
	h := newHarness(t)
	putLibrary(t, h, "studio")
	cookie, _ := h.login(t)

	for _, path := range []string{"..%2F..%2Fetc%2Fpasswd", "%2Fetc%2Fpasswd", "Docs%2F..%2F..%2Fsecret"} {
		w := h.do(t, "GET", "/api/v1/library/file?path="+path, "", h.withCookie(cookie))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want a refusal\n%s", path, w.Code, w.Body)
		}
	}
}

func TestAskingForNothingAndForWhatIsNotThere(t *testing.T) {
	h := newHarness(t)
	putLibrary(t, h, "studio")
	cookie, _ := h.login(t)

	if w := h.do(t, "GET", "/api/v1/library/file", "", h.withCookie(cookie)); w.Code != http.StatusBadRequest {
		t.Errorf("no path answered %d", w.Code)
	}
	if w := h.do(t, "GET", "/api/v1/library/file?path=nope.go", "", h.withCookie(cookie)); w.Code != http.StatusNotFound {
		t.Errorf("an unknown file answered %d", w.Code)
	}
}

// A machine mirroring no repository is the ordinary case, and it must read as an
// empty library rather than an error.
func TestAMachineWithNoLibraryIsNotAnError(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio") // no library in it
	cookie, _ := h.login(t)

	w := h.do(t, "GET", "/api/v1/library", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var view struct {
		Files []json.RawMessage `json:"files"`
	}
	decodeInto(t, w.Body.Bytes(), &view)
	if len(view.Files) != 0 {
		t.Errorf("files = %v, want none", view.Files)
	}
}

func TestTheLibraryNeedsASession(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/v1/library", "/api/v1/library/file?path=a.go"} {
		if w := h.do(t, "GET", path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a session", path, w.Code)
		}
	}
}
