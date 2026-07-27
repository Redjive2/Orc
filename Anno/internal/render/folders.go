package render

import (
	"strings"

	"orc/anno/internal/style"
)

// The folders beside the files an overview drew.
//
// An overview is a tree per annotated file, so a folder is visible in it only
// through what it holds. One holding nothing anno reads — or nothing at all —
// appears nowhere, which makes a folder somebody has just made look exactly like
// a folder that was never created.
//
// They are listed after the trees, with what each holds, because "empty" and
// "cannot be read" are different problems and a listing that showed neither would
// hide both behind the same silence.

// Folder is one directory beside the files, already described.
type Folder struct {
	Name string
	Why  string
}

// Folders draws the list, or "" when there are none.
func Folders(folders []Folder, p style.Palette) string {
	if len(folders) == 0 {
		return ""
	}

	wide := 0
	for _, f := range folders {
		if w := width(f.Name); w > wide {
			wide = w
		}
	}

	var b strings.Builder
	b.WriteString(p.Paint("folders here", style.Quiet))
	b.WriteByte('\n')
	for _, f := range folders {
		b.WriteString("  ")
		b.WriteString(f.Name)
		b.WriteString(strings.Repeat(" ", wide-width(f.Name)+2))
		// Quiet: what a folder holds is a remark about it, not a fault in it.
		// The words carry the meaning; the colour is only a layer over them.
		b.WriteString(p.Paint(f.Why, style.Quiet))
		b.WriteByte('\n')
	}
	return b.String()
}
