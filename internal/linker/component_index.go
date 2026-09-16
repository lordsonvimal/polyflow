package linker

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// component_index.go is what remains of the retired
// internal/linker/rails_views.go (Tier FX FX.8.33, 2026-09-16 — the rest of
// LinkRailsViews moved to internal/factpipe/hub_rails_views.go). componentIndex/
// newComponentIndex survive because internal/linker/valuegraph_adapter.go
// still calls newComponentIndex(nodes, svc) directly for its own per-service
// owner inference — a second, still-active caller, same "grep before
// deleting a shared helper" discipline as js_prop_client_shared.go and
// ruby_host_registry.go.

// componentIndex resolves a data-react-class name to the JSX that implements it.
//
// The authority is the Tier Z global registry (`window.Foo = Foo`), not the
// helper's `containers/<Name>.jsx` path convention, because the convention is
// only the *dev-mode* script tag. orion mounts LinkIcon and OnboardingTip
// from app/javascript/components/, which the path rule cannot reach and the
// registry can.
type componentIndex struct {
	bySymbol map[string][]string
	// viaBarrel[name] is set when resolveBarrels (internal/factpipe/
	// hub_rails_views.go's rvComponentIndex.resolveBarrels) rewrote name's
	// resolution from a re-export variable to the real component in the
	// imported file. valuegraph_adapter.go's own caller reads bySymbol
	// directly and never calls resolveBarrels, so this field is unused here
	// but kept for shape parity with the FX hub's ported copy.
	viaBarrel map[string]bool
}

// newComponentIndex builds the data-react-class → JSX map. Passing one or more
// service names restricts the scan to those services; passing none scans every
// service — the mount lives in the Rails service but the component it names is
// almost always in a sibling JS service (orion splits app/javascript into its
// own `js` service), so the cross-service (no-filter) form is what wires
// `react_component` to its implementation.
func newComponentIndex(nodes []graph.Node, svcs ...string) *componentIndex {
	svcSet := map[string]bool{}
	for _, s := range svcs {
		svcSet[s] = true
	}
	// symbol → files that register it, and (file,label) → implementation node.
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

	idx := &componentIndex{bySymbol: map[string][]string{}, viaBarrel: map[string]bool{}}
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
