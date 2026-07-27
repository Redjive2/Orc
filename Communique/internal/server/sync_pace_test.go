package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// How often the mirror syncs, set from the browser.
//
// It is the one setting in this feature the *server* owns. Wake and tend belong to
// the fleet and are queued to the machine that runs it; sync is about the link
// between the two, and the browser can only reach this end — so the value lives
// here and rides back on each response.

func setPace(t *testing.T, h *harness, body string) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrf := h.login(t)
	return h.do(t, "POST", "/api/v1/sync/pace", body, h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
}

func readPace(t *testing.T, h *harness) map[string]any {
	t.Helper()
	cookie, _ := h.login(t)
	res := h.do(t, "GET", "/api/v1/sync/pace", "", h.withCookie(cookie))
	if res.Code != http.StatusOK {
		t.Fatalf("reading the pace: %d %s", res.Code, res.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return got
}

func TestTheSyncPaceIsSetAndReadBack(t *testing.T) {
	h := newHarness(t)

	if res := setPace(t, h, `{"watch":"45s"}`); res.Code != http.StatusOK {
		t.Fatalf("setting: %d %s", res.Code, res.Body)
	}
	if got := readPace(t, h)["watch"]; got != "45s" {
		t.Errorf("the pace read back as %v", got)
	}
}

// Nothing takes effect until a watcher asks, and the answer says so rather than
// reporting a change on machines it has not spoken to.
func TestSettingThePaceSaysWhenItLands(t *testing.T) {
	h := newHarness(t)
	res := setPace(t, h, `{"watch":"30s"}`)

	var got map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	note, _ := got["note"].(string)
	if note == "" {
		t.Error("the answer claims a change without saying when it reaches a machine")
	}
}

// A floor, because a mirror that syncs every second is a machine spending its time
// telling another machine what it has not done.
func TestABusyWaitIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{`{"watch":"1s"}`, `{"watch":"soon"}`, `{"watch":"9000h"}`} {
		if res := setPace(t, h, bad); res.Code == http.StatusOK {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// Clearing it means "whatever each watcher was told on its own command line",
// which is what a fleet had before this existed.
func TestClearingThePaceLeavesWatchersAlone(t *testing.T) {
	h := newHarness(t)
	setPace(t, h, `{"watch":"45s"}`)

	if res := setPace(t, h, `{"watch":""}`); res.Code != http.StatusOK {
		t.Fatalf("clearing: %d %s", res.Code, res.Body)
	}
	if got := readPace(t, h)["watch"]; got != "" {
		t.Errorf("the pace is still %v after clearing", got)
	}
}

// The floor travels with the answer, so a form can say what it will take rather
// than making somebody discover it by being refused.
func TestTheFloorIsTold(t *testing.T) {
	if got := readPace(t, newHarness(t))["floor"]; got == "" || got == nil {
		t.Error("the answer does not say what the shortest interval is")
	}
}
