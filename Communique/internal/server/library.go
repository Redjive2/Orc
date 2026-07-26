package server

import (
	"net/http"
	"strings"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// The library, served in two pieces.
//
// The tree is structure and never text: a repository is megabytes, and a browser
// that had to receive all of it to draw a list of filenames would be unusable on
// the connection this site exists to be usable on. One file's text is a second
// request, made when a reader opens it.
//
// That split is also what makes folding honest. The interface can show every
// file and section without having fetched any of them, so what is on screen is
// the whole repository rather than the part that happened to be downloaded.

// treeEntry is one file as the tree lists it: enough to draw and fold, no text.
type treeEntry struct {
	Path        string                `json:"path"`
	Machine     protocol.MachineID    `json:"machine"`
	Lines       int                   `json:"lines"`
	Bytes       int                   `json:"bytes"`
	Sections    []protocol.Section    `json:"sections,omitempty"`
	Annotations []protocol.Annotation `json:"annotations,omitempty"`
	Skipped     string                `json:"skipped,omitempty"`
}

type treeView struct {
	Root      string      `json:"root"`
	Files     []treeEntry `json:"files"`
	Truncated string      `json:"truncated,omitempty"`
	// Notes say what went wrong collecting this, in the reader's terms. Without
	// them an uninstalled `dock` looks exactly like a repository whose documents
	// have no sections, which is a claim about the operator's files rather than
	// about their setup.
	Notes    []string `json:"notes,omitempty"`
	Machines []string `json:"machines,omitempty"`
}

// library lists every file the mirrored machines carry, without their text.
func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	view := treeView{Files: []treeEntry{}}
	for _, id := range ids {
		snap, _, err := s.snapshot(id)
		if err != nil {
			s.log.Warn("skipping unreadable machine", "machine", id, "error", err)
			continue
		}
		if snap.Library == nil {
			continue
		}
		view.Machines = append(view.Machines, string(id))
		if view.Root == "" {
			view.Root = snap.Library.Root
		}
		if snap.Library.Truncated != "" {
			view.Truncated = snap.Library.Truncated
		}
		view.Notes = append(view.Notes, snap.Library.Notes...)
		for _, f := range snap.Library.Files {
			view.Files = append(view.Files, treeEntry{
				Path: f.Path, Machine: id, Lines: f.Lines, Bytes: f.Bytes,
				Sections: f.Sections, Annotations: f.Annotations, Skipped: f.Skipped,
			})
		}
	}
	s.ok(w, r, view)
}

// fileView is one file, with its text.
type fileView struct {
	Path        string                `json:"path"`
	Machine     protocol.MachineID    `json:"machine"`
	Lines       int                   `json:"lines"`
	Bytes       int                   `json:"bytes"`
	Text        string                `json:"text"`
	Sections    []protocol.Section    `json:"sections,omitempty"`
	Annotations []protocol.Annotation `json:"annotations,omitempty"`
	Skipped     string                `json:"skipped,omitempty"`
}

// libraryFile returns one file's text.
func (s *Server) libraryFile(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("path")
	if want == "" {
		s.fail(w, r, fault.Usage{Reason: "no path given"})
		return
	}
	// The path is matched against what a snapshot carried, never used to reach
	// the filesystem — the server has no repository of its own. Even so it is
	// checked, because a path that climbs out of a tree is a bug or an attempt
	// and neither should be answered.
	if strings.HasPrefix(want, "/") || strings.Contains(want, "..") {
		s.fail(w, r, fault.Usage{Reason: "a path may not be absolute or climb out of the tree"})
		return
	}

	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	only := r.URL.Query().Get("machine")

	for _, id := range ids {
		if only != "" && string(id) != only {
			continue
		}
		snap, _, err := s.snapshot(id)
		if err != nil {
			continue
		}
		if snap.Library == nil {
			continue
		}
		for _, f := range snap.Library.Files {
			if f.Path != want {
				continue
			}
			s.ok(w, r, fileView{
				Path: f.Path, Machine: id, Lines: f.Lines, Bytes: f.Bytes,
				Text: f.Text, Sections: f.Sections, Annotations: f.Annotations,
				Skipped: f.Skipped,
			})
			return
		}
	}
	s.fail(w, r, fault.NotFound{What: "file", Name: want})
}

// The library verbs, queued the way every other action is.
//
// Nothing here writes anything: the server has no checkout, and an edit made in
// a browser is a request that leaves on the next sync. The path is checked all
// the same, because the action carries it across a wire to a machine that does
// have one — and a path that climbs out of a tree is a bug or an attempt
// whichever end notices it first.

type editBody struct {
	Machine string `json:"machine,omitempty"`
	Path    string `json:"path"`
	Text    string `json:"text,omitempty"`
	Base    string `json:"base,omitempty"`
}

// Validate refuses a bad path here rather than only when the action it becomes
// is validated, so the message names the request the caller actually made.
//
// The operands each verb requires are not checked here — that is argRules' job,
// and duplicating it would be two tables to keep in step.
func (b editBody) Validate() error {
	if err := protocol.CheckPath("path", b.Path); err != nil {
		return err
	}
	return protocol.CheckText("text", b.Text, protocol.MaxFileBytes)
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request, op protocol.Op) {
	var body editBody
	if err := decode(r, MaxRequestBytes+protocol.MaxFileBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	s.enqueue(w, r, body.Machine, op, protocol.Args{
		Path: body.Path, Text: body.Text, Base: body.Base,
	})
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request)  { s.edit(w, r, protocol.OpWrite) }
func (s *Server) createFile(w http.ResponseWriter, r *http.Request) { s.edit(w, r, protocol.OpCreate) }
func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) { s.edit(w, r, protocol.OpDelete) }
func (s *Server) makeDir(w http.ResponseWriter, r *http.Request)    { s.edit(w, r, protocol.OpMakeDir) }
func (s *Server) removeDir(w http.ResponseWriter, r *http.Request) {
	s.edit(w, r, protocol.OpRemoveDir)
}
