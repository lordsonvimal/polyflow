// Package valuegraphfacts is the RC.1 leaf (docs/js-declarative-composition-cluster-plan.md):
// a factpipe-and-linker-importable adapter over internal/valuegraph, so a
// crossing-aware resolution no longer needs a private copy of the
// owner-index/engine-construction glue per pass. It resolves nothing itself
// — internal/valuegraph.Engine still does that, unchanged — it only builds
// the CrossSource and runs the engine over a caller-supplied site list.
package valuegraphfacts

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// componentIndex resolves a data-react-class name to the JSX that implements
// it. Ported from internal/linker/component_index.go (Tier FX FX.8.33's
// survivor) — that package cannot be imported here (linker -> patterns ->
// factpipe would cycle back through this package's eventual factpipe
// caller), so this is a second copy by the same "extract into a shared
// leaf" precedent internal/schemaurl/internal/jsast already used. Keep
// byte-identical to internal/linker/component_index.go's algorithm; a
// divergence here silently changes which owner a crossing joins on.
type componentIndex struct {
	bySymbol map[string][]string
}

// newComponentIndex builds the data-react-class -> JSX map, restricted to
// svcs (no filter scans every service).
func newComponentIndex(nodes []graph.Node, svcs ...string) *componentIndex {
	svcSet := map[string]bool{}
	for _, s := range svcs {
		svcSet[s] = true
	}
	// symbol -> files that register it, and (file,label) -> implementation node.
	regFiles := map[string][]string{}
	implID := map[string]string{}
	varID := map[string]string{}

	for i := range nodes {
		n := &nodes[i]
		if len(svcSet) > 0 && !svcSet[n.Service] {
			continue
		}
		if n.Meta["is_test"] == "true" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeClass:
			key := n.File + "\x00" + n.Label
			if _, dup := implID[key]; !dup {
				implID[key] = n.ID
			}
		case graph.NodeTypeVariable:
			sym := n.Meta["global_symbol"]
			if sym == "" || n.Meta["scope"] != "global" {
				continue
			}
			regFiles[sym] = append(regFiles[sym], n.File)
			varID[sym+"\x00"+n.File] = n.ID
		}
	}

	idx := &componentIndex{bySymbol: map[string][]string{}}
	for sym, fs := range regFiles {
		sort.Strings(fs)
		for _, f := range fs {
			// Prefer the declaration over the assignment: `window.Foo = Foo`
			// registers a function defined in the same file, and that function
			// is what a caller wants to trace into.
			if id, ok := implID[f+"\x00"+sym]; ok {
				idx.bySymbol[sym] = append(idx.bySymbol[sym], id)
			} else if id, ok := varID[sym+"\x00"+f]; ok {
				idx.bySymbol[sym] = append(idx.bySymbol[sym], id)
			}
		}
	}
	return idx
}

// ComponentIndex is a valuegraph.CrossSource built from the graph's own
// component index — no new dataflow, this is data the indexer already has
// (which files declare/implement which component). Build once per service
// with BuildComponentIndex and reuse across every Resolve call for that
// service.
type ComponentIndex struct {
	ownersByFile map[string][]string
	filesByOwner map[string][]string
}

// OwnersIn lists the owners defined in file. Implements valuegraph.CrossSource.
func (c *ComponentIndex) OwnersIn(file string) []string { return c.ownersByFile[file] }

// FilesForOwner lists the files defining owner. Implements valuegraph.CrossSource.
func (c *ComponentIndex) FilesForOwner(owner string) []string { return c.filesByOwner[owner] }

// BuildComponentIndex builds the per-service owner<->file index, from the
// graph's component nodes plus a Capitalized-top-level-declaration fallback
// (a component that only forwards a prop is often never registered on
// `window`) — same two sources internal/linker/valuegraph_adapter.go's
// newJSPropScope already combines, ported here so a factpipe caller can
// build the same index without depending on internal/linker.
func BuildComponentIndex(nodes []graph.Node, svc string) *ComponentIndex {
	c := &ComponentIndex{
		ownersByFile: map[string][]string{},
		filesByOwner: map[string][]string{},
	}
	nodeFile := map[string]string{}
	for i := range nodes {
		nodeFile[nodes[i].ID] = nodes[i].File
	}

	addOwner := func(owner, file string) {
		if owner == "" || file == "" {
			return
		}
		for _, have := range c.ownersByFile[file] {
			if have == owner {
				return
			}
		}
		c.ownersByFile[file] = append(c.ownersByFile[file], owner)
		c.filesByOwner[owner] = append(c.filesByOwner[owner], file)
	}

	ci := newComponentIndex(nodes, svc)
	for sym, ids := range ci.bySymbol {
		for _, id := range ids {
			addOwner(sym, nodeFile[id])
		}
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Service != svc {
			continue
		}
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeClass {
			continue
		}
		if l := n.Label; l != "" && l[0] >= 'A' && l[0] <= 'Z' {
			addOwner(l, n.File)
		}
	}

	for file := range c.ownersByFile {
		sort.Strings(c.ownersByFile[file])
	}
	for owner := range c.filesByOwner {
		sort.Strings(c.filesByOwner[owner])
	}
	return c
}
