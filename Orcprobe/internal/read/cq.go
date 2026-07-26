package read

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/fault"
)

// Communiqué's local state, from its plan §5.1.
const (
	cqAppliedFile = "applied.jsonl"
	cqCursorFile  = "cursor.json"
)

// Sync is a decoded cq state.
//
// There is deliberately no "how stale is this" here. A probe never syncs, so
// the only interesting facts are how much had been applied when the snapshot
// was taken and whether the cursor survived the scrub — which, in a neutered
// probe, it should not have.
type Sync struct {
	Present bool
	// Applied is how many of the user's actions had been carried out.
	Applied int
	// Cursor reports whether a sync watermark is still present. In a neutered
	// probe this is false, and a true here is worth seeing.
	Cursor bool
	Damage []Damage
}

// CQ decodes a copied Communiqué state.
func CQ(root string) (Sync, error) {
	var out Sync

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fault.IO{Op: "look at", Path: root, Err: err}
	}
	if !info.IsDir() {
		return out, fault.Conflict{Path: root, Reason: "is not a cq state directory"}
	}
	out.Present = true

	if _, err := os.Stat(filepath.Join(root, cqCursorFile)); err == nil {
		out.Cursor = true
	}

	path := filepath.Join(root, cqAppliedFile)
	lines, complete, err := readLines(path)
	if err != nil {
		return out, err
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			if i == len(lines)-1 && !complete {
				break
			}
			out.Damage = append(out.Damage, Damage{Path: path, Why: "an applied-action line does not parse"})
			continue
		}
		out.Applied++
	}
	return out, nil
}
