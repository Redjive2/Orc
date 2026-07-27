package render

import (
	"path/filepath"
	"strings"

	"orc/dock/internal/style"
)

// The folders an overview passed through and had nothing to say about.
//
// An overview draws one table per document, so a folder holding no document
// appears nowhere — which makes a folder somebody has just made indistinguishable
// from one that does not exist. That is the moment it matters most: a new folder
// is exactly the thing whose absence from a listing reads as "it did not save".
//
// They are listed after the documents rather than among them, and they carry a
// reason each, because "empty" and "cannot be read" are different problems with
// different fixes and a listing that only said "nothing here" would hide the
// second behind the first.

// Folder is one such directory, already described. The reason is a string rather
// than a code so this package stays free of the walker's vocabulary; root.Why is
// where the words are decided.
type Folder struct {
	Path string
	Why  string
}

// Folders draws the list, relative to base, or "" when there is nothing to say.
func Folders(base string, folders []Folder, pal style.Palette) string {
	if len(folders) == 0 {
		return ""
	}

	rows := make([][2]string, 0, len(folders))
	width := 0
	for _, f := range folders {
		name := relative(base, f.Path)
		if style.Width(name) > width {
			width = style.Width(name)
		}
		rows = append(rows, [2]string{name, f.Why})
	}

	var b strings.Builder
	b.WriteString(pal.Paint(plural2(len(folders), "folder", "folders")+" with nothing to show", style.Quiet))
	b.WriteByte('\n')
	for _, r := range rows {
		b.WriteString("  ")
		b.WriteString(r[0])
		b.WriteString(strings.Repeat(" ", width-style.Width(r[0])+2))
		// The reason is quiet: it is a remark about a folder, not a fault in it.
		// An unreadable one is the exception, and it says so in words rather than
		// in colour — colour is a layer here and never the information.
		b.WriteString(pal.Paint(r[1], style.Quiet))
		b.WriteByte('\n')
	}
	return b.String()
}

// relative shortens a path against the tree being shown, leaving it alone when it
// is not under it — a path that cannot be made relative is still a real path, and
// printing an error's worth of `../..` would be worse than printing it whole.
func relative(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	if rel == "." {
		// The tree being listed is itself the barren one. Its own name reads
		// better than the absolute path it was named by, which on a deep checkout
		// is a line of directories nobody asked about.
		return filepath.Base(strings.TrimSuffix(path, string(filepath.Separator)))
	}
	return rel
}
