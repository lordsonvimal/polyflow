package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// EnrichRouteGroups stamps the reconstructed full prefix path into
// http_handler route nodes that are declared inside a router group.
//
// This is the G.3 meta-enrichment pass: a contextual node-join that a pure
// normalizer cannot perform because the group→variable binding sits outside
// the route call (gin: `api := r.Group("/v1")` then `api.GET("/users", h)`;
// chi: `r.Route("/admin", func(r chi.Router) { r.Get("/users", h) })`).
//
// The pass operates on a copy of the input slice. Nodes are identified by
// meta["pattern"] so it is safe to call with the full allNodes set.
//
// Scope: variable-scoped groups within a function/file (including nesting).
// Groups passed across functions/files are NOT resolved here — affected routes
// surface as unresolved via the normal contract-engine unmatched policy.
func EnrichRouteGroups(nodes []graph.Node) []graph.Node {
	// Deep-copy the slice so callers' Meta maps are not mutated.
	enriched := make([]graph.Node, len(nodes))
	for i, n := range nodes {
		if n.Meta != nil {
			copied := make(map[string]string, len(n.Meta))
			for k, v := range n.Meta {
				copied[k] = v
			}
			n.Meta = copied
		}
		enriched[i] = n
	}

	// ── Collect group descriptors grouped by file ──────────────────────────

	type ginGroup struct {
		varName  string // variable assigned: `api` in `api := r.Group(...)`
		prefix   string // the path prefix literal
		receiver string // parent router variable: `r` in `api := r.Group(...)`
		line     int    // declared at this line (for same-scope filtering)
	}
	type chiGroup struct {
		prefix  string // path prefix literal
		line    int    // call expression start line
		endLine int    // func_literal end line (from meta["end_line"])
	}

	ginGroupsByFile := map[string][]ginGroup{}
	chiGroupsByFile := map[string][]chiGroup{}

	for i := range enriched {
		n := &enriched[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		pat := n.Meta["pattern"]
		switch {
		case strings.HasPrefix(pat, "gin_route_group"):
			vn := n.Meta["var_name"]
			pfx := n.Meta["prefix"]
			if vn == "" || pfx == "" {
				continue
			}
			ginGroupsByFile[n.File] = append(ginGroupsByFile[n.File], ginGroup{
				varName:  vn,
				prefix:   pfx,
				receiver: n.Meta["receiver"],
				line:     n.Line,
			})
		case strings.HasPrefix(pat, "chi_route_group"):
			pfx := n.Meta["prefix"]
			if pfx == "" {
				continue
			}
			endLine := 0
			if v := n.Meta["end_line"]; v != "" {
				fmt.Sscanf(v, "%d", &endLine)
			}
			chiGroupsByFile[n.File] = append(chiGroupsByFile[n.File], chiGroup{
				prefix:  pfx,
				line:    n.Line,
				endLine: endLine,
			})
		}
	}

	// ── Resolve gin prefix chains per file ────────────────────────────────
	// Each group's full prefix = receiver-chain prefix + own prefix.
	// Groups whose receiver is not another known group are at the root level.
	type ginPrefixEntry struct {
		fullPrefix string
		resolved   bool
	}
	ginPrefixByFile := map[string]map[string]string{} // file → varName → fullPrefix

	for file, groups := range ginGroupsByFile {
		// Build a set of group var names for fast lookup.
		knownVars := make(map[string]bool, len(groups))
		for _, g := range groups {
			knownVars[g.varName] = true
		}

		pm := make(map[string]string, len(groups))

		// Iterative fixpoint: resolve groups bottom-up (stop when no progress).
		for iteration := 0; iteration <= len(groups); iteration++ {
			changed := false
			for _, g := range groups {
				if _, done := pm[g.varName]; done {
					continue
				}
				if knownVars[g.receiver] {
					// Receiver is another group: can only resolve once parent is done.
					if parentPrefix, ok := pm[g.receiver]; ok {
						pm[g.varName] = parentPrefix + g.prefix
						changed = true
					}
				} else {
					// Receiver is a root router (not a group): prefix chain starts here.
					pm[g.varName] = g.prefix
					changed = true
				}
			}
			if !changed {
				break
			}
		}
		ginPrefixByFile[file] = pm
	}

	// ── Stamp route nodes ─────────────────────────────────────────────────
	for i := range enriched {
		n := &enriched[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		pat := n.Meta["pattern"]
		if strings.HasPrefix(pat, "gin_route_group") || strings.HasPrefix(pat, "chi_route_group") {
			continue // group nodes themselves are not stamped
		}

		currentPath := n.Meta["path"]
		router := n.Meta["router"]

		// Gin enrichment: look up router variable in the file's prefix map.
		if pm, ok := ginPrefixByFile[n.File]; ok && router != "" {
			if fullPrefix, found := pm[router]; found {
				setPath(n, fullPrefix+currentPath)
				continue
			}
		}

		// Chi enrichment: line-range containment inside func_literal bodies.
		if chiGroups, ok := chiGroupsByFile[n.File]; ok {
			var containing []chiGroup
			for _, g := range chiGroups {
				if g.endLine > 0 && n.Line > g.line && n.Line <= g.endLine {
					containing = append(containing, g)
				}
			}
			if len(containing) > 0 {
				// Sort by line ascending so outermost group prefix comes first.
				sort.Slice(containing, func(a, b int) bool {
					return containing[a].line < containing[b].line
				})
				fullPrefix := ""
				for _, g := range containing {
					fullPrefix += g.prefix
				}
				setPath(n, fullPrefix+currentPath)
			}
		}
	}

	return enriched
}

// setPath updates meta["path"] on a node, initialising meta if needed.
func setPath(n *graph.Node, path string) {
	if n.Meta == nil {
		n.Meta = make(map[string]string)
	}
	n.Meta["path"] = path
}
