package cli

import (
	"encoding/json"

	"orc/common/fault"
	"orc/dock/internal/render"
)

// The `--json` projection of an index.
//
// It exists so another tool can read a document's structure without parsing the
// box-drawn table: a presentation format is a bad contract, and Communiqué needs
// a good one to mirror this repository to the web.
//
// Two rules keep it usable as a contract:
//
//   - It is a projection of the same render.Index the table draws, so JSON and
//     the table can never disagree about what a document contains.
//   - Fields are added, never repurposed or removed. A reader that ignores what
//     it does not recognise keeps working across a version of Dock it has not
//     seen.

// jsonDoc is one document: its path, and the sections in it.
type jsonDoc struct {
	Path     string        `json:"path"`
	Lines    int           `json:"lines"`
	Sections []jsonSection `json:"sections"`
}

// jsonSection is one section of one document.
//
// Depth is carried explicitly even though it is derivable from the number,
// because a reader building a fold tree needs the nesting and should not have to
// re-implement Dock's rule about `#`s matching dotted components to get it.
type jsonSection struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	Depth  int    `json:"depth"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Lines  int    `json:"lines"`
	Out    int    `json:"out"`
	// In is how many sections link here, or null when it is unknown — which is
	// what `index` on a single file reports, since counting inbound links means
	// reading the whole tree around it. A number and "not counted" are different
	// answers and must not both be zero.
	In *int `json:"in,omitempty"`
}

// indexJSON projects one document's index.
func indexJSON(path string, ix render.Index) jsonDoc {
	out := jsonDoc{Path: path, Sections: []jsonSection{}}
	for _, row := range ix.Rows() {
		if row.Depth == 0 {
			// The file row: it names the document and its whole length rather
			// than a section.
			out.Lines = row.Span.Len()
			continue
		}
		section := jsonSection{
			Number: row.Number,
			Name:   row.Name,
			Depth:  row.Depth,
			Start:  row.Span.Start(),
			End:    row.Span.End(),
			Lines:  row.Span.Len(),
			Out:    row.Counts.Out,
		}
		if row.Counts.In != render.Unknown {
			in := row.Counts.In
			section.In = &in
		}
		out.Sections = append(out.Sections, section)
	}
	return out
}

// emitJSON writes a value as indented JSON.
func (a App) emitJSON(v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.Internal{Where: "cli.emitJSON", Detail: err.Error()}
	}
	return a.say(string(body))
}
