// Package artifact promotes in-tree data assets to a first-class fact source
// (Tier AR, docs/static-architecture-target-plan.md SA.3).
//
// It has two halves, deliberately separated:
//
//   - Reader flattens one file format to leaves. A new format is one Reader and
//     nothing else — this is the L1-artifacts extension point.
//   - Gate decides whether a flattened artifact declares facts of a given kind,
//     by corroborating its leaves against a relation the graph already knows.
//     Generalised from linker.LoadSchemaURLTables' route-corroboration gate,
//     whose thresholds stay the defaults here.
package artifact

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Leaf is one scalar in a structured file, with the container path that reached
// it. Path is the key chain for object-like formats and the index chain for
// arrays; a reader MUST produce it deterministically (sorted keys, ascending
// indices) so two reads of the same bytes are byte-identical.
type Leaf struct {
	Path  []string
	Value string
	Line  int // 1-based; 0 when the format cannot report it
}

// Artifact is one data file flattened to its scalar leaves.
type Artifact struct {
	Service string
	File    string // repo-relative
	Format  string // "json" | "yaml" | "env" | ...
	Leaves  []Leaf
}

// Reader flattens one file format to leaves.
type Reader interface {
	// Exts returns the lower-case extensions (with leading dot) this reader
	// claims, e.g. []string{".yaml", ".yml"}.
	Exts() []string
	// Read parses src and returns the flattened artifact. A parse failure is an
	// error, not a panic; callers treat any error as "not an artifact".
	Read(file string, src []byte) (*Artifact, error)
}

var readers = map[string]Reader{}

// Register wires a Reader for each of its extensions. Call from package init.
func Register(r Reader) {
	for _, e := range r.Exts() {
		readers[strings.ToLower(e)] = r
	}
}

// Formats returns every registered format name (deduped extension stems),
// sorted — used by mapping loading to reject a mapping that names a format no
// reader provides.
func Formats() []string {
	seen := map[string]bool{}
	for ext := range readers {
		seen[strings.TrimPrefix(ext, ".")] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// HasReader reports whether some registered reader claims the given format stem
// (e.g. "yaml", "json").
func HasReader(format string) bool {
	format = strings.ToLower(format)
	for ext := range readers {
		if strings.TrimPrefix(ext, ".") == format {
			return true
		}
	}
	return false
}

// ReadFile dispatches on file extension and returns the flattened artifact.
// ok is false when no reader claims the extension or parsing fails.
func ReadFile(file string, src []byte) (a *Artifact, ok bool) {
	r, found := readers[strings.ToLower(filepath.Ext(file))]
	if !found {
		return nil, false
	}
	art, err := r.Read(file, src)
	if err != nil || art == nil {
		return nil, false
	}
	art.File = file
	if art.Format == "" {
		art.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(file)), ".")
	}
	return art, true
}

// flattenTree walks an arbitrary decoded JSON/YAML value recording every string
// leaf with its container chain. Objects sort their keys; arrays contribute the
// index as a chain element. Ported verbatim from linker.flattenSchema so the
// SA.3 refactor is a move, not a behaviour change.
func flattenTree(node interface{}, chain []string, out *[]Leaf) {
	switch v := node.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			flattenTree(v[k], appendChain(chain, k), out)
		}
	case map[interface{}]interface{}: // yaml.v3 non-string keys
		m := make(map[string]interface{}, len(v))
		keys := make([]string, 0, len(v))
		for k, val := range v {
			ks := fmt.Sprint(k)
			keys = append(keys, ks)
			m[ks] = val
		}
		sort.Strings(keys)
		for _, k := range keys {
			flattenTree(m[k], appendChain(chain, k), out)
		}
	case []interface{}:
		for i, item := range v {
			flattenTree(item, appendChain(chain, strconv.Itoa(i)), out)
		}
	case string:
		*out = append(*out, Leaf{Path: appendChain(chain, ""), Value: v})
	}
}

// appendChain returns chain + tail as a fresh slice (no aliasing across
// recursion). A "" tail means "no new element" — used for the leaf itself,
// whose key is already the last element of chain.
func appendChain(chain []string, tail string) []string {
	if tail == "" {
		cp := make([]string, len(chain))
		copy(cp, chain)
		return cp
	}
	cp := make([]string, len(chain)+1)
	copy(cp, chain)
	cp[len(chain)] = tail
	return cp
}
