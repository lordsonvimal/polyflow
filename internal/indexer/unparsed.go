package indexer

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// assetExts are file classes that carry no code flow by nature. Anything
// NOT in this list and not parseable is a reportable blind spot.
var assetExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".ico": true, ".webp": true, ".woff": true, ".woff2": true, ".ttf": true,
	".eot": true, ".otf": true, ".mp3": true, ".mp4": true, ".webm": true,
	".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".map": true,
	".lock": true, ".sum": true, ".mod": true, ".toml": true, ".ini": true,
	".env": true, ".example": true, ".md": true, ".txt": true,
	".json": true, ".yaml": true, ".yml": true,
}

// NOTE: .json/.yaml/.yml move OUT of this list the moment a plan gives them a
// reader (plan 4 K.1/K.2 for yaml, plan 5 Q.2 for json IaC). Each removal is
// part of that plan's phase, with this comment updated. .css/.scss left in
// Tier K.5, which gave them internal/parser/scss.go.

// unparsedKey returns the extension or basename for extensionless files.
func unparsedKey(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return filepath.Base(path)
	}
	return ext
}

// serializeUnparsed serializes per-service unparsed counts to deterministic
// JSON (bug-class rule 2). The meta key is always written; value is {} when clean.
// encoding/json sorts map keys alphabetically, satisfying the (service,ext) order.
func serializeUnparsed(counts map[string]map[string]int) string {
	b, _ := json.Marshal(counts)
	return string(b)
}

// UnparsedSummary returns the total unparsed count and a comma-joined list of
// the top-3 extensions (sorted) with per-extension counts for status/doctor output.
func UnparsedSummary(exts map[string]int) (int, string) {
	total := 0
	keys := make([]string, 0, len(exts))
	for k, v := range exts {
		total += v
		keys = append(keys, k)
	}
	sort.Strings(keys)
	top := keys
	if len(top) > 3 {
		top = top[:3]
	}
	parts := make([]string, 0, len(top))
	for _, k := range top {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, exts[k]))
	}
	return total, strings.Join(parts, ", ")
}
